// Package session ties together pion (rtc), the AvatarEngine gRPC stream, the
// media forwarders (mediaenc) and the TTS client into one live WebRTC session —
// replacing LiveTalking's Python RTCManager + HumanPlayer + omnitts in-session
// TTS. It also owns echo-mode turn orchestration (text -> TTS -> AudioChunk into
// the engine). Both media are encoded by the worker; cored just paces them.
package session

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"

	"mynah/core"
	pbasr "mynah/gen/go/proto/asr/v1"
	pb "mynah/gen/go/proto/avatarengine/v1"
	"mynah/internal/asrclient"
	"mynah/internal/avatarcatalog"
	"mynah/internal/engineclient"
	"mynah/internal/llm"
	"mynah/internal/mediaenc"
	"mynah/internal/qwenrt"
	"mynah/internal/rtc"
	"mynah/internal/tts"
)

// ClientBundle is an optional per-session frozen client set. When a session
// carries one (channel-published visitor), it reads its own LLM/TTS/RAG instead
// of the manager's live globals — the published config snapshot stays in effect
// regardless of later config-center edits. nil bundle = use manager globals
// (default :8020 visitor + admin playground, zero behavior change).
type ClientBundle struct {
	LLM       core.ChatProvider
	TTS       *tts.Client
	Retriever core.Retriever
	Reranker  core.Reranker
	RAGOpts   RAGOptions
	Greeting  string // spoken once on connect; "" = no greeting
	// Brain freezes the channel's brain selection at publish time. nil =
	// snapshot predates the brain group; the session inherits the live
	// console selection instead.
	Brain *BrainConfig
	// Engine binds this channel to one avatar engine ("musetalk" /
	// "flashhead" / ...). Empty = any engine, which is the pre-binding
	// behaviour and stays the default so existing channels are unaffected.
	//
	// Normally DERIVED from Avatar rather than configured: a baked avatar can
	// only be rendered by the engine it was baked for, so binding the
	// appearance implies binding the engine.
	Engine string
	// Avatar is the frozen appearance id ("bake:leiya_mt", "builtin:f", ...).
	// Sent to the worker as SessionSpec.cond_image at session start. Empty =
	// keep whatever the worker already has loaded.
	Avatar string
}

// BrainConfig is a frozen brain selection (config group "brain" + the persona
// prompt captured alongside it). Mode "qwen" routes chat turns to the Qwen
// realtime brain when credentials are available; anything else = local.
type BrainConfig struct {
	Mode         string
	Model        string
	Voice        string
	Instructions string
}

// Session is one browser <-> avatar connection.
type Session struct {
	ID     string
	peer   *rtc.Peer
	stream pb.AvatarEngine_SessionClient
	mgr    *Manager          // llm/tts looked up per turn (hot-swappable via config)
	asr    *asrclient.Client // nil = voice input unavailable

	ctx    context.Context
	cancel context.CancelFunc

	createdAt time.Time
	turns     atomic.Int64

	histMu  sync.Mutex
	history []llm.Message // chat history (user/assistant turns)

	venc *mediaenc.VideoEncoder
	aenc *mediaenc.AudioEncoder

	idle       *mediaenc.IdleLoop // non-nil = idle-local mode
	idleSet    *AvatarIdle        // this avatar's idle+actions (nil = engine-driven idle)
	silencePkt []byte
	videoCodec string

	sendMu   sync.Mutex // serialize stream.Send (gRPC client streams are not concurrent-safe)
	speaking atomic.Bool
	lastKF   atomic.Int64 // unix nanos of the last viewer keyframe request we acted on

	// fsm mirrors the speaking/turn transitions into a 4-state view + typed
	// event stream (observability only; speaking stays the hot-path flag). See
	// state.go.
	fsm *convFSM
	// turn is the optional semantic turn-taking orchestrator (Phase 3). nil =
	// legacy "answer every utterance immediately" behavior. See turn.go.
	turn *turnOrchestrator

	// Browser-opened "chat" DataChannel: cored pushes subtitle/reply state
	// as {"status":"start|ing|end","text":...} JSON (LiveTalking protocol).
	dcMu sync.Mutex
	dc   *webrtc.DataChannel

	turnMu     sync.Mutex
	turnCancel context.CancelFunc // cancels the in-flight speak goroutine

	// per-turn latency instrumentation (wall-clock milestones)
	turnStart   atomic.Int64
	turnTTSSeen atomic.Bool
	turnSpkSeen atomic.Bool

	// bundle is an optional per-session frozen client set (channel-published
	// visitor). nil = read the manager's live globals (default visitor +
	// playground). See llmClient/ttsClient/retrieverConf.
	bundle *ClientBundle
	// qwen is the lazy per-session Qwen realtime connection (only used when the
	// manager runs in qwen brain mode; see qwen.go).
	qwen qwenState
	// qwenCfg is this session's resolved brain selection, frozen at create:
	// non-nil = chat turns run against the Qwen realtime speech-to-speech
	// brain, nil = local LLM+TTS pipeline. A config PUT applies to the NEXT
	// session (see qwen.go).
	qwenCfg *qwenrt.Config
	// release is called exactly once when the session is removed (channel slot
	// teardown); nil for non-channel sessions. Invoked from Manager.remove,
	// whose map-delete-under-lock guarantees a single call.
	release   func()
	greetOnce sync.Once // channel greeting spoken at most once, on engine Ready

	// turnMode is this session's resolved turn-taking mode, frozen at create:
	// "local" (default) or "server_vad"/"smart_turn" (cloud VAD, qwen mode
	// only — see cloudvad.go). A config PUT applies to the NEXT session.
	turnMode string
	turnLive TurnModeConfig

	onClose func()
}

// TurnModeConfig is the live turn-taking selection the console edits (config
// group "turn"), read fresh at each session start. Zero values = keep the
// startup flag defaults.
type TurnModeConfig struct {
	Mode           string  // "local" / "server_vad" / "smart_turn"
	GraceMS        int     // local: override --turn-grace-ms when > 0
	MinInterruptMS int     // local: override --turn-min-interrupt-ms when != 0
	VadThreshold   float64 // server_vad tuning (0 = server default)
	VadSilenceMS   int     // server_vad tail silence (0 = server default)
}

