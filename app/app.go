// Package app assembles and runs the cored server. It is the public assembly
// seam of the open-core split: the OSS cmd/cored calls app.Main with zero
// options; an EE distribution imports this same package, registers its own
// flags and richer implementations (reranker, feature gate) via Options, and
// never patches OSS sources. Options signatures only use core types — the
// internal/* packages stay internal.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"mynah/core"
	"mynah/internal/asrclient"
	"mynah/internal/auth"
	"mynah/internal/avatarcatalog"
	"mynah/internal/avatarengine"
	"mynah/internal/avatarscan"
	"mynah/internal/channel"
	"mynah/internal/clientfactory"
	"mynah/internal/config"
	"mynah/internal/control"
	"mynah/internal/engineclient"
	"mynah/internal/knowledge"
	"mynah/internal/mediaenc"
	"mynah/internal/mtbake"
	"mynah/internal/qwenrt"
	"mynah/internal/session"
	"mynah/internal/signaling"
	"mynah/internal/store"
	"mynah/internal/supervisor"
	"mynah/internal/training"
	"mynah/internal/turnclient"

	pb "mynah/gen/go/proto/avatarengine/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Options are the assembly extension points. cmd/cored registers the full
// open-source feature set; a distribution can assemble a narrower build.
type Options struct {
	// Gate answers feature queries for /features. nil = every feature off
	// (a bare data-plane build).
	Gate core.FeatureGate
	// RegisterFlags runs before flag.Parse so an edition can add its own
	// flags.
	RegisterFlags func()
	// MakeReranker builds a second-stage reranker client from the live rag
	// config (rerank_url / rerank_model / rerank_api_key). It is called at
	// startup and again on every hot config apply, so changing the rerank
	// settings in the console takes effect immediately. It is never called
	// with an empty url. nil hook (the OSS build) = rerank settings are
	// quietly ignored.
	MakeReranker func(url, model, apiKey string) core.Reranker
	// AdminExt mounts the avatar-pipeline admin routes (voice clone, training,
	// motion) when the feature gate permits. nil = those routes don't exist.
	AdminExt core.AdminExtension
}

