// Package config is the control plane's three-layer configuration model:
// code defaults < explicitly-set CLI flags < DB settings (JSONB per group).
// Groups llm/tts/rag apply hot (the session manager swaps clients under a
// lock); the system group needs a restart, so PUT returns restart_required
// and the merged value takes effect on next boot.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type LLM struct {
	// Provider selects the chat protocol: "openai" (default; covers ollama,
	// vLLM, FastGPT, RAGFlow, OneAPI, clouds), "dify", or "coze". With a
	// platform provider the persona and knowledge live on the platform side:
	// system_prompt is ignored and rag should stay off.
	Provider     string `json:"provider"`
	BaseURL      string `json:"base_url"`
	Model        string `json:"model"`
	APIKey       string `json:"api_key"`
	SystemPrompt string `json:"system_prompt"`
	BotID        string `json:"bot_id"` // coze only: which bot the key talks to
	// Stream toggles streaming replies. On (default) the reply is sliced into
	// sentences and spoken while the model is still generating (lowest first
	// response). Off waits for the complete reply before synthesis — only
	// needed for OpenAI-compatible backends that can't do SSE streaming.
	Stream *bool `json:"stream"`
}

// StreamOn resolves the stream toggle: nil (pre-toggle configs) means on.
func (l LLM) StreamOn() bool { return l.Stream == nil || *l.Stream }

type TTS struct {
	BaseURL string `json:"base_url"`
	Voice   string `json:"voice"`
	// Seed pins the TTS sampling seed for the default Voice. 0 = unset → the model
	// samples a fresh rendition each utterance (timbre drifts slightly per sentence).
	// A positive value makes every utterance reproducible so the voice stays consistent.
	// Invariant maintained by the admin UI: Seed == VoiceSeeds[Voice].
	Seed int `json:"seed"`
	// VoiceSeeds remembers a per-voice seed (voiceID → seed) so each voice keeps its
	// own preferred rendition. Live playback reads Voice+Seed; channel publish/voice
	// patch look the new voice's seed up here so a frozen snapshot never pairs a
	// voice with another voice's seed. PUT replaces the whole map (top-level shallow
	// merge), so the admin always sends the full map.
	VoiceSeeds map[string]int `json:"voice_seeds"`
	// Instructions is an optional style steer ("更沉稳") sent with every synthesis,
	// so production speaks with the same style the console auditioned. Empty = none.
	Instructions string `json:"instructions"`
	// Speed is the synthesis speed multiplier. 0 = unset → 1.0; clamped to [0.5, 2.0]
	// where consumed (clientfactory.BuildTTS).
	Speed float64 `json:"speed"`
}

// SeedFor returns the remembered seed for a voice (0 = none saved → the TTS
// samples freely each utterance).
func (t TTS) SeedFor(voice string) int { return t.VoiceSeeds[voice] }

type RAG struct {
	Enabled      bool    `json:"enabled"`
	EmbedURL     string  `json:"embed_url"`   // ollama-compatible /api/embed base
	EmbedModel   string  `json:"embed_model"` // bge-m3 (1024-dim)
	EmbedDim     int     `json:"embed_dim"`
	ChunkSize    int     `json:"chunk_size"`    // runes per chunk
	ChunkOverlap int     `json:"chunk_overlap"` // runes of overlap
	TopK         int     `json:"top_k"`
	Threshold    float64 `json:"threshold"`  // min cosine similarity
	TimeoutMS    int     `json:"timeout_ms"` // per-turn retrieve budget; degrade past it

	// Second-stage rerank (EE capability; the OSS binary has no client and
	// quietly ignores these). Empty rerank_url = vector order is final.
	RerankURL    string `json:"rerank_url"`
	RerankModel  string `json:"rerank_model"`
	RerankAPIKey string `json:"rerank_api_key"` // optional; cloud providers
	// RerankCandidates is how many vector hits to over-fetch for the reranker
	// to pick top_k from. 0 = auto: max(top_k*4, 12) capped at 20.
	RerankCandidates int `json:"rerank_candidates"`
}

// CandidateCount resolves how many vector hits to over-fetch for the
// reranker to pick topK from: the explicit rerank_candidates if set (floored
// at topK), otherwise auto max(topK*4, 12) capped at 20.
func (r RAG) CandidateCount(topK int) int {
	if r.RerankCandidates > 0 {
		if r.RerankCandidates < topK {
			return topK
		}
		return r.RerankCandidates
	}
	n := topK * 4
	if n < 12 {
		n = 12
	}
	if n > 20 {
		n = 20
	}
	return n
}

