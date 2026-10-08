// Package channel implements published channels (控制台二期发布管理): a channel is
// a frozen config snapshot served to visitors at /channel/<slug> with access
// control and a transactional concurrency cap. The Registry is the single owner
// of channel persistence, the per-channel client-bundle cache, and the
// anti-oversell concurrency ledger.
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"mynah/internal/clientfactory"
	"mynah/internal/avatarcatalog"
	"mynah/internal/config"
	"mynah/internal/session"
	"mynah/internal/store"
)

// Sentinel errors the signaling layer maps to HTTP status codes.
var (
	ErrNotFound           = errors.New("channel not found")   // 404: missing/disabled
	ErrChannelFull        = errors.New("channel at capacity") // 429: max_concurrent reached
	ErrChannelUnavailable = errors.New("channel unavailable") // 404: disabled mid-reserve
)

// Snapshot is the frozen config captured at publish time. It is stored as the
// channels.config JSONB and rebuilt into clients on first visit.
type Snapshot struct {
	LLM      config.LLM `json:"llm"`
	TTS      config.TTS `json:"tts"`
	RAG      config.RAG `json:"rag"`
	Greeting string     `json:"greeting"`
	// Brain freezes the local/qwen selection (and the qwen model/voice) at
	// publish time. Zero Mode = snapshot predates the brain group: sessions
	// inherit the live selection instead.
	Brain config.Brain `json:"brain"`
	// Avatar freezes which digital-human appearance this channel speaks as.
	// Empty = whatever the worker currently has loaded (pre-binding behaviour,
	// so existing published channels are unaffected).
	//
	// Frozen at publish like everything else here: an operator switching the
	// global avatar must not silently change the face of an already-published
	// channel.
	Avatar string `json:"avatar"`
}

// ToConfig adapts the snapshot to a config.Config for clientfactory.Build.
// System is left zero — clients don't read it.
func (s Snapshot) ToConfig() config.Config {
	return config.Config{LLM: s.LLM, TTS: s.TTS, RAG: s.RAG, Brain: s.Brain}
}

// Channel is one published channel row.
type Channel struct {
	ID            int64
	Slug          string
	Name          string
	Snapshot      Snapshot
	Version       int
	Enabled       bool
	AccessMode    string // "public" | "token"
	AccessToken   string
	Domains       []string // allowed Origin/Referer hosts; nil/empty = any
	CIDRs         []string // allowed client CIDRs; nil/empty = any
	MaxConcurrent int

	// Display/branding config (channel metadata, not part of the frozen
	// Snapshot). Served by the public /channel/<slug>/config; PATCH-editable.
	Description        string
	SuggestedQuestions []string
	BrandName          string
	BrandLogo          string // top-bar logo image URL; '' = built-in mark
	BgImage            string // stage background; visitor page chroma-keys the green screen over it. '' = no keying
	ThemeColor         string
	AvatarPreview      string
}

// Registry owns channel resolution, the bundle cache, and the concurrency
// ledger. It is created only when cored runs with --db.
type Registry struct {
	db   *store.Store
	deps clientfactory.Deps

	mu    sync.Mutex
	cache map[int64]cachedBundle // keyed by channel id; version-checked
}

type cachedBundle struct {
	version int
	bundle  *session.ClientBundle
}

// NewRegistry truncates the live ledger (any rows are from a dead process —
// single-instance deployment) and returns a ready registry.
func NewRegistry(ctx context.Context, db *store.Store, deps clientfactory.Deps) (*Registry, error) {
	if _, err := db.Pool.Exec(ctx, `DELETE FROM channel_sessions`); err != nil {
		return nil, fmt.Errorf("clear channel ledger: %w", err)
	}
	return &Registry{db: db, deps: deps, cache: map[int64]cachedBundle{}}, nil
}

const channelCols = `id, slug, name, config, version, enabled, access_mode, access_token, domains, cidrs, max_concurrent, description, suggested_questions, brand_name, brand_logo, bg_image, theme_color, avatar_preview`

