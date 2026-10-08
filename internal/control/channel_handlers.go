package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"mynah/internal/channel"
)

// registerChannels is called from New when the channel registry + config
// manager are present (publish snapshots the live config).
func (s *Server) registerChannels() {
	s.private("GET /api/v1/channels", s.handleChannelList)
	s.private("POST /api/v1/channels", s.handleChannelPublish)
	s.private("GET /api/v1/channels/{id}", s.handleChannelGet)
	s.private("PATCH /api/v1/channels/{id}", s.handleChannelUpdate)
	s.private("POST /api/v1/channels/{id}/republish", s.handleChannelRepublish)
	s.private("POST /api/v1/channels/{id}/rotate-token", s.handleChannelRotateToken)
	s.private("DELETE /api/v1/channels/{id}", s.handleChannelDelete)
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// checkConcurrencyQuota enforces the pool budget for a channel about to be
// published or edited: both Hall conditions in poolFeasible, against the live
// dispatch pool. excludeID is the channel being edited (0 when creating) and
// avatar is its frozen avatar id, which decides whether its demand lands on one
// engine or on any of them.
//
// Returns "" when within budget or when the pool size is unknown. Fails OPEN on
// a query error: the quota is protection against over-promising, not access
// control, and a database hiccup must not block publishing.
func (s *Server) checkConcurrencyQuota(ctx context.Context, excludeID int64, want int, avatar string) string {
	workers, total := s.liveComposition()
	if total == 0 {
		return ""
	}
	demand, err := s.poolDemand(ctx, &quotaOverride{
		ID: excludeID, MaxConcurrent: want, Avatar: avatar,
	})
	if err != nil {
		logErr("concurrency quota query", err)
		return ""
	}
	return poolFeasible(workers, demand)
}

type channelRow struct {
	ID            int64     `json:"id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	Version       int       `json:"version"`
	Enabled       bool      `json:"enabled"`
	AccessMode    string    `json:"access_mode"`
	AccessToken   string    `json:"access_token"` // admin-only API; used to build share links
	Domains       []string  `json:"domains"`
	CIDRs         []string  `json:"cidrs"`
	MaxConcurrent int       `json:"max_concurrent"`
	Active        int       `json:"active"` // live ledger count
	Voice         string    `json:"voice,omitempty"` // frozen effective voice (qwen cloud voice in qwen-brain snapshots)
	BrainMode     string    `json:"brain_mode,omitempty"` // frozen brain mode; "" = pre-brain snapshot (inherits live)
	// Display/branding (channel metadata, not part of the frozen snapshot).
	Description        string   `json:"description"`
	SuggestedQuestions []string `json:"suggested_questions"`
	BrandName          string   `json:"brand_name"`
	BrandLogo          string   `json:"brand_logo"` // top-bar logo image URL; '' = built-in mark
	BgImage            string   `json:"bg_image"`   // stage background for green-screen keying; '' = off
	ThemeColor         string   `json:"theme_color"`
	AvatarPreview      string   `json:"avatar_preview"`
	ShareURL           string   `json:"share_url"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// shareURL builds the public visitor link for a channel from the admin
// request's hostname and the real visitor listen flags (the config group may
// not reflect them). Prefers the TLS port (WebRTC mic needs a secure origin).
func (s *Server) shareURL(r *http.Request, slug, mode, token string) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	scheme, addr := "http", s.deps.VisitorListen
	if s.deps.VisitorTLSListen != "" {
		scheme, addr = "https", s.deps.VisitorTLSListen
	}
	port := addr
	if _, p, err := net.SplitHostPort(addr); err == nil {
		port = p
	} else {
		port = strings.TrimPrefix(addr, ":")
	}
	u := scheme + "://" + host
	if port != "" && port != "80" && port != "443" {
		u += ":" + port
	}
	u += "/channel/" + slug
	if mode == "token" && token != "" {
		u += "?k=" + token
	}
	return u
}

const channelRowCols = `c.id, c.slug, c.name, c.version, c.enabled, c.access_mode, c.access_token,
	c.domains, c.cidrs, c.max_concurrent,
	c.description, c.suggested_questions, c.brand_name, c.brand_logo, c.bg_image, c.theme_color, c.avatar_preview,
	(SELECT count(*) FROM channel_sessions cs WHERE cs.channel_id = c.id),
	c.created_at, c.updated_at`

// channelInsertCols mirrors channelRowCols' order for an INSERT ... RETURNING
// (no table alias, and a literal 0 active count for the brand-new row).
const channelInsertCols = `id, slug, name, version, enabled, access_mode, access_token,
	domains, cidrs, max_concurrent,
	description, suggested_questions, brand_name, brand_logo, bg_image, theme_color, avatar_preview,
	0, created_at, updated_at`

func scanChannelRow(row pgx.Row) (channelRow, error) {
	var c channelRow
	var domainsRaw, cidrsRaw, suggestionsRaw, cfgRaw []byte
	err := row.Scan(&c.ID, &c.Slug, &c.Name, &c.Version, &c.Enabled, &c.AccessMode,
		&c.AccessToken, &domainsRaw, &cidrsRaw, &c.MaxConcurrent,
		&c.Description, &suggestionsRaw, &c.BrandName, &c.BrandLogo, &c.BgImage, &c.ThemeColor, &c.AvatarPreview,
		&c.Active, &c.CreatedAt, &c.UpdatedAt, &cfgRaw)
	if err != nil {
		return c, err
	}
	c.Domains = jsonStrs(domainsRaw)
	c.CIDRs = jsonStrs(cidrsRaw)
	c.SuggestedQuestions = jsonStrs(suggestionsRaw)
	c.Voice, c.BrainMode = snapshotVoice(cfgRaw)
	return c, nil
}

func jsonStrs(raw []byte) []string {
	out := []string{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func (s *Server) handleChannelList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.deps.DB.Pool.Query(r.Context(),
		`SELECT `+channelRowCols+`, c.config FROM channels c ORDER BY c.id`)
	if err != nil {
		logErr("channel list", err)
		fail(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []channelRow{}
	for rows.Next() {
		c, err := scanChannelRow(rows)
		if err != nil {
			fail(w, http.StatusInternalServerError, "scan failed")
			return
		}
		c.ShareURL = s.shareURL(r, c.Slug, c.AccessMode, c.AccessToken)
		out = append(out, c)
	}
	ok(w, out)
}

func (s *Server) handleChannelGet(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(w, r)
	if bad {
		return
	}
	var c channelRow
	var domainsRaw, cidrsRaw, suggestionsRaw, cfgRaw []byte
	err := s.deps.DB.Pool.QueryRow(r.Context(),
		`SELECT `+channelRowCols+`, c.config FROM channels c WHERE c.id = $1`, id).
		Scan(&c.ID, &c.Slug, &c.Name, &c.Version, &c.Enabled, &c.AccessMode, &c.AccessToken,
			&domainsRaw, &cidrsRaw, &c.MaxConcurrent,
			&c.Description, &suggestionsRaw, &c.BrandName, &c.BrandLogo, &c.BgImage, &c.ThemeColor, &c.AvatarPreview,
			&c.Active, &c.CreatedAt, &c.UpdatedAt, &cfgRaw)
	if err == pgx.ErrNoRows {
		fail(w, http.StatusNotFound, "channel not found")
		return
	}
	if err != nil {
		logErr("channel get", err)
		fail(w, http.StatusInternalServerError, "query failed")
		return
	}
	c.Domains, c.CIDRs = jsonStrs(domainsRaw), jsonStrs(cidrsRaw)
	c.SuggestedQuestions = jsonStrs(suggestionsRaw)
	c.Voice, c.BrainMode = snapshotVoice(cfgRaw)
	c.ShareURL = s.shareURL(r, c.Slug, c.AccessMode, c.AccessToken)
	ok(w, map[string]any{"channel": c, "snapshot": snapshotSummary(cfgRaw)})
}

// channelPublishReq is the publish/republish body. Pointers distinguish absent
// from zero on republish (preserve existing value).
type channelPublishReq struct {
	Name          string   `json:"name"`
	Slug          string   `json:"slug"`
	Greeting      string   `json:"greeting"`
	// Avatar binds this channel to one appearance. Empty = take the live
	// global selection at publish time.
	Avatar        string   `json:"avatar"`
	AccessMode    string   `json:"access_mode"` // "public" (default) | "token"
	Domains       []string `json:"domains"`
	CIDRs         []string `json:"cidrs"`
	MaxConcurrent int      `json:"max_concurrent"`
	// Voice overrides the snapshot's TTS voice for this channel (empty = inherit
	// whatever the live config currently has).
	Voice string `json:"voice"`
	// Display/branding (channel metadata, not frozen into the snapshot).
	Description        string   `json:"description"`
	SuggestedQuestions []string `json:"suggested_questions"`
	BrandName          string   `json:"brand_name"`
	BrandLogo          string   `json:"brand_logo"`
	BgImage            string   `json:"bg_image"`
	ThemeColor         string   `json:"theme_color"`
	AvatarPreview      string   `json:"avatar_preview"`
}

func (s *Server) handleChannelPublish(w http.ResponseWriter, r *http.Request) {
	var req channelPublishReq
	if !decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		fail(w, http.StatusBadRequest, "name is required")
		return
	}
	if !slugRe.MatchString(req.Slug) {
		fail(w, http.StatusBadRequest, "slug must be 2-63 chars: lowercase letters, digits, hyphens")
		return
	}
	mode, token, ferr := normalizeAccess(req.AccessMode)
	if ferr != "" {
		fail(w, http.StatusBadRequest, ferr)
		return
	}
	if ferr := validateCIDRs(req.CIDRs); ferr != "" {
		fail(w, http.StatusBadRequest, ferr)
		return
	}
	if ferr := validateThemeColor(req.ThemeColor); ferr != "" {
		fail(w, http.StatusBadRequest, ferr)
		return
	}
	maxC := req.MaxConcurrent
	if maxC < 1 {
		maxC = 1
	}

	cfg := s.deps.Config.Current()
	// Avatar: the channel's own choice if given, else the live global one.
	// Frozen here like everything else, so a later global switch cannot change
	// a published channel's face.
	//
	// Resolved BEFORE the quota check because it decides which engine this
	// channel's concurrency is drawn from — a baked avatar can only be served by
	// musetalk workers, so the pool-wide total is not the whole budget.
	avatar := strings.TrimSpace(req.Avatar)
	if avatar == "" {
		avatar = strings.TrimSpace(cfg.Avatar.Current)
	}
	if msg := s.checkConcurrencyQuota(r.Context(), 0, maxC, avatar); msg != "" {
		fail(w, http.StatusConflict, msg)
		return
	}

	snap := channel.Snapshot{LLM: cfg.LLM, TTS: cfg.TTS, RAG: cfg.RAG,
		Greeting: req.Greeting, Brain: cfg.Brain, Avatar: avatar}
	if req.Voice != "" {
		if snap.Brain.Mode == "qwen" {
			// qwen brain: the cloud voice is what visitors hear; the local
			// TTS voice is inert in this mode.
			snap.Brain.Voice = req.Voice
		} else {
			// Per-channel voice override: carry the voice's own saved seed along
			// (the inherited cfg.TTS.Seed belongs to the default voice — freezing
			// it with a different voice would drift the timbre per sentence).
			snap.TTS.Voice = req.Voice
			snap.TTS.Seed = cfg.TTS.SeedFor(req.Voice)
		}
	}
	cfgJSON, _ := json.Marshal(snap)

	var c channelRow
	var domainsRaw, cidrsRaw, suggestionsRaw []byte
	err := s.deps.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO channels (slug, name, config, access_mode, access_token,
		                      domains, cidrs, max_concurrent,
		                      description, suggested_questions, brand_name, brand_logo, bg_image, theme_color, avatar_preview)
		VALUES ($1, $2, $3::jsonb, $4, $5, $6::jsonb, $7::jsonb, $8,
		        $9, $10::jsonb, $11, $12, $13, $14, $15)
		RETURNING `+channelInsertCols,
		req.Slug, req.Name, cfgJSON, mode, token,
		mustJSON(req.Domains), mustJSON(req.CIDRs), maxC,
		req.Description, mustJSON(normalizeSuggestions(req.SuggestedQuestions)),
		req.BrandName, req.BrandLogo, req.BgImage, req.ThemeColor, req.AvatarPreview).
		Scan(&c.ID, &c.Slug, &c.Name, &c.Version, &c.Enabled, &c.AccessMode, &c.AccessToken,
			&domainsRaw, &cidrsRaw, &c.MaxConcurrent,
			&c.Description, &suggestionsRaw, &c.BrandName, &c.BrandLogo, &c.BgImage, &c.ThemeColor, &c.AvatarPreview,
			&c.Active, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		fail(w, http.StatusConflict, "publish failed (duplicate slug?): "+err.Error())
		return
	}
	c.Domains, c.CIDRs = jsonStrs(domainsRaw), jsonStrs(cidrsRaw)
	c.SuggestedQuestions = jsonStrs(suggestionsRaw)
	c.Voice, c.BrainMode = effectiveVoice(snap), snap.Brain.Mode // echo the frozen voice (INSERT RETURNING omits config)
	c.ShareURL = s.shareURL(r, c.Slug, c.AccessMode, c.AccessToken)
	ok(w, c)
}

func (s *Server) handleChannelUpdate(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(w, r)
	if bad {
		return
	}
	var req struct {
		Name          *string   `json:"name"`
		Enabled       *bool     `json:"enabled"`
		AccessMode    *string   `json:"access_mode"`
		Domains       *[]string `json:"domains"`
		CIDRs         *[]string `json:"cidrs"`
		MaxConcurrent *int      `json:"max_concurrent"`
		// Voice re-freezes just the snapshot's TTS voice in place (version +1).
		Voice *string `json:"voice"`
		// Display/branding — hot-editable (not part of the frozen snapshot).
		Description        *string   `json:"description"`
		SuggestedQuestions *[]string `json:"suggested_questions"`
		BrandName          *string   `json:"brand_name"`
		BrandLogo          *string   `json:"brand_logo"`
		BgImage            *string   `json:"bg_image"`
		ThemeColor         *string   `json:"theme_color"`
		AvatarPreview      *string   `json:"avatar_preview"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.AccessMode != nil && *req.AccessMode != "public" && *req.AccessMode != "token" {
		fail(w, http.StatusBadRequest, "access_mode must be public or token")
		return
	}
	if req.CIDRs != nil {
		if ferr := validateCIDRs(*req.CIDRs); ferr != "" {
			fail(w, http.StatusBadRequest, ferr)
			return
		}
	}
	if req.ThemeColor != nil {
		if ferr := validateThemeColor(*req.ThemeColor); ferr != "" {
			fail(w, http.StatusBadRequest, ferr)
			return
		}
	}
	if req.MaxConcurrent != nil && *req.MaxConcurrent < 1 {
		fail(w, http.StatusBadRequest, "max_concurrent must be >= 1")
		return
	}
	// Quota check when the resulting state holds an allocation: raising
	// max_concurrent, or re-enabling a channel (its quota was released while
	// disabled and must fit again).
	if req.MaxConcurrent != nil || (req.Enabled != nil && *req.Enabled) {
		var curEnabled bool
		var curMaxC int
		err := s.deps.DB.Pool.QueryRow(r.Context(),
			`SELECT enabled, max_concurrent FROM channels WHERE id = $1`, id).
			Scan(&curEnabled, &curMaxC)
		if err == pgx.ErrNoRows {
			fail(w, http.StatusNotFound, "channel not found")
			return
		}
		if err == nil {
			newEnabled, newMaxC := curEnabled, curMaxC
			if req.Enabled != nil {
				newEnabled = *req.Enabled
			}
			if req.MaxConcurrent != nil {
				newMaxC = *req.MaxConcurrent
			}
			// Lowering (or keeping) an already-enabled channel's allocation is
			// always allowed — otherwise a legacy over-allocated state would
			// block every settings save, including the one that fixes it.
			tightening := curEnabled && newEnabled && newMaxC <= curMaxC
			if newEnabled && !tightening {
				if msg := s.checkConcurrencyQuota(r.Context(), id, newMaxC, ""); msg != "" {
					fail(w, http.StatusConflict, msg)
					return
				}
			}
		}
	}
	tag, err := s.deps.DB.Pool.Exec(r.Context(), `
		UPDATE channels SET
			name                = COALESCE($2, name),
			enabled             = COALESCE($3, enabled),
			access_mode         = COALESCE($4, access_mode),
			domains             = COALESCE($5::jsonb, domains),
			cidrs               = COALESCE($6::jsonb, cidrs),
			max_concurrent      = COALESCE($7, max_concurrent),
			description         = COALESCE($8, description),
			suggested_questions = COALESCE($9::jsonb, suggested_questions),
			brand_name          = COALESCE($10, brand_name),
			brand_logo          = COALESCE($11, brand_logo),
			bg_image            = COALESCE($12, bg_image),
			theme_color         = COALESCE($13, theme_color),
			avatar_preview      = COALESCE($14, avatar_preview),
			updated_at          = now()
		WHERE id = $1`,
		id, req.Name, req.Enabled, req.AccessMode,
		jsonbArg(req.Domains), jsonbArg(req.CIDRs), req.MaxConcurrent,
		req.Description, jsonbSuggestions(req.SuggestedQuestions),
		req.BrandName, req.BrandLogo, req.BgImage, req.ThemeColor, req.AvatarPreview)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, http.StatusNotFound, "channel not found")
		return
	}
	// Disabling a channel drops its cached bundle (Resolve already filters on
	// enabled, so access stops immediately regardless).
	if req.Enabled != nil && !*req.Enabled {
		s.deps.Channels.Invalidate(id)
	}
	// Voice is the one snapshot field editable here: patch it in place (leaving
	// the frozen persona/RAG untouched) and bump version so new sessions pick it
	// up. Use "重新发布" to re-freeze the rest of the live config. On a qwen-brain
	// snapshot the patch targets the cloud voice — the local TTS voice is inert
	// in that mode.
	if req.Voice != nil && *req.Voice != "" {
		var raw []byte
		if err := s.deps.DB.Pool.QueryRow(r.Context(),
			`SELECT config FROM channels WHERE id = $1`, id).Scan(&raw); err == nil {
			var snap channel.Snapshot
			if json.Unmarshal(raw, &snap) == nil {
				changed := false
				if snap.Brain.Mode == "qwen" {
					if snap.Brain.Voice != *req.Voice {
						snap.Brain.Voice = *req.Voice
						changed = true
					}
				} else {
					// The seed rides with the voice (looked up from the live voice_seeds
					// map), so a voice patch also repairs snapshots frozen with a stale
					// or missing seed — hence "no-op" requires voice AND seed to match.
					seed := s.deps.Config.Current().TTS.SeedFor(*req.Voice)
					if snap.TTS.Voice != *req.Voice || snap.TTS.Seed != seed {
						snap.TTS.Voice = *req.Voice
						snap.TTS.Seed = seed
						changed = true
					}
				}
				if changed {
					if cfgJSON, mErr := json.Marshal(snap); mErr == nil {
						if _, uErr := s.deps.DB.Pool.Exec(r.Context(),
							`UPDATE channels SET config = $2::jsonb, version = version + 1, updated_at = now() WHERE id = $1`,
							id, cfgJSON); uErr == nil {
							s.deps.Channels.Invalidate(id)
						}
					}
				}
			}
		}
	}
	ok(w, map[string]any{"updated": true})
}

// handleChannelRepublish re-freezes the live config into the channel and bumps
// version (evicting the cached bundle). The greeting is preserved unless the
// body supplies a new one.
func (s *Server) handleChannelRepublish(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(w, r)
	if bad {
		return
	}
	var req struct {
		Greeting *string `json:"greeting"`
		Avatar   *string `json:"avatar"`
	}
	// Body is optional (greeting override); an empty POST is valid.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	var oldRaw []byte
	if err := s.deps.DB.Pool.QueryRow(r.Context(),
		`SELECT config FROM channels WHERE id = $1`, id).Scan(&oldRaw); err != nil {
		if err == pgx.ErrNoRows {
			fail(w, http.StatusNotFound, "channel not found")
			return
		}
		logErr("channel republish read", err)
		fail(w, http.StatusInternalServerError, "query failed")
		return
	}
	var old channel.Snapshot
	_ = json.Unmarshal(oldRaw, &old)
	greeting := old.Greeting
	if req.Greeting != nil {
		greeting = *req.Greeting
	}
	cfg := s.deps.Config.Current()
	avatar := strings.TrimSpace(old.Avatar)
	if req.Avatar != nil {
		avatar = strings.TrimSpace(*req.Avatar)
	}
	// Re-check the quota when the avatar moves. Republish is the one path that can
	// change a channel's ENGINE without changing its concurrency, and under a
	// mixed pool the engine is half the budget: rebinding a 2-concurrency channel
	// from a musetalk bake to a portrait moves its demand onto the single
	// flashhead worker, which publish/edit would have refused with a 409. Same
	// fail-open contract as those two — the quota protects against
	// over-promising, it is not access control.
	if avatar != strings.TrimSpace(old.Avatar) {
		var maxC int
		if err := s.deps.DB.Pool.QueryRow(r.Context(),
			`SELECT max_concurrent FROM channels WHERE id = $1`, id).Scan(&maxC); err == nil {
			if msg := s.checkConcurrencyQuota(r.Context(), id, maxC, avatar); msg != "" {
				fail(w, http.StatusConflict, msg)
				return
			}
		} else {
			logErr("channel republish quota read", err)
		}
	}
	snap := channel.Snapshot{LLM: cfg.LLM, TTS: cfg.TTS, RAG: cfg.RAG,
		Greeting: greeting, Brain: cfg.Brain, Avatar: avatar}
	cfgJSON, _ := json.Marshal(snap)

	var version int
	if err := s.deps.DB.Pool.QueryRow(r.Context(), `
		UPDATE channels SET config = $2::jsonb, version = version + 1, updated_at = now()
		WHERE id = $1 RETURNING version`, id, cfgJSON).Scan(&version); err != nil {
		logErr("channel republish", err)
		fail(w, http.StatusInternalServerError, "update failed")
		return
	}
	s.deps.Channels.Invalidate(id)
	ok(w, map[string]any{"republished": true, "version": version})
}

// handleChannelRotateToken issues a fresh access token (revoking every old
// share link) and switches the channel to token mode. Resolve reads the token
// fresh per offer, so no cache invalidation is needed.
func (s *Server) handleChannelRotateToken(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(w, r)
	if bad {
		return
	}
	token := randToken()
	tag, err := s.deps.DB.Pool.Exec(r.Context(), `
		UPDATE channels SET access_token = $2, access_mode = 'token', updated_at = now()
		WHERE id = $1`, id, token)
	if err != nil {
		logErr("channel rotate-token", err)
		fail(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, http.StatusNotFound, "channel not found")
		return
	}
	ok(w, map[string]any{"access_mode": "token", "access_token": token})
}

func (s *Server) handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(w, r)
	if bad {
		return
	}
	tag, err := s.deps.DB.Pool.Exec(r.Context(),
		`DELETE FROM channels WHERE id = $1`, id) // cascades channel_sessions
	if err != nil {
		logErr("channel delete", err)
		fail(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, http.StatusNotFound, "channel not found")
		return
	}
	s.deps.Channels.Invalidate(id)
	ok(w, map[string]any{"deleted": true})
}

