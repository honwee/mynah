// Package core defines the seam between the OSS runtime and pluggable
// implementations. The control plane (internal/control) reaches the runtime
// ONLY through these interfaces; a future EE module provides its own
// implementations (multi-tenant auth, reranking, scheduling) by registering
// them at assembly time in its own cmd/ — never by patching OSS sources.
package core

import (
	"context"
	"time"
)

// AuthProvider authenticates admin API requests. OSS ships a single-admin
// JWT implementation (internal/auth); EE may swap in RBAC/SSO.
type AuthProvider interface {
	// Login verifies credentials and returns a bearer token.
	Login(ctx context.Context, username, password string) (token string, mustChange bool, err error)
	// Verify validates a bearer token and returns the authenticated identity.
	Verify(ctx context.Context, token string) (*Identity, error)
	// ChangePassword rotates the password for the authenticated identity.
	ChangePassword(ctx context.Context, ident *Identity, oldPw, newPw string) error
}

type Identity struct {
	ID       int64
	Username string
}

// FeatureGate answers "is this capability enabled in this build/license".
// OSS returns the static community set.
type FeatureGate interface {
	Enabled(feature string) bool
}

// Retriever is the online RAG query path, called by cored inside a chat turn.
type Retriever interface {
	Retrieve(ctx context.Context, query string, topK int, threshold float32) ([]Chunk, error)
}

// Reranker reorders retrieved chunks. OSS has no implementation (P4 leaves
// the seam); EE plugs a cross-encoder service here.
type Reranker interface {
	Rerank(ctx context.Context, query string, chunks []Chunk) ([]Chunk, error)
}

type Chunk struct {
	DocID    int64   `json:"doc_id"`
	Filename string  `json:"filename"`
	Seq      int     `json:"seq"`
	Content  string  `json:"content"`
	Score    float32 `json:"score"`
	// RerankScore is set only when a Reranker reordered this chunk (EE).
	RerankScore float32 `json:"rerank_score,omitempty"`
}

// SessionAdmin exposes live-session observation/control to the control plane.
type SessionAdmin interface {
	ListSessions() []SessionInfo
	GetSession(id string) (*SessionInfo, bool)
	KickSession(id string) bool
}

// WorkerReporter reports the avatar-worker dispatch pool (each worker is a
// single-session engine; Session is the live session holding it, "" = free).
// Optional on SessionAdmin — the console shows the pool when present.
type WorkerReporter interface {
	ListWorkers() []WorkerInfo
}

type WorkerInfo struct {
	Addr    string `json:"addr"`
	Detail  string `json:"detail"`
	Session string `json:"session,omitempty"`
}

// ChatDebugger runs one stateless chat turn — live LLM config + RAG injection,
// no TTS or avatar — for the console playground. The caller owns the history;
// the last user message is the RAG query.
type ChatDebugger interface {
	DebugChat(ctx context.Context, history []ChatMessage) (DebugChatResult, error)
}

// ChatStreamDebugger is the streaming variant: onDelta receives reply
// fragments as they arrive (one final call with the whole reply when the
// provider runs non-streaming). The console renders them as typing.
type ChatStreamDebugger interface {
	DebugChatStream(ctx context.Context, history []ChatMessage, onDelta func(string)) (DebugChatResult, error)
}

