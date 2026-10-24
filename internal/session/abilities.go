// Ability contract: the browser tells cored what the HOST PAGE can do, cored
// advertises those abilities to the model and forwards the calls straight back
// over the same DataChannel. cored never interprets an ability's arguments and
// has no idea what any of them mean — that is the whole point. It is what lets
// a BI dashboard say "I can open a page and filter a chart" without a line of
// dashboard-specific code in here, and what makes the browser SDK generic.
//
// Wire protocol (all on the existing "chat" DataChannel):
//
//	browser -> cored  {"type":"abilities","abilities":[{name,description,
//	                   parameters,blocking?,filler?}]}
//	cored -> browser  {"type":"tool_call","id":"c1","name":"...","arguments":{...}}
//	browser -> cored  {"type":"tool_result","id":"c1","result":{...}}   (blocking only)
//
// Two kinds of ability, and the difference is what makes this feel alive:
//
//   - action (default): fire-and-forget. cored answers the model with a bare ok
//     immediately, so "好的，我打开华东区的销售" and the page actually switching
//     happen at the same time instead of one after the other.
//   - query (blocking:true): the result belongs in the answer, so cored waits
//     for it — and speaks `filler` first, because two seconds of silence in the
//     middle of a turn reads as a frozen avatar, not as thinking.
package session

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"mynah/core"
)

const (
	// maxToolRounds bounds one turn's model<->ability ping-pong. 3 covers
	// "call, look, answer" with a round to spare; past that a model is looping.
	maxToolRounds = 3
	// defaultFiller covers a blocking ability registered without one.
	defaultFiller = "我查一下。"
)

// toolTimeout is how long a blocking ability may take before cored gives up and
// lets the model answer without it. Longer than any dashboard query should
// need, short enough that the filler doesn't run dry. Var only so tests don't
// have to sleep for it.
var toolTimeout = 6 * time.Second

// ability is one host-declared capability. Parameters is a JSON Schema object
// passed to the model verbatim.
type ability struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Blocking    bool           `json:"blocking"`
	Filler      string         `json:"filler"`
}

// toolRegistry holds a session's abilities and its in-flight blocking calls.
//
// Both are touched from two goroutines that never met before this feature: the
// pion DataChannel callback (registration, results) and the turn goroutine
// (advertising, waiting). Everything here is under the mutex.
type toolRegistry struct {
	mu      sync.Mutex
	list    []ability
	byName  map[string]ability
	pending map[string]chan string
}

func newToolRegistry() *toolRegistry {
	return &toolRegistry{byName: map[string]ability{}, pending: map[string]chan string{}}
}

// set replaces the whole ability set (the host re-declares, never patches).
func (r *toolRegistry) set(abs []ability) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = nil
	r.byName = make(map[string]ability, len(abs))
	for _, a := range abs {
		if a.Name == "" {
			continue
		}
		if a.Parameters == nil {
			a.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		r.list = append(r.list, a)
		r.byName[a.Name] = a
	}
}

// specs renders the abilities for the model, in declaration order.
func (r *toolRegistry) specs() []core.ToolSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.list) == 0 {
		return nil
	}
	out := make([]core.ToolSpec, 0, len(r.list))
	for _, a := range r.list {
		out = append(out, core.ToolSpec{Name: a.Name, Description: a.Description,
			Parameters: a.Parameters})
	}
	return out
}

func (r *toolRegistry) lookup(name string) (ability, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byName[name]
	return a, ok
}

// await registers interest in a call's result BEFORE the call goes out, so a
// host that answers instantly can't lose the race.
func (r *toolRegistry) await(id string) chan string {
	ch := make(chan string, 1)
	r.mu.Lock()
	r.pending[id] = ch
	r.mu.Unlock()
	return ch
}

func (r *toolRegistry) forget(id string) {
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
}

// resolve delivers a host result. Unknown or already-abandoned ids are dropped
// (a result arriving after the timeout, or after a barge-in cancelled the turn).
func (r *toolRegistry) resolve(id, result string) bool {
	r.mu.Lock()
	ch, ok := r.pending[id]
	delete(r.pending, id)
	r.mu.Unlock()
	if !ok {
		return false
	}
	ch <- result
	return true
}