// ErrNoFreeWorker is returned when every avatar worker is already serving a
// session. The signaling layer maps it to 503 so visitors see "busy, retry"
// instead of a generic failure.
var ErrNoFreeWorker = errors.New("all avatar workers busy")

// Worker is one avatar-engine endpoint in the dispatch pool. Each engine
// serves a single session at a time, so cored assigns at most one live
// session per worker and picks a free one at session create.
type Worker struct {
	Addr   string
	Detail string // Health detail captured at startup (engine type banner)
	// Engine is the id parsed out of Detail ("musetalk"/"flashhead"/...).
	// Dispatch filters on it so a channel bound to one engine never lands on
	// another. Empty = unrecognized banner; such a worker only serves
	// unbound channels.
	Engine string
	Client *engineclient.Client
	busy   string // session id currently holding this worker ("" = free)
	// draining: the pool reconciler found this worker unhealthy while it was
	// serving a session. It keeps that session to the end but takes no new
	// ones, and is reaped on release. See pool.go.
	draining bool
}

func NewWorker(addr, detail string, cli *engineclient.Client) *Worker {
	return &Worker{Addr: addr, Detail: detail, Engine: EngineFromDetail(detail), Client: cli}
}

// EngineFromDetail maps a worker's Health banner to an engine id. The banners
// come from the workers themselves ("musetalk 1.5 avatar engine", "flashhead
// avatar engine", "wav2lipLS avatar engine (face_size=384)").
func EngineFromDetail(detail string) string {
	d := strings.ToLower(detail)
	switch {
	case strings.Contains(d, "musetalk"):
		return "musetalk"
	case strings.Contains(d, "flashhead"):
		return "flashhead"
	case strings.Contains(d, "wav2lip"):
		return "wav2lipls"
	}
	return ""
}

// Manager creates and tracks sessions, dispatching each onto a free worker
// from the pool (every worker is single-session).
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	workers  []*Worker
	stun     []string
	counter  int64

	// Hot-swappable clients: admin config PUTs replace these while sessions
	// are live, so sessions must read them through LLM()/TTS() each turn.
	cliMu     sync.RWMutex
	tts       *tts.Client
	llm       core.ChatProvider
	retriever core.Retriever // nil = RAG off
	reranker  core.Reranker  // nil = vector order final (EE plugs in)
	ragOpts   RAGOptions

	// Idle-local assets, keyed by avatar id (see idle.go). An avatar with no
	// entry runs engine-driven idle instead. Mutex-guarded: the idle-bake apply
	// path writes it while sessions are being created.
	idleMu       sync.RWMutex
	idles        map[string]*AvatarIdle
	noIdleWarned map[string]bool // avatars already logged as engine-driven idle
	silencePkt   []byte
	videoCodec   string // "vp8" (default) or "h264"
	// avatarCurrent returns the live config.avatar.current (read fresh at each
	// session start; nil = "default"). avatarAssetsDir is the avatar-assets base
	// dir as the WORKER sees it, used to resolve a built-in id to a cond_image
	// path the worker can stat.
	avatarCurrent   func() string
	avatarAssetsDir string
	// avatarResolve overrides the built-in catalog resolution when set —
	// app wires a resolver that also maps trained:<jobId> likenesses to their
	// artifact portraits (a DB lookup this package must stay free of).
	avatarResolve func(id string) string
	// mouthJSON returns marshaled config.avatar.mouth ("" = none); sent to the
	// worker as a session-start SetAction so saved lip-blend defaults apply to
	// all sessions. Marshal lives in the wiring layer to keep this pkg config-free.
	mouthJSON func() string
	asr       *asrclient.Client
	turnDet   core.TurnDetector // nil = no semantic turn-taking (legacy)
	turnCfg   TurnConfig
	// turnModeSource returns the live console turn-mode selection (config
	// group "turn"); nil = always local with flag defaults. Read fresh at each
	// session start, like avatarCurrent.
	turnModeSource func() TurnModeConfig
	// qwenSource resolves a brain selection into a Qwen realtime dial config
	// (nil = local LLM+TTS pipeline; see qwen.go). The argument is a frozen
	// channel snapshot's brain, or nil to read the live console selection.
	// Called once per session at create, like turnModeSource; nil func = qwen
	// credentials not provided at startup.
	qwenSource func(frozen *BrainConfig) *qwenrt.Config
}

func NewManager(workers []*Worker, stun []string, ttsClient *tts.Client) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		workers:  workers,
		stun:     stun,
		tts:      ttsClient,
	}
}

// acquireWorker claims a free worker for sid (nil = all busy). busy is
// authoritative on its own: it is set here before the session registers in
// m.sessions, and cleared by remove()/the create error paths — do NOT
// cross-check m.sessions, a session mid-create isn't registered yet.
// acquireWorker claims a free worker for sid. engine != "" restricts the
// choice to workers of that engine — a channel bound to one engine must never
// be served by another, because each engine drives its own material and the
// visitor would see a different person.
func (m *Manager) acquireWorker(sid, engine string) *Worker {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.workers {
		// Skip draining workers: they are on their way out of the pool and
		// must not pick up new sessions.
		if w.busy != "" || w.draining {
			continue
		}
		if engine != "" && w.Engine != engine {
			continue
		}
		w.busy = sid
		return w
	}
	return nil
}

// hasEngine reports whether the pool contains any worker of an engine, busy or
// not. Used to tell "all busy, retry" apart from "this channel's engine is not
// deployed at all" — very different problems for the operator.
func (m *Manager) hasEngine(engine string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.workers {
		if w.Engine == engine {
			return true
		}
	}
	return false
}

func (m *Manager) releaseWorker(sid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.workers {
		if w.busy == sid {
			w.busy = ""
		}
	}
	// A worker that went unhealthy mid-session leaves the pool now that its
	// session is over.
	m.reapDrainedLocked()
}

