// Package rag is the shared retrieval layer: embedding client (ollama
// /api/embed), text chunker, pgvector storage, and the online Retriever
// used inside chat turns. Ingestion orchestration lives in
// internal/knowledge; this package stays pure query/storage.
package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Embedder calls an ollama-compatible /api/embed endpoint.
type Embedder struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
}

func NewEmbedder(baseURL, model string) *Embedder {
	return &Embedder{BaseURL: baseURL, Model: model, HTTP: &http.Client{}}
}

type embedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
	// KeepAlive pins the model in ollama's memory. Without it ollama unloads
	// after 5 idle minutes and the next turn pays a multi-second cold reload —
	// far past the per-turn RAG budget, so retrieval degrades every time.
	// -1 = resident forever. Non-ollama endpoints ignore the field.
	KeepAlive int `json:"keep_alive"`
}

type embedResp struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed returns one vector per input text.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(embedReq{Model: e.Model, Input: texts, KeepAlive: -1})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.BaseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("embed status %d: %s", resp.StatusCode, string(b))
	}
	var er embedResp
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	if len(er.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embed returned %d vectors for %d inputs", len(er.Embeddings), len(texts))
	}
	return er.Embeddings, nil
}