// chatTools runs one chat turn as up to maxToolRounds model rounds, executing
// the abilities the model asks for in between. Text from every round flows
// through the caller's onDelta, so the sentence slicer, subtitle timeline and
// TTS ordering are the same machinery a plain turn uses — a tool round is not
// a separate speech path, it is a pause in the same one.
//
// Returns the full text spoken across all rounds (what goes into history).
func (s *Session) chatTools(ctx context.Context, tp core.ToolChatProvider,
	hist, extra []core.ChatMessage, onDelta func(string), flushTail func(),
	sentences chan<- string) (string, error) {

	specs := s.tools.specs()
	conv := append([]core.ChatMessage(nil), hist...)
	var spoken strings.Builder

	for round := 0; round < maxToolRounds; round++ {
		reply, calls, err := tp.StreamChatTools(ctx, s.ID, conv, extra, specs, onDelta)
		flushTail()
		spoken.WriteString(reply)
		if err != nil {
			return spoken.String(), err
		}
		if len(calls) == 0 {
			return spoken.String(), nil
		}
		if ctx.Err() != nil { // barged in: drop the calls, don't dispatch
			return spoken.String(), nil
		}
		names := make([]string, 0, len(calls))
		for _, c := range calls {
			names = append(names, c.Name)
		}
		log.Printf("[turn %s] tool round %d: %v", s.ID, round+1, names)

		conv = append(conv, core.ChatMessage{Role: "assistant", Content: reply, ToolCalls: calls})
		for _, c := range calls {
			conv = append(conv, core.ChatMessage{Role: "tool", ToolCallID: c.ID,
				Content: s.runAbility(ctx, c, sentences)})
		}
	}
	log.Printf("[turn %s] tool rounds exhausted (%d), answering with what we have",
		s.ID, maxToolRounds)
	return spoken.String(), nil
}

// dcSend ships one arbitrary JSON message over the chat DataChannel. dcEmit is
// the {"status","text"} subtitle protocol; this is for the {"type":...} family
// (events, tool calls) — clients that don't know a type simply ignore it.
func (s *Session) dcSend(v any) {
	s.dcMu.Lock()
	dc := s.dc
	s.dcMu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	if b, err := json.Marshal(v); err == nil {
		_ = dc.SendText(string(b))
	}
}

// handleAbilities processes a browser->cored control message. Returns false if
// it wasn't one of ours, so the caller can try its other cases.
func (s *Session) handleAbilities(typ string, data []byte) bool {
	switch typ {
	case "abilities":
		var m struct {
			Abilities []ability `json:"abilities"`
		}
		if json.Unmarshal(data, &m) != nil {
			return true
		}
		s.tools.set(m.Abilities)
		names := make([]string, 0, len(m.Abilities))
		for _, a := range m.Abilities {
			names = append(names, a.Name)
		}
		log.Printf("[session %s] abilities registered: %v", s.ID, names)
		return true
	case "tool_result":
		var m struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if json.Unmarshal(data, &m) != nil || m.ID == "" {
			return true
		}
		payload := string(m.Result)
		if m.Error != "" {
			b, _ := json.Marshal(map[string]string{"error": m.Error})
			payload = string(b)
		} else if payload == "" {
			payload = `{"ok":true}`
		}
		if !s.tools.resolve(m.ID, payload) {
			log.Printf("[session %s] tool_result for unknown/expired call %s", s.ID, m.ID)
		}
		return true
	}
	return false
}

// runAbility dispatches one model-requested call to the browser and returns the
// JSON the model should see as the result. sentences is the live TTS pipeline —
// a blocking ability's filler goes in there so it rides the same sentence
// ordering and subtitle timing as any other speech.
func (s *Session) runAbility(ctx context.Context, c core.ToolCall, sentences chan<- string) string {
	ab, known := s.tools.lookup(c.Name)

	var args any = map[string]any{}
	if json.Valid([]byte(c.Arguments)) {
		args = json.RawMessage(c.Arguments)
	} else if c.Arguments != "" {
		log.Printf("[session %s] tool %s: unparsable arguments %q", s.ID, c.Name, c.Arguments)
	}

	if !known {
		log.Printf("[session %s] model called unregistered ability %q", s.ID, c.Name)
		s.dcSend(map[string]any{"type": "tool_call", "id": c.ID, "name": c.Name,
			"arguments": args, "unknown": true})
		return `{"error":"no such ability"}`
	}

	// Register before dispatch so an instant reply can't arrive first.
	var wait chan string
	if ab.Blocking {
		wait = s.tools.await(c.ID)
		defer s.tools.forget(c.ID)
	}
	s.dcSend(map[string]any{"type": "tool_call", "id": c.ID, "name": c.Name,
		"arguments": args, "blocking": ab.Blocking})

	if !ab.Blocking {
		// Action: don't wait. The model gets its ok now and keeps talking, so
		// the sentence and the page change land together.
		return `{"ok":true}`
	}

	filler := ab.Filler
	if filler == "" {
		filler = defaultFiller
	}
	select {
	case sentences <- filler:
	case <-ctx.Done():
		return `{"error":"cancelled"}`
	}

	select {
	case res := <-wait:
		return res
	case <-time.After(toolTimeout):
		log.Printf("[session %s] tool %s timed out after %s", s.ID, c.Name, toolTimeout)
		return `{"error":"timeout"}`
	case <-ctx.Done():
		return `{"error":"cancelled"}`
	}
}