// PlayAction queues the named one-shot clip on this session's video track: it
// plays at the next idle tick and the track returns to the idle loop after.
// Actions layer on the same baked-frame domain as the idle loop, so they are
// only meaningful while idle — a running speech turn keeps priority and
// preempts the clip. A session whose avatar has no idle material of its own has
// no actions either (the visitor action bar is empty for it).
func (s *Session) PlayAction(id string) error {
	if s.idleSet == nil {
		return errors.New("this avatar has no baked idle material, so it has no actions")
	}
	clip := s.idleSet.Actions[id]
	if clip == nil {
		return errors.New("unknown action " + id)
	}
	if s.venc == nil {
		return errors.New("session not ready")
	}
	return s.venc.PlayAction(clip)
}

// SetTurnModeSource wires the live console turn-mode selection (config group
// "turn") into session creation; read fresh per session, like SetAvatarSource.
func (m *Manager) SetTurnModeSource(fn func() TurnModeConfig) { m.turnModeSource = fn }

// SetVideoCodec selects the session video codec ("vp8" or "h264"); it must
// match the idle assets when idle-local mode is on.
func (m *Manager) SetVideoCodec(c string) { m.videoCodec = c }

// SetAvatarSource wires the live avatar selection into the engine: current()
// returns config.avatar.current (read at each session start, so a non-hot
// avatar PUT applies to the NEXT session), and assetsDir is the avatar-assets
// base dir as the avatar worker sees it. Built-in ids resolve to a cond_image
// portrait path the worker loads; "default"/unknown/"trained:*" leave the
// worker on its boot --avatar (see avatarcatalog.Resolve).
func (m *Manager) SetAvatarSource(current func() string, assetsDir string) {
	m.avatarCurrent = current
	m.avatarAssetsDir = assetsDir
}

// SetAvatarResolver overrides the id→portrait resolution used at session
// start (and shared with the idle-bake admin extension). Set by app to add
// trained:<jobId> lookups on top of the built-in catalog.
func (m *Manager) SetAvatarResolver(fn func(id string) string) { m.avatarResolve = fn }

// ResolveAvatar maps an avatar id to a worker-readable portrait path using
// the active resolver ("" = no override / unknown).
func (m *Manager) ResolveAvatar(id string) string {
	if m.avatarResolve != nil {
		return m.avatarResolve(id)
	}
	// ResolveAny also handles "bake:*" ids (baked avatar DIRECTORIES, which
	// MuseTalk/wav2lipLS consume) on top of the built-in portrait files.
	return avatarcatalog.ResolveAny(id, m.avatarAssetsDir)
}

// SetMouthParams wires live config.avatar.mouth (JSON) into the session-start
// SetAction so saved wav2lipLS lip-blend defaults apply to every session.
func (m *Manager) SetMouthParams(fn func() string) { m.mouthJSON = fn }

// SetLLM enables chat-mode turns (/human type=chat). Safe to call while
// sessions are live (hot config apply); pass nil to disable chat.
func (m *Manager) SetLLM(c core.ChatProvider) {
	m.cliMu.Lock()
	m.llm = c
	m.cliMu.Unlock()
}

// SetTTS swaps the TTS client (hot config apply).
func (m *Manager) SetTTS(c *tts.Client) {
	m.cliMu.Lock()
	m.tts = c
	m.cliMu.Unlock()
}

// LLM returns the current chat provider (nil = chat unavailable).
func (m *Manager) LLM() core.ChatProvider {
	m.cliMu.RLock()
	defer m.cliMu.RUnlock()
	return m.llm
}

// TTS returns the current TTS client.
func (m *Manager) TTS() *tts.Client {
	m.cliMu.RLock()
	defer m.cliMu.RUnlock()
	return m.tts
}

// SetASR enables voice input: browser-mic audio is segmented + transcribed by
// the ASR worker and each utterance runs a chat turn.
func (m *Manager) SetASR(c *asrclient.Client) { m.asr = c }

// SetTurnDetector enables semantic turn-taking: instead of answering every
// ASR utterance immediately, sessions consult td to decide whether the user
// has finished, coalescing mid-thought pauses and filtering backchannels. cfg
// carries the timing knobs. nil td = legacy immediate behavior.
func (m *Manager) SetTurnDetector(td core.TurnDetector, cfg TurnConfig) {
	m.turnDet = td
	m.turnCfg = cfg
}

func (m *Manager) codec() string {
	if m.videoCodec != "" {
		return m.videoCodec
	}
	return "vp8"
}

func (m *Manager) Get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (m *Manager) remove(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	for _, w := range m.workers {
		if w.busy == id {
			w.busy = ""
		}
	}
	m.mu.Unlock()
	if s != nil {
		// Map-delete under m.mu makes s non-nil for the first remover only, so
		// the channel slot is released exactly once even across concurrent
		// teardown paths (conn-failed, kick, explicit close).
		if s.release != nil {
			s.release()
		}
		s.close()
	}
}

func (m *Manager) nextID() string {
	n := atomic.AddInt64(&m.counter, 1)
	return fmt.Sprintf("%06d", 100000+n)
}

// CreateFromOffer builds a session for a WebRTC offer and returns the answer
// SDP. The session reads the manager's live global clients (default :8020
// visitor + admin playground).
func (m *Manager) CreateFromOffer(offerSDP string) (answerSDP, sessionID string, err error) {
	return m.create(offerSDP, "", nil, nil)
}

// NextSessionID mints a session id up front, so a channel offer can reserve its
// concurrency slot under the very id the session will be created with.
func (m *Manager) NextSessionID() string { return m.nextID() }

// CreateFromOfferChannel builds a session bound to a published channel's frozen
// client bundle, under a caller-provided session id (wantID, from
// NextSessionID). onRelease is called exactly once when the session ends (to
// free the channel's concurrency slot).
func (m *Manager) CreateFromOfferChannel(offerSDP, wantID string, bundle *ClientBundle, onRelease func()) (answerSDP, sessionID string, err error) {
	return m.create(offerSDP, wantID, bundle, onRelease)
}

