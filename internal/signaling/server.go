// Package signaling serves the HTTP control plane that the browser widget
// already speaks (drop-in for LiveTalking's /offer + /human + /interrupt_talk
// routes) and statically hosts the existing web/ assets.
package signaling

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"mynah/internal/channel"
	"mynah/internal/config"
	"mynah/internal/session"
)

type Server struct {
	mgr      *session.Manager
	webDir   string
	channels *channel.Registry // nil = channel publish disabled (no --db)
	rl       *ratelimit        // per-IP guard on visitor endpoints; nil = off
	chroma   func() config.Chroma // live keyer knobs for the visitor page; nil = defaults
}

func New(mgr *session.Manager, webDir string, channels *channel.Registry) *Server {
	return &Server{mgr: mgr, webDir: webDir, channels: channels}
}

// SetRateLimit enables the per-IP token bucket on the visitor endpoints
// (requests/min sustained, equal burst). 0 disables it.
func (s *Server) SetRateLimit(perMin int) { s.rl = newRatelimit(perMin) }

// SetChromaSource makes the visitor page's keyer knobs follow the live
// config (avatar.chroma) instead of the built-in defaults; the playground's
// 绿幕调试 card saves there, and channel pages pick it up on next load.
func (s *Server) SetChromaSource(fn func() config.Chroma) { s.chroma = fn }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/offer", s.rl.guard(s.handleOffer))
	mux.HandleFunc("/human", s.rl.guard(s.handleHuman))
	mux.HandleFunc("/interrupt_talk", s.rl.guard(s.handleInterrupt))
	mux.HandleFunc("/is_speaking", s.handleIsSpeaking)
	mux.HandleFunc("/set_audiotype", s.rl.guard(s.handleSetAudiotype))
	// Action choreography (动作编排): list the baked one-shot clips and trigger
	// one on a live session (channel visitor page demo buttons use these).
	mux.HandleFunc("GET /actions", s.handleActions)
	mux.HandleFunc("POST /action", s.rl.guard(s.handleAction))
	if s.channels != nil {
		// Published channels: /channel/<slug> serves the visitor page
		// (web/user/channel.html) and the page posts its WebRTC offer to
		// /channel/offer with the slug + optional token. /channel/<slug>/config
		// is the public display-config feed the page fetches on load. Without
		// --db these routes are absent and /channel/* falls through to a 404.
		mux.HandleFunc("GET /channel/{slug}", s.handleChannelPage)
		mux.HandleFunc("GET /channel/{slug}/config", s.handleChannelConfig)
		mux.HandleFunc("POST /channel/offer", s.rl.guard(s.handleChannelOffer))
	}
	if s.webDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(s.webDir)))
	}
	return mux
}

// ---- /offer (raw, unwrapped — matches aiortc handler) ----------------------

type offerReq struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"`
}
type offerResp struct {
	SDP       string `json:"sdp"`
	Type      string `json:"type"`
	SessionID string `json:"sessionid"`
}

func (s *Server) handleOffer(w http.ResponseWriter, r *http.Request) {
	var req offerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	answer, sid, err := s.mgr.CreateFromOffer(req.SDP)
	if err != nil {
		log.Printf("offer failed: %v", err)
		if errors.Is(err, session.ErrNoFreeWorker) {
			http.Error(w, "all avatars busy", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, offerResp{SDP: answer, Type: "answer", SessionID: sid})
}

// ---- control routes (wrapped {code,data}/{code,msg}) -----------------------

type humanReq struct {
	Text      string `json:"text"`
	Type      string `json:"type"`
	Interrupt bool   `json:"interrupt"`
	SessionID string `json:"sessionid"`
}

func (s *Server) handleHuman(w http.ResponseWriter, r *http.Request) {
	var req humanReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error())
		return
	}
	sess := s.mgr.Get(req.SessionID)
	if sess == nil {
		jsonError(w, "session not found")
		return
	}
	if req.Interrupt {
		sess.Interrupt()
	}
	// echo = speak the text verbatim; chat = run it through the LLM (P3).
	if req.Type == "chat" {
		sess.Chat(req.Text)
	} else {
		sess.Speak(req.Text)
	}
	jsonOK(w, nil)
}

