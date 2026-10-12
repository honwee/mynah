package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"mynah/core"
)

// handlePlayground runs one stateless debug chat turn (admin-console PRD §8
// "对话调试 playground"): live persona + RAG config, no TTS/avatar. The client
// keeps the history and resends it every turn — nothing is stored server-side
// and visitor sessions are untouched.
func (s *Server) handlePlayground(w http.ResponseWriter, r *http.Request) {
	var req struct {
		History []core.ChatMessage `json:"history"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.History) == 0 {
		fail(w, http.StatusBadRequest, "history is required")
		return
	}
	if last := req.History[len(req.History)-1]; last.Role != "user" || last.Content == "" {
		fail(w, http.StatusBadRequest, "history must end with a non-empty user message")
		return
	}
	debugger, _ := s.deps.Sessions.(core.ChatDebugger)
	if debugger == nil {
		fail(w, http.StatusServiceUnavailable, "playground not available in this deployment")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := debugger.DebugChat(ctx, req.History)
	if err != nil {
		fail(w, http.StatusBadGateway, "chat turn failed: "+err.Error())
		return
	}
	ok(w, res)
}

// handlePlaygroundStream is the SSE variant of handlePlayground: reply
// fragments stream as `event: delta` so the console renders typing, followed
// by one `event: result` carrying the full DebugChatResult (reply + RAG
// chunks + latency). Errors after the stream opens come as `event: error`
// with a human-readable message. The non-streaming endpoint stays for older
// consoles and scripted smoke tests.
func (s *Server) handlePlaygroundStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		History []core.ChatMessage `json:"history"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.History) == 0 {
		fail(w, http.StatusBadRequest, "history is required")
		return
	}
	if last := req.History[len(req.History)-1]; last.Role != "user" || last.Content == "" {
		fail(w, http.StatusBadRequest, "history must end with a non-empty user message")
		return
	}
	debugger, _ := s.deps.Sessions.(core.ChatStreamDebugger)
	if debugger == nil {
		fail(w, http.StatusServiceUnavailable, "playground not available in this deployment")
		return
	}
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		fail(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // keep reverse proxies from buffering
	w.WriteHeader(http.StatusOK)

	emit := func(event string, payload any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}
	res, err := debugger.DebugChatStream(ctx, req.History, func(delta string) {
		emit("delta", map[string]string{"content": delta})
	})
	if err != nil {
		emit("error", map[string]string{"message": "chat turn failed: " + err.Error()})
		return
	}
	emit("result", res)
}

// ---- avatar playground (admin-console drives a real avatar session) --------
//
// These wrap the visitor-plane session pipeline behind admin JWT auth so the
// console can test the avatar without exposing the visitor port. The session
// created here is a REAL session: it occupies the (single-session) worker and
// shows up in the sessions list like any visitor.

func (s *Server) avatarDebugger(w http.ResponseWriter) core.AvatarDebugger {
	dbg, _ := s.deps.Sessions.(core.AvatarDebugger)
	if dbg == nil {
		fail(w, http.StatusServiceUnavailable, "avatar playground not available in this deployment")
	}
	return dbg
}

func (s *Server) handleAvatarOffer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SDP string `json:"sdp"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.SDP == "" {
		fail(w, http.StatusBadRequest, "sdp is required")
		return
	}
	dbg := s.avatarDebugger(w)
	if dbg == nil {
		return
	}
	answer, sid, err := dbg.AvatarOffer(req.SDP)
	if err != nil {
		fail(w, http.StatusBadGateway, "create avatar session failed: "+err.Error())
		return
	}
	ok(w, map[string]string{"sdp": answer, "type": "answer", "sessionid": sid})
}

func (s *Server) handleAvatarHuman(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"sessionid"`
		Text      string `json:"text"`
		Type      string `json:"type"` // "chat" | "echo" (default echo)
		Interrupt bool   `json:"interrupt"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.SessionID == "" || req.Text == "" {
		fail(w, http.StatusBadRequest, "sessionid and text are required")
		return
	}
	dbg := s.avatarDebugger(w)
	if dbg == nil {
		return
	}
	if err := dbg.AvatarDrive(req.SessionID, req.Text, req.Type, req.Interrupt); err != nil {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	ok(w, nil)
}

func (s *Server) handleAvatarInterrupt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"sessionid"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.SessionID == "" {
		fail(w, http.StatusBadRequest, "sessionid is required")
		return
	}
	dbg := s.avatarDebugger(w)
	if dbg == nil {
		return
	}
	if err := dbg.AvatarInterrupt(req.SessionID); err != nil {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	ok(w, nil)
}

func (s *Server) handleAvatarActions(w http.ResponseWriter, r *http.Request) {
	dbg := s.avatarDebugger(w)
	if dbg == nil {
		return
	}
	ok(w, dbg.AvatarActions())
}

func (s *Server) handleAvatarAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"sessionid"`
		Action    string `json:"action"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.SessionID == "" || req.Action == "" {
		fail(w, http.StatusBadRequest, "sessionid and action are required")
		return
	}
	dbg := s.avatarDebugger(w)
	if dbg == nil {
		return
	}
	if err := dbg.AvatarAction(req.SessionID, req.Action); err != nil {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	ok(w, nil)
}
