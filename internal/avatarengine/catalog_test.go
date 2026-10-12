package avatarengine

import (
	"strings"
	"testing"
)

// The capacity math must reproduce the real reference-host picture, since that is
// the one configuration whose correct answer is independently known: two
// MuseTalk workers fit on GPU1 and none on GPU0 (TTS owns it).
func TestPlanMatchesMeasuredProduction(t *testing.T) {
	musetalk, ok := Get("musetalk")
	if !ok {
		t.Fatal("musetalk missing from catalog")
	}
	// Live figures from the RTX 4090 reference host on 2026-07-29 with the pool stopped:
	// GPU0 holds TTS (~19.4GB), GPU1 holds nothing but the pool.
	gpus := []GPU{
		{Index: 0, TotalMB: 24564, UsedByOthersMB: 19475},
		{Index: 1, TotalMB: 24564, UsedByOthersMB: 0},
	}
	cap, err := Plan(musetalk, gpus)
	if err != nil {
		t.Fatal(err)
	}
	if cap.PerGPU[0] != 0 {
		t.Errorf("GPU0 slots = %d, want 0 (TTS leaves only ~5GB, under one 7.7GB worker)", cap.PerGPU[0])
	}
	if cap.PerGPU[1] != 2 {
		t.Errorf("GPU1 slots = %d, want 2 — production runs exactly 2 and a 3rd does not fit", cap.PerGPU[1])
	}
	if cap.Total != 2 {
		t.Errorf("total = %d, want 2", cap.Total)
	}
	t.Log(cap.Explain)
}

// A worker cannot span GPUs, so two half-slots on separate cards are zero
// slots — not one. floor(sum/per) would wrongly promise a slot here.
func TestPlanNeverSpansGPUs(t *testing.T) {
	e := Engine{Name: "x", Image: "i", Script: "s", VRAMSteadyMB: 8000}
	gpus := []GPU{
		{Index: 0, TotalMB: 24564, UsedByOthersMB: 16000}, // ~6.1GB usable
		{Index: 1, TotalMB: 24564, UsedByOthersMB: 16000}, // ~6.1GB usable
	}
	cap, err := Plan(e, gpus)
	if err != nil {
		t.Fatal(err)
	}
	if cap.Total != 0 {
		t.Fatalf("total = %d, want 0: 6.1GB free on each card cannot host an 8GB worker, "+
			"even though 12.2GB is free in aggregate", cap.Total)
	}
}

// The reserve is what stops the planner from filling a card to the brim.
func TestReserveIsAtLeast2GBAndScales(t *testing.T) {
	if got := ReserveMB(24564); got != 2456 {
		t.Errorf("ReserveMB(24GB) = %d, want 2456 (10%%)", got)
	}
	if got := ReserveMB(8192); got != 2048 {
		t.Errorf("ReserveMB(8GB) = %d, want the 2048 floor", got)
	}
}

// An engine with no measured profile must be refused outright — a guessed
// number here becomes either an OOM or a permanently idle GPU.
func TestUnmeasuredEngineIsUnselectable(t *testing.T) {
	w := withGhostEngine(t)
	if sel, why := w.Selectable(); sel {
		t.Error("ghost has no measured VRAM but reports selectable")
	} else if why == "" {
		t.Error("Selectable() refused without saying why")
	}
	if _, err := Plan(w, []GPU{{Index: 0, TotalMB: 24564}}); err == nil {
		t.Error("Plan() accepted an unmeasured engine")
	}
}

// withGhostEngine adds an unmeasured engine to the catalog for one test (all
// shipped engines are measured now; wav2lipLS got its figure on 2026-10-08).
func withGhostEngine(t *testing.T) Engine {
	t.Helper()
	g := Engine{Name: "ghost", Label: "Ghost", Script: "ghost.py", Image: "mynah/ghost:test"}
	saved := catalog
	catalog = append(append([]Engine(nil), catalog...), g)
	t.Cleanup(func() { catalog = saved })
	return g
}

// wav2lipLS is the lightest tier and must stay selectable with its provenance.
func TestWav2lipLSIsMeasuredAndLightest(t *testing.T) {
	w, ok := Get("wav2lipls")
	if !ok {
		t.Fatal("wav2lipls missing from catalog")
	}
	if sel, why := w.Selectable(); !sel {
		t.Fatalf("wav2lipls should be selectable, got: %s", why)
	}
	for _, e := range All() {
		if e.Name != "wav2lipls" && e.VRAMSteadyMB > 0 && e.VRAMSteadyMB < w.VRAMSteadyMB {
			t.Errorf("%s (%dMB) is lighter than wav2lipls (%dMB); README tiers assume otherwise", e.Name, e.VRAMSteadyMB, w.VRAMSteadyMB)
		}
	}
}

// Every measured engine must carry its measurement conditions: the number is
// meaningless without the resolution/material it was taken at.
func TestMeasuredEnginesRecordProvenance(t *testing.T) {
	for _, e := range All() {
		if e.VRAMSteadyMB > 0 && e.VRAMMeasuredAt == "" {
			t.Errorf("engine %q has a VRAM figure but no VRAMMeasuredAt", e.Name)
		}
	}
}