// ---- helpers ---------------------------------------------------------------

// snapshotSummary exposes the human-visible parts of a frozen snapshot without
// leaking secrets (api keys stay server-side).
func snapshotSummary(raw []byte) map[string]any {
	var snap channel.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil
	}
	return map[string]any{
		"provider":      snap.LLM.Provider,
		"model":         snap.LLM.Model,
		"system_prompt": snap.LLM.SystemPrompt,
		"voice":         effectiveVoice(snap),
		"rag_enabled":   snap.RAG.Enabled,
		"greeting":      snap.Greeting,
		"avatar":        snap.Avatar,
		"brain_mode":    snap.Brain.Mode, // "" on pre-brain snapshots (= inherit live)
	}
}

// snapshotVoice extracts just the frozen TTS voice from a snapshot, for the
// channel list/detail summary (and to detect no-op voice patches).
// effectiveVoice is the voice a visitor of this snapshot actually hears: the
// qwen cloud voice when the frozen brain is qwen, the local TTS voice otherwise.
func effectiveVoice(snap channel.Snapshot) string {
	if snap.Brain.Mode == "qwen" {
		return snap.Brain.Voice
	}
	return snap.TTS.Voice
}

func snapshotVoice(raw []byte) (voice, brainMode string) {
	var snap channel.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return "", ""
	}
	return effectiveVoice(snap), snap.Brain.Mode
}

