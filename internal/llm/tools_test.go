package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mynah/core"
)

// sseServer replays canned SSE lines as a /v1/chat/completions stream and
// records the request body it was given.
func sseServer(t *testing.T, lines []string, gotBody *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotBody != nil {
			b := make([]byte, 8192)
			n, _ := r.Body.Read(b)
			*gotBody = string(b[:n])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprintf(w, "data: %s\n\n", l)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestStreamChatToolsParsesStreamedCalls(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		text  string
		calls []core.ToolCall
	}{{
		name: "arguments split across chunks",
		lines: []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"goto_page","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ge\":\"sales\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		},
		calls: []core.ToolCall{{ID: "c1", Name: "goto_page", Arguments: `{"page":"sales"}`}},
	}, {
		// Map iteration is random, so two parallel calls are the regression
		// guard for ordering.
		name: "two parallel calls keep model order",
		lines: []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"goto_page","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"filter","arguments":"{\"r\":1}"}}]}}]}`,
		},
		calls: []core.ToolCall{
			{ID: "a", Name: "goto_page", Arguments: "{}"},
			{ID: "b", Name: "filter", Arguments: `{"r":1}`},
		},
	}, {
		name: "no-argument tool defaults to empty object",
		lines: []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"refresh"}}]}}]}`,
		},
		calls: []core.ToolCall{{ID: "c", Name: "refresh", Arguments: "{}"}},
	}, {
		name: "plain content, no tools used",
		lines: []string{
			`{"choices":[{"delta":{"content":"你好"}}]}`,
			`{"choices":[{"delta":{"content":"，世界"}}]}`,
		},
		text: "你好，世界",
	}, {
		// Some backends narrate before calling; both must survive one round.
		name: "content and a call in the same round",
		lines: []string{
			`{"choices":[{"delta":{"content":"好的"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"goto_page","arguments":"{}"}}]}}]}`,
		},
		text:  "好的",
		calls: []core.ToolCall{{ID: "c1", Name: "goto_page", Arguments: "{}"}},
	}, {
		name: "fragment that never gets a name is dropped",
		lines: []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := sseServer(t, tc.lines, nil)
			defer srv.Close()
			c := New(srv.URL, "m", "", "sys")

			var got strings.Builder
			reply, calls, err := c.StreamChatTools(context.Background(), "k",
				[]Message{{Role: "user", Content: "hi"}}, nil, nil,
				func(d string) { got.WriteString(d) })
			if err != nil {
				t.Fatalf("StreamChatTools: %v", err)
			}
			if reply != tc.text || got.String() != tc.text {
				t.Errorf("text = %q (deltas %q), want %q", reply, got.String(), tc.text)
			}
			if len(calls) != len(tc.calls) {
				t.Fatalf("calls = %+v, want %+v", calls, tc.calls)
			}
			for i := range calls {
				if calls[i] != tc.calls[i] {
					t.Errorf("call %d = %+v, want %+v", i, calls[i], tc.calls[i])
				}
			}
		})
	}
}

// A tool round sends the assistant's tool_calls and each result back in the
// openai nesting ("function": {...}, arguments as a string) — not core's flat
// shape, which the model would reject.
func TestStreamChatToolsSendsToolHistoryInWireShape(t *testing.T) {
	var body string
	srv := sseServer(t, []string{`{"choices":[{"delta":{"content":"卖了三百万"}}]}`}, &body)
	defer srv.Close()
	c := New(srv.URL, "m", "", "sys")

	hist := []Message{
		{Role: "user", Content: "销售额"},
		{Role: "assistant", ToolCalls: []core.ToolCall{
			{ID: "c1", Name: "query_metric", Arguments: `{"m":"sales"}`}}},
		{Role: "tool", ToolCallID: "c1", Content: `{"value":3000000}`},
	}
	tools := []core.ToolSpec{{Name: "query_metric", Description: "查指标",
		Parameters: map[string]any{"type": "object"}}}

	if _, _, err := c.StreamChatTools(context.Background(), "k", hist, nil, tools, func(string) {}); err != nil {
		t.Fatalf("StreamChatTools: %v", err)
	}
	for _, want := range []string{
		`"tools":[{"type":"function","function":{"name":"query_metric"`,
		`"tool_calls":[{"id":"c1","type":"function","function":{"name":"query_metric","arguments":"{\"m\":\"sales\"}"}}]`,
		`"tool_call_id":"c1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %s\ngot: %s", want, body)
		}
	}
}

// With no abilities registered the request must look exactly like the plain
// chat path — no stray "tools" key, no tool fields on any message.
func TestStreamChatToolsWithoutToolsMatchesPlainRequest(t *testing.T) {
	var body string
	srv := sseServer(t, []string{`{"choices":[{"delta":{"content":"hi"}}]}`}, &body)
	defer srv.Close()
	c := New(srv.URL, "m", "", "sys")

	if _, _, err := c.StreamChatTools(context.Background(), "k",
		[]Message{{Role: "user", Content: "hi"}}, nil, nil, func(string) {}); err != nil {
		t.Fatalf("StreamChatTools: %v", err)
	}
	for _, bad := range []string{"tools", "tool_calls", "tool_call_id"} {
		if strings.Contains(body, bad) {
			t.Errorf("request body contains %q with no tools registered: %s", bad, body)
		}
	}
}

// The type assertion the session does must succeed for openai and fail for the
// providers that manage tools server-side.
func TestOpenAIImplementsToolChatProvider(t *testing.T) {
	var _ core.ToolChatProvider = (*Client)(nil)
	if _, ok := any(&Dify{}).(core.ToolChatProvider); ok {
		t.Error("dify must not implement ToolChatProvider")
	}
	if _, ok := any(&Coze{}).(core.ToolChatProvider); ok {
		t.Error("coze must not implement ToolChatProvider")
	}
}
