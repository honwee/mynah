package control

import (
	"strings"
	"testing"

	"mynah/internal/supervisor"
)

// The pool inventory must be read from the engine attribution, not from name
// prefixes. This listing is the shape production actually has after adding a
// second engine: two compose-managed musetalk workers plus a dynamic flashhead
// slot, alongside services that are not pool members at all.
func TestCurrentCompositionSeesBothWorkerShapes(t *testing.T) {
	list := []supervisor.Service{
		{Name: "tts", State: supervisor.StateRunning},
		{Name: "asr", State: supervisor.StateRunning},
		{Name: "musetalk", State: supervisor.StateRunning},
		{Name: "musetalk2", State: supervisor.StateStopped},
		{Name: "flashhead-3", State: supervisor.StateRunning},
	}
	inv := currentComposition(list)
	if len(inv) != 3 {
		t.Fatalf("inventory = %+v, want the 3 avatar workers only (TTS/ASR are not pool members)", inv)
	}
	got := countByEngine(inv)
	if got["musetalk"] != 1 {
		t.Errorf("running musetalk = %d, want 1 (musetalk2 is stopped)", got["musetalk"])
	}
	if got["flashhead"] != 1 {
		t.Errorf("running flashhead = %d, want 1 — a name-prefix count would miss it entirely", got["flashhead"])
	}
	// The stopped compose worker must still be in the inventory: it is reusable
	// capacity, and forgetting it is what makes a reconciler accrete containers.
	stopped := 0
	for _, wkr := range inv {
		if !wkr.Running {
			stopped++
			if wkr.dynamic() {
				t.Errorf("%s reported as a dynamic slot; it is compose-managed and must only be stopped, never removed", wkr.Name)
			}
		}
	}
	if stopped != 1 {
		t.Errorf("stopped workers = %d, want 1", stopped)
	}
}

// A worker that is still loading its model is on its way up, not spare capacity
// to start again — counting it as stopped would create a duplicate.
func TestCurrentCompositionTreatsStartingAsRunning(t *testing.T) {
	inv := currentComposition([]supervisor.Service{
		{Name: "flashhead-1", State: supervisor.StateStarting},
	})
	if len(inv) != 1 || !inv[0].Running {
		t.Fatalf("starting worker = %+v, want one running entry", inv)
	}
}

