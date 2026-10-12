package rag

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"mynah/core"
	"mynah/internal/store"
)

// vecLiteral renders a float32 slice as a pgvector text literal: [0.1,0.2,...]
func vecLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// ReplaceChunks deletes a document's old chunks and inserts the new set in
// one transaction — reindexing stays idempotent.
func ReplaceChunks(ctx context.Context, db *store.Store, docID, kbID int64, contents []string, vectors [][]float32) error {
	if len(contents) != len(vectors) {
		return fmt.Errorf("chunks/vectors length mismatch: %d != %d", len(contents), len(vectors))
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE doc_id = $1`, docID); err != nil {
		return err
	}
	for i := range contents {
		if _, err := tx.Exec(ctx, `
			INSERT INTO chunks (doc_id, kb_id, seq, content, embedding)
			VALUES ($1, $2, $3, $4, $5::vector)`,
			docID, kbID, i, contents[i], vecLiteral(vectors[i])); err != nil {
			return fmt.Errorf("insert chunk %d: %w", i, err)
		}
	}
	return tx.Commit(ctx)
}

// Retriever is the online query path (implements core.Retriever): embed the
// query, then cosine-search ready documents in enabled knowledge bases.
type Retriever struct {
	DB    *store.Store
	Embed *Embedder
}

func NewRetriever(db *store.Store, embed *Embedder) *Retriever {
	return &Retriever{DB: db, Embed: embed}
}

func (r *Retriever) Retrieve(ctx context.Context, query string, topK int, threshold float32) ([]core.Chunk, error) {
	if topK <= 0 {
		topK = 5
	}
	vecs, err := r.Embed.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	// pgvector <=> is cosine *distance*; similarity = 1 - distance.
	rows, err := r.DB.Pool.Query(ctx, `
		SELECT c.doc_id, d.filename, c.seq, c.content,
		       1 - (c.embedding <=> $1::vector) AS score
		FROM chunks c
		JOIN documents d ON d.id = c.doc_id AND d.status = 'ready'
		JOIN knowledge_bases kb ON kb.id = c.kb_id AND kb.enabled
		WHERE 1 - (c.embedding <=> $1::vector) >= $2
		ORDER BY c.embedding <=> $1::vector
		LIMIT $3`,
		vecLiteral(vecs[0]), threshold, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Chunk{}
	for rows.Next() {
		var c core.Chunk
		if err := rows.Scan(&c.DocID, &c.Filename, &c.Seq, &c.Content, &c.Score); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Warmup embeds a throwaway string so the first real turn doesn't pay
// ollama's cold-start cost.
func (r *Retriever) Warmup(ctx context.Context) error {
	_, err := r.Embed.Embed(ctx, []string{"warmup"})
	return err
}