func scanChannel(row pgx.Row) (*Channel, error) {
	var c Channel
	var cfgRaw, domainsRaw, cidrsRaw, suggestionsRaw []byte
	if err := row.Scan(&c.ID, &c.Slug, &c.Name, &cfgRaw, &c.Version, &c.Enabled,
		&c.AccessMode, &c.AccessToken, &domainsRaw, &cidrsRaw, &c.MaxConcurrent,
		&c.Description, &suggestionsRaw, &c.BrandName, &c.BrandLogo, &c.BgImage, &c.ThemeColor, &c.AvatarPreview); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(cfgRaw, &c.Snapshot); err != nil {
		return nil, fmt.Errorf("parse channel %d config: %w", c.ID, err)
	}
	_ = json.Unmarshal(domainsRaw, &c.Domains)
	_ = json.Unmarshal(cidrsRaw, &c.CIDRs)
	_ = json.Unmarshal(suggestionsRaw, &c.SuggestedQuestions)
	return &c, nil
}

// Resolve loads an enabled channel by slug. Disabled/missing → ErrNotFound.
func (r *Registry) Resolve(ctx context.Context, slug string) (*Channel, error) {
	row := r.db.Pool.QueryRow(ctx,
		`SELECT `+channelCols+` FROM channels WHERE slug=$1 AND enabled`, slug)
	c, err := scanChannel(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Bundle returns the channel's frozen client set, building it on first use and
// caching it keyed by (id, version). A re-publish bumps version, so the next
// Bundle rebuilds; Invalidate drops the entry outright.
func (r *Registry) Bundle(ctx context.Context, ch *Channel) (*session.ClientBundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.cache[ch.ID]; ok && c.version == ch.Version {
		return c.bundle, nil
	}
	b, err := clientfactory.Build(ch.Snapshot.ToConfig(), r.deps)
	if err != nil {
		return nil, err
	}
	b.Greeting = ch.Snapshot.Greeting
	b.Avatar = ch.Snapshot.Avatar
	// Engine is DERIVED, not configured: a baked avatar can only be rendered
	// by the engine it was baked for, so binding the appearance already
	// determines the engine. Deriving it keeps the two from disagreeing.
	b.Engine = avatarcatalog.EngineFor(ch.Snapshot.Avatar)
	r.cache[ch.ID] = cachedBundle{version: ch.Version, bundle: b}
	return b, nil
}

// Invalidate drops a channel's cached bundle (called on re-publish/disable/
// delete from the control plane).
func (r *Registry) Invalidate(channelID int64) {
	r.mu.Lock()
	delete(r.cache, channelID)
	r.mu.Unlock()
}

// Reserve atomically claims one concurrency slot. It serializes on the channel
// row via FOR UPDATE so two simultaneous reserves can't both pass the cap check
// (anti-oversell). ErrChannelFull at capacity; ErrChannelUnavailable if the
// channel was disabled/deleted between Resolve and Reserve.
func (r *Registry) Reserve(ctx context.Context, channelID int64, sid string) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var maxC int
	err = tx.QueryRow(ctx,
		`SELECT max_concurrent FROM channels WHERE id=$1 AND enabled FOR UPDATE`,
		channelID).Scan(&maxC)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrChannelUnavailable
	}
	if err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM channel_sessions WHERE channel_id=$1`, channelID).Scan(&n); err != nil {
		return err
	}
	if n >= maxC {
		return ErrChannelFull
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO channel_sessions (channel_id, session_id) VALUES ($1, $2)`,
		channelID, sid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Release frees a slot. Fired from session teardown (the release hook), so it
// owns its own short-lived context — the session's ctx is already cancelled.
// Idempotent: deleting an absent row is a no-op.
func (r *Registry) Release(channelID int64, sid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.db.Pool.Exec(ctx,
		`DELETE FROM channel_sessions WHERE channel_id=$1 AND session_id=$2`,
		channelID, sid); err != nil {
		log.Printf("channel %d release %s: %v", channelID, sid, err)
	}
}
