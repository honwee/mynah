package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mynah/core"
)

// fakeToolProvider replays a canned script of rounds and records the
// conversation it was handed each time.
type fakeToolProvider struct {
	rounds []struct {
		text  string
		calls []core.ToolCall
	}
	seen [][]core.ChatMessage
	n    int
}

func (f *fakeToolProvider) StreamChatTools(ctx context.Context, _ string,
	history, _ []core.ChatMessage, _ []core.ToolSpec, onDelta func(string)) (string, []core.ToolCall, error) {
	f.seen = append(f.seen, append([]core.ChatMessage(nil), history...))
	if f.n >= len(f.rounds) {
		return "", nil, nil
	}
	r := f.rounds[f.n]
	f.n++
	if r.text != "" {
		onDelta(r.text)
	}
	return r.text, r.calls, nil
}

// testSession is the minimum for the ability paths: no peer, no DataChannel
// (dcSend no-ops on a nil channel, which is exactly the "host went away" case).
func testSession() *Session { return &Session{ID: "t", tools: newToolRegistry()} }

func drain(ch <-chan string, into *[]string, done chan<- struct{}) {
	go func() {
		for s := range ch {
			*into = append(*into, s)
		}
		close(done)
	}()
}

func TestRegistrySpecsKeepOrderAndDefaults(t *testing.T) {
	r := newToolRegistry()
	r.set([]ability{
		{Name: "goto_page", Description: "开页面", Parameters: map[string]any{"type": "object"}},
		{Name: ""}, // no name: unusable, must be dropped
		{Name: "refresh"},
	})
	specs := r.specs()
	if len(specs) != 2 || specs[0].Name != "goto_page" || specs[1].Name != "refresh" {
		t.Fatalf("specs = %+v", specs)
	}
	// A no-parameter ability still needs a schema or strict backends reject it.
	if specs[1].Parameters["type"] != "object" {
		t.Errorf("refresh parameters = %+v, want an empty object schema", specs[1].Parameters)
	}
	// Re-declaring replaces wholesale rather than merging.
	r.set([]ability{{Name: "only"}})
	if specs := r.specs(); len(specs) != 1 || specs[0].Name != "only" {
		t.Errorf("after re-set specs = %+v", specs)
	}
}

// An action ability must not wait for the browser: the model gets its ok now so
// the sentence and the page change land together.
func TestActionAbilityDoesNotBlock(t *testing.T) {
	s := testSession()
	s.tools.set([]ability{{Name: "goto_page"}})

	sentences := make(chan string, 4)
	start := time.Now()
	res := s.runAbility(context.Background(), core.ToolCall{ID: "c1", Name: "goto_page",
		Arguments: `{"page":"sales"}`}, sentences)

	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("action waited %v; must be fire-and-forget", d)
	}
	if !strings.Contains(res, `"ok"`) {
		t.Errorf("result = %s, want ok", res)
	}
	if len(sentences) != 0 {
		t.Errorf("action spoke a filler (%d queued); only blocking abilities do", len(sentences))
	}
}

func TestBlockingAbilitySpeaksFillerThenUsesResult(t *testing.T) {
	s := testSession()
	s.tools.set([]ability{{Name: "query_metric", Blocking: true, Filler: "我查一下"}})

	sentences := make(chan string, 4)
	go func() {
		// Answer only once the filler is in the pipeline — the ordering the
		// visitor actually hears.
		for len(sentences) == 0 {
			time.Sleep(time.Millisecond)
		}
		s.tools.resolve("c1", `{"value":42}`)
	}()

	res := s.runAbility(context.Background(),
		core.ToolCall{ID: "c1", Name: "query_metric", Arguments: "{}"}, sentences)
	if res != `{"value":42}` {
		t.Errorf("result = %s, want the host's payload", res)
	}
	if got := <-sentences; got != "我查一下" {
		t.Errorf("filler = %q", got)
	}
}

func TestBlockingAbilityWithoutFillerStillSpeaks(t *testing.T) {
	s := testSession()
	s.tools.set([]ability{{Name: "q", Blocking: true}})
	old := toolTimeout
	toolTimeout = 50 * time.Millisecond
	defer func() { toolTimeout = old }()

	sentences := make(chan string, 4)
	res := s.runAbility(context.Background(), core.ToolCall{ID: "c1", Name: "q"}, sentences)
	if !strings.Contains(res, "timeout") {
		t.Errorf("result = %s, want a timeout so the model can answer anyway", res)
	}
	if got := <-sentences; got != defaultFiller {
		t.Errorf("filler = %q, want the default (silence looks like a frozen avatar)", got)
	}
	// The timed-out call must not stay parked in the registry.
	if s.tools.resolve("c1", "late") {
		t.Error("a late result was still accepted after the timeout")
	}
}

