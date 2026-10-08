package app

import (
	"testing"

	"mynah/internal/avatarcatalog"
)

// The shared resolver must know baked DIRECTORIES, not just built-in portrait
// files. It called avatarcatalog.Resolve for two releases, which knows only the
// built-ins, so every "bake:*" id resolved to "" — session start read that as
// "keep your current avatar" (the bake looked right purely because the MuseTalk
// worker boots with one preloaded) and bind-time preload rejected the exact case
// it exists for.
func TestAvatarResolverHandlesBakes(t *testing.T) {
	avatarcatalog.SetBaked([]avatarcatalog.Baked{
		{ID: "bake:leiya_mt", Name: "leiya_mt", Engine: "musetalk", Path: "/bakes/leiya_mt"},
	})
	defer avatarcatalog.SetBaked(nil)

	r := avatarResolver("/assets", nil)
	if got := r("bake:leiya_mt"); got != "/bakes/leiya_mt" {
		t.Errorf("bake id resolved to %q, want /bakes/leiya_mt", got)
	}
	if got := r("f"); got != "/assets/mynah-f.jpg" {
		t.Errorf("built-in resolved to %q", got)
	}
}

// Unknown ids fall through to the trained-likeness lookup, and "" (no override)
// when there is none. The fallback must not shadow the catalog.
func TestAvatarResolverTrainedFallback(t *testing.T) {
	calls := 0
	r := avatarResolver("/assets", func(id string) string {
		calls++
		return "/trained/" + id
	})
	if got := r("trained:42"); got != "/trained/trained:42" {
		t.Errorf("unknown id = %q, want the trained fallback", got)
	}
	if got := r("f"); got != "/assets/mynah-f.jpg" || calls != 1 {
		t.Errorf("built-in = %q with %d fallback calls; the catalog must win", got, calls)
	}
	if got := avatarResolver("/assets", nil)("trained:42"); got != "" {
		t.Errorf("no fallback should give %q, got %q", "", got)
	}
}