func (m *Manager) create(offerSDP, wantID string, bundle *ClientBundle, onRelease func()) (answerSDP, sessionID string, err error) {
	sessionID = wantID
	if sessionID == "" {
		sessionID = m.nextID()
	}
	ctx, cancel := context.WithCancel(context.Background())

	// Resolve the effective avatar FIRST: it decides which engine can serve this
	// session, so it has to be known before a worker is picked. avatarID is
	// forwarded for logging/future use; the worker switches on cond_image (empty
	// = keep its boot --avatar; missing path = worker logs + keeps it).
	avatarID := m.currentAvatarID()
	// A channel's frozen avatar WINS over the live global selection — that is
	// the whole point of binding: switching the console's global avatar must
	// not change the face of an already-published channel.
	if bundle != nil {
		avatarID = m.EffectiveAvatarID(bundle.Avatar)
	}
	condImage := m.ResolveAvatar(avatarID)

	// Engine binding: a channel bound to one engine must not be served by
	// another, since each engine drives its own material.
	//
	// The bundle's engine is the channel's frozen intent. Fall back to the
	// avatar's own engine so the unbundled paths are covered too — the visitor
	// default and the admin playground both come through here with bundle == nil
	// and inherit the live global avatar. Without this fallback a mixed pool
	// serves them from whichever engine happened to be free, and MuseTalk handed
	// a portrait file (or FlashHead handed a bake directory) does not fail: it
	// keeps the face it booted with, so the visitor meets the wrong person.
	wantEngine := ""
	if bundle != nil {
		wantEngine = bundle.Engine
	}
	if wantEngine == "" {
		wantEngine = avatarcatalog.EngineFor(avatarID)
	}
	worker := m.acquireWorker(sessionID, wantEngine)
	if worker == nil {
		cancel()
		if wantEngine != "" && !m.hasEngine(wantEngine) {
			// Not "busy" but "not deployed" — very different for the operator.
			return "", "", fmt.Errorf("%w: no worker running engine %q",
				ErrNoFreeWorker, wantEngine)
		}
		return "", "", ErrNoFreeWorker
	}
	stream, err := worker.Client.AE.Session(ctx)
	if err != nil {
		m.releaseWorker(sessionID)
		cancel()
		return "", "", fmt.Errorf("open engine session: %w", err)
	}
	peer, err := rtc.New(m.stun, m.codec())
	if err != nil {
		m.releaseWorker(sessionID)
		cancel()
		return "", "", fmt.Errorf("new peer: %w", err)
	}

	// This avatar's own idle material. Nil = it has none, so the session runs
	// with idle-local OFF and the worker drives the engine with silence: costs
	// GPU, but shows the right person. Baking this avatar an idle puts it back
	// on the zero-cost replay path.
	idleSet := m.idleFor(avatarID)
	if idleSet == nil {
		m.warnNoIdleOnce(avatarID)
	}

	s := &Session{
		ID: sessionID, peer: peer, stream: stream, mgr: m,
		asr: m.asr,
		ctx: ctx, cancel: cancel,
		createdAt: time.Now(),
		idleSet:   idleSet, silencePkt: m.silence(), videoCodec: m.codec(),
		bundle: bundle, release: onRelease,
	}
	// Viewer PLI/FIR -> keyframe: speech frames are re-keyed by the worker,
	// the idle replay re-enters its loop at a keyframe.
	peer.SetKeyframeHandler(s.onKeyframeRequest)
	if idleSet != nil {
		s.idle = idleSet.Idle
	}
	s.fsm = newConvFSM(s.emitEvent)

	// Resolve this session's brain (config group "brain", frozen for the
	// session's lifetime): non-nil = Qwen realtime speech-to-speech, nil =
	// local LLM+TTS. A channel bundle's published snapshot wins over the live
	// console selection. Resolved before turn mode — cloud turn modes need it.
	if m.qwenSource != nil {
		var frozen *BrainConfig
		if bundle != nil {
			frozen = bundle.Brain
		}
		s.qwenCfg = m.qwenSource(frozen)
	}

	// Resolve this session's turn-taking mode from the live console selection
	// (frozen for the session's lifetime; cloud modes need the qwen brain).
	tm := TurnModeConfig{}
	if m.turnModeSource != nil {
		tm = m.turnModeSource()
	}
	mode := tm.Mode
	if mode != "server_vad" && mode != "smart_turn" {
		mode = "local"
	}
	if mode != "local" && !s.qwenMode() {
		log.Printf("[session %s] turn mode %q needs the qwen realtime brain; falling back to local", sessionID, mode)
		mode = "local"
	}
	s.turnMode, s.turnLive = mode, tm

	if m.turnDet != nil && mode == "local" {
		// Console-tuned knobs override the startup flags for this session.
		cfg := m.turnCfg
		if tm.GraceMS > 0 {
			cfg.GraceMS = tm.GraceMS
		}
		if tm.MinInterruptMS != 0 {
			cfg.MinInterruptMS = tm.MinInterruptMS
		}
		s.turn = newTurnOrchestrator(s, m.turnDet, cfg)
	}
	s.onClose = func() { m.remove(sessionID) }

	if s.qwenMode() {
		// Pre-warm the realtime WebSocket so the first turn doesn't pay the
		// ~1s dial+handshake (qwenConn redials lazily if this races/fails).
		go func() { _, _ = s.qwenConn() }()
	}

	if s.qwenRAGUnavailable() {
		// Fail loudly rather than answering ungrounded: the console shows a
		// knowledge base attached, but this session cannot consult it.
		log.Printf("[session %s] ⚠ 知识库已配置但本会话用不上："+
			"云端大脑 + 云端轮次(%s)下服务端在收到问题的同时就开始生成，"+
			"没有插入检索结果的时机。把「对话轮次」切到本地即可恢复。", sessionID, s.turnMode)
	}

	if s.asr != nil && (s.llmClient() != nil || s.qwenMode()) {
		// Voice input: the browser may send a mic audio track (sendrecv offer).
		peer.PC.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			if track.Kind() != webrtc.RTPCodecTypeAudio {
				return
			}
			log.Printf("[session %s] mic track up: %s", sessionID, track.Codec().MimeType)
			if s.cloudVADMode() {
				// Turn-taking delegated to the qwen server (see cloudvad.go):
				// the local VAD/ASR/turn stack is bypassed for this session.
				go s.listenMicCloud(track)
			} else {
				go s.listenMic(track)
			}
		})
	}

	peer.PC.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[session %s] conn state: %s", sessionID, state)
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			m.remove(sessionID)
		case webrtc.PeerConnectionStateDisconnected:
			// A page refresh usually skips the clean close, and ICE takes ~30s
			// to reach failed — meanwhile this session holds the single engine
			// slot and every reconnect bounces off "engine busy". Give the ICE
			// a short grace to recover, then free the slot.
			go func() {
				timer := time.NewTimer(4 * time.Second)
				defer timer.Stop()
				select {
				case <-s.ctx.Done():
				case <-timer.C:
					if peer.PC.ConnectionState() == webrtc.PeerConnectionStateDisconnected {
						log.Printf("[session %s] disconnected >4s, releasing", sessionID)
						m.remove(sessionID)
					}
				}
			}()
		}
	})

	// Subtitle/typing feed: if the browser opens a DataChannel, reply text
	// streams over it alongside the media (no extra transport or auth).
	peer.PC.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("[session %s] datachannel up: %s", sessionID, dc.Label())
		s.dcMu.Lock()
		s.dc = dc
		s.dcMu.Unlock()
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			// application-level ping/pong keepalive
			if string(msg.Data) == "ping" {
				_ = dc.SendText("pong")
				return
			}
			// Live debug control: {"type":"mouth", ...knobs} -> forward knobs to
			// the worker as a SetAction (wav2lipLS applies live; others ignore).
			var raw map[string]json.RawMessage
			if json.Unmarshal(msg.Data, &raw) != nil {
				return
			}
			var typ string
			if tv, ok := raw["type"]; ok {
				_ = json.Unmarshal(tv, &typ)
			}
			if typ == "mouth" {
				delete(raw, "type")
				if b, err := json.Marshal(raw); err == nil {
					_ = s.send(&pb.ClientFrame{Msg: &pb.ClientFrame_SetAction{
						SetAction: &pb.SetAction{Action: "mouth:" + string(b)}}})
				}
			}
		})
	})

	go s.recvLoop()

	if err := s.send(&pb.ClientFrame{Msg: &pb.ClientFrame_Start{
		Start: &pb.SessionSpec{SessionId: sessionID, AvChunks: true,
			AvatarId: avatarID, CondImage: condImage,
			IdleLocal: s.idle != nil, VideoCodec: s.videoCodec},
	}}); err != nil {
		m.releaseWorker(sessionID)
		cancel()
		peer.Close()
		return "", "", fmt.Errorf("send sessionspec: %w", err)
	}

	// Apply saved mouth-blend defaults right after SessionSpec on the ordered
	// stream (worker has created the session before it applies). wav2lipLS
	// consumes "mouth:{json}"; other engines ignore it.
	if m.mouthJSON != nil {
		if mj := m.mouthJSON(); mj != "" {
			_ = s.send(&pb.ClientFrame{Msg: &pb.ClientFrame_SetAction{
				SetAction: &pb.SetAction{Action: "mouth:" + mj}}})
		}
	}

	answerSDP, err = peer.Answer(offerSDP)
	if err != nil {
		m.releaseWorker(sessionID)
		cancel()
		peer.Close()
		return "", "", fmt.Errorf("answer: %w", err)
	}

	m.mu.Lock()
	m.sessions[sessionID] = s
	m.mu.Unlock()
	log.Printf("[session %s] created", sessionID)
	return answerSDP, sessionID, nil
}