func names(steps []step, action stepAction) []string {
	var out []string
	for _, st := range steps {
		if st.Action != action {
			continue
		}
		if st.Action == stepCreate {
			out = append(out, st.Engine)
		} else {
			out = append(out, st.Worker.Name)
		}
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Adding a second engine to a healthy pool must not touch the first one.
func TestPlanCompositionAddsWithoutDisturbingTheOtherEngine(t *testing.T) {
	inv := []poolWorker{
		{Name: "musetalk", Engine: "musetalk", Running: true},
		{Name: "musetalk2", Engine: "musetalk", Running: true},
	}
	steps := planComposition(inv, map[string]int{"musetalk": 2, "flashhead": 1})
	if got := names(steps, stepRetire); len(got) != 0 {
		t.Errorf("retired %v while only adding an engine", got)
	}
	if got := names(steps, stepCreate); !eq(got, []string{"flashhead"}) {
		t.Errorf("created %v, want one flashhead", got)
	}
}

// A stopped worker of the wanted engine is started, not duplicated. Without this
// every 1→0→1 cycle would leave the compose worker idle and burn a dynamic slot,
// until MaxPoolSlots ran out.
func TestPlanCompositionReusesStoppedWorkersBeforeCreating(t *testing.T) {
	inv := []poolWorker{
		{Name: "musetalk", Engine: "musetalk", Running: true},
		{Name: "musetalk2", Engine: "musetalk", Running: false},
		{Name: "musetalk-4", Engine: "musetalk", Slot: 4, Running: false},
	}
	steps := planComposition(inv, map[string]int{"musetalk": 3})
	if got := names(steps, stepCreate); len(got) != 0 {
		t.Errorf("created %v when two stopped workers were available for reuse", got)
	}
	// compose-managed first: it exists whether the pool wants it or not.
	if got := names(steps, stepStart); !eq(got, []string{"musetalk2", "musetalk-4"}) {
		t.Errorf("started %v, want musetalk2 then musetalk-4", got)
	}
}

// Shrinking must give up dynamic slots before compose-managed ones: this server
// can recreate a dynamic slot on its own, but a stopped compose worker needs
// `docker compose up -d` to come back.
func TestPlanCompositionRetiresDynamicSlotsFirstHighestFirst(t *testing.T) {
	inv := []poolWorker{
		{Name: "musetalk", Engine: "musetalk", Running: true},
		{Name: "musetalk-4", Engine: "musetalk", Slot: 4, Running: true},
		{Name: "musetalk-5", Engine: "musetalk", Slot: 5, Running: true},
	}
	steps := planComposition(inv, map[string]int{"musetalk": 1})
	if got := names(steps, stepRetire); !eq(got, []string{"musetalk-5", "musetalk-4"}) {
		t.Errorf("retired %v, want the dynamic slots highest-first and the compose worker untouched", got)
	}
}

// Dropping an engine entirely is expressed by leaving it out of the target.
func TestPlanCompositionOmittedEngineMeansZero(t *testing.T) {
	inv := []poolWorker{
		{Name: "musetalk", Engine: "musetalk", Running: true},
		{Name: "flashhead-3", Engine: "flashhead", Slot: 3, Running: true},
	}
	steps := planComposition(inv, map[string]int{"musetalk": 1})
	if got := names(steps, stepRetire); !eq(got, []string{"flashhead-3"}) {
		t.Errorf("retired %v, want flashhead-3 (absent from the target = zero)", got)
	}
}

// Step ORDER is the safety property. Replacing an engine on a multi-worker pool
// must interleave retire-then-add, so peak worker count stays at max(current,
// want) instead of current+want — otherwise the swap can OOM a live worker,
// because the VRAM check validated the target, not the transient overlap.
func TestPlanCompositionRetiresBeforeAddingOnASwap(t *testing.T) {
	inv := []poolWorker{
		{Name: "musetalk", Engine: "musetalk", Running: true},
		{Name: "musetalk2", Engine: "musetalk", Running: true},
	}
	steps := planComposition(inv, map[string]int{"flashhead": 2})
	if len(steps) != 4 {
		t.Fatalf("steps = %d, want 4 (2 retire + 2 create)", len(steps))
	}
	live := 2
	peak := 2
	for _, st := range steps {
		if st.Action == stepRetire {
			live--
		} else {
			live++
		}
		if live > peak {
			peak = live
		}
		if live < 1 {
			t.Fatalf("pool hit %d workers mid-plan — visitors would be cut off: %v", live, steps)
		}
	}
	if peak > 2 {
		t.Errorf("peak %d workers during a 2→2 swap; the VRAM budget only covers 2", peak)
	}
}

// The one case where an add must go first: a single-worker pool. Retiring first
// would empty the pool for a full cold start. Overshooting by one is safe here by
// construction — a card hosting one worker has room for a second, or the first
// would not have fit.
func TestPlanCompositionAddsFirstWhenPoolWouldEmpty(t *testing.T) {
	inv := []poolWorker{{Name: "musetalk", Engine: "musetalk", Running: true}}
	steps := planComposition(inv, map[string]int{"flashhead": 1})
	if len(steps) != 2 {
		t.Fatalf("steps = %+v, want a create and a retire", steps)
	}
	if steps[0].Action != stepCreate {
		t.Errorf("first step is %s; on a one-worker pool the replacement must exist first", steps[0].Action)
	}
}

// Emptying the pool is legitimate (maintenance), and must not deadlock the
// "never leave the pool empty" rule.
func TestPlanCompositionCanEmptyThePool(t *testing.T) {
	inv := []poolWorker{{Name: "musetalk", Engine: "musetalk", Running: true}}
	steps := planComposition(inv, map[string]int{})
	if !eq(names(steps, stepRetire), []string{"musetalk"}) {
		t.Errorf("steps = %+v, want the single worker retired", steps)
	}
}

// Already-satisfied targets do nothing at all — the endpoint is idempotent, so
// the console can send the full composition on every click.
func TestPlanCompositionNoOpWhenAlreadyThere(t *testing.T) {
	inv := []poolWorker{
		{Name: "musetalk", Engine: "musetalk", Running: true},
		{Name: "flashhead-3", Engine: "flashhead", Slot: 3, Running: true},
		{Name: "musetalk2", Engine: "musetalk", Running: false}, // stopped: not wanted
	}
	if steps := planComposition(inv, map[string]int{"musetalk": 1, "flashhead": 1}); len(steps) != 0 {
		t.Errorf("steps = %+v, want none", steps)
	}
}

func TestNormalizeCompositionRejectsBadInput(t *testing.T) {
	if _, ferr := normalizeComposition(map[string]int{"nosuch": 1}); ferr == "" {
		t.Error("accepted an unknown engine")
	}
	// All shipped engines are measured now (wav2lipLS since 2026-10-08), so the
	// "unmeasured" refusal is covered in avatarengine with a synthetic engine;
	// here just assert the lightest tier is accepted.
	if _, ferr := normalizeComposition(map[string]int{"wav2lipls": 1}); ferr != "" {
		t.Errorf("rejected wav2lipls, which is measured and selectable: %s", ferr)
	}
	if _, ferr := normalizeComposition(map[string]int{"musetalk": -1}); ferr == "" {
		t.Error("accepted a negative count")
	}
	if _, ferr := normalizeComposition(map[string]int{"musetalk": 5, "flashhead": 5}); ferr == "" ||
		!strings.Contains(ferr, "上限") {
		t.Errorf("10 workers should exceed MaxPoolSlots, got %q", ferr)
	}
	// A zero count is how the console says "none of this engine"; it must not be
	// rejected for being unselectable, and must not survive into the target.
	want, ferr := normalizeComposition(map[string]int{"musetalk": 2, "wav2lipls": 0})
	if ferr != "" {
		t.Fatalf("zero count rejected: %s", ferr)
	}
	if _, present := want["wav2lipls"]; present {
		t.Error("zero count survived into the target composition")
	}
	if want["musetalk"] != 2 {
		t.Errorf("musetalk = %d, want 2", want["musetalk"])
	}
}
