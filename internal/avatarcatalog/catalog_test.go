package avatarcatalog

import (
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	const dir = "/app/workers/avatar/assets"
	cases := []struct {
		id   string
		want string
	}{
		{"", ""},
		{"default", dir + "/mynah-default.jpg"},
		{"  default  ", dir + "/mynah-default.jpg"},
		{"builtin:f", ""}, // not the id we use; the front-end PUTs bare "f"
		{"f", dir + "/mynah-f.jpg"},
		{"trained:job123", ""},
		{"builtin:nonexistent", ""},
		{"nonsense", ""},
	}
	for _, c := range cases {
		got := Resolve(c.id, dir)
		if got != c.want {
			t.Errorf("Resolve(%q) = %q, want %q", c.id, got, c.want)
		}
		if strings.Contains(got, `\`) {
			t.Errorf("Resolve(%q) = %q contains a backslash; cond_image must be a forward-slash container path", c.id, got)
		}
	}
}

func TestBuiltinsHaveDefault(t *testing.T) {
	var hasDefault bool
	for _, p := range Builtins {
		if p.ID == "default" {
			hasDefault = true
			if p.File == "" {
				t.Error(`"default" preset must map to the bundled default portrait so the catalog can switch back to it`)
			}
		}
	}
	if !hasDefault {
		t.Error(`Builtins must include a "default" entry`)
	}
}

// A built-in added without an engine is a silent hazard, not a compile error: on
// a mixed pool it dispatches to whichever engine is free, and the engine that
// cannot read the material keeps the face it booted with instead of failing. So
// require attribution for every built-in an operator can deliberately pick.
// "default" is exempt — it is the shipped fallback meaning "keep the worker's
// boot face", and constraining it would refuse every session on a single-engine
// deployment that never chose an avatar.
func TestBuiltinsDeclareTheirEngine(t *testing.T) {
	for _, p := range Builtins {
		if p.ID == "default" {
			if p.Engine != "" {
				t.Errorf(`"default" must stay engine-free, got %q`, p.Engine)
			}
			continue
		}
		if p.Engine == "" {
			t.Errorf("built-in %q (%s) declares no Engine; a portrait file is FlashHead material — "+
				"say so, or a mixed pool will serve it from MuseTalk and show a different person",
				p.ID, p.File)
		}
	}
}