// llmClient/ttsClient/retrieverConf return this session's clients: the frozen
// channel bundle if present, else the manager's live globals. Reads are
// lock-free for channel sessions (bundle is immutable after construction) and
// fall through to the manager's RLock-guarded accessors otherwise — so the
// global hot path is unchanged.
func (s *Session) llmClient() core.ChatProvider {
	if s.bundle != nil {
		return s.bundle.LLM
	}
	return s.mgr.LLM()
}

func (s *Session) ttsClient() *tts.Client {
	if s.bundle != nil {
		return s.bundle.TTS
	}
	return s.mgr.TTS()
}

func (s *Session) retrieverConf() (core.Retriever, core.Reranker, RAGOptions) {
	if s.bundle != nil {
		return s.bundle.Retriever, s.bundle.Reranker, s.bundle.RAGOpts
	}
	return s.mgr.retrieverConf()
}

// sinceTurnMs returns ms elapsed since the current turn began.
func (s *Session) sinceTurnMs() int64 {
	return (time.Now().UnixNano() - s.turnStart.Load()) / 1e6
}

// listenMic forwards the browser's mic Opus packets to the ASR worker and
// reacts to its events: speech onset barges in over a talking avatar, and
// each final transcript runs a chat turn (same path as typed /human chat).
func (s *Session) listenMic(track *webrtc.TrackRemote) {
	stream, err := s.asr.ASR.Session(s.ctx)
	if err != nil {
		log.Printf("[session %s] asr session: %v", s.ID, err)
		return
	}
	if err := stream.Send(&pbasr.ClientFrame{Msg: &pbasr.ClientFrame_Start{
		Start: &pbasr.SessionSpec{SessionId: s.ID, Codec: "opus", Language: "zh",
			WantUtteranceAudio: s.turn != nil},
	}}); err != nil {
		log.Printf("[session %s] asr start: %v", s.ID, err)
		return
	}

	// worker -> cored: VAD edges + transcripts
	go func() {
		for {
			sf, err := stream.Recv()
			if err != nil {
				if s.ctx.Err() == nil {
					log.Printf("[session %s] asr recv ended: %v", s.ID, err)
				}
				return
			}
			switch x := sf.Msg.(type) {
			case *pbasr.ServerFrame_Vad:
				if x.Vad.Speech {
					s.fsm.emit(EvUserSpeechStart, "")
				} else {
					s.fsm.emit(EvUserSpeechEnd, "")
				}
				if s.turn != nil {
					// Semantic turn-taking owns barge-in (min-duration filter).
					s.turn.onVad(x.Vad.Speech, x.Vad.Utterance)
				} else if x.Vad.Speech && s.speaking.Load() {
					// Legacy: any speech onset while speaking barges in.
					log.Printf("[session %s] mic barge-in (utt=%d)", s.ID, x.Vad.Utterance)
					s.Interrupt()
				}
			case *pbasr.ServerFrame_UtteranceAudio:
				// The utterance's audio, just ahead of its transcript — stash it
				// for the audio turn detector (only sent when s.turn != nil).
				if s.turn != nil {
					s.turn.onUtteranceAudio(x.UtteranceAudio.Utterance, x.UtteranceAudio.Pcm)
				}
			case *pbasr.ServerFrame_Transcript:
				t := x.Transcript
				log.Printf("[session %s] mic utt=%d: %q", s.ID, t.Utterance, t.Text)
				if strings.TrimSpace(t.Text) != "" {
					s.dcEmit("user", t.Text) // mirror the recognized question to the page
					s.fsm.emit(EvTranscript, t.Text)
					if s.turn != nil {
						// Hold/coalesce/commit per the turn detector.
						s.turn.onTranscript(t.Text, t.Utterance)
					} else {
						s.Chat(t.Text)
					}
				}
			case *pbasr.ServerFrame_Error:
				log.Printf("[session %s] asr error: %s", s.ID, x.Error.Message)
			}
		}
	}()

	// browser -> worker: RTP Opus payloads verbatim
	seq := uint64(0)
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			if s.ctx.Err() == nil {
				log.Printf("[session %s] mic track ended: %v", s.ID, err)
			}
			_ = stream.Send(&pbasr.ClientFrame{Msg: &pbasr.ClientFrame_Close{Close: &pbasr.Close{}}})
			return
		}
		if len(pkt.Payload) == 0 {
			continue
		}
		if err := stream.Send(&pbasr.ClientFrame{Msg: &pbasr.ClientFrame_Audio{
			Audio: &pbasr.AudioPacket{Data: pkt.Payload, Seq: seq},
		}}); err != nil {
			log.Printf("[session %s] asr send: %v", s.ID, err)
			return
		}
		seq++
	}
}

