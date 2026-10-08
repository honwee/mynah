package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"mynah/internal/avatarengine"
	"mynah/internal/supervisor"
)

func (s *Server) registerEngineSwitch() {
	s.private("POST /api/v1/avatar/engine", s.handleEngineSwitch)
}

type engineSwitchReq struct {
	Engine string `json:"engine"`
	// Size lets the switch also resize; 0 = keep the current pool size.
	Size int `json:"size"`
}

// handleEngineSwitch makes the pool run ONE engine: the target at the requested
// size, everything else at zero.
//
// It is now a thin wrapper over the composition reconciler. Keeping it means the
// console's "只跑此引擎" button stays a single click, while the mixed-pool
// endpoint handles the general case; keeping it as its own rolling-replacement
// implementation is what made "pool = one engine" an assumption in three places.
func (s *Server) handleEngineSwitch(w http.ResponseWriter, r *http.Request) {
	var req engineSwitchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}

	size := req.Size
	if size <= 0 {
		// Keep the pool's current total: switching engine is not a resize.
		list, err := s.deps.Supervisor.List(r.Context())
		if err != nil {
			fail(w, http.StatusBadGateway, "list services: "+err.Error())
			return
		}
		for _, n := range countByEngine(currentComposition(list)) {
			size += n
		}
		if size < 1 {
			size = 1
		}
	}

	want, ferr := normalizeComposition(map[string]int{req.Engine: size})
	if ferr != "" {
		fail(w, http.StatusBadRequest, ferr)
		return
	}
	s.reconcilePool(w, r, want)
}

// retireWorker removes a dynamic slot, or stops a compose-managed one (which
// this server must not delete — compose owns it).
func (s *Server) retireWorker(ctx context.Context, d *supervisor.Docker,
	svc supervisor.Service) error {
	if _, _, err := supervisor.ParsePoolSlot("mynah-" + svc.Name); err == nil {
		return d.Remove(ctx, "mynah-"+svc.Name)
	}
	// compose-managed: stop but keep, so `docker compose up` can restore it.
	return d.Stop(ctx, svc.Name)
}

// waitReady is intentionally NOT used to block the HTTP response: model load
// takes 50-150s, far past a sane request timeout. cored's own pool reconciler
// picks the worker up within one probe interval, and the console polls. Kept
// here as the documented reason the API returns before workers are serving.
var _ = time.Second

// engineMaterial resolves the configured material for an engine. Sourced from
// startup flags rather than the DB: these are host paths tied to the
// deployment, and a wrong value creates containers that cannot start.
func (s *Server) engineMaterial(engine string) (avatarengine.Material, bool) {
	m, ok := s.deps.EngineMaterial[engine]
	return m, ok && !isZeroMaterial(m)
}

func isZeroMaterial(m avatarengine.Material) bool {
	return strings.TrimSpace(m.ModelsDir) == "" &&
		strings.TrimSpace(m.RepoDir) == "" &&
		strings.TrimSpace(m.AvatarDir) == "" &&
		strings.TrimSpace(m.AvatarImage) == ""
}
