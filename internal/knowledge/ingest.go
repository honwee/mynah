package knowledge

import (
	"context"
	"fmt"
	"log"
	"time"

	"mynah/internal/config"
	"mynah/internal/rag"
	"mynah/internal/store"
)

// Ingester is the async ingestion worker: documents enter as status=pending
// and are processed (chunk -> embed -> store) one at a time. On startup it
// re-enqueues anything left pending/processing by a previous run, so a
// restart mid-ingest loses nothing.
type Ingester struct {
	db   *store.Store
	cfg  func() config.RAG // live config snapshot (hot-swappable)
	wake chan struct{}
}

func NewIngester(db *store.Store, cfg func() config.RAG) *Ingester {
	return &Ingester{db: db, cfg: cfg, wake: make(chan struct{}, 1)}
}

// Notify nudges the worker loop; safe to call from any goroutine.
func (in *Ingester) Notify() {
	select {
	case in.wake <- struct{}{}:
	default:
	}
}

// Run processes the queue until ctx is canceled. Call in a goroutine.
func (in *Ingester) Run(ctx context.Context) {
	// Crash recovery: anything stuck in processing goes back to pending.
	if _, err := in.db.Pool.Exec(ctx,
		`UPDATE documents SET status = 'pending' WHERE status = 'processing'`); err != nil {
		log.Printf("knowledge: requeue stale: %v", err)
	}
	for {
		processed, err := in.processNext(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("knowledge: %v", err)
		}
		if processed {
			continue // drain the queue without waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-in.wake:
		case <-time.After(30 * time.Second): // safety-net poll
		}
	}
}

// processNext claims and processes one pending document. Returns whether a
// document was processed (claimed), regardless of success.
func (in *Ingester) processNext(ctx context.Context) (bool, error) {
	var (
		docID, kbID int64
		filename    string
		rawText     string
	)
	err := in.db.Pool.QueryRow(ctx, `
		UPDATE documents SET status = 'processing', updated_at = now()
		WHERE id = (
			SELECT id FROM documents WHERE status = 'pending'
			ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		RETURNING id, kb_id, filename, raw_text`).
		Scan(&docID, &kbID, &filename, &rawText)
	if err != nil {
		return false, nil // empty queue (pgx.ErrNoRows) or transient error: just wait
	}

	cfg := in.cfg()
	start := time.Now()
	ingestErr := in.ingest(ctx, docID, kbID, rawText, cfg)
	if ingestErr != nil {
		_, _ = in.db.Pool.Exec(ctx, `
			UPDATE documents SET status = 'failed', error = $2, updated_at = now()
			WHERE id = $1`, docID, ingestErr.Error())
		return true, fmt.Errorf("ingest doc %d (%s): %w", docID, filename, ingestErr)
	}
	log.Printf("knowledge: doc %d (%s) ready in %dms", docID, filename, time.Since(start).Milliseconds())
	return true, nil
}

func (in *Ingester) ingest(ctx context.Context, docID, kbID int64, rawText string, cfg config.RAG) error {
	chunks := rag.Chunk(rawText, cfg.ChunkSize, cfg.ChunkOverlap)
	if len(chunks) == 0 {
		return fmt.Errorf("document produced no chunks")
	}
	embedder := rag.NewEmbedder(cfg.EmbedURL, cfg.EmbedModel)

	// Embed in batches to bound request size.
	const batch = 16
	vectors := make([][]float32, 0, len(chunks))
	for i := 0; i < len(chunks); i += batch {
		end := min(i+batch, len(chunks))
		vecs, err := embedder.Embed(ctx, chunks[i:end])
		if err != nil {
			return fmt.Errorf("embed batch %d: %w", i/batch, err)
		}
		vectors = append(vectors, vecs...)
	}
	for i, v := range vectors {
		if len(v) != cfg.EmbedDim {
			return fmt.Errorf("chunk %d: embed dim %d != configured %d", i, len(v), cfg.EmbedDim)
		}
	}

	if err := rag.ReplaceChunks(ctx, in.db, docID, kbID, chunks, vectors); err != nil {
		return err
	}
	_, err := in.db.Pool.Exec(ctx, `
		UPDATE documents SET status = 'ready', error = '', chunk_count = $2, updated_at = now()
		WHERE id = $1`, docID, len(chunks))
	return err
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