func (s *Session) recvLoop() {
	for {
		sf, err := s.stream.Recv()
		if err != nil {
			log.Printf("[session %s] recv ended: %v", s.ID, err)
			return
		}
		switch x := sf.Msg.(type) {
		case *pb.ServerFrame_Ready:
			r := x.Ready
			log.Printf("[session %s] engine ready %dx%d@%dfps sr=%d idle_local=%v codec=%s",
				s.ID, r.Width, r.Height, r.Fps, r.SampleRate, s.idle != nil, s.videoCodec)
			// Shared start barrier so video-frame-0 and audio-packet-0 are lip-synced.
			gate := mediaenc.NewGate(2)
			venc, err := mediaenc.NewVideoEncoder(s.peer.Video, int(r.Fps), s.ID, gate, s.idle, s.videoCodec)
			if err != nil {
				log.Printf("[session %s] video encoder: %v", s.ID, err)
				return
			}
			aenc, err := mediaenc.NewAudioEncoder(s.peer.Audio, s.ID, gate, s.silencePkt)
			if err != nil {
				log.Printf("[session %s] audio encoder: %v", s.ID, err)
				return
			}
			s.venc, s.aenc = venc, aenc
			// Channel greeting: speak the opening line once, now that the
			// encoders exist (audio chunks would be dropped before Ready).
			if s.bundle != nil && s.bundle.Greeting != "" {
				s.greetOnce.Do(func() { go s.Speak(s.bundle.Greeting) })
			}
		case *pb.ServerFrame_Video:
			// x.Video.Data is a complete VP8 frame (worker-side libvpx).
			if s.venc != nil {
				_ = s.venc.WriteFrame(x.Video.Data)
			}
		case *pb.ServerFrame_Audio:
			// x.Audio.PcmF32Le_16K now carries a worker-encoded Opus packet.
			pkt := x.Audio.PcmF32Le_16K
			// Latency probe: a non-tiny Opus packet = real speech (silence ≈3B).
			if s.turnStart.Load() != 0 && !s.turnSpkSeen.Load() && len(pkt) > 20 {
				if s.turnSpkSeen.CompareAndSwap(false, true) {
					log.Printf("[turn %s] first speech audio from engine +%dms",
						s.ID, s.sinceTurnMs())
				}
			}
			if s.aenc != nil {
				_ = s.aenc.WriteOpus(pkt)
			}
		case *pb.ServerFrame_SpeechEnd:
			log.Printf("[session %s] speech end (gen=%d)", s.ID, x.SpeechEnd.Gen)
			s.speaking.Store(false)
			s.fsm.to(StateIdle, EvAgentSpeechEnd, "")
			if s.venc != nil {
				s.venc.SpeechEnd()
			}
		case *pb.ServerFrame_Stats:
			log.Printf("[session %s] stats: rtf=%.2f frames=%d",
				s.ID, x.Stats.Rtf, x.Stats.Frames)
		case *pb.ServerFrame_Error:
			log.Printf("[session %s] engine error: %s", s.ID, x.Error.Message)
		}
	}
}

// onKeyframeRequest runs on the viewer's RTCP PLI / FIR. Rate-limited: a
// burst of PLIs after one loss should cost one IDR, not one per packet.
func (s *Session) onKeyframeRequest() {
	now := time.Now().UnixNano()
	if now-s.lastKF.Load() < int64(300*time.Millisecond) {
		return
	}
	s.lastKF.Store(now)
	if s.venc != nil {
		s.venc.RequestKeyframe()
	}
	if s.speaking.Load() {
		_ = s.send(&pb.ClientFrame{Msg: &pb.ClientFrame_SetAction{
			SetAction: &pb.SetAction{Action: "keyframe"}}})
	}
	log.Printf("[session %s] viewer requested keyframe (PLI), speaking=%v", s.ID, s.speaking.Load())
}

func (s *Session) send(cf *pb.ClientFrame) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(cf)
}

// dcEmit pushes one subtitle/state event to the browser's DataChannel (no-op
// when the browser didn't open one). Protocol matches LiveTalking:
// {"status":"start"|"ing"|"end","text":...}, plus "user" for mic transcripts.
func (s *Session) dcEmit(status, text string) {
	s.dcMu.Lock()
	dc := s.dc
	s.dcMu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	b, _ := json.Marshal(map[string]string{"status": status, "text": text})
	_ = dc.SendText(string(b))
}