type System struct {
	Listen      string `json:"listen"`
	TLSListen   string `json:"tls_listen"`
	AdminListen string `json:"admin_listen"`
	WorkerAddr  string `json:"worker_addr"`
	AsrAddr     string `json:"asr_addr"`
	VideoCodec  string `json:"video_codec"`
	// IdleAsset is LEGACY: applyIdle used to write it "so the hot-swapped idle
	// survives a restart", but nothing ever read it back. Per-avatar idles live
	// in Avatar.Idles now; this is migrated into Avatar.Idles["default"] at
	// startup when that key is absent, and no longer written.
	IdleAsset   string `json:"idle_asset"`
	IdleSilence string `json:"idle_silence"`
	WebDir      string `json:"web_dir"`
	StunURLs    string `json:"stun_urls"`
}

// Avatar holds which digital-human appearance the console has selected. Like
// TTS.Voice it is a single pointer: "default", a built-in avatar id, or
// "trained:<jobId>" for a trained likeness. Read fresh at each session start
// and resolved to the worker's cond_image (built-in catalog or the training
// artifact); the idle-bake extension uses the same resolution as its source.
type Avatar struct {
	Current string `json:"current"`
	// Motions remembers which driving-motion skeleton (动作骨架) each avatar
	// should use for its idle, keyed by avatar id; value is a skeleton ref
	// ("builtin:d9" / "asset:<id>"). The console reads it when it queues an
	// idle bake for the selected avatar. Same PUT semantics as TTS.VoiceSeeds:
	// the avatar group merges shallowly, so a PUT carrying motions replaces
	// the whole map — the front end always sends the full map.
	Motions map[string]string `json:"motions"`
	// Idles maps avatar id -> the baked idle asset applied for that avatar
	// (the path a completed idle-bake job produced). It is the CURRENT
	// selection, not history — avatar_bake_jobs keeps the latter. Read at
	// startup on top of the --idle-ivf flags, so a hot-applied idle survives a
	// restart; the predecessor field System.IdleAsset was written but never
	// read anywhere, so it never did. Same shallow-merge PUT semantics as
	// Motions: the front end always sends the full map.
	Idles map[string]string `json:"idles"`
	// Mouth holds wav2lipLS lip-blend tuning (live-tunable in the playground,
	// saved here as the default applied at every session start). Other engines
	// ignore it. Part of the avatar group (PUT-able, non-hot).
	Mouth Mouth `json:"mouth"`
	// Chroma holds the green-screen keying knobs the visitor channel page uses
	// when a channel has a bg_image (WebGL keyer in web/user/channel.html).
	// Live-tunable in the playground's 绿幕调试 card, saved here as the default;
	// served publicly via /channel/<slug>/config (presentation-only, safe).
	Chroma Chroma `json:"chroma"`
}

// Chroma are the OBS-style keyer knobs: key_color is the screen color (hex,
// "#00b140" for our bake pipeline's RGB 0,177,64), similarity the UV-distance
// threshold below which a pixel is keyed, smoothness the transition band, and
// spill the despill strength (how hard green excess is compressed to max(r,b)).
type Chroma struct {
	KeyColor   string  `json:"key_color"`
	Similarity float64 `json:"similarity"`
	Smoothness float64 `json:"smoothness"`
	Spill      float64 `json:"spill"`
}

// Mouth are the wav2lipLS paste-blend knobs (workers/avatar/wav2lipls_engine.py):
// gain enlarges lip motion, smooth ramps the silence mouth-close, sil_rms/voice_rms
// bound the voicedness gate.
type Mouth struct {
	Gain     float64 `json:"gain"`
	Smooth   float64 `json:"smooth"`
	SilRms   float64 `json:"sil_rms"`
	VoiceRms float64 `json:"voice_rms"`
}

// Turn selects who detects end-of-turn on voice input, and its knobs. Read
// fresh at each session start (like Avatar), so a PUT applies to the NEXT
// session — existing WebRTC sessions keep the mode they connected with.
type Turn struct {
	// Mode: "local" (default; fsmn-vad + Smart Turn + local barge-in),
	// "server_vad" (qwen realtime acoustic VAD) or "smart_turn" (qwen realtime
	// acoustic+semantic). Cloud modes only take effect when the qwen realtime
	// brain is enabled; otherwise sessions silently fall back to local.
	Mode string `json:"mode"`
	// Local-mode knobs (mirror --turn-grace-ms / --turn-min-interrupt-ms;
	// cloud modes ignore them).
	GraceMS        int `json:"grace_ms"`
	MinInterruptMS int `json:"min_interrupt_ms"`
	// server_vad tuning; 0 = server defaults (threshold 0.5, silence 800ms).
	// smart_turn decides semantically and ignores both.
	VadThreshold float64 `json:"vad_threshold"`
	VadSilenceMS int     `json:"vad_silence_ms"`
}

