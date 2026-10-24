// Tool calling for the openai provider (core.ToolChatProvider). Kept in its
// own file because it is optional: nothing in the base chat path changes, and
// a build that never registers abilities never executes a line of this.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"mynah/core"
)

// Wire shapes. The openai protocol nests a tool call under "function" and
// carries its arguments as a JSON *string*, which is not how core models it —
// hence the translation rather than marshalling core types directly.
type wireToolFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireToolFunc `json:"function"`
}

type wireMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Omitted unless this is a tool round, so an ordinary message serializes
	// byte-identically to the plain chatReq path.
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireTool struct {
	Type     string        `json:"type"`
	Function core.ToolSpec `json:"function"`
}

type chatToolsReq struct {
	Model    string     `json:"model"`
	Messages []wireMsg  `json:"messages"`
	Stream   bool       `json:"stream"`
	Tools    []wireTool `json:"tools,omitempty"`
}

func toWire(msgs []Message) []wireMsg {
	out := make([]wireMsg, 0, len(msgs))
	for _, m := range msgs {
		w := wireMsg{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			w.ToolCalls = append(w.ToolCalls, wireToolCall{
				ID: tc.ID, Type: "function",
				Function: wireToolFunc{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		out = append(out, w)
	}
	return out
}

// toolCallDelta is one streamed tool-call fragment. The model sends a call in
// pieces keyed by index: id and name usually arrive first, then "arguments"
// accumulates across many chunks (often a few characters each).
type toolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolChunk struct {
	Choices []struct {
		Delta struct {
			Content   string          `json:"content"`
			ToolCalls []toolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		Message struct { // non-streaming shape
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// StreamChatTools implements core.ToolChatProvider: one round of the chat with
// `tools` advertised. Content deltas stream through onDelta exactly as in
// Stream; any tool calls the model asks for are returned assembled. With no
// tools, or a model that ignores them, this behaves like Stream.
func (c *Client) StreamChatTools(ctx context.Context, _ string, history, extra []core.ChatMessage,
	tools []core.ToolSpec, onDelta func(string)) (string, []core.ToolCall, error) {

	msgs := make([]Message, 0, len(history)+len(extra)+1)
	msgs = append(msgs, Message{Role: "system", Content: c.System})
	msgs = append(msgs, extra...)
	msgs = append(msgs, history...)

	req := chatToolsReq{Model: c.Model, Messages: toWire(msgs), Stream: c.Streaming}
	for _, t := range tools {
		req.Tools = append(req.Tools, wireTool{Type: "function", Function: t})
	}
	body, _ := json.Marshal(req)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", nil, fmt.Errorf("llm status %d: %s", resp.StatusCode, string(b))
	}

	if !c.Streaming {
		var tc toolChunk
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&tc); err != nil {
			return "", nil, err
		}
		if len(tc.Choices) == 0 {
			return "", nil, fmt.Errorf("llm response has no choices")
		}
		reply := tc.Choices[0].Message.Content
		if reply != "" {
			onDelta(reply)
		}
		var calls []core.ToolCall
		for _, w := range tc.Choices[0].Message.ToolCalls {
			calls = append(calls, core.ToolCall{ID: w.ID, Name: w.Function.Name,
				Arguments: w.Function.Arguments})
		}
		return reply, calls, nil
	}

	var full strings.Builder
	acc := map[int]*core.ToolCall{}
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
		var ch toolChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			continue
		}
		for _, choice := range ch.Choices {
			if choice.Delta.Content != "" {
				full.WriteString(choice.Delta.Content)
				onDelta(choice.Delta.Content)
			}
			for _, d := range choice.Delta.ToolCalls {
				cur, ok := acc[d.Index]
				if !ok {
					cur = &core.ToolCall{}
					acc[d.Index] = cur
				}
				// Only the first fragment of a call carries id/name; later
				// ones repeat neither, so never overwrite with empty.
				if d.ID != "" {
					cur.ID = d.ID
				}
				if d.Function.Name != "" {
					cur.Name = d.Function.Name
				}
				cur.Arguments += d.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return full.String(), nil, err
	}

	idx := make([]int, 0, len(acc))
	for i := range acc {
		idx = append(idx, i)
	}
	sort.Ints(idx) // map iteration is random; calls must keep model order
	var calls []core.ToolCall
	for _, i := range idx {
		if acc[i].Name == "" {
			continue // fragment with no name ever assembled: unusable
		}
		if acc[i].Arguments == "" {
			acc[i].Arguments = "{}" // no-parameter tools may send nothing
		}
		calls = append(calls, *acc[i])
	}
	return full.String(), calls, nil
}
