package session

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"mynah/core"
	"mynah/internal/llm"
)

// RAGOptions is the live retrieval config (hot-swappable with the retriever).
type RAGOptions struct {
	Enabled   bool
	TopK      int
	Threshold float32
	Timeout   time.Duration
	// Candidates is how many vector hits to over-fetch when a reranker picks
	// the final TopK from them. <=0 = auto: max(TopK*4, 12) capped at 20.
	Candidates int
}

// SetRetriever swaps the RAG query path (nil disables). Hot config apply.
func (m *Manager) SetRetriever(r core.Retriever, opts RAGOptions) {
	m.cliMu.Lock()
	m.retriever = r
	m.ragOpts = opts
	m.cliMu.Unlock()
}

// SetReranker plugs a second-stage reranker behind the retriever (EE feature;
// OSS never calls this). nil = vector order is final.
func (m *Manager) SetReranker(r core.Reranker) {
	m.cliMu.Lock()
	m.reranker = r
	m.cliMu.Unlock()
}

func (m *Manager) retrieverConf() (core.Retriever, core.Reranker, RAGOptions) {
	m.cliMu.RLock()
	defer m.cliMu.RUnlock()
	return m.retriever, m.reranker, m.ragOpts
}

// rerankCandidates is how many vector hits to over-fetch when a reranker will
// pick the final topK from them. explicit > 0 wins (floored at topK);
// otherwise auto max(topK*4, 12) capped at 20.
func rerankCandidates(topK, explicit int) int {
	if explicit > 0 {
		if explicit < topK {
			return topK
		}
		return explicit
	}
	n := topK * 4
	if n < 12 {
		n = 12
	}
	if n > 20 {
		n = 20
	}
	return n
}

// retrieveForTurn runs the retrieval(+rerank) pipeline for one query and
// returns the final topK chunks (nil on miss/degrade — RAG must never block
// a reply). tag labels log lines ("turn <id>" / "playground"). The retriever,
// reranker and options are passed in so a channel session can supply its frozen
// snapshot while the global/playground path supplies the live globals.
func (m *Manager) retrieveForTurn(ctx context.Context, tag, query string, r core.Retriever, reranker core.Reranker, opts RAGOptions) []core.Chunk {
	if r == nil || !opts.Enabled {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	start := time.Now()
	topK := opts.TopK
	if reranker != nil {
		topK = rerankCandidates(opts.TopK, opts.Candidates)
	}
	chunks, err := r.Retrieve(rctx, query, topK, opts.Threshold)
	if err != nil {
		log.Printf("[%s] rag degraded (%dms): %v", tag, time.Since(start).Milliseconds(), err)
		return nil
	}
	if len(chunks) == 0 {
		log.Printf("[%s] rag: no hits (%dms)", tag, time.Since(start).Milliseconds())
		return nil
	}
	if reranker != nil && len(chunks) > 1 {
		rstart := time.Now()
		reranked, err := reranker.Rerank(rctx, query, chunks)
		if err != nil {
			// Degrade to vector order; the candidates are already usable.
			log.Printf("[%s] rerank degraded (%dms): %v", tag, time.Since(rstart).Milliseconds(), err)
		} else {
			chunks = reranked
			log.Printf("[%s] rerank: %d candidates +%dms", tag, len(chunks), time.Since(rstart).Milliseconds())
		}
	}
	if len(chunks) > opts.TopK {
		chunks = chunks[:opts.TopK]
	}
	log.Printf("[%s] rag: %d chunks (top=%.3f) +%dms", tag, len(chunks), chunks[0].Score, time.Since(start).Milliseconds())
	return chunks
}

// renderRAG turns retrieved chunks into the per-turn system message injected
// after the persona prompt (never stored in history).
func renderRAG(chunks []core.Chunk) []llm.Message {
	if len(chunks) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("以下是与用户问题相关的知识库内容，回答时优先依据这些内容；与问题无关时忽略：\n")
	for i, c := range chunks {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(c.Content))
	}
	return []llm.Message{{Role: "system", Content: b.String()}}
}

// ragContext retrieves knowledge for this turn's user text and renders it as
// one extra system message. Hard latency budget: past the timeout the turn
// proceeds without context.
func (s *Session) ragContext(ctx context.Context, query string) []llm.Message {
	r, reranker, opts := s.retrieverConf()
	return renderRAG(s.mgr.retrieveForTurn(ctx, "turn "+s.ID, query, r, reranker, opts))
}
