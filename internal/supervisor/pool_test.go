package supervisor

import "testing"

// validatePoolName is the ONLY thing standing between an HTTP caller and a
// root-equivalent docker socket for create/remove. These cases are the reason
// it exists; treat a failure here as a security regression, not a style nit.
func TestValidatePoolNameRejectsEverythingButPoolWorkers(t *testing.T) {
	bad := []struct {
		name string
		why  string
	}{
		{"mynah-tts", "fixed service: removing TTS through the pool API must be impossible"},
		{"mynah-asr", "fixed service"},
		{"mynah-musetalk", "fixed service (compose-managed pool member, not a dynamic slot)"},
		{"mynah-cored", "cored would be deleting itself"},
		{"postgres", "someone else's container — the database"},
		{"postgresql_8ewf-postgresql_8Ewf-1", "the actual production database container"},
		{"", "empty"},
		{"mynah-musetalk-0", "slot 0 is out of range (slots are 1-based)"},
		{"mynah-musetalk-9", "slot 9 exceeds MaxPoolSlots"},
		{"mynah-musetalk-99", "far out of range"},
		{"mynah-musetalk-1/../../etc", "path traversal"},
		{"mynah-musetalk-1/x", "path separator"},
		{"../mynah-musetalk-1", "leading traversal"},
		{"mynah-MuseTalk-1", "uppercase is not the canonical form"},
		{"mynah-musetalk-1 ", "trailing space"},
		{"prefix-mynah-musetalk-1", "not anchored at the start"},
		{"mynah-musetalk-1x", "trailing junk"},
		{"mynah--1", "empty engine"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := validatePoolName(tc.name); err == nil {
				t.Fatalf("ACCEPTED %q — %s", tc.name, tc.why)
			}
		})
	}
}

func TestValidatePoolNameAcceptsCanonicalSlots(t *testing.T) {
	for _, tc := range []struct {
		name   string
		engine string
		slot   int
	}{
		{"mynah-musetalk-1", "musetalk", 1},
		{"mynah-musetalk-8", "musetalk", 8},
		{"mynah-flashhead-3", "flashhead", 3},
		{"mynah-wav2lipls-2", "wav2lipls", 2},
	} {
		engine, slot, err := validatePoolName(tc.name)
		if err != nil {
			t.Errorf("rejected valid name %q: %v", tc.name, err)
			continue
		}
		if engine != tc.engine || slot != tc.slot {
			t.Errorf("%q parsed as (%q,%d), want (%q,%d)", tc.name, engine, slot, tc.engine, tc.slot)
		}
	}
}

// PoolName must produce names its own validator accepts, for every legal slot —
// otherwise the pool manager could generate a name it then refuses to act on.
func TestPoolNameRoundTrips(t *testing.T) {
	for _, engine := range []string{"musetalk", "flashhead", "wav2lipls"} {
		for n := 1; n <= MaxPoolSlots; n++ {
			name := PoolName(engine, n)
			gotEngine, gotSlot, err := validatePoolName(name)
			if err != nil {
				t.Fatalf("PoolName(%q,%d)=%q rejected by validator: %v", engine, n, name, err)
			}
			if gotEngine != engine || gotSlot != n {
				t.Fatalf("round trip mismatch for %q: got (%q,%d)", name, gotEngine, gotSlot)
			}
		}
	}
}

// EngineOfService is the single source of "which engine does this worker run".
// Both shapes must resolve: a fixed compose worker (engine from the registry)
// and a dynamic slot (engine from its name). Non-worker services must resolve
// to "" so they never get counted into a pool.
func TestEngineOfServiceCoversBothWorkerShapes(t *testing.T) {
	for name, want := range map[string]string{
		"musetalk":    "musetalk",  // fixed, compose-managed
		"musetalk2":   "musetalk",  // fixed, second slot — same engine
		"flashhead-3": "flashhead", // dynamic slot
		"musetalk-4":  "musetalk",  // dynamic slot of the same engine
		"tts":         "",
		"asr":         "",
		"postgres":    "",
		"":            "",
		"../escape":   "",
	} {
		if got := EngineOfService(name); got != want {
			t.Errorf("EngineOfService(%q) = %q, want %q", name, got, want)
		}
	}
}

// Ensure/Remove must refuse an illegal name BEFORE touching the socket. A nil
// http client makes any actual request panic, so reaching docker fails loudly
// rather than passing silently.
func TestEnsureAndRemoveValidateBeforeDialing(t *testing.T) {
	d := &Docker{} // no http client on purpose
	spec := ContainerSpec{Name: "mynah-tts", Image: "x", Cmd: []string{"y"}}
	if err := d.Ensure(nil, spec); err == nil {
		t.Error("Ensure accepted a fixed service name")
	}
	if err := d.Remove(nil, "postgres"); err == nil {
		t.Error("Remove accepted a non-pool container")
	}
}

// A spec missing its image or command must be refused rather than sent to
// docker, where it would create a broken container.
func TestEnsureRequiresImageAndCmd(t *testing.T) {
	d := &Docker{}
	if err := d.Ensure(nil, ContainerSpec{Name: "mynah-musetalk-1"}); err == nil {
		t.Error("Ensure accepted a spec with no image or command")
	}
	if err := d.Ensure(nil, ContainerSpec{
		Name: "mynah-musetalk-1", Image: "img",
	}); err == nil {
		t.Error("Ensure accepted a spec with no command")
	}
}