func TestBargeInAbandonsBlockingCall(t *testing.T) {
	s := testSession()
	s.tools.set([]ability{{Name: "q", Blocking: true}})
	ctx, cancel := context.WithCancel(context.Background())
	sentences := make(chan string, 4)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	res := s.runAbility(ctx, core.ToolCall{ID: "c1", Name: "q"}, sentences)
	if !strings.Contains(res, "cancelled") {
		t.Errorf("result = %s, want cancelled", res)
	}
}

// A model can hallucinate an ability the host never declared; that must be a
// normal tool result, not a hang or a crash.
func TestUnknownAbilityReturnsError(t *testing.T) {
	s := testSession()
	s.tools.set([]ability{{Name: "goto_page"}})
	res := s.runAbility(context.Background(),
		core.ToolCall{ID: "c1", Name: "nope"}, make(chan string, 1))
	if !strings.Contains(res, "no such ability") {
		t.Errorf("result = %s", res)
	}
}

func TestHandleAbilitiesRegistersAndResolves(t *testing.T) {
	s := testSession()
	if !s.handleAbilities("abilities", []byte(
		`{"type":"abilities","abilities":[{"name":"goto_page","description":"d"}]}`)) {
		t.Fatal("abilities message not handled")
	}
	if len(s.tools.specs()) != 1 {
		t.Fatalf("specs = %+v", s.tools.specs())
	}
	if s.handleAbilities("mouth", []byte(`{"type":"mouth"}`)) {
		t.Error("handleAbilities claimed a message that isn't its own")
	}

	ch := s.tools.await("c1")
	s.handleAbilities("tool_result", []byte(`{"type":"tool_result","id":"c1","error":"boom"}`))
	if got := <-ch; !strings.Contains(got, "boom") {
		t.Errorf("result = %s, want the host error forwarded to the model", got)
	}
}

func TestChatToolsRunsRoundsAndFeedsResultsBack(t *testing.T) {
	s := testSession()
	s.tools.set([]ability{{Name: "goto_page"}})
	fake := &fakeToolProvider{rounds: []struct {
		text  string
		calls []core.ToolCall
	}{
		{calls: []core.ToolCall{{ID: "c1", Name: "goto_page", Arguments: `{"page":"sales"}`}}},
		{text: "好的，已经打开销售页。"},
	}}

	sentences := make(chan string, 8)
	var spoken []string
	done := make(chan struct{})
	drain(sentences, &spoken, done)

	reply, err := s.chatTools(context.Background(), fake,
		[]core.ChatMessage{{Role: "user", Content: "看销售"}}, nil,
		func(string) {}, func() {}, sentences)
	close(sentences)
	<-done

	if err != nil {
		t.Fatalf("chatTools: %v", err)
	}
	if reply != "好的，已经打开销售页。" {
		t.Errorf("reply = %q", reply)
	}
	if fake.n != 2 {
		t.Errorf("rounds run = %d, want 2", fake.n)
	}
	// Round 2 must see the assistant's tool_calls followed by the tool result,
	// or an OpenAI-compatible backend rejects the request outright.
	second := fake.seen[1]
	last := second[len(second)-1]
	if last.Role != "tool" || last.ToolCallID != "c1" {
		t.Fatalf("last message = %+v, want the tool result for c1", last)
	}
	prev := second[len(second)-2]
	if prev.Role != "assistant" || len(prev.ToolCalls) != 1 || prev.ToolCalls[0].ID != "c1" {
		t.Fatalf("message before it = %+v, want the assistant tool_calls turn", prev)
	}
}

// A model that keeps calling tools must not hold the turn open forever.
func TestChatToolsStopsAtRoundLimit(t *testing.T) {
	s := testSession()
	s.tools.set([]ability{{Name: "loop"}})
	var rounds []struct {
		text  string
		calls []core.ToolCall
	}
	for i := 0; i < 10; i++ {
		rounds = append(rounds, struct {
			text  string
			calls []core.ToolCall
		}{calls: []core.ToolCall{{ID: "c", Name: "loop", Arguments: "{}"}}})
	}
	fake := &fakeToolProvider{rounds: rounds}

	sentences := make(chan string, 32)
	var spoken []string
	done := make(chan struct{})
	drain(sentences, &spoken, done)
	if _, err := s.chatTools(context.Background(), fake, nil, nil,
		func(string) {}, func() {}, sentences); err != nil {
		t.Fatalf("chatTools: %v", err)
	}
	close(sentences)
	<-done
	if fake.n != maxToolRounds {
		t.Errorf("rounds run = %d, want the %d-round cap", fake.n, maxToolRounds)
	}
}

// Unparsable arguments (a truncated stream) must degrade to an empty object
// rather than putting broken JSON on the DataChannel.
func TestUnparsableArgumentsDoNotBreakDispatch(t *testing.T) {
	s := testSession()
	s.tools.set([]ability{{Name: "goto_page"}})
	res := s.runAbility(context.Background(),
		core.ToolCall{ID: "c1", Name: "goto_page", Arguments: `{"page":`}, make(chan string, 1))
	var v map[string]any
	if err := json.Unmarshal([]byte(res), &v); err != nil {
		t.Errorf("result is not valid JSON: %s", res)
	}
}
