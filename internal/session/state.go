package session

import (
	"sync/atomic"
	"time"
)

// ConvState is the conversation FSM state. It is an additive observability
// layer over the existing turn machinery: `speaking atomic.Bool` stays the
// precise hot-path flag that Speak/Chat/Interrupt/SpeechEnd already manage, and
// the FSM mirrors those same transitions into a 4-state view + a typed event
// stream (DataChannel + logs + admin). Borrowed from LiveKit's AgentSession,
// which exposes listening/thinking/speaking instead of a single bool.
type ConvState int32

const (
	StateIdle      ConvState = iota // not listening, agent not active
	StateListening                  // user mic speech in progress
	StateThinking                   // turn committed, LLM generating, no audio yet
	StateSpeaking                   // avatar audio playing
)

func (s ConvState) String() string {
	switch s {
	case StateListening:
		return "listening"
	case StateThinking:
		return "thinking"
	case StateSpeaking:
		return "speaking"
	default:
		return "idle"
	}
}

// Event is one entry on the conversation event stream. Type is the trigger;
// State is the FSM state after it. Marshaled to the DataChannel as
// {"type":"event","event":...,"state":...,"text":...} — additive to the
// existing {"status":...} subtitle protocol, which is left untouched.
type Event struct {
	Type  string    `json:"event"`
	State string    `json:"state"`
	Text  string    `json:"text,omitempty"`
	At    time.Time `json:"-"`
}

// Event type constants (the typed stream LiveKit calls user_started_speaking /
// agent_state_changed / etc.).
const (
	EvStateChanged     = "state"
	EvUserSpeechStart  = "user_speech_start"
	EvUserSpeechEnd    = "user_speech_end"
	EvTranscript       = "transcript"
	EvTurnCommitted    = "turn_committed"
	EvAgentThinking    = "agent_thinking"
	EvAgentSpeechStart = "agent_speech_start"
	EvAgentSpeechEnd   = "agent_speech_end"
	EvInterrupted      = "interrupted"
)

// convFSM holds the atomic state and an optional sink. All methods are safe for
// concurrent use; the sink is invoked synchronously by the caller's goroutine
// (it must be cheap — the session's sink just marshals + DataChannel-sends).
type convFSM struct {
	state atomic.Int32
	sink  func(Event) // nil = no observers (events dropped)
}

func newConvFSM(sink func(Event)) *convFSM {
	return &convFSM{sink: sink}
}

func (f *convFSM) get() ConvState { return ConvState(f.state.Load()) }

// to transitions to st and fires evType (plus a "state" event when the state
// actually changed). evType "" = update state silently. A no-op transition
// (same state, no evType) emits nothing.
func (f *convFSM) to(st ConvState, evType, text string) {
	prev := ConvState(f.state.Swap(int32(st)))
	if f.sink == nil {
		return
	}
	if evType != "" {
		f.sink(Event{Type: evType, State: st.String(), Text: text, At: time.Now()})
	}
	if prev != st {
		f.sink(Event{Type: EvStateChanged, State: st.String(), At: time.Now()})
	}
}

// emit fires an event without changing state (e.g. a transcript notice).
func (f *convFSM) emit(evType, text string) {
	if f.sink != nil {
		f.sink(Event{Type: evType, State: f.get().String(), Text: text, At: time.Now()})
	}
}
