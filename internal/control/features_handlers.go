package control

import (
	"net/http"

	"mynah/core"
)

// handleFeatures reports the build's capability switches so the console can
// show/hide feature UI without guessing. Every feature ships in the
// open-source build (default gate: all on); a narrower assembly reports what
// it actually enables.
func (s *Server) handleFeatures(w http.ResponseWriter, r *http.Request) {
	gate := s.deps.Gate
	if gate == nil {
		gate = core.StaticGate{} // bare build: everything off
	}
	features := map[string]bool{}
	for _, f := range core.Features {
		features[f] = gate.Enabled(f)
	}
	ok(w, map[string]any{"features": features, "engine": s.deps.EngineDetail})
}
