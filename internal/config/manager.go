package config

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"mynah/internal/store"
)

// ApplyHook receives the freshly-merged config whenever a hot group changes.
// The session manager registers one to swap its llm/tts/rag clients.
type ApplyHook func(cfg Config)

// Manager owns the merged view: defaults < flag overrides < DB settings.
// Reads are cheap (RWMutex + value copy); writes persist the group to the
// settings table, re-merge, and fan out to hooks.
type Manager struct {
	db        *store.Store // nil = no DB: flags-only, PUTs rejected upstream
	flagLayer map[string]json.RawMessage

	mu    sync.RWMutex
	cfg   Config
	hooks []ApplyHook
}

// NewManager builds the merged config. flagLayer carries only the flags the
// operator explicitly set (so an untouched flag never shadows a DB value).
func NewManager(ctx context.Context, db *store.Store, flagLayer map[string]json.RawMessage) (*Manager, error) {
	m := &Manager{db: db, flagLayer: flagLayer}
	cfg := Defaults()
	if err := applyLayer(&cfg, flagLayer); err != nil {
		return nil, err
	}
	if db != nil {
		dbLayer, err := m.loadDBLayer(ctx)
		if err != nil {
			return nil, err
		}
		if err := applyLayer(&cfg, dbLayer); err != nil {
			return nil, err
		}
	}
	m.cfg = cfg
	return m, nil
}

func (m *Manager) loadDBLayer(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := m.db.Pool.Query(ctx, `SELECT grp, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	layer := map[string]json.RawMessage{}
	for rows.Next() {
		var grp string
		var raw json.RawMessage
		if err := rows.Scan(&grp, &raw); err != nil {
			return nil, err
		}
		layer[grp] = raw
	}
	return layer, rows.Err()
}

// Current returns a copy of the merged config.
func (m *Manager) Current() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// OnApply registers a hook called (synchronously) after every hot update.
func (m *Manager) OnApply(h ApplyHook) {
	m.mu.Lock()
	m.hooks = append(m.hooks, h)
	m.mu.Unlock()
}

// Put validates and persists a group override, re-merges, and (for hot
// groups) fans out to hooks. Returns whether the change applied hot.
func (m *Manager) Put(ctx context.Context, group string, body []byte) (hot bool, err error) {
	if _, ok := Hot[group]; !ok {
		return false, fmt.Errorf("unknown config group %q", group)
	}
	if err := Validate(group, body); err != nil {
		return false, fmt.Errorf("invalid %s config: %w", group, err)
	}

	// Merge the PUT body into the existing DB override for the group so a
	// partial body ({"system_prompt": "..."}) doesn't drop sibling keys.
	if _, err := m.db.Pool.Exec(ctx, `
		INSERT INTO settings (grp, value) VALUES ($1, $2::jsonb)
		ON CONFLICT (grp) DO UPDATE
		SET value = settings.value || EXCLUDED.value, updated_at = now()`,
		group, body); err != nil {
		return false, err
	}

	// Rebuild the full merge (defaults < flags < DB) under the write lock.
	cfg := Defaults()
	if err := applyLayer(&cfg, m.flagLayer); err != nil {
		return false, err
	}
	dbLayer, err := m.loadDBLayer(ctx)
	if err != nil {
		return false, err
	}
	if err := applyLayer(&cfg, dbLayer); err != nil {
		return false, err
	}

	m.mu.Lock()
	m.cfg = cfg
	hooks := append([]ApplyHook(nil), m.hooks...)
	m.mu.Unlock()

	if Hot[group] {
		for _, h := range hooks {
			h(cfg)
		}
		return true, nil
	}
	return false, nil
}

// FlagLayer is a helper for main: collect explicitly-set flags into one
// group's raw-JSON overlay.
func FlagLayer(dst map[string]json.RawMessage, group string, v any) {
	b, _ := json.Marshal(v)
	dst[group] = b
}
