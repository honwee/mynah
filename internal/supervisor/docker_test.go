package supervisor

import "testing"

// A dynamic pool slot has no registry entry to read a gpu index from, so the
// only truthful source is the container's own device request. "No request" must
// stay distinguishable from "GPU 0", because the console renders the absent case
// as 不占用 GPU — a lie for a worker holding several GB.
func TestDeviceRequestGPU(t *testing.T) {
	type req = struct {
		DeviceIDs []string `json:"DeviceIDs"`
	}
	cases := []struct {
		name      string
		reqs      []req
		want      int
		wantFound bool
	}{
		{"none", nil, 0, false},
		{"empty ids", []req{{}}, 0, false},
		{"gpu0", []req{{DeviceIDs: []string{"nvidia.com/gpu=0"}}}, 0, true},
		{"gpu1", []req{{DeviceIDs: []string{"nvidia.com/gpu=1"}}}, 1, true},
		{"second entry", []req{{DeviceIDs: []string{"other=x"}},
			{DeviceIDs: []string{"nvidia.com/gpu=1"}}}, 1, true},
		{"not a number", []req{{DeviceIDs: []string{"nvidia.com/gpu=all"}}}, 0, false},
	}
	for _, c := range cases {
		got, found := deviceRequestGPU(c.reqs)
		if got != c.want || found != c.wantFound {
			t.Errorf("%s: got (%d, %v), want (%d, %v)", c.name, got, found, c.want, c.wantFound)
		}
	}
}

// The label is what the operator reads on the card. Two engines both have a
// "#1", so the engine name has to be in the label itself — otherwise a mixed
// pool shows two identically-titled cards and no way to tell which is which.
func TestEngineLabelNamesTheEngine(t *testing.T) {
	if got := engineLabel("musetalk"); got != "MuseTalk 1.5" {
		t.Errorf("engineLabel(musetalk) = %q", got)
	}
	if got := engineLabel("flashhead"); got != "SoulX-FlashHead 1.3B" {
		t.Errorf("engineLabel(flashhead) = %q", got)
	}
	// A hand-created container for an engine not in the catalog still has to
	// read as something rather than an empty title.
	if got := engineLabel("homegrown"); got != "homegrown" {
		t.Errorf("engineLabel(unknown) = %q, want the raw id", got)
	}
	if engineLabel("musetalk") == engineLabel("flashhead") {
		t.Error("the two pool engines must not share a label")
	}
}

// Every engine the pool can compose has to explain what material it needs;
// silence on a dynamic slot's card is what prompted this.
func TestEngineDescriptionCoversPoolEngines(t *testing.T) {
	for _, e := range []string{"musetalk", "flashhead"} {
		if engineDescription(e) == "" {
			t.Errorf("engine %q has no description; its dynamic slot card would say nothing", e)
		}
	}
}
