package control

import (
	"testing"

	"mynah/core"
)

// Dispatch is engine-blind (acquireWorker takes the first free worker), so a
// mixed pool silently hands visitors different avatars. Detecting it is the
// only thing standing between that and a confused operator.
func TestPoolEngineMixDetectsMultipleEngines(t *testing.T) {
	engines, mixed := poolEngineMix([]core.WorkerInfo{
		{Addr: "a", Detail: "musetalk 1.5 avatar engine"},
		{Addr: "b", Detail: "flashhead avatar engine"},
	})
	if !mixed {
		t.Fatal("two engines in the pool not reported as mixed")
	}
	if len(engines) != 2 {
		t.Fatalf("got %v, want both engines listed", engines)
	}
}

func TestPoolEngineMixQuietWhenHomogeneous(t *testing.T) {
	engines, mixed := poolEngineMix([]core.WorkerInfo{
		{Addr: "a", Detail: "musetalk 1.5 avatar engine"},
		{Addr: "b", Detail: "musetalk 1.5 avatar engine"},
	})
	if mixed {
		t.Error("a single-engine pool reported as mixed")
	}
	if len(engines) != 1 || engines[0] != "musetalk" {
		t.Errorf("got %v, want [musetalk]", engines)
	}
}

// An unrecognized banner must not be counted as a distinct engine, or every
// unknown worker would raise a false mixed-pool alarm.
func TestPoolEngineMixIgnoresUnknownBanners(t *testing.T) {
	_, mixed := poolEngineMix([]core.WorkerInfo{
		{Addr: "a", Detail: "musetalk 1.5 avatar engine"},
		{Addr: "b", Detail: "some future engine"},
	})
	if mixed {
		t.Error("unknown banner triggered a false mixed-pool warning")
	}
}
