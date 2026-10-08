package control

import (
	"encoding/json"
	"net/http"

	"mynah/core"
	"mynah/internal/avatarcatalog"
)

// handleAvatars returns the OSS built-in avatar catalog so the console can offer
// an appearance picker instead of a free-text id. Response shape matches the
// voices contract: [{id, name}]. It is static (no worker round-trip, no file
// stat), so it works without --db's pipeline being live and without cored
// needing access to the worker's portrait files. The selected id is persisted
// via PUT /api/v1/config/avatar and resolved to a portrait at session start
// (see avatarcatalog.Resolve + session.Manager.SetAvatarSource).
func (s *Server) handleAvatars(w http.ResponseWriter, r *http.Request) {
	// Rescan on read. Baking happens outside cored (a GPU script writes a new
	// directory), so there is no event to hook — and a stale list means an
	// operator bakes an avatar and cannot find it. The scan is a stat of a
	// handful of directories, far cheaper than the alternative of telling
	// people to restart cored.
	if s.deps.RescanAvatars != nil {
		s.deps.RescanAvatars()
	}
	type item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		// Engine names which avatar engine can speak this appearance, empty for
		// "any". Baked avatars carry it because they are NOT interchangeable: a
		// MuseTalk bake cannot be rendered by FlashHead, so the console must
		// stop an operator binding a channel to something its pool can't draw.
		//
		// Built-in PORTRAITS carry it too. They used to be reported as
		// engine-less on the theory that any engine takes a cond_image file —
		// which stopped being true when mixed pools shipped: a single JPEG is
		// FlashHead-shaped material, MuseTalk handed one keeps the face it
		// booted with. avatarcatalog.EngineFor is the single source of that
		// attribution and it already says so; dropping the field here made the
		// console show no FlashHead-renderable avatar at all, so an operator
		// running a FlashHead worker had nothing to bind a channel to.
		Engine string `json:"engine,omitempty"`
		// Baked marks a directory-shaped avatar, which needs preloading before
		// a session can use it without a ~15s stall.
		Baked bool `json:"baked,omitempty"`
		// HasIdle: this avatar has a baked idle loop of its own, so its idle
		// segment is replayed by cored at zero GPU cost. False means the engine
		// renders the idle live — the right person, but a worker busy for as
		// long as the channel is open. The console shows that so the operator
		// can bake one.
		HasIdle bool `json:"has_idle"`
	}
	idle := map[string]bool{}
	if rep, okI := s.deps.Sessions.(core.AvatarIdleReporter); okI {
		for _, id := range rep.AvatarsWithIdle() {
			idle[id] = true
		}
	}
	baked := avatarcatalog.BakedAvatars()
	out := make([]item, 0, len(avatarcatalog.Builtins)+len(baked))
	for _, p := range avatarcatalog.Builtins {
		out = append(out, item{ID: p.ID, Name: p.Name, Engine: p.Engine, HasIdle: idle[p.ID]})
	}
	for _, b := range baked {
		out = append(out, item{ID: b.ID, Name: b.Name, Engine: b.Engine, Baked: true, HasIdle: idle[b.ID]})
	}
	ok(w, out)
}

// registerAvatarPreload mounts the bind-time warmup.
func (s *Server) registerAvatarPreload() {
	s.private("POST /api/v1/avatars/preload", s.handleAvatarPreload)
}

// handleAvatarPreload makes an avatar resident on the workers that can render it.
//
// This is the endpoint the console calls when an operator BINDS an avatar to a
// channel. Loading a bake costs ~15s and the worker withholds Ready until it
// finishes, so without this the first visitor to that channel stares at a blank
// screen for 15 seconds. Paying it here — synchronously, in an admin action —
// is the whole point.
//
// Broadcast to every worker running the avatar's engine (dispatch picks
// whichever of those is free, so an avatar resident on only some of them would
// work intermittently). Scoped rather than pool-wide: under a mixed pool the
// other engine cannot load this material at all, and counting it as a failed
// worker would warn the operator about a pool that is correctly warmed.
func (s *Server) handleAvatarPreload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Avatar string `json:"avatar"` // avatar id, e.g. "bake:leiya_mt"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	pre, canPreload := s.deps.Sessions.(core.AvatarPreloader)
	if !canPreload {
		fail(w, http.StatusNotImplemented, "当前构建不支持形象预载")
		return
	}
	path := s.deps.ResolveAvatarSource(req.Avatar)
	if path == "" {
		fail(w, http.StatusBadRequest,
			"无法解析形象 "+req.Avatar+"（不在目录中，或不是烘焙形象）")
		return
	}
	// avatarcatalog is the single source of engine attribution — the same call
	// session.Create makes to decide which workers may serve this avatar.
	engine := avatarcatalog.EngineFor(req.Avatar)
	ready, total, reason := pre.PreloadAvatar(r.Context(), path, engine)
	out := map[string]any{"avatar": req.Avatar, "path": path,
		"engine": engine, "ready": ready, "total": total}
	if reason != "" {
		out["warning"] = reason
	}
	ok(w, out)
}
