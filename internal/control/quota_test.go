package control

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"mynah/internal/avatarcatalog"
	"mynah/internal/config"
)

// A channel bound to an engine can only be served by that engine's workers. The
// pool-wide total says this passes (2 channels, 2 workers); the per-engine
// condition is what catches it. Without it the publish succeeds and the visitor
// meets a 503 instead.
func TestPoolFeasibleRejectsBoundChannelsOverEngineCapacity(t *testing.T) {
	msg := poolFeasible(
		map[string]int{"musetalk": 2},
		[]channelDemand{
			{slug: "baked", want: 1, engine: "musetalk"},
			{slug: "portrait", want: 1, engine: "flashhead"},
		})
	if msg == "" {
		t.Fatal("accepted a flashhead channel against a pool with no flashhead worker")
	}
	// The message must name the obstacle, not just report a shortage: adding
	// musetalk workers would not help this channel at all.
	if !strings.Contains(msg, "FlashHead") && !strings.Contains(msg, "flashhead") {
		t.Errorf("message does not name the missing engine: %s", msg)
	}
	if !strings.Contains(msg, "portrait") {
		t.Errorf("message does not name the affected channel: %s", msg)
	}
}

// Same engine, merely oversubscribed — a different wording, because the fix here
// is "more of this engine" rather than "this engine at all".
func TestPoolFeasibleRejectsOversubscribedEngine(t *testing.T) {
	msg := poolFeasible(
		map[string]int{"musetalk": 1, "flashhead": 3},
		[]channelDemand{{slug: "baked", want: 2, engine: "musetalk"}})
	if msg == "" {
		t.Fatal("2 musetalk-bound concurrency accepted against 1 musetalk worker")
	}
	if !strings.Contains(msg, "加别的引擎不解决") {
		t.Errorf("message does not explain that the 3 idle flashhead workers cannot help: %s", msg)
	}
}

// The total condition is not implied by the per-engine one: unbound channels
// consume pool capacity without ever failing a per-engine check.
func TestPoolFeasibleUnboundChannelsConsumeTheTotal(t *testing.T) {
	workers := map[string]int{"musetalk": 1, "flashhead": 1}
	demand := []channelDemand{
		{slug: "baked", want: 1, engine: "musetalk"},
		{slug: "anyengine", want: 2, engine: ""},
	}
	msg := poolFeasible(workers, demand)
	if msg == "" {
		t.Fatal("3 concurrency accepted against a 2-worker pool")
	}
	if !strings.Contains(msg, "总额") {
		t.Errorf("want the total-quota message, got: %s", msg)
	}
	// Trim the unbound channel and the same pool becomes feasible — proving the
	// rejection was the total and not a stray per-engine failure.
	demand[1].want = 1
	if msg := poolFeasible(workers, demand); msg != "" {
		t.Errorf("2 concurrency on a 2-worker mixed pool refused: %s", msg)
	}
}

// Conversely, the per-engine condition is not implied by the total: this demand
// fits the total exactly while starving the bound channel.
func TestPoolFeasibleTotalDoesNotImplyPerEngine(t *testing.T) {
	if msg := poolFeasible(
		map[string]int{"musetalk": 1, "flashhead": 1},
		[]channelDemand{{slug: "baked", want: 2, engine: "musetalk"}},
	); msg == "" {
		t.Fatal("2 musetalk-bound concurrency accepted because the pool total happened to be 2")
	}
}

func TestPoolFeasiblePassesWhenBothConditionsHold(t *testing.T) {
	if msg := poolFeasible(
		map[string]int{"musetalk": 2, "flashhead": 1},
		[]channelDemand{
			{slug: "baked", want: 2, engine: "musetalk"},
			{slug: "portrait", want: 1, engine: "flashhead"},
		},
	); msg != "" {
		t.Errorf("exactly-fitting mixed composition refused: %s", msg)
	}
}