type sidReq struct {
	SessionID string `json:"sessionid"`
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	var req sidReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error())
		return
	}
	sess := s.mgr.Get(req.SessionID)
	if sess == nil {
		jsonError(w, "session not found")
		return
	}
	sess.Interrupt()
	jsonOK(w, nil)
}

func (s *Server) handleIsSpeaking(w http.ResponseWriter, r *http.Request) {
	var req sidReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error())
		return
	}
	sess := s.mgr.Get(req.SessionID)
	if sess == nil {
		jsonError(w, "session not found")
		return
	}
	jsonOK(w, sess.IsSpeaking())
}

func (s *Server) handleSetAudiotype(w http.ResponseWriter, r *http.Request) {
	// P2 placeholder (action orchestration is not wired through the engine yet).
	jsonOK(w, nil)
}

// ---- action choreography (one-shot baked clips) -----------------------------

// handleActions lists the action clips for the DEFAULT visitor page, which runs
// on the console's current avatar. The per-channel page uses the channel's own
// avatar (see the meta handler) — actions are per-avatar, so an avatar with no
// baked clips renders an empty action bar rather than someone else's gestures.
func (s *Server) handleActions(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.mgr.ActionIDs(s.mgr.EffectiveAvatarID("")))
}

type actionReq struct {
	SessionID string `json:"sessionid"`
	Action    string `json:"action"`
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	var req actionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error())
		return
	}
	sess := s.mgr.Get(req.SessionID)
	if sess == nil {
		jsonError(w, "session not found")
		return
	}
	if err := sess.PlayAction(req.Action); err != nil {
		jsonError(w, err.Error())
		return
	}
	jsonOK(w, nil)
}

// ---- json helpers ----------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonOK(w http.ResponseWriter, data any) {
	writeJSON(w, map[string]any{"code": 0, "data": data})
}

func jsonError(w http.ResponseWriter, msg string) {
	writeJSON(w, map[string]any{"code": -1, "msg": msg})
}

// ---- published channels ----------------------------------------------------

func (s *Server) handleChannelPage(w http.ResponseWriter, r *http.Request) {
	if s.webDir == "" {
		http.NotFound(w, r)
		return
	}
	// The visitor page is self-contained (no relative asset refs), so serving
	// it under /channel/<slug> is safe; the page reads the slug from its own URL
	// and fetches its display config from /channel/<slug>/config.
	http.ServeFile(w, r, filepath.Join(s.webDir, "channel.html"))
}

// handleChannelConfig is the public (no-auth) display-config feed the visitor
// page fetches on load. It returns ONLY presentation fields — never the frozen
// persona snapshot (llm/tts/rag), api keys, or the access token. A
// missing/disabled channel is a 404.
func (s *Server) handleChannelConfig(w http.ResponseWriter, r *http.Request) {
	ch, err := s.channels.Resolve(r.Context(), r.PathValue("slug"))
	if errors.Is(err, channel.ErrNotFound) {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("channel config %q: %v", r.PathValue("slug"), err)
		http.Error(w, "channel error", http.StatusInternalServerError)
		return
	}
	suggestions := ch.SuggestedQuestions
	if suggestions == nil {
		suggestions = []string{}
	}
	chroma := config.Defaults().Avatar.Chroma
	if s.chroma != nil {
		chroma = s.chroma()
	}
	writeJSON(w, map[string]any{
		"name":                ch.Name,
		"description":         ch.Description,
		"suggested_questions": suggestions,
		"greeting":            ch.Snapshot.Greeting,
		"brand_name":          ch.BrandName,
		"brand_logo":          ch.BrandLogo,
		"bg_image":            ch.BgImage,
		"theme_color":         ch.ThemeColor,
		"avatar_preview":      ch.AvatarPreview,
		"access_mode":         ch.AccessMode,
		"actions":             s.mgr.ActionIDs(s.mgr.EffectiveAvatarID(ch.Snapshot.Avatar)),
		"chroma":              chroma,
	})
}

