package app

import "testing"

// Avatar ids contain colons ("bake:leiya_mt", "trained:42") but never "=", so
// the idle flag splits on the FIRST "=" only. A bare path keeps working and
// lands on "default" — the id an unbundled session runs under — so a
// single-avatar deployment upgrades with no flag change.
func TestParseIdleFlag(t *testing.T) {
	got, err := parseIdleFlag("/idle/lin.h264f")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Avatar != "default" || got[0].Path != "/idle/lin.h264f" {
		t.Errorf("bare path = %+v, want it keyed to default", got)
	}

	got, err = parseIdleFlag("default=/a.h264f,bake:leiya_mt=/b.h264f")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[1].Avatar != "bake:leiya_mt" || got[1].Path != "/b.h264f" {
		t.Errorf("colon-bearing avatar id mangled: %+v", got[1])
	}

	for _, bad := range []string{"=/a.h264f", "f=", "a=/x,a=/y"} {
		if _, err := parseIdleFlag(bad); err == nil {
			t.Errorf("--idle-ivf %q should be rejected", bad)
		}
	}
}

// Actions use "@" for the avatar rather than a second colon: "bake:leiya_mt:wave"
// cannot be split unambiguously.
func TestParseActionsFlag(t *testing.T) {
	got, err := parseActionsFlag("wave=/x.h264f,wave@bake:leiya_mt=/y.h264f")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Action != "wave" || got[0].Avatar != "default" {
		t.Errorf("un-keyed action = %+v, want it on default", got[0])
	}
	if got[1].Avatar != "bake:leiya_mt" || got[1].Path != "/y.h264f" {
		t.Errorf("keyed action = %+v", got[1])
	}

	for _, bad := range []string{"wave", "@f=/x", "wave@=/x", "w@f=/x,w@f=/y"} {
		if _, err := parseActionsFlag(bad); err == nil {
			t.Errorf("--actions %q should be rejected", bad)
		}
	}
}

// Actions play in the same baked-frame domain as their avatar's idle loop, so
// one bound to an avatar without a loop can never run. Catch it at boot, the
// way the old "--actions requires idle-local mode" check did.
func TestValidateActionAvatars(t *testing.T) {
	idles := []idleEntry{{Avatar: "default", Path: "/a"}}
	if err := validateActionAvatars(idles, []actionEntry{{Action: "wave", Avatar: "default", Path: "/w"}}); err != nil {
		t.Errorf("matching avatar rejected: %v", err)
	}
	if err := validateActionAvatars(idles, []actionEntry{{Action: "wave", Avatar: "f", Path: "/w"}}); err == nil {
		t.Error("an action on an avatar with no idle loop must be fatal")
	}
}
