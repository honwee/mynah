// Package llm holds cored's chat providers behind core.ChatProvider: the
// openai provider (this file) speaks /v1/chat/completions and covers ollama,
// vLLM, FastGPT, RAGFlow, OneAPI and the clouds; dify.go and coze.go translate
// to those platforms' native protocols. The session orchestrator consumes the
// token stream and slices it into punctuation-bounded sentences for TTS.
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

	"mynah/core"
)

// Message is one chat-history entry (alias so existing callers keep working).
type Message = core.ChatMessage

// DefaultSystemPrompt is the persona used when the operator leaves
// llm.system_prompt empty.
const DefaultSystemPrompt = "你是一个数字人助手，用简短、口语化的中文回答，不要用列表、markdown 或表情符号。"

// Client is the openai provider: POST {BaseURL}/v1/chat/completions with
// stream=true (or a single non-streaming request when Stream is off).
type Client struct {
	BaseURL string // e.g. http://127.0.0.1:11434 (no trailing /v1)
	Model   string
	APIKey  string // optional (bearer)
	System  string // system prompt
	// Streaming off = one blocking request; for OpenAI-compatible backends
	// that can't do SSE. onDelta then fires once with the whole reply.
	Streaming bool
	HTTP      *http.Client
}

func New(baseURL, model, apiKey, system string) *Client {
	if system == "" {
		system = DefaultSystemPrompt
	}
	return &Client{BaseURL: baseURL, Model: model, APIKey: apiKey, System: system,
		Streaming: true, HTTP: &http.Client{}}
}

type chatReq struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// chatResp is the non-streaming response shape (stream toggle off).
type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Stream sends the system prompt + extra per-turn messages (e.g. RAG context,
// injected after the system prompt, never stored in history) + history, and
// invokes onDelta with each content fragment as it arrives. Returns the full
// assistant reply.
func (c *Client) Stream(ctx context.Context, history []Message, extra []Message, onDelta func(string)) (string, error) {
	msgs := make([]Message, 0, len(history)+len(extra)+1)
	msgs = append(msgs, Message{Role: "system", Content: c.System})
	msgs = append(msgs, extra...)
	msgs = append(msgs, history...)
	body, _ := json.Marshal(chatReq{Model: c.Model, Messages: msgs, Stream: c.Streaming})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, string(b))
	}

	if !c.Streaming {
		var cr chatResp
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&cr); err != nil {
			return "", err
		}
		if len(cr.Choices) == 0 {
			return "", fmt.Errorf("llm response has no choices")
		}
		reply := cr.Choices[0].Message.Content
		if reply != "" {
			onDelta(reply)
		}
		return reply, nil
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
		if data == "" || data == "[DONE]" {
			continue
		}
		var ch chatChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			continue
		}
		for _, choice := range ch.Choices {
			if choice.Delta.Content != "" {
				full.WriteString(choice.Delta.Content)
				onDelta(choice.Delta.Content)
			}
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return full.String(), err
	}
	return full.String(), nil
}

// StreamChat implements core.ChatProvider. The openai protocol is stateless,
// so convKey is ignored and the full history is sent every turn.
func (c *Client) StreamChat(ctx context.Context, _ string, history, extra []core.ChatMessage, onDelta func(string)) (string, error) {
	return c.Stream(ctx, history, extra, onDelta)
}
