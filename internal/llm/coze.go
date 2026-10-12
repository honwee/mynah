// Coze (扣子) chat provider: POST {BaseURL}/v3/chat with streaming SSE.
// Coze bots hold the persona and knowledge on the platform side. The bot is
// addressed by bot_id (the one field the 傻瓜式 contract can't avoid — the
// Coze API key alone doesn't identify a bot). Coze v3 accepts the rolling
// history in additional_messages, so no server-side conversation mapping is
// needed.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Coze calls a Coze bot's v3 chat API.
type Coze struct {
	BaseURL string // https://api.coze.cn (China) or https://api.coze.com
	APIKey  string
	BotID   string
	HTTP    *http.Client
}

func NewCoze(baseURL, apiKey, botID string) *Coze {
	if baseURL == "" {
		baseURL = "https://api.coze.cn"
	}
	return &Coze{BaseURL: strings.TrimSuffix(baseURL, "/"), APIKey: apiKey, BotID: botID,
		HTTP: &http.Client{}}
}

type cozeMessage struct {
	Role        string `json:"role"`
	Content     string `json:"content"`
	ContentType string `json:"content_type"`
}

type cozeReq struct {
	BotID              string        `json:"bot_id"`
	UserID             string        `json:"user_id"`
	Stream             bool          `json:"stream"`
	AdditionalMessages []cozeMessage `json:"additional_messages"`
}

type cozeEvent struct {
	Role    string `json:"role"`
	Type    string `json:"type"`
	Content string `json:"content"`
	// error payload fields (event:error / conversation.chat.failed)
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	LastErr *struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	} `json:"last_error"`
}

// StreamChat implements core.ChatProvider. extra (RAG context) is folded in
// front of the final user message — same reasoning as the Dify adapter: the
// supported configuration keeps cored's RAG off when a platform owns the bot.
func (c *Coze) StreamChat(ctx context.Context, convKey string, history, extra []Message, onDelta func(string)) (string, error) {
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		return "", fmt.Errorf("coze: history must end with a user message")
	}
	msgs := make([]cozeMessage, 0, len(history))
	for i, m := range history {
		content := m.Content
		if i == len(history)-1 && len(extra) > 0 {
			var b strings.Builder
			for _, e := range extra {
				b.WriteString(e.Content)
				b.WriteString("\n")
			}
			b.WriteString(content)
			content = b.String()
		}
		msgs = append(msgs, cozeMessage{Role: m.Role, Content: content, ContentType: "text"})
	}
	body, _ := json.Marshal(cozeReq{
		BotID: c.BotID, UserID: "mynah-" + convKey, Stream: true,
		AdditionalMessages: msgs,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v3/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("coze status %d: %s", resp.StatusCode, string(b))
	}

	var full strings.Builder
	var event string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(line[5:])
			if data == "" || data == "[DONE]" || data == `"[DONE]"` {
				continue
			}
			var ev cozeEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			switch event {
			case "conversation.message.delta":
				// type=answer carries the reply; other types (follow_up,
				// function_call, verbose) are platform chrome we don't speak.
				if ev.Type == "answer" || ev.Type == "" {
					if ev.Content != "" {
						full.WriteString(ev.Content)
						onDelta(ev.Content)
					}
				}
			case "conversation.chat.failed":
				if ev.LastErr != nil {
					return full.String(), fmt.Errorf("coze chat failed: %d %s", ev.LastErr.Code, ev.LastErr.Msg)
				}
				return full.String(), fmt.Errorf("coze chat failed")
			case "error":
				return full.String(), fmt.Errorf("coze stream error: %d %s", ev.Code, ev.Msg)
			}
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return full.String(), err
	}
	return full.String(), nil
}
