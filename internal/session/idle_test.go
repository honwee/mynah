package session

import (
	"testing"

	"mynah/internal/mediaenc"
)

func set(frames int, actions ...string) *AvatarIdle {
	s := &AvatarIdle{
		Idle:    &mediaenc.IdleLoop{Frames: make([][]byte, frames), Codec: "h264"},
		Actions: map[string]*mediaenc.IdleLoop{},
	}
	for _, a := range actions {
		s.Actions[a] = &mediaenc.IdleLoop{Codec: "h264"}
		s.ActionIDs = append(s.ActionIDs, a)
	}
	return s
}

// Lookup is EXACT — no global fallback. This is the whole point of the change:
// the idle loop used to be one process-global asset, so every channel showed
// the same person while silent no matter which avatar it was bound to. An
// avatar with no entry must get nil, which turns idle-local OFF for its
// sessions and lets the worker drive the engine (right person, costs GPU).
func TestIdleForIsExactWithNoFallback(t *testing.T) {
	m := &Manager{}
	m.SetAvatarIdle("default", set(10))
	m.SetAvatarIdle("bake:leiya_mt", set(20))

	if got := m.idleFor("bake:leiya_mt"); got == nil || len(got.Idle.Frames) != 20 {
		t.Fatalf("bake avatar got %v, want its own 20-frame loop", got)
	}
	if got := m.idleFor("f"); got != nil {
		t.Errorf("avatar with no idle got %v; a fallback here reinstates the bug being fixed", got)
	}
	if m.HasIdle("f") {
		t.Error("HasIdle must report false so the console can flag engine-driven idle")
	}
}

// Applying a bake for one avatar must not touch another's — the old
// applyAsset dropped the job's avatar_id and swapped the single global loop.
func TestSetAvatarIdleIsIsolated(t *testing.T) {
	m := &Manager{}
	m.SetAvatarIdle("default", set(10))
	m.SetAvatarIdle("f", set(5))

	m.SetAvatarIdle("f", set(99))
	if got := len(m.idleFor("default").Idle.Frames); got != 10 {
		t.Errorf("default loop became %d frames after applying an idle to f", got)
	}
	if got := len(m.idleFor("f").Idle.Frames); got != 99 {
		t.Errorf("f loop = %d frames, want the newly applied 99", got)
	}
	if want := []string{"default", "f"}; len(m.AvatarsWithIdle()) != len(want) {
		t.Errorf("AvatarsWithIdle = %v, want %v", m.AvatarsWithIdle(), want)
	}
}

// Action clips belong to an avatar too: they are baked in the same frame domain
// as that avatar's idle loop, so serving another avatar's ids would offer the
// visitor a gesture that plays as the wrong person.
func TestActionIDsArePerAvatar(t *testing.T) {
	m := &Manager{}
	m.SetAvatarIdle("default", set(10, "wave"))
	m.SetAvatarIdle("f", set(10))

	if got := m.ActionIDs("default"); len(got) != 1 || got[0] != "wave" {
		t.Errorf("default actions = %v, want [wave]", got)
	}
	if got := m.ActionIDs("f"); len(got) != 0 {
		t.Errorf("f actions = %v, want none (it has no baked clips)", got)
	}
	// Never nil: the visitor page JSON-encodes this straight into an array.
	if got := m.ActionIDs("nobody"); got == nil {
		t.Error("unknown avatar must give an empty slice, not nil")
	}
}

// The action bar and session dispatch have to agree on which avatar is in play,
// or a channel shows clips it cannot run.
func TestEffectiveAvatarID(t *testing.T) {
	m := &Manager{}
	if got := m.EffectiveAvatarID(""); got != "default" {
		t.Errorf("no selection = %q, want default", got)
	}
	m.avatarCurrent = func() string { return "bake:leiya_mt" }
	if got := m.EffectiveAvatarID(""); got != "bake:leiya_mt" {
		t.Errorf("live selection = %q", got)
	}
	if got := m.EffectiveAvatarID("f"); got != "f" {
		t.Errorf("a channel's frozen avatar must win over the live selection, got %q", got)
	}
	if got := m.EffectiveAvatarID("  "); got != "bake:leiya_mt" {
		t.Errorf("blank frozen avatar = unbound channel, got %q", got)
	}
}

// An avatar without idle material has no actions, and PlayAction must say so
// rather than panic on a nil set.
func TestPlayActionWithoutIdleMaterial(t *testing.T) {
	s := &Session{}
	if err := s.PlayAction("wave"); err == nil {
		t.Error("want an error for an avatar with no baked idle material")
	}
}
