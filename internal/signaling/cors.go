package signaling

import "net/http"

// Cross-origin support for the browser SDK (web/sdk). A dashboard that embeds
// the digital human is served from its own domain, so every visitor endpoint it
// touches needs CORS headers or the browser refuses to hand the page the reply.
//
// The Origin is reflected rather than checked here, and that is deliberate: the
// real access control already runs inside the handlers — token (constant-time),
// ch.Domains via originAllowed, and the CIDR list, in that order. Duplicating
// the domain check in the middleware would give a rejected origin an opaque
// network error instead of the 403 the SDK maps to 「访问受限」.
//
// What makes reflection safe here is that credentials are never allowed: cored
// authenticates with a token in the request body, not with cookies, so a
// third-party page gains nothing it couldn't already do with a plain server-side
// POST. Do not add Access-Control-Allow-Credentials without revisiting this.
func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			// Same URL can legitimately answer differently per origin.
			h.Add("Vary", "Origin")
		}
		next(w, r)
	}
}

// preflight answers an OPTIONS request. Registered per path because the mux's
// method-prefixed patterns ("GET /channel/{slug}/config") don't match OPTIONS.
func preflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Add("Vary", "Origin")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type")
	h.Set("Access-Control-Max-Age", "600") // keep preflights off the hot path
	w.WriteHeader(http.StatusNoContent)
}