// emitEvent is the convFSM sink: it ships one structured event over the same
// DataChannel as a {"type":"event",...} message — additive to the {"status":...}
// subtitle protocol above (old clients ignore the unknown shape) — and logs it.
func (s *Session) emitEvent(ev Event) {
	log.Printf("[session %s] event=%s state=%s%s", s.ID, ev.Type, ev.State,
		func() string {
			if ev.Text != "" {
				return " text=" + ev.Text
			}
			return ""
		}())
	s.dcMu.Lock()
	dc := s.dc
	s.dcMu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	b, _ := json.Marshal(map[string]any{
		"type": "event", "event": ev.Type, "state": ev.State, "text": ev.Text,
	})
	_ = dc.SendText(string(b))
}

// State returns the current FSM state string (for admin observation).
func (s *Session) State() string { return s.fsm.get().String() }

// subtitleLag approximates the fixed delay between feeding TTS pcm into the
// engine and the browser actually playing it (0.96s chunk pairing + pacing +
// jitter buffer). Subtitles are scheduled on the audio timeline plus this lag.
const subtitleLag = 800 * time.Millisecond

// pcmDur converts a 16k f32 sample count into play duration.
func pcmDur(samples int) time.Duration {
	return time.Duration(samples) * time.Second / 16000
}

// scheduleDC emits a DataChannel event at a wall-clock instant — unless the
// turn was canceled first (the interrupt path emits its own "end").
func (s *Session) scheduleDC(ctx context.Context, at time.Time, status, text string) {
	if d := time.Until(at); d > 0 {
		time.AfterFunc(d, func() {
			if ctx.Err() == nil {
				s.dcEmit(status, text)
			}
		})
		return
	}
	if ctx.Err() == nil {
		s.dcEmit(status, text)
	}
}

// Speak runs an echo turn: synthesize text via TTS and feed AudioChunks to the
// engine. A new Speak cancels any in-flight turn.
//
// Qwen brain mode routes to the cloud model instead: local TTS would answer in
// a different voice than every other utterance of the session (this is what
// made the channel greeting sound like a second person).
func (s *Session) Speak(text string) {
	if s.qwenMode() {
		s.qwenSpeak(text)
		return
	}
	s.turnMu.Lock()
	if s.turnCancel != nil {
		s.turnCancel()
	}
	tctx, tcancel := context.WithCancel(s.ctx)
	s.turnCancel = tcancel
	s.turnMu.Unlock()

	s.turnStart.Store(time.Now().UnixNano())
	s.turnTTSSeen.Store(false)
	s.turnSpkSeen.Store(false)
	s.turns.Add(1)
	log.Printf("[turn %s] start: %q", s.ID, text)
	s.dcEmit("start", "")

	go func() {
		s.speaking.Store(true)
		s.fsm.to(StateSpeaking, EvAgentSpeechStart, "")
		defer s.speaking.Store(false)
		seq := uint64(0)
		subTitled := false
		var playhead time.Time
		err := s.ttsClient().Stream(tctx, text, func(pcm []float32) {
			if s.turnTTSSeen.CompareAndSwap(false, true) {
				log.Printf("[turn %s] tts first pcm +%dms", s.ID, s.sinceTurnMs())
			}
			if !subTitled {
				subTitled = true
				playhead = time.Now()
				s.scheduleDC(tctx, playhead.Add(subtitleLag), "ing", text)
			}
			playhead = playhead.Add(pcmDur(len(pcm)))
			buf := make([]byte, len(pcm)*4)
			for i, v := range pcm {
				binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
			}
			_ = s.send(&pb.ClientFrame{Msg: &pb.ClientFrame_Audio{
				Audio: &pb.AudioChunk{PcmF32Le_16K: buf, Seq: seq},
			}})
			seq++
		})
		if err != nil && tctx.Err() == nil {
			log.Printf("[session %s] tts error: %v", s.ID, err)
		}
		if playhead.IsZero() {
			s.dcEmit("end", "")
		} else {
			s.scheduleDC(tctx, playhead.Add(subtitleLag), "end", "")
		}
	}()
}