// A worker whose health banner cannot be parsed lands under "". It must add to
// the total (it can serve unbound channels) but never satisfy a bound one —
// otherwise an unrecognized banner would silently license engine-bound
// concurrency the pool cannot deliver.
func TestPoolFeasibleUnknownEngineWorkerCannotServeBoundChannels(t *testing.T) {
	workers := map[string]int{"": 2}
	if msg := poolFeasible(workers,
		[]channelDemand{{slug: "baked", want: 1, engine: "musetalk"}}); msg == "" {
		t.Error("an unidentified worker was allowed to satisfy a musetalk-bound channel")
	}
	if msg := poolFeasible(workers,
		[]channelDemand{{slug: "anyengine", want: 2, engine: ""}}); msg != "" {
		t.Errorf("unidentified workers should still cover unbound demand: %s", msg)
	}
}

func TestPoolFeasibleEmptyPoolRejectsAnyDemand(t *testing.T) {
	if msg := poolFeasible(nil, []channelDemand{{slug: "x", want: 1}}); msg == "" {
		t.Error("an empty pool accepted a published channel")
	}
	// No enabled channels: emptying the pool for maintenance is allowed.
	if msg := poolFeasible(nil, nil); msg != "" {
		t.Errorf("empty pool with no channels refused: %s", msg)
	}
}

func TestDescribeCompositionNamesUnidentifiedWorkers(t *testing.T) {
	if got := describeComposition(map[string]int{"musetalk": 2, "flashhead": 1}); !strings.Contains(got, "+") {
		t.Errorf("mixed composition rendered as %q", got)
	}
	if got := describeComposition(map[string]int{"": 1}); !strings.Contains(got, "引擎未识别") {
		t.Errorf("unidentified worker rendered as %q", got)
	}
	if got := describeComposition(map[string]int{"musetalk": 0}); got != "空池" {
		t.Errorf("all-zero composition = %q, want 空池", got)
	}
	if got := describeComposition(nil); got != "空池" {
		t.Errorf("nil composition = %q, want 空池", got)
	}
}

// A channel that never chose an avatar is NOT engine-free at dispatch: it
// inherits the console's live global selection (session.create does exactly
// this). Counting it as unbound would let it pass a per-engine check against
// workers it can never land on.
func TestEngineForChannelFallsBackToGlobalAvatar(t *testing.T) {
	avatarcatalog.SetBaked([]avatarcatalog.Baked{
		{ID: "bake:leiya_mt", Name: "leiya_mt", Engine: "musetalk", Path: "/bakes/leiya_mt"},
	})
	t.Cleanup(func() { avatarcatalog.SetBaked(nil) })

	newServer := func(globalAvatar string) *Server {
		layer := map[string]json.RawMessage{
			"avatar": json.RawMessage(`{"current":` + strconv.Quote(globalAvatar) + `}`),
		}
		mgr, err := config.NewManager(context.Background(), nil, layer)
		if err != nil {
			t.Fatalf("config.NewManager: %v", err)
		}
		return &Server{deps: Deps{Config: mgr}}
	}

	// Global avatar is a musetalk bake -> an avatar-less channel demands musetalk.
	if got := newServer("bake:leiya_mt").engineForChannel(""); got != "musetalk" {
		t.Errorf("engineForChannel(\"\") with global bake:leiya_mt = %q, want musetalk", got)
	}
	// The channel's own avatar always wins over the global one.
	if got := newServer("bake:leiya_mt").engineForChannel("f"); got != "flashhead" {
		t.Errorf("engineForChannel(\"f\") = %q, want flashhead (channel avatar wins)", got)
	}
	// The shipped default carries no intent, so such a channel is genuinely free.
	if got := newServer("default").engineForChannel(""); got != "" {
		t.Errorf("engineForChannel(\"\") with global default = %q, want empty", got)
	}
	// An avatar was chosen but has no known engine: do not invent one from the
	// global selection — the channel is pinned to something we cannot attribute.
	if got := newServer("bake:leiya_mt").engineForChannel("trained:job42"); got != "" {
		t.Errorf("engineForChannel(\"trained:job42\") = %q, want empty", got)
	}
	// No config manager at all (flag-less embedding) must not panic.
	if got := (&Server{}).engineForChannel(""); got != "" {
		t.Errorf("engineForChannel with no config = %q, want empty", got)
	}
}
