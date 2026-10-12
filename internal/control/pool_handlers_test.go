package control

import "testing"

// Ports in the pool range are NOT contiguous with slot numbers: the
// compose-managed workers hold 9414 and 9417. Allocating base+slot-1 would hand
// a new worker the port musetalk2 already binds, and the container would fail
// to start with a confusing error. Allocation must skip what is taken.
func TestFreePoolPortSkipsPortsTakenByFixedWorkers(t *testing.T) {
	taken := map[int]bool{9414: true, 9417: true} // musetalk, musetalk2

	p1, err := freePoolPort(taken)
	if err != nil {
		t.Fatal(err)
	}
	if p1 == 9414 || p1 == 9417 {
		t.Fatalf("allocated an in-use port %d", p1)
	}
	if p1 != 9415 {
		t.Errorf("got %d, want the lowest free port 9415", p1)
	}

	taken[p1] = true
	p2, err := freePoolPort(taken)
	if err != nil {
		t.Fatal(err)
	}
	if p2 == p1 || p2 == 9414 || p2 == 9417 {
		t.Fatalf("second allocation collided: %d", p2)
	}
	if p2 != 9416 {
		t.Errorf("got %d, want 9416", p2)
	}
}

func TestFreePoolPortExhausts(t *testing.T) {
	taken := map[int]bool{}
	for p := poolPortBase; p <= poolPortEnd; p++ {
		taken[p] = true
	}
	if _, err := freePoolPort(taken); err == nil {
		t.Error("expected an error when the range is exhausted")
	}
}

// Slot numbers are reused after a shrink, so allocation must find the lowest
// gap rather than counting upward from the pool size.
func TestFreePoolSlotFillsGaps(t *testing.T) {
	used := map[int]bool{1: true, 2: true, 4: true}
	n, err := freePoolSlot(used)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("got slot %d, want the gap at 3", n)
	}
}

func TestFreePoolSlotExhausts(t *testing.T) {
	used := map[int]bool{}
	for i := 1; i <= 8; i++ {
		used[i] = true
	}
	if _, err := freePoolSlot(used); err == nil {
		t.Error("expected an error when all slots are used")
	}
}