// Chat runs an LLM turn: stream tokens from the model, slice them into
// punctuation-bounded sentences, and speak each sentence through TTS -> engine
// as soon as it completes — first audio starts while the model is still
// generating. A new Chat/Speak (or Interrupt) cancels the in-flight turn.
func (s *Session) Chat(text string) {
	// Qwen realtime brain mode: one speech-to-speech turn replaces LLM+TTS.
	// Typed/injected text goes up as an input_text item (no utterance audio).
	if s.qwenMode() {
		s.qwenChat(text, nil)
		return
	}
	// Pin this turn's clients once: a hot config swap mid-turn shouldn't mix
	// two models inside one reply.
	llmCli, ttsCli := s.llmClient(), s.ttsClient()
	if llmCli == nil {
		log.Printf("[session %s] chat requested but no LLM configured; echoing", s.ID)
		s.Speak(text)
		return
	}
	s.turnMu.Lock()
	if s.turnCancel != nil {
		s.turnCancel()
	}
	tctx, tcancel := context.WithCancel(s.ctx)
	s.turnCancel = tcancel
	s.turnMu.Unlock()

	s.turnStart.Store(time.Now().UnixNano())
	s.turnTTSSeen.Store(false)
	s.turnSpkSeen.Store(false)
	s.turns.Add(1)
	log.Printf("[turn %s] chat start: %q", s.ID, text)
	s.dcEmit("start", "")

	s.histMu.Lock()
	s.history = append(s.history, llm.Message{Role: "user", Content: text})
	if len(s.history) > 20 { // keep the last 10 exchanges
		s.history = append([]llm.Message(nil), s.history[len(s.history)-20:]...)
	}
	hist := append([]llm.Message(nil), s.history...)
	s.histMu.Unlock()

	go func() {
		s.speaking.Store(true)
		s.fsm.to(StateThinking, EvAgentThinking, "")
		defer s.speaking.Store(false)

		// Sentence pipeline: the slicer feeds completed sentences in, the
		// speaker goroutine TTS-streams them to the engine strictly in order.
		// Subtitles ride the audio timeline: each sentence's "ing" fires when
		// its audio is estimated to start playing (playhead + fixed lag), not
		// when the LLM produced it — the model runs seconds ahead of speech.
		sentences := make(chan string, 16)
		done := make(chan struct{})
		go func() {
			defer close(done)
			seq := uint64(0)
			llmSeen := false
			var playhead time.Time // when the next fed pcm will start playing
			for sent := range sentences {
				if tctx.Err() != nil {
					continue // drain
				}
				if !llmSeen {
					llmSeen = true
					log.Printf("[turn %s] first sentence ready +%dms: %q",
						s.ID, s.sinceTurnMs(), sent)
				}
				subTitled := false
				err := ttsCli.Stream(tctx, sent, func(pcm []float32) {
					if s.turnTTSSeen.CompareAndSwap(false, true) {
						log.Printf("[turn %s] tts first pcm +%dms", s.ID, s.sinceTurnMs())
						s.fsm.to(StateSpeaking, EvAgentSpeechStart, "")
					}
					if !subTitled {
						subTitled = true
						if now := time.Now(); playhead.Before(now) {
							playhead = now
						}
						s.scheduleDC(tctx, playhead.Add(subtitleLag), "ing", sent)
					}
					playhead = playhead.Add(pcmDur(len(pcm)))
					buf := make([]byte, len(pcm)*4)
					for i, v := range pcm {
						binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
					}
					_ = s.send(&pb.ClientFrame{Msg: &pb.ClientFrame_Audio{
						Audio: &pb.AudioChunk{PcmF32Le_16K: buf, Seq: seq},
					}})
					seq++
				})
				if err != nil && tctx.Err() == nil {
					log.Printf("[session %s] tts error: %v", s.ID, err)
				}
			}
			// "end" when the last fed audio finishes playing (or now if none).
			if playhead.IsZero() {
				s.dcEmit("end", "")
			} else {
				s.scheduleDC(tctx, playhead.Add(subtitleLag), "end", "")
			}
		}()

		var acc strings.Builder
		flush := func(force bool) {
			t := acc.String()
			if t == "" {
				return
			}
			// hold tiny fragments ("好，") for the next boundary unless forced
			if force || len([]rune(t)) >= 6 {
				if strings.TrimSpace(t) != "" {
					select {
					case sentences <- t:
					case <-tctx.Done():
					}
				}
				acc.Reset()
			}
		}
		var tagBuf strings.Builder // partial tag spanning deltas
		think := false             // inside <think>...</think> (reasoning models)
		feedRune := func(r rune) {
			acc.WriteRune(r)
			switch r {
			case '。', '！', '？', '；', '：', '，', '.', '!', '?', ';', ',', '\n':
				flush(false)
			}
		}
		reply, err := llmCli.StreamChat(tctx, s.ID, hist, s.ragContext(tctx, text), func(delta string) {
			// Strip <think>...</think> blocks (reasoning models) before the
			// sentence slicer; tags may span delta boundaries via tagBuf.
			tagBuf.WriteString(delta)
			text := tagBuf.String()
			tagBuf.Reset()
			for text != "" {
				if think {
					if i := strings.Index(text, "</think>"); i >= 0 {
						think = false
						text = text[i+len("</think>"):]
						continue
					}
					// keep a tail in case "</think>" is split across deltas
					if n := len(text); n > 8 {
						text = text[n-8:]
					}
					tagBuf.WriteString(text)
					return
				}
				if i := strings.Index(text, "<think>"); i >= 0 {
					for _, r := range text[:i] {
						feedRune(r)
					}
					think = true
					text = text[i+len("<think>"):]
					continue
				}
				// emit all but a small tail that could be a split "<think>"
				keep := 0
				if j := strings.LastIndexByte(text, '<'); j >= 0 && len(text)-j < 7 {
					keep = len(text) - j
				}
				for _, r := range text[:len(text)-keep] {
					feedRune(r)
				}
				tagBuf.WriteString(text[len(text)-keep:])
				return
			}
		})
		// flush any held-back tail (e.g. a lone '<' that wasn't a tag)
		if !think {
			for _, r := range tagBuf.String() {
				feedRune(r)
			}
		}
		flush(true)
		close(sentences)
		<-done
		if err != nil && tctx.Err() == nil {
			log.Printf("[session %s] llm error: %v", s.ID, err)
		}
		if reply != "" {
			s.histMu.Lock()
			s.history = append(s.history, llm.Message{Role: "assistant", Content: reply})
			s.histMu.Unlock()
			log.Printf("[turn %s] llm reply (%d chars) done +%dms",
				s.ID, len([]rune(reply)), s.sinceTurnMs())
		}
	}()
}

// ChatVoice runs a chat turn for a committed VOICE turn. In qwen brain mode
// the utterance audio itself (f32le mono 16k) goes to the model when the turn
// stack captured it — tone and paralinguistics survive; otherwise identical to
// Chat(text).
func (s *Session) ChatVoice(text string, pcm []byte) {
	if s.qwenMode() {
		s.qwenChat(text, pcm)
		return
	}
	s.Chat(text)
}

// Interrupt barges in: cancel the current turn and tell the engine to drop
// in-flight generation (bumps gen). The worker falls back to idle.
func (s *Session) Interrupt() {
	s.turnMu.Lock()
	if s.turnCancel != nil {
		s.turnCancel()
		s.turnCancel = nil
	}
	s.turnMu.Unlock()
	if s.qwenMode() {
		s.qwenCancel() // abort the in-flight cloud response too
	}
	s.speaking.Store(false)
	s.fsm.to(StateIdle, EvInterrupted, "")
	s.dcEmit("end", "")
	_ = s.send(&pb.ClientFrame{Msg: &pb.ClientFrame_Interrupt{Interrupt: &pb.Interrupt{}}})
	// Drop the ~0.8s of buffered speech so the barge-in lands instantly.
	if s.venc != nil {
		s.venc.Flush()
	}
	if s.aenc != nil {
		s.aenc.Flush()
	}
}

func (s *Session) IsSpeaking() bool { return s.speaking.Load() }

func (s *Session) close() {
	log.Printf("[session %s] closing", s.ID)
	s.qwenClose()
	_ = s.send(&pb.ClientFrame{Msg: &pb.ClientFrame_Close{Close: &pb.Close{}}})
	s.cancel()
	if s.venc != nil {
		s.venc.Close()
	}
	if s.aenc != nil {
		s.aenc.Close()
	}
	s.peer.Close()
}
