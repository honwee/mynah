package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mynah/core"
)

// fakeRetriever returns canned chunks (or an error) for one query.
type fakeRetriever struct {
	chunks  []core.Chunk
	err     error
	gotQ    string
	gotTopK int
	delay   time.Duration
}

func (f *fakeRetriever) Retrieve(ctx context.Context, query string, topK int, _ float32) ([]core.Chunk, error) {
	f.gotQ, f.gotTopK = query, topK
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.chunks, f.err
}

func ragOpts() RAGOptions {
	return RAGOptions{Enabled: true, TopK: 2, Threshold: 0, Timeout: 2 * time.Second}
}

// The knowledge a qwen push-to-talk turn seeds is exactly what the local brain
// injects as a system message: same retriever, same rendering. This pins the
// contract seedQwenRAG relies on — msgs[0].Content carries the framed chunks.
func TestRenderRAGCarriesChunksForCloudSeeding(t *testing.T) {
	const fact = "退货政策为签收后 17 天内无理由退货"
	m := &Manager{}
	r := &fakeRetriever{chunks: []core.Chunk{
		{Content: fact, Score: 0.91},
		{Content: "生鲜类商品除外", Score: 0.72},
	}}

	chunks := m.retrieveForTurn(context.Background(), "test", "退货几天？", r, nil, ragOpts())
	if len(chunks) != 2 {
		t.Fatalf("retrieveForTurn returned %d chunks, want 2", len(chunks))
	}
	if r.gotQ != "退货几天？" {
		t.Errorf("retriever saw query %q, want the user's text", r.gotQ)
	}

	msgs := renderRAG(chunks)
	if len(msgs) != 1 {
		t.Fatalf("renderRAG returned %d messages, want exactly 1 (seedQwenRAG uses msgs[0])", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("role = %q, want system", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, fact) {
		t.Errorf("rendered content lost the retrieved fact:\n%s", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "生鲜类商品除外") {
		t.Errorf("rendered content dropped the second chunk:\n%s", msgs[0].Content)
	}
}

// RAG must never block or break a turn: a retriever failure, a timeout and an
// empty result all have to yield "no context" rather than an error path, since
// seedQwenRAG skips seeding on an empty render and proceeds with the turn.
func TestRAGDegradesToNoContext(t *testing.T) {
	m := &Manager{}
	cases := []struct {
		name string
		r    *fakeRetriever
		opts RAGOptions
	}{
		{"retriever error", &fakeRetriever{err: errors.New("embedder down")}, ragOpts()},
		{"no hits", &fakeRetriever{chunks: nil}, ragOpts()},
		{"timeout", &fakeRetriever{
			chunks: []core.Chunk{{Content: "too late"}},
			delay:  50 * time.Millisecond,
		}, RAGOptions{Enabled: true, TopK: 2, Timeout: 5 * time.Millisecond}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunks := m.retrieveForTurn(context.Background(), "test", "q", tc.r, nil, tc.opts)
			if len(chunks) != 0 {
				t.Fatalf("got %d chunks, want 0", len(chunks))
			}
			if msgs := renderRAG(chunks); len(msgs) != 0 {
				t.Fatalf("renderRAG(nil) returned %d messages, want 0 (seedQwenRAG would seed junk)", len(msgs))
			}
		})
	}
}

// A disabled knowledge base must not reach the cloud at all.
func TestRAGDisabledRetrievesNothing(t *testing.T) {
	m := &Manager{}
	r := &fakeRetriever{chunks: []core.Chunk{{Content: "should not be used"}}}
	opts := ragOpts()
	opts.Enabled = false
	if chunks := m.retrieveForTurn(context.Background(), "test", "q", r, nil, opts); len(chunks) != 0 {
		t.Fatalf("disabled RAG returned %d chunks", len(chunks))
	}
	if r.gotQ != "" {
		t.Errorf("disabled RAG still queried the retriever with %q", r.gotQ)
	}
}