// Brain selects which "brain" answers chat turns. Read fresh at each session
// start (like Turn), so a PUT applies to the NEXT session.
type Brain struct {
	// Mode: "local" (default; LLM+TTS sentence pipeline) or "qwen" (Qwen
	// realtime speech-to-speech). qwen only takes effect when the realtime
	// credentials (--qwen-rt-url + key) were provided at startup; otherwise
	// sessions silently fall back to local. Passing --qwen-rt-url flips the
	// flag-layer default to "qwen" so pre-console deployments keep working.
	Mode string `json:"mode"`
	// Qwen realtime model id and TTS voice (local mode ignores both; the
	// local TTS voice config does not apply in qwen mode).
	Model string `json:"model"`
	Voice string `json:"voice"`
}

type Config struct {
	LLM    LLM    `json:"llm"`
	TTS    TTS    `json:"tts"`
	RAG    RAG    `json:"rag"`
	System System `json:"system"`
	Avatar Avatar `json:"avatar"`
	Turn   Turn   `json:"turn"`
	Brain  Brain  `json:"brain"`
}

// Groups that can be PUT via the admin API, and whether they apply hot. avatar
// is non-hot like system: persisted and read fresh by the console, but no live
// client swap (nothing consumes it in the pipeline yet). turn and brain count
// as hot: no restart needed, each NEW session reads them fresh (the UI says so).
var Hot = map[string]bool{"llm": true, "tts": true, "rag": true, "system": false, "avatar": false, "turn": true, "brain": true}

func Defaults() Config {
	return Config{
		LLM: LLM{
			Provider:     "openai",
			Model:        "qwen2.5:7b-instruct-q4_K_M",
			SystemPrompt: "你是一个数字人助手，用简短、口语化的中文回答，不要用列表、markdown 或表情符号。",
			Stream:       boolPtr(true),
		},
		TTS: TTS{
			BaseURL:    "http://127.0.0.1:8091",
			Voice:      "vivian",
			VoiceSeeds: map[string]int{},
		},
		RAG: RAG{
			Enabled:      false,
			EmbedURL:     "http://127.0.0.1:11434",
			EmbedModel:   "bge-m3:567m",
			EmbedDim:     1024,
			ChunkSize:    500,
			ChunkOverlap: 50,
			TopK:         5,
			// bge-m3 cosine similarity for a true zh QA hit typically lands
			// around 0.55-0.65 (measured 0.637 on our test doc); 0.7 would
			// filter real hits out.
			Threshold: 0.5,
			// bge-m3 query embed alone measures ~470ms on this ollama; the
			// budget must clear it with headroom or RAG degrades randomly.
			TimeoutMS: 1500,
			// Rerank off by default (empty URL). The model default matches the
			// self-hosted workers/rerank service; candidates 0 = auto.
			RerankModel: "BAAI/bge-reranker-v2-m3",
		},
		System: System{
			Listen:      ":8020",
			AdminListen: "127.0.0.1:9080",
			WorkerAddr:  "127.0.0.1:9401",
			VideoCodec:  "vp8",
			WebDir:      "web/user",
			StunURLs:    "stun:stun.l.google.com:19302",
		},
		Avatar: Avatar{Current: "default", Motions: map[string]string{},
			Mouth:  Mouth{Gain: 1.30, Smooth: 0.34, SilRms: 0.012, VoiceRms: 0.030},
			Chroma: Chroma{KeyColor: "#00b140", Similarity: 0.10, Smoothness: 0.08, Spill: 1.0}},
		Turn: Turn{Mode: "local", GraceMS: 700},
		Brain: Brain{Mode: "local", Model: "qwen-audio-3.0-realtime-flash",
			Voice: "longanqian"},
	}
}

func boolPtr(b bool) *bool { return &b }

// groupPtr returns a pointer to the named group inside cfg, for JSON-merge.
func groupPtr(cfg *Config, group string) (any, error) {
	switch group {
	case "llm":
		return &cfg.LLM, nil
	case "tts":
		return &cfg.TTS, nil
	case "rag":
		return &cfg.RAG, nil
	case "system":
		return &cfg.System, nil
	case "avatar":
		return &cfg.Avatar, nil
	case "turn":
		return &cfg.Turn, nil
	case "brain":
		return &cfg.Brain, nil
	}
	return nil, fmt.Errorf("unknown config group %q", group)
}

// applyLayer overlays one layer (group -> raw JSON object) onto cfg.
// json.Unmarshal onto an existing struct only touches keys present in the
// document, which is exactly the partial-override semantics we want.
func applyLayer(cfg *Config, layer map[string]json.RawMessage) error {
	for group, raw := range layer {
		dst, err := groupPtr(cfg, group)
		if err != nil {
			continue // ignore unrelated rows (e.g. grp='internal')
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			return fmt.Errorf("config group %s: %w", group, err)
		}
	}
	return nil
}

// Validate rejects unknown keys in a PUT body for the given group (typos
// should 400, not silently persist).
func Validate(group string, body []byte) error {
	cfg := Defaults()
	dst, err := groupPtr(&cfg, group)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
