// Package rerank is the EE second-stage retrieval client. It speaks the
// de-facto standard rerank HTTP API (POST {base}/v1/rerank with
// {model, query, documents[]} -> results[{index, relevance_score}]) shared by
// jina / cohere / siliconflow / TEI / vLLM and our self-hosted
// workers/rerank/server.py.
package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"mynah/core"
)

type Client struct {
	base   string
	model  string
	apiKey string
	http   *http.Client
}

// New builds a client; base like http://127.0.0.1:9403, apiKey may be empty
// for self-hosted services.
func New(base, model, apiKey string) *Client {
	return &Client{
		base:   strings.TrimRight(base, "/"),
		model:  model,
		apiKey: apiKey,
		http:   &http.Client{}, // per-call deadlines come from ctx (RAG budget)
	}
}

type request struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type response struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float32 `json:"relevance_score"`
	} `json:"results"`
}

// Rerank implements core.Reranker: score every chunk against the query with a
// cross-encoder and return them in descending relevance order. The caller
// truncates to its topK and owns the latency budget via ctx.
func (c *Client) Rerank(ctx context.Context, query string, chunks []core.Chunk) ([]core.Chunk, error) {
	if len(chunks) == 0 {
		return chunks, nil
	}
	docs := make([]string, len(chunks))
	for i, ch := range chunks {
		docs[i] = ch.Content
	}
	body, err := json.Marshal(request{Model: c.model, Query: query, Documents: docs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rerank: HTTP %d", resp.StatusCode)
	}
	var out response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Results) == 0 {
		return nil, fmt.Errorf("rerank: empty results")
	}
	ranked := make([]core.Chunk, 0, len(out.Results))
	for _, r := range out.Results {
		if r.Index < 0 || r.Index >= len(chunks) {
			return nil, fmt.Errorf("rerank: index %d out of range", r.Index)
		}
		ch := chunks[r.Index]
		ch.RerankScore = r.RelevanceScore
		ranked = append(ranked, ch)
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].RerankScore > ranked[j].RerankScore })
	return ranked, nil
}