type channelOfferReq struct {
	SDP   string `json:"sdp"`
	Type  string `json:"type"`
	Slug  string `json:"slug"`
	Token string `json:"token"`
}

// handleChannelOffer resolves the channel, enforces access control, reserves a
// concurrency slot, then creates a session on the channel's frozen client
// bundle. Enforcement order is cheapest-strongest-first; the slot is reserved
// last so a rejected request never holds capacity, and the bundle is built only
// after the slot is secured.
func (s *Server) handleChannelOffer(w http.ResponseWriter, r *http.Request) {
	var req channelOfferReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SDP == "" {
		http.Error(w, "bad offer", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	ch, err := s.channels.Resolve(ctx, req.Slug)
	if errors.Is(err, channel.ErrNotFound) {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("channel resolve %q: %v", req.Slug, err)
		http.Error(w, "channel error", http.StatusInternalServerError)
		return
	}

	// token (strongest, constant-time) -> domain -> IP (defense in depth)
	if ch.AccessMode == "token" {
		if req.Token == "" || subtle.ConstantTimeCompare([]byte(req.Token), []byte(ch.AccessToken)) != 1 {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
	}
	if len(ch.Domains) > 0 && !originAllowed(r, ch.Domains) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if len(ch.CIDRs) > 0 && !ipAllowed(r, ch.CIDRs) {
		http.Error(w, "address not allowed", http.StatusForbidden)
		return
	}

	// Reserve under the exact id the session will use, so the ledger row maps
	// to a real session.
	sid := s.mgr.NextSessionID()
	if err := s.channels.Reserve(ctx, ch.ID, sid); err != nil {
		switch {
		case errors.Is(err, channel.ErrChannelFull):
			http.Error(w, "channel at capacity", http.StatusTooManyRequests)
		case errors.Is(err, channel.ErrChannelUnavailable):
			http.Error(w, "channel not found", http.StatusNotFound)
		default:
			log.Printf("channel reserve %q: %v", req.Slug, err)
			http.Error(w, "channel error", http.StatusInternalServerError)
		}
		return
	}

	bundle, err := s.channels.Bundle(ctx, ch)
	if err != nil {
		s.channels.Release(ch.ID, sid)
		log.Printf("channel bundle %q: %v", req.Slug, err)
		http.Error(w, "channel error", http.StatusInternalServerError)
		return
	}

	release := func() { s.channels.Release(ch.ID, sid) }
	answer, gotID, err := s.mgr.CreateFromOfferChannel(req.SDP, sid, bundle, release)
	if err != nil {
		s.channels.Release(ch.ID, sid) // session never created -> hook won't fire
		if errors.Is(err, session.ErrNoFreeWorker) {
			http.Error(w, "all avatars busy", http.StatusServiceUnavailable)
			return
		}
		log.Printf("channel offer create %q: %v", req.Slug, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[channel %s] session %s created", ch.Slug, gotID)
	writeJSON(w, offerResp{SDP: answer, Type: "answer", SessionID: gotID})
}

// originAllowed checks the request's Origin (falling back to Referer) host
// against the channel's domain whitelist. A restricted channel with no
// Origin/Referer header is rejected.
func originAllowed(r *http.Request, allowed []string) bool {
	host := headerHost(r.Header.Get("Origin"))
	if host == "" {
		host = headerHost(r.Header.Get("Referer"))
	}
	if host == "" {
		return false
	}
	for _, d := range allowed {
		if strings.EqualFold(host, strings.TrimSpace(d)) {
			return true
		}
	}
	return false
}

func headerHost(v string) string {
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// ipAllowed checks the client address against the channel's CIDR whitelist.
// Uses RemoteAddr, which is the real client only when cored is reached
// directly; behind a reverse proxy this is the proxy and an X-Forwarded-For
// parse (trusting the proxy) would be needed instead.
func ipAllowed(r *http.Request, cidrs []string) bool {
	ip := clientIP(r)
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		_, netw, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			continue
		}
		if netw.Contains(ip) {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}
