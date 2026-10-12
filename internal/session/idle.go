// Per-avatar idle and action clips.
//
// The idle segment (nobody speaking) used to be ONE process-global pre-encoded
// H.264 loop replayed by cored itself, so every channel showed the same person
// while silent no matter which avatar it was bound to — the avatar only took
// effect during the seconds someone was actually speaking. The data model was
// per-avatar all along (avatar_bake_jobs.avatar_id, cache files named
// idle_<avatar>__<motion>.h264f); only the last mile threw the id away.
//
// A registry keyed by avatar id fixes that. Lookup is EXACT: an avatar with no
// entry gets a nil set, which flips the session out of idle-local mode and lets
// the worker drive the engine with silence instead (avatar_common.py: "no idle
// library"). That costs GPU but shows the right person, and baking that avatar
// an idle returns it to zero-cost replay. A global fallback here would silently
// reinstate the very bug this replaces.
package session

import (
	"log"
	"sort"
	"strings"

	"mynah/internal/mediaenc"
)

// AvatarIdle is one avatar's baked idle-local material: the loop replayed
// during silence plus its one-shot action clips (动作编排), which live in the
// same baked-frame domain and are therefore meaningless without the loop.
type AvatarIdle struct {
	Idle      *mediaenc.IdleLoop
	Actions   map[string]*mediaenc.IdleLoop
	ActionIDs []string // stable display order
}

// SetAvatarIdle installs (or replaces, or with a nil set removes) one avatar's
// idle material. Sessions created afterwards pick it up; running ones keep the
// set they were created with.
//
// Unlike the startup-only SetIdleLocal it replaces, this is called at runtime by
// the idle-bake apply path, so the registry is mutex-guarded — session create
// reads it concurrently.
func (m *Manager) SetAvatarIdle(avatarID string, set *AvatarIdle) {
	m.idleMu.Lock()
	defer m.idleMu.Unlock()
	if m.idles == nil {
		m.idles = map[string]*AvatarIdle{}
	}
	if set == nil {
		delete(m.idles, avatarID)
		return
	}
	m.idles[avatarID] = set
}

// SetIdleSilence installs the 20ms silence packet the audio track sends while
// idle. It is codec-level, not avatar-level, so it stays global.
func (m *Manager) SetIdleSilence(silence []byte) {
	m.idleMu.Lock()
	defer m.idleMu.Unlock()
	m.silencePkt = silence
}

// idleFor returns the avatar's idle material, or nil for "this avatar has no
// idle of its own" — see the package comment: no fallback, on purpose.
func (m *Manager) idleFor(avatarID string) *AvatarIdle {
	m.idleMu.RLock()
	defer m.idleMu.RUnlock()
	return m.idles[avatarID]
}

func (m *Manager) silence() []byte {
	m.idleMu.RLock()
	defer m.idleMu.RUnlock()
	return m.silencePkt
}

// ActionIDs lists the action clips available for one avatar, in display order
// (empty = none, which is what the visitor action bar renders as "hidden").
func (m *Manager) ActionIDs(avatarID string) []string {
	if set := m.idleFor(avatarID); set != nil && set.ActionIDs != nil {
		return set.ActionIDs
	}
	return []string{}
}

// AvatarsWithIdle lists the avatar ids that have idle material of their own,
// sorted. The console uses it to mark the rest as "idle rendered live by the
// engine (costs GPU)" — without that, dropping the global fallback would be a
// silent performance regression.
func (m *Manager) AvatarsWithIdle() []string {
	m.idleMu.RLock()
	defer m.idleMu.RUnlock()
	out := make([]string, 0, len(m.idles))
	for id := range m.idles {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// HasIdle reports whether one avatar has idle material of its own.
func (m *Manager) HasIdle(avatarID string) bool { return m.idleFor(avatarID) != nil }

// AvatarIdleSet returns one avatar's installed material (nil = none). The
// idle-bake apply path reads it to keep the avatar's existing action clips when
// swapping in a new idle loop.
func (m *Manager) AvatarIdleSet(avatarID string) *AvatarIdle { return m.idleFor(avatarID) }

// currentAvatarID is the live console selection (config.avatar.current), which
// is what an unbundled session — the default visitor and the admin playground —
// runs on. "default" when nothing is selected; a channel bundle's frozen avatar
// wins over it at session create.
func (m *Manager) currentAvatarID() string {
	if m.avatarCurrent != nil {
		if v := strings.TrimSpace(m.avatarCurrent()); v != "" {
			return v
		}
	}
	return "default"
}

// EffectiveAvatarID resolves the avatar a session will actually run on: a
// channel's frozen selection wins, otherwise the live console one. Same rule as
// session create, exported so the visitor-facing metadata (the action bar) is
// answered for the same avatar the session will use.
func (m *Manager) EffectiveAvatarID(frozen string) string {
	if v := strings.TrimSpace(frozen); v != "" {
		return v
	}
	return m.currentAvatarID()
}

// warnNoIdleOnce logs the engine-driven-idle fallback the first time each avatar
// hits it. Once per avatar, not per session: this is a standing property of the
// avatar, and a busy channel would otherwise repeat it on every visitor.
func (m *Manager) warnNoIdleOnce(avatarID string) {
	m.idleMu.Lock()
	if m.noIdleWarned == nil {
		m.noIdleWarned = map[string]bool{}
	}
	first := !m.noIdleWarned[avatarID]
	m.noIdleWarned[avatarID] = true
	m.idleMu.Unlock()
	if first {
		log.Printf("avatar %q has no idle material of its own: its idle segment will be rendered live by the engine (right person, costs GPU). Bake it an idle to get zero-cost replay back.", avatarID)
	}
}
