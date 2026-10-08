package avatarcatalog

import "testing"

func withBakes(t *testing.T, list []Baked) {
	t.Helper()
	prev := BakedAvatars()
	SetBaked(list)
	t.Cleanup(func() { SetBaked(prev) })
}

// A baked avatar resolves to its DIRECTORY (what MuseTalk/wav2lipLS consume),
// while a built-in resolves to a portrait FILE (what FlashHead consumes). Both
// travel in cond_image, so the resolver has to keep them straight.
func TestResolveAnyHandlesBothShapes(t *testing.T) {
	withBakes(t, []Baked{
		{ID: "bake:leiya_mt", Name: "leiya_mt", Engine: "musetalk", Path: "/bakes/leiya_mt"},
	})

	if got := ResolveAny("bake:leiya_mt", "/assets"); got != "/bakes/leiya_mt" {
		t.Errorf("baked id resolved to %q, want the bake directory", got)
	}
	if got := ResolveAny("f", "/assets"); got != "/assets/mynah-f.jpg" {
		t.Errorf("builtin id resolved to %q, want the portrait file", got)
	}
}

// An unknown id must resolve to "" — the worker reads that as "keep what you
// have", which is the safe outcome. Returning a wrong path would make the
// worker silently render someone else.
func TestResolveAnyUnknownIsEmpty(t *testing.T) {
	withBakes(t, nil)
	for _, id := range []string{"bake:does-not-exist", "totally-unknown", ""} {
		if got := ResolveAny(id, "/assets"); got != "" {
			t.Errorf("ResolveAny(%q) = %q, want empty", id, got)
		}
	}
}

// Engine binding is DERIVED from the avatar: a MuseTalk bake cannot be drawn by
// FlashHead, so the appearance determines the engine. Deliberately-chosen
// built-in portraits are attributed too (they are single JPEGs = FlashHead
// material); only "default" and unknown ids stay unbound.
func TestEngineForDerivesFromBake(t *testing.T) {
	withBakes(t, []Baked{
		{ID: "bake:leiya_mt", Name: "leiya_mt", Engine: "musetalk", Path: "/bakes/leiya_mt"},
		{ID: "bake:portrait_fh", Name: "portrait_fh", Engine: "flashhead", Path: "/bakes/p"},
	})

	if got := EngineFor("bake:leiya_mt"); got != "musetalk" {
		t.Errorf("EngineFor(bake:leiya_mt) = %q, want musetalk", got)
	}
	if got := EngineFor("bake:portrait_fh"); got != "flashhead" {
		t.Errorf("EngineFor(bake:portrait_fh) = %q, want flashhead", got)
	}
	// A built-in portrait is FlashHead material. Unattributed, a mixed pool
	// would hand it to MuseTalk, which keeps its own face instead of failing —
	// the visitor silently meets the wrong person.
	if got := EngineFor("f"); got != "flashhead" {
		t.Errorf("EngineFor(f) = %q, want flashhead", got)
	}
	// "default" is the shipped fallback for config.avatar.current, i.e. "whatever
	// this worker boots with" rather than a choice. Binding it would refuse every
	// session on a musetalk-only deployment that never picked a face.
	if got := EngineFor("default"); got != "" {
		t.Errorf("EngineFor(default) = %q, want empty (must not constrain the shipped fallback)", got)
	}
	if got := EngineFor("nope"); got != "" {
		t.Errorf("EngineFor(unknown) = %q, want empty", got)
	}
	if got := EngineFor(""); got != "" {
		t.Errorf("EngineFor(empty) = %q, want empty", got)
	}
}
