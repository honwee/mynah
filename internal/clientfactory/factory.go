// Package clientfactory builds the runtime client set (LLM chat provider, TTS
// client, RAG retriever + options, reranker) from a config.Config. Both cored
// startup (app.go, applied to the global session.Manager) and per-channel
// publish resolution (internal/channel) construct clients through here, so a
// published channel's frozen config snapshot behaves identically to the live
// global config — there is exactly one place that maps config -> clients.
package clientfactory

import (
	"context"
	"log"
	"time"

	"mynah/core"
	"mynah/internal/config"
	"mynah/internal/llm"
	"mynah/internal/rag"
	"mynah/internal/session"
	"mynah/internal/store"
	"mynah/internal/tts"
)

// Deps are the non-config inputs shared across builds.
type Deps struct {
	DB           *store.Store                   // nil = RAG unavailable
	MakeReranker func(config.RAG) core.Reranker // EE seam, already adapted by the caller; nil-safe
}

// BuildLLM constructs the chat provider. Returns (nil, nil) when chat is
// disabled — no base URL on a non-coze provider — matching cored's startup gate
// (app.go makeChat).
func BuildLLM(c config.LLM) (core.ChatProvider, error) {
	if c.BaseURL == "" && c.Provider != "coze" { // coze defaults its base URL
		return nil, nil
	}
	return llm.FromConfig(c.Provider, c.BaseURL, c.Model, c.APIKey, c.SystemPrompt, c.BotID, c.StreamOn())
}

// BuildTTS constructs the TTS client (always non-nil). Style instructions and
// speed come from the same config the console audition uses, so production
// speaks the way the audition sounded. Speed 0 (pre-field configs) means 1.0;
// out-of-range values are clamped like the audition endpoint clamps them.
func BuildTTS(c config.TTS) *tts.Client {
	cli := tts.New(c.BaseURL, c.Voice, c.Seed)
	cli.Instructions = c.Instructions
	if c.Speed != 0 {
		cli.Speed = c.Speed
		if cli.Speed < 0.5 {
			cli.Speed = 0.5
		} else if cli.Speed > 2.0 {
			cli.Speed = 2.0
		}
	}
	return cli
}

// BuildRAG constructs the retriever + live options. When RAG is enabled it pays
// ollama's embed cold-start now (async, 30s budget) so the first turn doesn't —
// same as cored startup (app.go applyRAG). Returns (nil, opts{Enabled:false})
// when RAG is off or there's no DB.
func BuildRAG(db *store.Store, c config.RAG) (core.Retriever, session.RAGOptions) {
	opts := session.RAGOptions{
		Enabled:    c.Enabled,
		TopK:       c.TopK,
		Threshold:  float32(c.Threshold),
		Timeout:    time.Duration(c.TimeoutMS) * time.Millisecond,
		Candidates: c.RerankCandidates,
	}
	if db == nil || !c.Enabled {
		opts.Enabled = false
		return nil, opts
	}
	r := rag.NewRetriever(db, rag.NewEmbedder(c.EmbedURL, c.EmbedModel))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := r.Warmup(ctx); err != nil {
			log.Printf("rag warmup: %v", err)
		}
	}()
	return r, opts
}

// Build composes a full client bundle from a (frozen) config snapshot. Used by
// the channel registry to materialize a published channel's clients. The
// returned bundle's Greeting is left empty for the caller to fill from the
// channel row.
func Build(cfg config.Config, deps Deps) (*session.ClientBundle, error) {
	llmCli, err := BuildLLM(cfg.LLM)
	if err != nil {
		return nil, err
	}
	retriever, ragOpts := BuildRAG(deps.DB, cfg.RAG)
	var reranker core.Reranker
	if deps.MakeReranker != nil {
		reranker = deps.MakeReranker(cfg.RAG)
	}
	return &session.ClientBundle{
		LLM:       llmCli,
		TTS:       BuildTTS(cfg.TTS),
		Retriever: retriever,
		Reranker:  reranker,
		RAGOpts:   ragOpts,
		// Frozen brain selection (zero Mode = snapshot predates the brain
		// group: leave nil so the session inherits the live selection). The
		// snapshot's persona prompt rides along — a channel's published
		// persona must not drift with later console edits.
		Brain: func() *session.BrainConfig {
			if cfg.Brain.Mode == "" {
				return nil
			}
			return &session.BrainConfig{
				Mode: cfg.Brain.Mode, Model: cfg.Brain.Model, Voice: cfg.Brain.Voice,
				Instructions: cfg.LLM.SystemPrompt,
			}
		}(),
	}, nil
}
