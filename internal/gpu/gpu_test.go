package gpu

import "testing"

// The distinction that matters: no reading at all is NOT 0 MiB. A worker still
// loading its model holds no CUDA context yet, and reporting it as 0 would make
// the console show a busy-to-be worker as free.
func TestForPIDsDistinguishesAbsentFromZero(t *testing.T) {
	s := Snapshot{Processes: []Process{
		{PID: 100, UsedMB: 7700, Device: 1},
		{PID: 200, UsedMB: 6600, Device: 1},
		{PID: 300, UsedMB: 1400, Device: 0},
	}}

	if mb, dev, found := s.ForPIDs(map[int]bool{200: true}); !found || mb != 6600 || dev != 1 {
		t.Errorf("ForPIDs(200) = (%d, %d, %v), want (6600, 1, true)", mb, dev, found)
	}
	mb, dev, found := s.ForPIDs(map[int]bool{999: true})
	if found {
		t.Error("ForPIDs for pids with no CUDA context must report found=false")
	}
	if mb != 0 || dev != -1 {
		t.Errorf("absent pids = (%d, %d), want (0, -1)", mb, dev)
	}
	if _, _, found := s.ForPIDs(nil); found {
		t.Error("an empty pid set must not match anything")
	}
}

// A container's CUDA memory can live in a forked child (vLLM does exactly this),
// so the reading has to be the sum over every process in the container — the
// main pid alone reports the TTS as holding nothing.
func TestForPIDsSumsTheWholeContainer(t *testing.T) {
	s := Snapshot{Processes: []Process{
		{PID: 555, UsedMB: 11644, Device: 0}, // forked vLLM worker
		{PID: 777, UsedMB: 7652, Device: 1},  // unrelated worker
	}}
	// 500 is the container's main pid and holds no context of its own.
	mb, dev, found := s.ForPIDs(map[int]bool{500: true, 555: true})
	if !found || mb != 11644 || dev != 0 {
		t.Errorf("ForPIDs(main+child) = (%d, %d, %v), want (11644, 0, true)", mb, dev, found)
	}
}
