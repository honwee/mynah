// Package instinct is Mynah's "System One": a fast, cheap decision layer that
// watches the live conversation (what the visitor just said, what the avatar
// is about to say) and picks the avatar's instant physical reaction — nod,
// wave, shake, a listening pose — while the slow brain (LLM / cloud realtime)
// composes the actual reply.
//
// The split follows open-annie (Apache-2.0): the model that talks is not the
// model that moves. Decisions come from a Provider answering closed questions
// with probabilities; a Pacer bounds how often we ask; a Gate turns answers
// into at most one action per turn, only from clips the avatar actually has.
// Providers: "jev" (TypeSafe System One over HTTP) and "rules" (local keyword
// rules, zero latency, no key) — the rules provider is also the fallback
// whenever Jev is unavailable, so the layer never blocks or breaks a turn.
package instinct

import "context"

// Question is one closed question for the provider. Mirrors the Jev request
// shape so the rules provider can answer the exact same questions.
type Question struct {
	Type         string            `json:"type"` // "choice" | "noul" | "score"
	Instructions string            `json:"instructions,omitempty"`
	Criteria     map[string]string `json:"criteria,omitempty"` // choice: option -> meaning
}

// Answer is one provider answer. For "choice": Choice + Confidence (+ per-option
// Probabilities); for "noul" (yes/no): Noul in [0,1]; for "score": Score.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Score         float64            `json:"score,omitempty"`
}

// Provider answers a batch of questions about a state (free text: the recent
// transcript plus a one-line situation summary). Implementations must respect
// ctx; the engine enforces the timeout and never waits past it.
type Provider interface {
	Name() string
	Decide(ctx context.Context, state string, questions map[string]Question) (map[string]Answer, error)
}
