// Dify chat provider: POST {BaseURL}/v1/chat-messages with streaming SSE.
// Dify apps hold the persona and knowledge base on the platform side, so this
// adapter sends only the latest user query; multi-turn context lives in
// Dify's conversation, keyed by convKey (cored session id -> conversation_id).
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
	"sync"
)

// Dify calls a Dify application's chat API. The API key is bound to one Dify
// app, so base_url + api_key fully identify the target ("傻瓜式" contract).
type Dify struct {
	BaseURL string // e.g. https://api.dify.ai or a self-hosted https://dify.example.com
	APIKey  string
	// Streaming off = response_mode "blocking": one JSON reply, onDelta fires
	// once with the whole answer.
	Streaming bool

	mu   sync.Mutex
	conv map[string]string // convKey -> dify conversation_id
	HTTP *http.Client
}

func NewDify(baseURL, apiKey string) *Dify {
	return &Dify{
		BaseURL:   strings.TrimSuffix(baseURL, "/"),
		APIKey:    apiKey,
		Streaming: true,
		conv:      make(map[string]string),
		HTTP:      &http.Client{},
	}
}

type difyReq struct {
	Inputs         map[string]any `json:"inputs"`
	Query          string         `json:"query"`
	ResponseMode   string         `json:"response_mode"`
	ConversationID string         `json:"conversation_id,omitempty"`
	User           string         `json:"user"`
}

type difyEvent struct {
	Event          string `json:"event"`
	Answer         string `json:"answer"`
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"` // error event detail
}

// StreamChat implements core.ChatProvider. extra (RAG context) is folded into
// the query because Dify's chat API takes no system messages — but operators
// pointing at Dify normally keep cored's own RAG off (the platform owns the
// knowledge base), so extra is empty in the supported configuration.
func (d *Dify) StreamChat(ctx context.Context, convKey string, history, extra []Message, onDelta func(string)) (string, error) {
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		return "", fmt.Errorf("dify: history must end with a user message")
	}
	query := history[len(history)-1].Content
	if len(extra) > 0 {
		var b strings.Builder
		for _, m := range extra {
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
		b.WriteString(query)
		query = b.String()
	}

	d.mu.Lock()
	convID := d.conv[convKey]
	d.mu.Unlock()

	mode := "streaming"
	if !d.Streaming {
		mode = "blocking"
	}
	body, _ := json.Marshal(difyReq{
		Inputs: map[string]any{}, Query: query, ResponseMode: mode,
		ConversationID: convID, User: "mynah-" + convKey,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.BaseURL+"/v1/chat-messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.APIKey)
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		// A 404 "Conversation Not Found" means our cached id expired on the
		// platform; drop it so the next turn starts a fresh conversation.
		if resp.StatusCode == http.StatusNotFound && convID != "" {
			d.mu.Lock()
			delete(d.conv, convKey)
			d.mu.Unlock()
		}
		return "", fmt.Errorf("dify status %d: %s", resp.StatusCode, string(b))
	}

	if !d.Streaming {
		// blocking mode: one JSON object with the complete answer.
		var ev difyEvent
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&ev); err != nil {
			return "", err
		}
		if ev.ConversationID != "" && convID == "" {
			d.mu.Lock()
			d.conv[convKey] = ev.ConversationID
			d.mu.Unlock()
		}
		if ev.Answer != "" {
			onDelta(ev.Answer)
		}
		return ev.Answer, nil
	}

	var full strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" {
			continue
		}
		var ev difyEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		switch ev.Event {
		case "message", "agent_message":
			if ev.Answer != "" {
				full.WriteString(ev.Answer)
				onDelta(ev.Answer)
			}
			if ev.ConversationID != "" && convID == "" {
				convID = ev.ConversationID
				d.mu.Lock()
				d.conv[convKey] = convID
				d.mu.Unlock()
			}
		case "error":
			return full.String(), fmt.Errorf("dify stream error: %s", ev.Message)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return full.String(), err
	}
	return full.String(), nil
}