// Main parses flags, assembles cored and serves until the process exits.
func Main(opts Options) {
	var (
		listen          = flag.String("listen", ":8020", "HTTP listen address")
		workerAddr      = flag.String("worker", defaultWorkerAddr, "AvatarEngine gRPC worker addr(s), comma-separated pool. Fixed at startup — see --worker-pool for a pool that can change while cored runs")
		ttsURL          = flag.String("tts", "http://127.0.0.1:8091", "TTS service base URL")
		voice           = flag.String("voice", "zh-CN-XiaoxiaoNeural", "TTS voice id (EdgeTTS tier: zh-CN-XiaoxiaoNeural; Qwen3-TTS: vivian)")
		webDir          = flag.String("web", "web/user", "static web dir (visitor-facing)")
		stunCSV         = flag.String("stun", "stun:stun.l.google.com:19302", "comma-separated STUN URLs")
		idleIVF         = flag.String("idle-ivf", "", "baked idle loops KEYED BY AVATAR: <avatar>=<path>, comma-separated (split on the first '=', so ids with colons like bake:leiya_mt work). A bare path lands on the \"default\" avatar. Enables idle-local mode for the listed avatars ONLY: an avatar with no entry has its idle rendered live by the engine — right person, worker busy while the channel is open")
		idleSil         = flag.String("idle-silence", "", "baked 20ms Opus silence packet (required with -idle-ivf)")
		actionsFlag     = flag.String("actions", "", "one-shot action clips as <action>[@<avatar>]=<path>, comma-separated (e.g. wave@bake:leiya_mt=/root/idle_user/idle_wave.h264f); '@' rather than a colon because avatar ids already contain one. No @ = the \"default\" avatar. Same baked container/codec as --idle-ivf, played once on POST /action then back to idle. The avatar must have an --idle-ivf entry")
		vidCodec        = flag.String("video-codec", "vp8", "video codec: vp8 or h264 (h264 = iOS/Safari)")
		avatarAssetsDir = flag.String("avatar-assets-dir", "/app/workers/avatar/assets", "base dir holding built-in portrait files; a selected built-in avatar resolves to <dir>/<file> and is sent as SessionSpec.cond_image. THREE processes with different filesystem views read this one string: the avatar worker (container), cored itself (it stats the portrait before queueing an idle bake), and the idle-bake worker (a bare host process, where /app does not exist). On a containerized deployment set it to the HOST path and bind the workers tree at that path everywhere — see docker-compose.full.yml. The /app default only works when the worker is the sole consumer")
		llmURL          = flag.String("llm", "", "OpenAI-compatible LLM base URL (e.g. http://127.0.0.1:11434); enables /human type=chat")
		llmModel        = flag.String("llm-model", "qwen2.5:7b-instruct-q4_K_M", "LLM model name")
		llmKey          = flag.String("llm-key", "", "LLM API key (optional)")
		sysPrompt       = flag.String("system-prompt", "", "chat system prompt (default: short spoken-style Chinese assistant)")
		asrAddr         = flag.String("asr", "", "AsrEngine gRPC worker addr (e.g. 127.0.0.1:9402); enables voice input (requires --llm)")
		turnAddr        = flag.String("turn-detector", "", "TurnDetector gRPC worker addr (e.g. 127.0.0.1:9404); enables semantic turn-taking on voice input (requires --asr). Empty = answer every utterance immediately")
		turnGrace       = flag.Int("turn-grace-ms", 700, "hold window to coalesce a follow-up utterance after a not-finished result")
		turnMinInt      = flag.Int("turn-min-interrupt-ms", 0, "acoustic barge-in: mic speech sustained this long interrupts the avatar without waiting for the transcript. 0 = off (default; interruption is decided on transcript content so backchannels/echo never cut her off)")
		turnMinChars    = flag.Int("turn-interrupt-min-chars", 4, "while the avatar is speaking, a non-backchannel utterance needs at least this many substantive chars to interrupt her (shorter = treated as a backchannel). Lower = quicker to interrupt; the reverse 打断/停 wordlist always bypasses this")
		turnTimeout     = flag.Int("turn-predict-timeout-ms", 1000, "turn-detector RPC timeout; on timeout/error the turn commits immediately (legacy answer-now behavior)")
		turnHist        = flag.Int("turn-history-depth", 6, "DEPRECATED, no-op: the audio turn detector judges the utterance waveform, not chat-history text")
		turnIntWords    = flag.String("turn-interrupt-words", "", "comma-separated extra 'stop talking' words appended to the built-in 打断/停 list (these bypass the ack + min-char gates)")
		turnBackch      = flag.String("turn-backchannels", "", "comma-separated extra acknowledgement words appended to the built-in 嗯/对/好 list (these never interrupt nor get answered)")
		tlsListen       = flag.String("tls-listen", "", "HTTPS listen address (e.g. :8443); mic requires a secure origin")
		tlsCert         = flag.String("tls-cert", "cored.crt", "TLS cert file (auto-generated self-signed if missing)")
		tlsKey          = flag.String("tls-key", "cored.key", "TLS key file (auto-generated self-signed if missing)")
		dbDSN           = flag.String("db", "", "Postgres DSN (e.g. postgres://user:pass@127.0.0.1:5432/mynah); enables the admin/control plane and RAG. Empty = P3 behavior")
		adminListen     = flag.String("admin-listen", "127.0.0.1:9080", "admin API listen address (requires --db); bind 0.0.0.0:9080 to expose remotely")
		adminWeb        = flag.String("admin-web", "", "serve a static admin-console build on the admin listener (same-origin, no CORS needed); empty = API only")
		dockerSocket    = flag.String("docker-socket", "/var/run/docker.sock", "Docker Engine API socket used for console start/stop of the local components (TTS/ASR/avatar workers). Empty = service control disabled. NOTE: the socket is root-equivalent on the host; cored only ever touches its own allowlisted container names")
		mtModelsDir     = flag.String("musetalk-models-dir", "", "MuseTalk weights dir on the HOST (console engine-switch only; scaling an existing pool clones a live worker instead)")
		mtRepoDir       = flag.String("musetalk-repo", "", "MuseTalk upstream repo dir on the HOST")
		mtAvatarDir     = flag.String("musetalk-avatar-dir", "", "baked MuseTalk avatar dir on the HOST")
		bakesDir        = flag.String("avatar-bakes-dir", "", "directory holding baked avatar subdirs (as the WORKER sees it). Scanned at startup; each finished bake becomes a selectable avatar the console can bind to a channel. Empty = no baked avatars offered")
		fhRepoDir       = flag.String("flashhead-dir", "", "SoulX-FlashHead repo+weights dir on the HOST")
		fhAvatarImage   = flag.String("flashhead-avatar", "", "portrait image FlashHead speaks, on the HOST")
		poolGPU         = flag.Int("pool-gpu", 1, "CDI GPU index new avatar workers are created on")
		bakeImage       = flag.String("avatar-bake-image", "mynah/musetalk-bake:prod", "image used for one-shot avatar bakes (built from deploy/compose/worker-musetalk-bake.Dockerfile)")
		bakeWorkDir     = flag.String("avatar-bake-work-dir", "", "HOST dir for bake uploads and scratch, writable by cored and bind-mounted into the bake container. Empty = the console's avatar-bake upload is not offered")
		bakeTorchCache  = flag.String("avatar-bake-torch-cache", "", "HOST torch hub cache holding face_alignment's weights, mounted read-only into the bake container. Empty = the first bake downloads ~180MB")
		poolWorkersDir  = flag.String("pool-workers-dir", "", "repo workers/ dir bind-mounted into new avatar workers")
		poolGenDir      = flag.String("pool-gen-dir", "", "repo gen/ dir bind-mounted into new avatar workers")
		poolCacheVolume = flag.String("pool-cache-volume", "personalive_avatar-cache", "named volume mounted at /cache in new avatar workers")
		workerPool      = flag.String("worker-pool", "", "dynamic avatar worker pool as host:startPort-endPort (e.g. 127.0.0.1:9414-9419). cored probes the range and joins whichever workers answer, so the pool can grow/shrink without a restart. Mutually exclusive with --worker")
		adminTLS        = flag.String("admin-tls-listen", "", "HTTPS admin listen address (e.g. :9443); reuses --tls-cert/--tls-key. Use for remote console access over the public internet")
		visitorRate     = flag.Int("visitor-rate-limit", 30, "per-IP requests/min on the visitor endpoints (/offer, /human, /interrupt_talk, /channel/offer). 0 = off. See SECURITY.md before exposing cored directly to the internet")
		qwenRTURL       = flag.String("qwen-rt-url", "", "Qwen-Audio realtime WebSocket base URL (wss://<workspace>.<region>.maas.aliyuncs.com/api-ws/v1/realtime); with --qwen-rt-key, chat turns run speech-to-speech against the cloud model instead of the local LLM+TTS pipeline (experimental)")
		qwenRTKey       = flag.String("qwen-rt-key", "", "DashScope API key for --qwen-rt-url (or set QWEN_RT_KEY)")
		qwenRTModel     = flag.String("qwen-rt-model", "qwen-audio-3.0-realtime-flash", "Qwen realtime model id")
		qwenRTVoice     = flag.String("qwen-rt-voice", "longanqian", "Qwen realtime TTS voice (system voice or voice-clone id; the local TTS voice config does not apply in this mode)")
	)
	if opts.RegisterFlags != nil {
		opts.RegisterFlags()
	}
	flag.Parse()

	// Connect + migrate first: a misconfigured DSN should fail fast, before
	// the slow worker warmup wait.
	var db *store.Store
	if *dbDSN != "" {
		var err error
		db, err = store.Open(context.Background(), *dbDSN)
		if err != nil {
			log.Fatalf("store: %v", err)
		}
		defer db.Close()
		log.Printf("store: connected, schema up to date")
	}

	// Three-layer config: defaults < explicitly-set flags < DB settings.
	// flag.Visit only sees flags the operator actually passed, so untouched
	// flags never shadow a DB override.
	flagSet := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { flagSet[f.Name] = true })
	flagLayer := map[string]json.RawMessage{}
	{
		llmOv := map[string]any{}
		if flagSet["llm"] {
			llmOv["base_url"] = *llmURL
		}
		if flagSet["llm-model"] {
			llmOv["model"] = *llmModel
		}
		if flagSet["llm-key"] {
			llmOv["api_key"] = *llmKey
		}
		if flagSet["system-prompt"] {
			llmOv["system_prompt"] = *sysPrompt
		}
		if len(llmOv) > 0 {
			config.FlagLayer(flagLayer, "llm", llmOv)
		}
		ttsOv := map[string]any{}
		if flagSet["tts"] {
			ttsOv["base_url"] = *ttsURL
		}
		if flagSet["voice"] {
			ttsOv["voice"] = *voice
		}
		if len(ttsOv) > 0 {
			config.FlagLayer(flagLayer, "tts", ttsOv)
		}
		turnOv := map[string]any{}
		if flagSet["turn-grace-ms"] {
			turnOv["grace_ms"] = *turnGrace
		}
		if flagSet["turn-min-interrupt-ms"] {
			turnOv["min_interrupt_ms"] = *turnMinInt
		}
		if len(turnOv) > 0 {
			config.FlagLayer(flagLayer, "turn", turnOv)
		}
		brainOv := map[string]any{}
		if flagSet["qwen-rt-url"] {
			// Providing realtime credentials flips the default brain to qwen so
			// pre-console deployments keep their behavior; a console PUT (DB
			// layer) still overrides this back to local.
			brainOv["mode"] = "qwen"
		}
		if flagSet["qwen-rt-model"] {
			brainOv["model"] = *qwenRTModel
		}
		if flagSet["qwen-rt-voice"] {
			brainOv["voice"] = *qwenRTVoice
		}
		if len(brainOv) > 0 {
			config.FlagLayer(flagLayer, "brain", brainOv)
		}
	}
	cfgMgr, err := config.NewManager(context.Background(), db, flagLayer)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	cfg := cfgMgr.Current()

	// --worker takes a comma-separated pool of single-session engine workers;
	// each session is dispatched to a free one (all busy -> visitor sees 503).
	// --worker-pool instead names a port RANGE that cored keeps reconciling, so
	// workers can be added or removed while it runs (see session/pool.go).
	var workers []*session.Worker
	var poolSpec *session.PoolSpec
	if *workerPool != "" {
		if strings.TrimSpace(*workerAddr) != "" && *workerAddr != defaultWorkerAddr {
			log.Fatalf("--worker and --worker-pool are mutually exclusive")
		}
		spec, err := parseWorkerPool(*workerPool)
		if err != nil {
			log.Fatalf("--worker-pool: %v", err)
		}
		poolSpec = &spec
		// Deliberately do NOT block on a worker here. The whole point of a
		// dynamic pool is that cored survives an empty one: it comes up, serves
		// the console (so an operator can start workers), and answers visitors
		// with 503 until a worker appears. Blocking would make cored
		// un-startable exactly when someone needs the console to fix the pool.
		log.Printf("worker pool mode: %s (cored starts with an empty pool and joins workers as they appear)",
			*workerPool)
	} else {
		for _, addr := range strings.Split(*workerAddr, ",") {
			if addr = strings.TrimSpace(addr); addr == "" {
				continue
			}
			cli, err := engineclient.Dial(addr)
			if err != nil {
				log.Fatalf("dial worker %s: %v", addr, err)
			}
			defer cli.Close()
			// Wait for each worker to finish loading + warmup before accepting browsers.
			workers = append(workers, session.NewWorker(addr, waitHealthy(addr, cli), cli))
		}
		if len(workers) == 0 {
			log.Fatalf("--worker: no worker addresses given")
		}
	}
	engineDetail := ""
	if len(workers) > 0 {
		engineDetail = workers[0].Detail
	}

	var stun []string
	for _, u := range strings.Split(*stunCSV, ",") {
		if u = strings.TrimSpace(u); u != "" {
			stun = append(stun, u)
		}
	}

	ttsClient := clientfactory.BuildTTS(cfg.TTS)
	// Baked avatars: discovered from disk, then registered into the catalog so
	// both the console and session start resolve them the same way. Rescanned
	// on demand (the console calls it before listing) because baking happens
	// outside cored — a GPU script drops a new directory and there is no event
	// to hook.
	rescanAvatars := func() {
		bakes := avatarscan.ScanBakes(*bakesDir)
		avatarcatalog.SetBaked(bakes)
	}
	if *bakesDir != "" {
		rescanAvatars()
		names := make([]string, 0)
		for _, b := range avatarcatalog.BakedAvatars() {
			names = append(names, b.Name)
		}
		if len(names) > 0 {
			log.Printf("baked avatars discovered in %s: %v", *bakesDir, names)
		} else {
			log.Printf("no finished bakes found under %s", *bakesDir)
		}
	}

	mgr := session.NewManager(workers, stun, ttsClient)
	mgr.SetVideoCodec(*vidCodec)
	if poolSpec != nil {
		// Discovery starts here, after the manager exists. Workers that are
		// already up join within the first sweep (immediate, not after a tick).
		mgr.StartPoolReconciler(context.Background(), *poolSpec)
	}
	// Drive the worker's speaking portrait from the selected avatar. Read fresh
	// per session (avatar is non-hot, so a PUT applies to the next session); the
	// dir is the worker-visible avatar-assets base for resolving built-in ids.
	mgr.SetAvatarSource(func() string { return cfgMgr.Current().Avatar.Current }, *avatarAssetsDir)
	// Resolver shared by session start (speaking cond_image), the bind-time
	// preload and the idle-bake extension: catalog first, then trained:<jobId>
	// artifacts from the training_jobs table (needs --db).
	//
	// ResolveAny, not Resolve — Resolve knows only the built-in portrait FILES,
	// so this closure (which overrides the manager's own default) used to drop
	// "bake:*" directories on the floor and return "", i.e. "keep whatever avatar
	// you already have". Sessions on a baked avatar still looked right only
	// because the MuseTalk worker boots with one bake preloaded as its default;
	// a second bake on the same worker would have silently rendered the first
	// one's face, and bind-time preload rejected every bake id outright.
	resolveAvatar := avatarResolver(*avatarAssetsDir, func(id string) string {
		if db == nil {
			return ""
		}
		return training.ResolvePortrait(context.Background(), db.Pool, id)
	})
	mgr.SetAvatarResolver(resolveAvatar)
	// Live turn-mode selection (config group "turn"): each NEW session reads
	// it at create — "local" keeps the flag-tuned ASR+Smart Turn stack, the
	// cloud modes hand segmentation to the qwen realtime server (qwen brain
	// only; the session falls back to local otherwise).
	mgr.SetTurnModeSource(func() session.TurnModeConfig {
		t := cfgMgr.Current().Turn
		return session.TurnModeConfig{
			Mode: t.Mode, GraceMS: t.GraceMS, MinInterruptMS: t.MinInterruptMS,
			VadThreshold: t.VadThreshold, VadSilenceMS: t.VadSilenceMS,
		}
	})
	// Saved lip-blend defaults (config.avatar.mouth) -> session-start SetAction;
	// marshaled here to keep the session package decoupled from config.
	mgr.SetMouthParams(func() string {
		b, err := json.Marshal(cfgMgr.Current().Avatar.Mouth)
		if err != nil {
			return ""
		}
		return string(b)
	})

	// Rerank is config-driven (rag.rerank_url etc.) and applies hot, like the
	// LLM client. makeReranker centralizes config -> client so startup and hot
	// apply can't drift apart; the current client is also shared with the
	// control plane (kb/search-test) through getReranker.
	var (
		rerankMu   sync.RWMutex
		rerankCur  core.Reranker
		rerankWarn bool // log the "needs EE" hint once, not on every apply
	)
	makeReranker := func(c config.RAG) core.Reranker {
		if c.RerankURL == "" {
			return nil
		}
		if opts.MakeReranker == nil {
			if !rerankWarn {
				log.Printf("rag.rerank_url is set but this build has no reranker; ignoring")
				rerankWarn = true
			}
			return nil
		}
		return opts.MakeReranker(c.RerankURL, c.RerankModel, c.RerankAPIKey)
	}
	applyReranker := func(c config.RAG) {
		rerankMu.Lock()
		r := makeReranker(c)
		was := rerankCur != nil
		rerankCur = r
		rerankMu.Unlock()
		mgr.SetReranker(r)
		if r != nil {
			log.Printf("rerank enabled: %s model=%s", c.RerankURL, c.RerankModel)
		} else if was {
			log.Printf("rerank disabled")
		}
	}
	getReranker := func() core.Reranker {
		rerankMu.RLock()
		defer rerankMu.RUnlock()
		return rerankCur
	}
	applyReranker(cfg.RAG)

	// Chat stays flag-gated (--llm) like P3; the merged config supplies the
	// effective provider/endpoint/model/prompt and hot updates swap clients
	// live. makeChat centralizes config -> provider so startup and hot apply
	// can't drift apart.
	makeChat := func(c config.LLM) core.ChatProvider {
		p, err := clientfactory.BuildLLM(c)
		if err != nil {
			log.Printf("chat disabled: %v", err)
			return nil
		}
		return p
	}
	chatEnabled := false
	if p := makeChat(cfg.LLM); p != nil {
		mgr.SetLLM(p)
		chatEnabled = true
		log.Printf("chat enabled: provider=%s llm=%s model=%s", orDefault(cfg.LLM.Provider, "openai"), cfg.LLM.BaseURL, cfg.LLM.Model)
	}
	cfgMgr.OnApply(func(c config.Config) {
		mgr.SetLLM(makeChat(c.LLM))
		mgr.SetTTS(clientfactory.BuildTTS(c.TTS))
		applyReranker(c.RAG)
		log.Printf("config applied hot: provider=%s llm=%s model=%s tts=%s voice=%s rag=%v rerank=%v",
			orDefault(c.LLM.Provider, "openai"), c.LLM.BaseURL, c.LLM.Model, c.TTS.BaseURL, c.TTS.Voice, c.RAG.Enabled, getReranker() != nil)
	})
	if *asrAddr != "" {
		if !chatEnabled && *qwenRTURL == "" {
			log.Fatal("--asr requires --llm or --qwen-rt-url (voice input runs chat turns)")
		}
		asrCli, err := asrclient.Dial(*asrAddr)
		if err != nil {
			log.Fatalf("dial asr worker %s: %v", *asrAddr, err)
		}
		defer asrCli.Close()
		mgr.SetASR(asrCli)
		log.Printf("voice input enabled: asr=%s", *asrAddr)
	}
	if *turnAddr != "" {
		if *asrAddr == "" {
			log.Fatal("--turn-detector requires --asr (it gates voice utterances)")
		}
		tdCli, err := turnclient.Dial(*turnAddr)
		if err != nil {
			log.Fatalf("dial turn detector %s: %v", *turnAddr, err)
		}
		defer tdCli.Close()
		mgr.SetTurnDetector(tdCli, session.TurnConfig{
			GraceMS:             *turnGrace,
			MinInterruptMS:      *turnMinInt,
			PredictTimeoutMS:    *turnTimeout,
			HistoryDepth:        *turnHist,
			InterruptMinChars:   *turnMinChars,
			ExtraInterruptWords: splitCSV(*turnIntWords),
			ExtraBackchannels:   splitCSV(*turnBackch),
		})
		log.Printf("semantic turn-taking enabled: turn-detector=%s grace=%dms min-interrupt=%dms interrupt-min-chars=%d predict-timeout=%dms history-depth=%d +interrupt-words=%v +backchannels=%v",
			*turnAddr, *turnGrace, *turnMinInt, *turnMinChars, *turnTimeout, *turnHist, splitCSV(*turnIntWords), splitCSV(*turnBackch))
	}
	// Qwen realtime brain mode: chat turns go speech-to-speech against the
	// cloud model; the local LLM/TTS/RAG pipeline is bypassed for those turns.
	// The URL+key are startup-only credentials; WHETHER a session uses them is
	// the live config.brain.mode (console-editable, read fresh per NEW session
	// like the turn mode). Instructions follow the live llm.system_prompt so
	// the persona stays editable in the config center.
	var qwenPreview func(ctx context.Context, model, voice, text string) ([]float32, error)
	if *qwenRTURL != "" {
		key := *qwenRTKey
		if key == "" {
			key = os.Getenv("QWEN_RT_KEY")
		}
		if key == "" {
			log.Fatal("--qwen-rt-url requires --qwen-rt-key (or QWEN_RT_KEY)")
		}
		url := *qwenRTURL
		qwenPreview = func(ctx context.Context, model, voice, text string) ([]float32, error) {
			return qwenrt.Preview(ctx, qwenrt.Config{URL: url, APIKey: key, Model: model, Voice: voice}, text)
		}
		mgr.SetQwenSource(func(frozen *session.BrainConfig) *qwenrt.Config {
			if frozen != nil {
				// Channel visitor: the published snapshot's brain (and its
				// captured persona prompt) wins over later console edits.
				if frozen.Mode != "qwen" {
					return nil
				}
				instr := frozen.Instructions
				return &qwenrt.Config{
					URL: url, APIKey: key, Model: frozen.Model, Voice: frozen.Voice,
					Instructions: func() string { return instr },
				}
			}
			b := cfgMgr.Current().Brain
			if b.Mode != "qwen" {
				return nil
			}
			return &qwenrt.Config{
				URL: url, APIKey: key, Model: b.Model, Voice: b.Voice,
				Instructions: func() string { return cfgMgr.Current().LLM.SystemPrompt },
			}
		})
		log.Printf("qwen realtime brain available: model=%s voice=%s mode=%s (brain mode is console-switchable per new session)",
			cfgMgr.Current().Brain.Model, cfgMgr.Current().Brain.Voice, cfgMgr.Current().Brain.Mode)
	} else if cfgMgr.Current().Brain.Mode == "qwen" {
		log.Printf("config brain.mode=qwen but no --qwen-rt-url credentials; sessions will run the local pipeline")
	}
	// Per-avatar idle-local material. Each avatar gets its own baked loop (and
	// its own action clips); an avatar with no entry has its idle rendered live
	// by the engine instead — right person, costs GPU. See internal/session/idle.go.
	var silencePkt []byte
	{
		idleEntries, err := parseIdleFlag(*idleIVF)
		if err != nil {
			log.Fatal(err)
		}
		actionEntries, err := parseActionsFlag(*actionsFlag)
		if err != nil {
			log.Fatal(err)
		}
		// config.avatar.idles is what the console APPLIED (an idle bake that
		// completed), so it wins over the boot flags for the avatars it names.
		// Reading it here is also the fix for the old write-only
		// config.system.idle_asset: hot-applied idles now really do survive a
		// restart.
		cur := cfgMgr.Current()
		applied := map[string]string{}
		for id, p := range cur.Avatar.Idles {
			applied[id] = p
		}
		if _, ok := applied[defaultAvatarID]; !ok && strings.TrimSpace(cur.System.IdleAsset) != "" {
			applied[defaultAvatarID] = cur.System.IdleAsset // legacy field, one-way migration
		}
		for id, p := range applied {
			replaced := false
			for i := range idleEntries {
				if idleEntries[i].Avatar == id {
					idleEntries[i].Path, replaced = p, true
				}
			}
			if !replaced {
				idleEntries = append(idleEntries, idleEntry{Avatar: id, Path: p})
			}
		}
		// Bad entries are fatal: a mistyped path should stop the boot, not
		// silently leave an avatar on engine-driven idle.
		if err := validateActionAvatars(idleEntries, actionEntries); err != nil {
			log.Fatal(err)
		}
		if len(idleEntries) > 0 {
			sil, err := os.ReadFile(*idleSil)
			if err != nil {
				log.Fatalf("load idle silence: %v", err)
			}
			silencePkt = sil
			mgr.SetIdleSilence(sil)
		}
		sets := map[string]*session.AvatarIdle{}
		for _, e := range idleEntries {
			idle, err := mediaenc.LoadIdle(e.Path)
			if err != nil {
				log.Fatalf("load idle asset for avatar %s: %v", e.Avatar, err)
			}
			if idle.Codec != *vidCodec {
				log.Fatalf("idle asset for avatar %s: codec %q != -video-codec %q", e.Avatar, idle.Codec, *vidCodec)
			}
			sets[e.Avatar] = &session.AvatarIdle{Idle: idle, Actions: map[string]*mediaenc.IdleLoop{}}
		}
		for _, a := range actionEntries {
			clip, err := mediaenc.LoadIdle(a.Path)
			if err != nil {
				log.Fatalf("load action %s@%s: %v", a.Action, a.Avatar, err)
			}
			if clip.Codec != *vidCodec {
				log.Fatalf("action %s@%s: codec %q != -video-codec %q", a.Action, a.Avatar, clip.Codec, *vidCodec)
			}
			set := sets[a.Avatar]
			set.Actions[a.Action] = clip
			set.ActionIDs = append(set.ActionIDs, a.Action)
		}
		for id, set := range sets {
			mgr.SetAvatarIdle(id, set)
			log.Printf("idle-local: avatar %s = %d baked %s frames, %d action(s) %v (worker idle cost = 0)",
				id, len(set.Idle.Frames), set.Idle.Codec, len(set.ActionIDs), set.ActionIDs)
		}
		if len(sets) == 0 {
			log.Printf("idle-local: no --idle-ivf material; every avatar's idle is rendered live by its engine")
		} else {
			// Name the avatars that will fall back to engine-rendered idle. That
			// fallback shows the RIGHT PERSON (which is the point), but it keeps a
			// worker busy for as long as the channel is open — a silent shift from
			// a zero-cost idle, so it gets said out loud at boot as well as in the
			// console.
			var without []string
			for _, p := range avatarcatalog.Builtins {
				if sets[p.ID] == nil {
					without = append(without, p.ID)
				}
			}
			for _, b := range avatarcatalog.BakedAvatars() {
				if sets[b.ID] == nil {
					without = append(without, b.ID)
				}
			}
			if len(without) > 0 {
				log.Printf("idle-local: no own idle material for %v — their idle is rendered live by the engine (right person, worker stays busy while the channel is open)", without)
			}
		}
	}
	// applyIdle installs a freshly-baked asset as ONE AVATAR's idle loop (the
	// idle-bake extension calls it on job completion) and persists it to
	// config.avatar.idles so it survives a restart. New sessions on that avatar
	// pick it up; running ones and every other avatar are untouched — before
	// this was per-avatar, baking an idle for one avatar replaced it for all of
	// them. Requires a silence packet, which only loads when at least one avatar
	// has idle-local material.
	applyIdle := func(avatarID, assetPath string) error {
		avatarID = strings.TrimSpace(avatarID)
		if avatarID == "" {
			avatarID = defaultAvatarID
		}
		if len(silencePkt) == 0 {
			return errors.New("idle-local mode is off (--idle-ivf/--idle-silence not set); baked asset saved but not applied")
		}
		idle, err := mediaenc.LoadIdle(assetPath)
		if err != nil {
			return err
		}
		if idle.Codec != *vidCodec {
			return errors.New("baked idle codec " + idle.Codec + " != running -video-codec " + *vidCodec)
		}
		// Keep the avatar's existing action clips: they were baked against this
		// same avatar and a new idle loop does not invalidate them.
		set := &session.AvatarIdle{Idle: idle, Actions: map[string]*mediaenc.IdleLoop{}}
		if old := mgr.AvatarIdleSet(avatarID); old != nil {
			set.Actions, set.ActionIDs = old.Actions, old.ActionIDs
		}
		mgr.SetAvatarIdle(avatarID, set)
		if db != nil {
			idles := map[string]string{}
			for id, p := range cfgMgr.Current().Avatar.Idles {
				idles[id] = p
			}
			idles[avatarID] = assetPath
			body, _ := json.Marshal(map[string]any{"idles": idles})
			if _, err := cfgMgr.Put(context.Background(), "avatar", body); err != nil {
				log.Printf("applyIdle: persist avatar.idles: %v", err)
			}
		}
		log.Printf("idle-local: avatar %s hot-swapped to %s (%d %s frames)",
			avatarID, assetPath, len(idle.Frames), idle.Codec)
		return nil
	}
	// Channel publish registry (control plane + visitor /channel routes). Only
	// with --db; it clears the live concurrency ledger on startup.
	var chReg *channel.Registry
	if db != nil {
		reg, err := channel.NewRegistry(context.Background(), db, clientfactory.Deps{
			DB: db, MakeReranker: makeReranker,
		})
		if err != nil {
			log.Fatalf("channel registry: %v", err)
		}
		chReg = reg
	}
	srv := signaling.New(mgr, *webDir, chReg)
	srv.SetRateLimit(*visitorRate)
	srv.SetChromaSource(func() config.Chroma { return cfgMgr.Current().Avatar.Chroma })

	// Admin/control plane on its own listener, network-isolated from visitors.
	if db != nil {
		authProvider, err := auth.New(context.Background(), db)
		if err != nil {
			log.Fatalf("auth: %v", err)
		}

		// RAG: async ingestion queue + online retriever, both following the
		// live config. rag.enabled gates the per-turn injection.
		ingester := knowledge.NewIngester(db, func() config.RAG { return cfgMgr.Current().RAG })
		go ingester.Run(context.Background())
		applyRAG := func(c config.Config) {
			retriever, ragOpts := clientfactory.BuildRAG(db, c.RAG)
			mgr.SetRetriever(retriever, ragOpts)
		}
		applyRAG(cfg)
		cfgMgr.OnApply(applyRAG)

		// Local-component lifecycle for the console. Probed once at startup:
		// an unreachable socket leaves this nil so the services panel and its
		// routes simply don't exist, rather than showing buttons that fail.
		var localSupervisor supervisor.Supervisor
		var dockerSup *supervisor.Docker
		if *dockerSocket != "" {
			sup := supervisor.NewDocker(*dockerSocket)
			pctx, pcancel := context.WithTimeout(context.Background(), 3*time.Second)
			if _, err := sup.List(pctx); err != nil {
				log.Printf("service control disabled: docker socket %s unreachable: %v",
					*dockerSocket, err)
			} else {
				localSupervisor = sup
				dockerSup = sup
				log.Printf("service control enabled via docker socket %s", *dockerSocket)
			}
			pcancel()
		}

		// Avatar baking: one-shot containers, so it needs the docker socket AND
		// a DB for the job queue. Missing material leaves this nil and the
		// console hides the panel rather than offering an upload that fails
		// after the video is already on disk.
		var avatarBaker *mtbake.Runner
		if dockerSup != nil && db != nil {
			bakeCfg := mtbake.Config{
				Image: *bakeImage, WorkDir: *bakeWorkDir, BakesDir: *bakesDir,
				RepoDir: *mtRepoDir, ModelsDir: *mtModelsDir,
				WorkersDir: *poolWorkersDir, TorchCache: *bakeTorchCache,
				GPU: *poolGPU,
			}
			if err := bakeCfg.Ready(); err != nil {
				log.Printf("avatar baking disabled: %v", err)
			} else {
				avatarBaker = mtbake.NewRunner(db.Pool, dockerSup, bakeCfg, rescanAvatars)
				avatarBaker.Start(context.Background())
				log.Printf("avatar baking enabled (image %s, work dir %s, GPU %d)",
					bakeCfg.Image, bakeCfg.WorkDir, bakeCfg.GPU)
			}
		}

		ctrl := control.New(control.Deps{
			Auth:          authProvider,
			DB:            db,
			Config:        cfgMgr,
			Ingester:      ingester,
			Sessions:      mgr,
			Reranker:      getReranker,
			Channels:      chReg,
			Supervisor:    localSupervisor,
			RescanAvatars: rescanAvatars,
			AvatarBaker:   avatarBaker,
			EngineMaterial: map[string]avatarengine.Material{
				"musetalk": {
					ModelsDir: *mtModelsDir, RepoDir: *mtRepoDir, AvatarDir: *mtAvatarDir,
					GPU: *poolGPU, WorkersDir: *poolWorkersDir, GenDir: *poolGenDir,
					CacheVolume: *poolCacheVolume,
				},
				"flashhead": {
					RepoDir: *fhRepoDir, AvatarImage: *fhAvatarImage,
					GPU: *poolGPU, WorkersDir: *poolWorkersDir, GenDir: *poolGenDir,
					CacheVolume: *poolCacheVolume,
				},
			},
			VisitorListen:       *listen,
			VisitorTLSListen:    *tlsListen,
			Gate:                opts.Gate,
			EngineDetail:        engineDetail,
			WebDir:              *adminWeb,
			AdminExt:            opts.AdminExt,
			ResolveAvatarSource: resolveAvatar,
			ApplyIdle:           applyIdle,
			QwenPreview:         qwenPreview,
			Health: control.HealthTargets{
				// In pool mode --worker is never dialed, so probing it would
				// report the avatar engine down while a healthy pool serves
				// traffic. Leave it empty and let the health handler read the
				// live pool from the session manager instead.
				AvatarAddr: healthAvatarAddr(*workerAddr, poolSpec),
				AsrAddr:    *asrAddr,
				TTSURL:     cfg.TTS.BaseURL,
				LLMURL:     cfg.LLM.BaseURL,
				OllamaURL:  cfg.RAG.EmbedURL,
			},
		})
		go func() {
			log.Printf("admin API on http://%s/api/v1 (login: POST /api/v1/auth/login)", *adminListen)
			if *adminWeb != "" {
				log.Printf("admin console on http://%s/ (static dir: %s)", *adminListen, *adminWeb)
			}
			if err := http.ListenAndServe(*adminListen, ctrl.Handler()); err != nil {
				log.Fatal(err)
			}
		}()
		if *adminTLS != "" {
			certFile, keyFile, err := ensureCert(*tlsCert, *tlsKey)
			if err != nil {
				log.Fatalf("admin tls cert: %v", err)
			}
			go func() {
				log.Printf("admin HTTPS on %s (API + console)", *adminTLS)
				if err := http.ListenAndServeTLS(*adminTLS, certFile, keyFile, ctrl.Handler()); err != nil {
					log.Fatal(err)
				}
			}()
		}
	}

	log.Printf("cored listening on %s (worker=%s tts=%s voice=%s web=%s)",
		*listen, *workerAddr, *ttsURL, *voice, *webDir)
	log.Printf("open http://<serverip>%s/index.html", *listen)
	if *tlsListen != "" {
		certFile, keyFile, err := ensureCert(*tlsCert, *tlsKey)
		if err != nil {
			log.Fatalf("tls cert: %v", err)
		}
		go func() {
			log.Printf("HTTPS on %s (mic needs a secure origin: https://<serverip>%s/index.html)",
				*tlsListen, *tlsListen)
			if err := http.ListenAndServeTLS(*tlsListen, certFile, keyFile, srv.Handler()); err != nil {
				log.Fatal(err)
			}
		}()
	}
	if err := http.ListenAndServe(*listen, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

// avatarResolver maps an avatar id to the worker-visible cond_image value:
// catalog first (built-in portrait FILES and "bake:*" DIRECTORIES), then the
// trained-likeness fallback. "" means "no override, keep the worker's current
// avatar", which is also what an unknown id gets.
//
// It is shared by session start, the bind-time preload and the idle-bake
// extension, so a gap here shows up in three unrelated places at once: while it
// called avatarcatalog.Resolve (built-ins only) every "bake:*" id resolved to ""
// — sessions kept whatever the worker booted with, and preload rejected the very
// case it exists for (a cold bake is the ~15s load).
func avatarResolver(assetsDir string, trained func(id string) string) func(id string) string {
	return func(id string) string {
		if p := avatarcatalog.ResolveAny(id, assetsDir); p != "" {
			return p
		}
		if trained == nil {
			return ""
		}
		return trained(id)
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// splitCSV parses a comma-separated flag into a trimmed, non-empty slice
// (nil for an empty flag, so withDefaults appends nothing).
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// healthAvatarAddr picks what the health probe should dial. With a dynamic
// pool there is no fixed address list — membership changes at runtime — so the
// probe must not fall back to the unused --worker default (which points at a
// port nothing listens on and made the dashboard report the engine as down
// while two healthy workers were serving).
func healthAvatarAddr(workerAddr string, pool *session.PoolSpec) string {
	if pool != nil {
		return "" // handled by the live-pool check in the health handler
	}
	return workerAddr
}

// defaultWorkerAddr lets --worker-pool detect that --worker was left at its
// default rather than set on purpose, so the two flags can be mutually
// exclusive without breaking every existing command line.
const defaultWorkerAddr = "127.0.0.1:9401"

// parseWorkerPool reads "host:startPort-endPort".
func parseWorkerPool(s string) (session.PoolSpec, error) {
	host, ports, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || host == "" {
		return session.PoolSpec{}, fmt.Errorf("want host:startPort-endPort, got %q", s)
	}
	lo, hi, ok := strings.Cut(ports, "-")
	if !ok {
		return session.PoolSpec{}, fmt.Errorf("want a port RANGE like 9414-9419, got %q", ports)
	}
	start, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil {
		return session.PoolSpec{}, fmt.Errorf("bad start port %q", lo)
	}
	end, err := strconv.Atoi(strings.TrimSpace(hi))
	if err != nil {
		return session.PoolSpec{}, fmt.Errorf("bad end port %q", hi)
	}
	if start < 1 || end > 65535 || end < start {
		return session.PoolSpec{}, fmt.Errorf("port range %d-%d is not valid", start, end)
	}
	// A wide range means a slow full sweep and a lot of pointless dialing; the
	// real pool is a handful of ports.
	if end-start+1 > 32 {
		return session.PoolSpec{}, fmt.Errorf("port range %d-%d spans %d ports; cap is 32",
			start, end, end-start+1)
	}
	return session.PoolSpec{Host: host, PortStart: start, PortEnd: end}, nil
}

func waitHealthy(addr string, engine *engineclient.Client) string {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		rep, err := engine.AE.Health(ctx, &pb.HealthRequest{})
		cancel()
		if err == nil && rep.Ready {
			log.Printf("worker %s healthy: %s", addr, rep.Detail)
			return rep.Detail
		}
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable {
			log.Printf("worker %s not up yet, retrying...", addr)
		} else if err != nil {
			log.Printf("worker %s health error: %v (retrying)", addr, err)
		} else {
			log.Printf("worker %s not ready, retrying...", addr)
		}
		time.Sleep(2 * time.Second)
	}
}