// AvatarDebugger lets the console playground drive a real avatar session —
// WebRTC offer/answer plus text turns — through the authenticated admin
// plane. It is the same session pipeline a visitor uses (the worker serves
// one session at a time, so a debug session competes with visitors).
type AvatarDebugger interface {
	// AvatarOffer creates a session from a browser offer; non-trickle, the
	// returned answer carries gathered ICE candidates.
	AvatarOffer(offerSDP string) (answerSDP, sessionID string, err error)
	// AvatarDrive runs one turn: mode "chat" goes through the LLM, anything
	// else speaks the text verbatim (echo — exercises TTS + lip sync only).
	AvatarDrive(sessionID, text, mode string, interrupt bool) error
	// AvatarInterrupt cancels the in-flight turn (barge-in).
	AvatarInterrupt(sessionID string) error
	// AvatarActions lists the baked one-shot action clips (动作编排 ids, display
	// order; empty = none installed). AvatarAction plays one on the session:
	// starts at the next idle tick, returns to the idle loop when done.
	AvatarActions() []string
	AvatarAction(sessionID, action string) error
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// TurnDetector decides whether the user has finished speaking. Given the AUDIO
// of the just-finished utterance (16 kHz mono float32 LE PCM, last <=8 s), it
// returns P(end-of-turn): a low value means the user is likely still going, so
// cored holds the turn open and coalesces the next utterance instead of
// answering prematurely. eot is the worker's thresholded decision. OSS wires a
// gRPC client to the turn worker; a nil TurnDetector = the legacy "answer every
// utterance immediately" behavior (no semantic turn-taking).
type TurnDetector interface {
	Predict(ctx context.Context, audioPCM []byte, lang string) (eotProb float32, eot bool, err error)
}

// ChatProvider runs one streaming chat turn. The OSS openai provider covers
// every OpenAI-compatible endpoint (ollama, vLLM, FastGPT, RAGFlow, clouds);
// dify/coze adapters translate to their native protocols.
//
// convKey identifies the conversation for providers with server-side state
// (Dify/Coze map it to their conversation ids); stateless providers ignore it
// and use the full history. extra holds per-turn injected messages (RAG
// context) — providers that cannot take system messages fold it into the
// query. onDelta receives content fragments as they arrive; the return value
// is the full reply.
type ChatProvider interface {
	StreamChat(ctx context.Context, convKey string, history, extra []ChatMessage, onDelta func(string)) (string, error)
}

type DebugChatResult struct {
	Reply string `json:"reply"`
	// Chunks is the RAG context injected this turn (empty when rag is off,
	// missed, or degraded — same semantics as a visitor turn).
	Chunks    []Chunk `json:"chunks"`
	LatencyMS int64   `json:"latency_ms"`
}

type SessionInfo struct {
	ID        string
	CreatedAt time.Time
	Turns     int
	Speaking  bool
	Voice     bool
	// State is the conversation FSM state ("idle"/"listening"/"thinking"/
	// "speaking"); "" for builds/sessions that predate the FSM. Speaking stays
	// for back-compat (== State=="speaking").
	State string
}

// AvatarPreloader makes an avatar resident on every worker that could serve it,
// before a session needs it. Optional on SessionAdmin: a build without it simply
// reports that preloading is unsupported, and sessions fall back to loading
// inline (which stalls the first visitor by ~15s — see
// internal/session/preload.go).
type AvatarPreloader interface {
	// PreloadAvatar returns how many workers now hold the avatar, how many
	// were asked, and a human-readable reason when it is not all of them.
	//
	// engine scopes the broadcast to the workers that can actually render this
	// appearance ("" = every worker, for an engine-agnostic avatar). Under a
	// mixed pool an unscoped broadcast can never fully succeed: MuseTalk cannot
	// load a portrait, FlashHead cannot load a bake directory, so "all workers
	// ready" is unreachable and the operator gets a permanent warning about a
	// pool that is in fact correctly warmed.
	PreloadAvatar(ctx context.Context, workerPath, engine string) (ready, total int, reason string)
}

// AvatarIdleReporter lists the avatars that have baked idle material of their
// own. Optional on SessionAdmin, like AvatarPreloader.
//
// The console needs it because an avatar WITHOUT its own idle is not broken —
// its idle segment is rendered live by the engine, showing the right person at
// a continuous GPU cost. That is a deliberate fallback, but an invisible one:
// without surfacing it, binding a channel to a freshly added avatar silently
// turns a zero-cost idle into a permanently busy worker.
type AvatarIdleReporter interface {
	AvatarsWithIdle() []string
}