// The composition the user asked for, against the card production actually has.
// Worth pinning as a test because the arithmetic is counter-intuitive: on a
// 24564MiB 4090 the reserve leaves 22108MiB, so 2 musetalk + 1 flashhead
// (22000MiB) fits with 108MiB to spare — razor thin but genuinely feasible,
// since the 2456MiB reserve IS the safety margin. Adding one more worker does
// not fit, and the planner must say so rather than let the operator find out
// via OOM.
func TestPlanMixOnOneProductionCard(t *testing.T) {
	gpu1 := []GPU{{Index: 1, TotalMB: 24564, UsedByOthersMB: 0}}

	tight := PlanMix(map[string]int{"musetalk": 2, "flashhead": 1}, gpu1)
	if !tight.Feasible {
		t.Errorf("2 musetalk + 1 flashhead = 22000MiB should fit inside the 22108MiB budget: %s", tight.Reason)
	}
	t.Log(tight.Explain)

	over := PlanMix(map[string]int{"musetalk": 3, "flashhead": 1}, gpu1)
	if over.Feasible {
		t.Errorf("3 musetalk + 1 flashhead = 29700MiB reported as fitting a 24564MiB card: %s", over.Explain)
	}
	if over.Reason == "" {
		t.Error("infeasible placement gave no reason")
	}
	t.Log(over.Reason)

	ok := PlanMix(map[string]int{"musetalk": 1, "flashhead": 1}, gpu1)
	if !ok.Feasible {
		t.Errorf("musetalk:1 + flashhead:1 = 14300MiB should fit: %s", ok.Reason)
	}
	if ok.Count("musetalk") != 1 || ok.Count("flashhead") != 1 {
		t.Errorf("placement counts = musetalk %d, flashhead %d; want 1 and 1",
			ok.Count("musetalk"), ok.Count("flashhead"))
	}
	for _, s := range ok.Slots {
		if s.GPU != 1 {
			t.Errorf("worker %s placed on GPU%d, but only GPU1 was offered", s.Engine, s.GPU)
		}
	}
}

// Aggregate free VRAM is not placeable VRAM. Two cards with 6.1GB each cannot
// host one 7.7GB worker, and the mix planner must not paper over that the way
// floor(sum/per) would.
func TestPlanMixNeverSpansGPUs(t *testing.T) {
	// 24564 − 15500 − 2456 = 6608 usable per card: room for one flashhead
	// (6600) and not for one musetalk (7700).
	gpus := []GPU{
		{Index: 0, TotalMB: 24564, UsedByOthersMB: 15500},
		{Index: 1, TotalMB: 24564, UsedByOthersMB: 15500},
	}
	if p := PlanMix(map[string]int{"musetalk": 1}, gpus); p.Feasible {
		t.Errorf("placed a 7700MiB worker into two 6.6GB holes: %s", p.Explain)
	}
	// flashhead fits one hole — one per card, not two-halves-of-two.
	p := PlanMix(map[string]int{"flashhead": 2}, gpus)
	if !p.Feasible {
		t.Fatalf("two 6600MiB workers should fit one per card: %s", p.Reason)
	}
	if p.Slots[0].GPU == p.Slots[1].GPU {
		t.Errorf("both workers landed on GPU%d, but neither card has room for two", p.Slots[0].GPU)
	}
}

// Biggest-first matters: placing the small worker first can strand the big one
// on a card layout where a size-aware order succeeds.
func TestPlanMixPacksBiggestFirst(t *testing.T) {
	// GPU0 usable = 24564-14000-2456 = 8108 (fits musetalk 7700 OR flashhead).
	// GPU1 usable = 24564-15500-2456 = 6608 (fits flashhead 6600 only).
	gpus := []GPU{
		{Index: 0, TotalMB: 24564, UsedByOthersMB: 14000},
		{Index: 1, TotalMB: 24564, UsedByOthersMB: 15500},
	}
	p := PlanMix(map[string]int{"musetalk": 1, "flashhead": 1}, gpus)
	if !p.Feasible {
		t.Fatalf("a size-aware packing exists (musetalk->GPU0, flashhead->GPU1) but was not found: %s", p.Reason)
	}
	if p.GPUFor("musetalk", 0) != 0 || p.GPUFor("flashhead", 0) != 1 {
		t.Errorf("musetalk on GPU%d, flashhead on GPU%d; want 0 and 1",
			p.GPUFor("musetalk", 0), p.GPUFor("flashhead", 0))
	}
	if !strings.Contains(p.Explain, "GPU0") || !strings.Contains(p.Explain, "GPU1") {
		t.Errorf("explanation must show both cards' arithmetic, got %q", p.Explain)
	}
}

// An unmeasured engine cannot enter a composition — same rule as Plan, since a
// guessed size in a mix corrupts the placement of every other worker too.
func TestPlanMixRefusesUnmeasuredAndUnknownEngines(t *testing.T) {
	gpus := []GPU{{Index: 1, TotalMB: 24564}}
	withGhostEngine(t)
	if p := PlanMix(map[string]int{"ghost": 1}, gpus); p.Feasible {
		t.Error("PlanMix accepted ghost, whose VRAM is unmeasured")
	}
	if p := PlanMix(map[string]int{"nosuchengine": 1}, gpus); p.Feasible || p.Reason == "" {
		t.Error("PlanMix accepted an unknown engine name")
	}
	// Zero-count entries are a normal way to express "none of this engine" from
	// the console and must not trip the selectability check.
	if p := PlanMix(map[string]int{"musetalk": 1, "ghost": 0}, gpus); !p.Feasible {
		t.Errorf("a zero count for an unselectable engine blocked the plan: %s", p.Reason)
	}
}

// Emptying the pool is a legitimate composition, not an error.
func TestPlanMixEmptyCompositionIsFeasible(t *testing.T) {
	p := PlanMix(map[string]int{}, []GPU{{Index: 1, TotalMB: 24564}})
	if !p.Feasible || len(p.Slots) != 0 {
		t.Errorf("empty composition: feasible=%v slots=%d, want true/0", p.Feasible, len(p.Slots))
	}
}
