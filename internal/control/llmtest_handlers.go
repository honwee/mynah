package control

import (
	"context"
	"net/http"
	"strings"
	"time"

	"mynah/core"
	"mynah/internal/llm"
)

// handleLLMTest is the console's "测试连接" button: build a provider from the
// submitted (unsaved) values and run one tiny real turn against it. Errors
// come back as human-readable hints, not raw upstream dumps.
func (s *Server) handleLLMTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		BaseURL  string `json:"base_url"`
		Model    string `json:"model"`
		APIKey   string `json:"api_key"`
		BotID    string `json:"bot_id"`
		// Stream mirrors the form toggle so the test exercises the same mode
		// the operator is about to save; nil (older console) = streaming.
		Stream *bool `json:"stream"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, err := llm.FromConfig(req.Provider, req.BaseURL, req.Model, req.APIKey, "你是测试助手。", req.BotID,
		req.Stream == nil || *req.Stream)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	reply, err := p.StreamChat(ctx, "conn-test",
		[]core.ChatMessage{{Role: "user", Content: "请只回复：连接正常"}}, nil, func(string) {})
	if err != nil {
		fail(w, http.StatusBadGateway, testHint(err))
		return
	}
	ok(w, map[string]any{
		"reply":      strings.TrimSpace(reply),
		"latency_ms": time.Since(start).Milliseconds(),
	})
}

// testHint maps common connection-test failures to actionable text. The raw
// error rides along so an experienced operator still sees the details.
func testHint(err error) string {
	e := err.Error()
	switch {
	case strings.Contains(e, "401") || strings.Contains(e, "unauthorized") || strings.Contains(e, "invalid_api_key"):
		return "API Key 无效或已过期（401）。" + e
	case strings.Contains(e, "404"):
		return "地址通了但接口不存在（404）：检查 base_url 是否到服务根路径（不要带 /v1）、provider 是否选对。" + e
	case strings.Contains(e, "connection refused") || strings.Contains(e, "no such host") ||
		strings.Contains(e, "context deadline exceeded") || strings.Contains(e, "Client.Timeout"):
		return "服务地址不可达：检查 base_url、网络与防火墙。" + e
	}
	return e
}
