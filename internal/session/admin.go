package session

import (
	"context"
	"errors"
	"time"

	"mynah/core"
	"mynah/internal/llm"
)

// Manager implements core.SessionAdmin for the control plane.

func (m *Manager) ListSessions() []core.SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]core.SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.info())
	}
	return out
}

func (m *Manager) GetSession(id string) (*core.SessionInfo, bool) {
	s := m.Get(id)
	if s == nil {
		return nil, false
	}
	info := s.info()
	return &info, true
}

// KickSession force-closes a live session (admin "踢下线").
func (m *Manager) KickSession(id string) bool {
	s := m.Get(id)
	if s == nil {
		return false
	}
	m.remove(id)
	return true
}

// ListWorkers implements core.WorkerReporter: the avatar dispatch pool with
// each slot's engine banner and the live session holding it ("" = free).
func (m *Manager) ListWorkers() []core.WorkerInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]core.WorkerInfo, len(m.workers))
	for i, w := range m.workers {
		out[i] = core.WorkerInfo{Addr: w.Addr, Detail: w.Detail, Session: w.busy}
	}
	return out
}

func (s *Session) info() core.SessionInfo {
	return core.SessionInfo{
		ID:        s.ID,
		CreatedAt: s.createdAt,
		Turns:     int(s.turns.Load()),
		Speaking:  s.speaking.Load(),
		Voice:     s.asr != nil,
		State:     s.fsm.get().String(),
	}
}

// DebugChat implements core.ChatDebugger: one stateless turn through the same
// LLM + RAG pipeline a visitor turn uses, minus TTS/avatar. The console
// playground calls this to validate persona/RAG edits without opening the
// visitor page.
func (m *Manager) DebugChat(ctx context.Context, history []core.ChatMessage) (core.DebugChatResult, error) {
	return m.DebugChatStream(ctx, history, func(string) {})
}

// DebugChatStream implements core.ChatStreamDebugger: same turn, but reply
// fragments stream to onDelta so the console can render typing.
func (m *Manager) DebugChatStream(ctx context.Context, history []core.ChatMessage, onDelta func(string)) (core.DebugChatResult, error) {
	llmCli := m.LLM()
	if llmCli == nil {
		return core.DebugChatResult{}, errors.New("chat not configured (llm.base_url is empty)")
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		return core.DebugChatResult{}, errors.New("history must end with a user message")
	}
	hist := make([]llm.Message, len(history))
	for i, msg := range history {
		hist[i] = llm.Message{Role: msg.Role, Content: msg.Content}
	}
	start := time.Now()
	r, reranker, opts := m.retrieverConf()
	chunks := m.retrieveForTurn(ctx, "playground", history[len(history)-1].Content, r, reranker, opts)
	if chunks == nil {
		chunks = []core.Chunk{} // contract says empty array, not null
	}
	reply, err := llmCli.StreamChat(ctx, "playground", hist, renderRAG(chunks), onDelta)
	if err != nil {
		return core.DebugChatResult{}, err
	}
	return core.DebugChatResult{
		Reply:     reply,
		Chunks:    chunks,
		LatencyMS: time.Since(start).Milliseconds(),
	}, nil
}

// ---- core.AvatarDebugger (console playground drives a real avatar session) --

// AvatarOffer creates a real visitor-pipeline session from the console's
// browser offer. The worker serves one session at a time, so this competes
// with visitor sessions — the console warns before connecting.
func (m *Manager) AvatarOffer(offerSDP string) (answerSDP, sessionID string, err error) {
	return m.CreateFromOffer(offerSDP)
}

// AvatarDrive runs one text-driven turn. mode "chat" goes through the LLM;
// anything else is echo (speak verbatim — exercises TTS + lip sync only).
func (m *Manager) AvatarDrive(sessionID, text, mode string, interrupt bool) error {
	s := m.Get(sessionID)
	if s == nil {
		return errors.New("session not found")
	}
	if interrupt {
		s.Interrupt()
	}
	if mode == "chat" {
		s.Chat(text)
	} else {
		s.Speak(text)
	}
	return nil
}

// AvatarInterrupt cancels the in-flight turn (console barge-in button).
func (m *Manager) AvatarInterrupt(sessionID string) error {
	s := m.Get(sessionID)
	if s == nil {
		return errors.New("session not found")
	}
	s.Interrupt()
	return nil
}

// AvatarActions lists the one-shot action clips available for the console's
// currently selected avatar (display order) — actions are per-avatar now, and
// the playground runs on config.avatar.current.
func (m *Manager) AvatarActions() []string { return m.ActionIDs(m.currentAvatarID()) }

// AvatarAction queues a one-shot action clip on the session (console 动作 buttons).
func (m *Manager) AvatarAction(sessionID, action string) error {
	s := m.Get(sessionID)
	if s == nil {
		return errors.New("session not found")
	}
	return s.PlayAction(action)
}