func normalizeAccess(mode string) (resolvedMode, token, failMsg string) {
	switch mode {
	case "", "public":
		return "public", "", ""
	case "token":
		return "token", randToken(), ""
	default:
		return "", "", "access_mode must be public or token"
	}
}

func validateCIDRs(cidrs []string) string {
	for _, c := range cidrs {
		if _, _, err := net.ParseCIDR(c); err != nil {
			return "invalid CIDR: " + c
		}
	}
	return ""
}

var themeColorRe = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// validateThemeColor accepts empty (= system default) or a #RGB/#RRGGBB hex.
func validateThemeColor(c string) string {
	if c == "" || themeColorRe.MatchString(c) {
		return ""
	}
	return "theme_color must be empty or a hex color like #4361ee"
}

// normalizeSuggestions trims, drops blanks, and caps the quick-ask list at 8.
func normalizeSuggestions(in []string) []string {
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// jsonbSuggestions returns a normalized jsonb arg for an optional patch, or nil
// to leave the column unchanged via COALESCE.
func jsonbSuggestions(v *[]string) any {
	if v == nil {
		return nil
	}
	return mustJSON(normalizeSuggestions(*v))
}

func randToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mustJSON marshals a slice for a jsonb param, defaulting nil to an empty array.
func mustJSON(v []string) []byte {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return b
}

// jsonbArg returns a jsonb-castable arg for an optional slice patch, or nil to
// leave the column unchanged via COALESCE.
func jsonbArg(v *[]string) any {
	if v == nil {
		return nil
	}
	return mustJSON(*v)
}
