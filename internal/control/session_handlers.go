package control

import (
	"net/http"
	"sort"

	"mynah/core"
)

// registerSessions is called from New when a SessionAdmin is wired.
func (s *Server) registerSessions() {
	s.private("GET /api/v1/sessions", s.handleSessionList)
	s.private("GET /api/v1/workers", s.handleWorkerList)
	s.private("GET /api/v1/sessions/{id}", s.handleSessionGet)
	s.private("DELETE /api/v1/sessions/{id}", s.handleSessionKick)
}

func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	list := s.deps.Sessions.ListSessions()
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	out := make([]map[string]any, 0, len(list))
	for _, si := range list {
		out = append(out, map[string]any{
			"id": si.ID, "created_at": si.CreatedAt, "turns": si.Turns,
			"speaking": si.Speaking, "voice": si.Voice,
		})
	}
	ok(w, out)
}

func (s *Server) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	si, found := s.deps.Sessions.GetSession(r.PathValue("id"))
	if !found {
		fail(w, http.StatusNotFound, "session not found")
		return
	}
	ok(w, map[string]any{
		"id": si.ID, "created_at": si.CreatedAt, "turns": si.Turns,
		"speaking": si.Speaking, "voice": si.Voice,
	})
}

func (s *Server) handleSessionKick(w http.ResponseWriter, r *http.Request) {
	if !s.deps.Sessions.KickSession(r.PathValue("id")) {
		fail(w, http.StatusNotFound, "session not found")
		return
	}
	ok(w, map[string]any{"kicked": true})
}

// handleWorkerList reports the avatar dispatch pool (每个 worker 单会话；
// session 非空 = 被该会话占用)。SessionAdmin 未实现 WorkerReporter 时返回空表。
func (s *Server) handleWorkerList(w http.ResponseWriter, r *http.Request) {
	rep, _ := s.deps.Sessions.(core.WorkerReporter)
	if rep == nil {
		ok(w, []core.WorkerInfo{})
		return
	}
	ok(w, rep.ListWorkers())
}
