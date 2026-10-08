package control

import (
	"io"
	"net/http"

	"mynah/internal/config"
)

// registerConfig is called from New when a config manager is present.
func (s *Server) registerConfig() {
	s.private("GET /api/v1/config", s.handleConfigGetAll)
	s.private("GET /api/v1/config/{group}", s.handleConfigGet)
	s.private("PUT /api/v1/config/{group}", s.handleConfigPut)
}

func (s *Server) handleConfigGetAll(w http.ResponseWriter, r *http.Request) {
	ok(w, s.deps.Config.Current())
}

func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	group := r.PathValue("group")
	cfg := s.deps.Config.Current()
	switch group {
	case "llm":
		ok(w, cfg.LLM)
	case "tts":
		ok(w, cfg.TTS)
	case "rag":
		ok(w, cfg.RAG)
	case "system":
		ok(w, cfg.System)
	case "avatar":
		ok(w, cfg.Avatar)
	case "turn":
		ok(w, cfg.Turn)
	case "brain":
		ok(w, cfg.Brain)
	default:
		fail(w, http.StatusNotFound, "unknown config group: "+group)
	}
}

func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	group := r.PathValue("group")
	if _, known := config.Hot[group]; !known {
		fail(w, http.StatusNotFound, "unknown config group: "+group)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(body) == 0 {
		fail(w, http.StatusBadRequest, "empty body")
		return
	}
	hot, err := s.deps.Config.Put(r.Context(), group, body)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	applied := "hot"
	if !hot {
		applied = "restart_required"
	}
	ok(w, map[string]any{"applied": applied, "config": groupOf(s.deps.Config.Current(), group)})
}

func groupOf(cfg config.Config, group string) any {
	switch group {
	case "llm":
		return cfg.LLM
	case "tts":
		return cfg.TTS
	case "rag":
		return cfg.RAG
	case "system":
		return cfg.System
	case "avatar":
		return cfg.Avatar
	case "turn":
		return cfg.Turn
	case "brain":
		return cfg.Brain
	}
	return nil
}
