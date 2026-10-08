package core

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// RouteRegistrar lets an AdminExtension mount HTTP routes on the control plane
// without importing internal/control. The control server implements it; the
// two methods mirror its own public/private helpers.
type RouteRegistrar interface {
	// Private mounts a handler that requires a valid admin bearer token.
	Private(pattern string, h http.HandlerFunc)
	// Public mounts a handler with no authentication.
	Public(pattern string, h http.HandlerFunc)
}

// DBConn is the minimal Postgres surface a stateful AdminExtension needs (the
// avatar-training job table). *pgxpool.Pool satisfies it as-is, so the control
// plane passes its pool with no adapter. We expose the query shape — not the
// pool/driver lifecycle — to keep the open-core kernel from welding to a
// specific connection-management API.
type DBConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// AdminDeps is what the control plane hands an AdminExtension at assembly time.
// Fields beyond TTSBase are optional: a stateless extension (voice clone) reads
// only TTSBase; a stateful one (avatar training) also uses DB + Ctx; the
// idle-bake extension additionally uses ResolveAvatarSource + ApplyIdle.
type AdminDeps struct {
	// TTSBase returns the live TTS service base URL. It follows hot config
	// applies, so read it per request rather than capturing the value.
	TTSBase func() string
	// DB is the shared Postgres pool for persistence. nil when cored runs
	// without --db; a DB-backed extension must skip its routes rather than panic.
	DB DBConn
	// Ctx is process-lifetime; background goroutines (the training job
	// processor) run under it. The codebase has no graceful-shutdown signal
	// today, so this is context.Background() — the field is the seam for a
	// future shutdown refactor.
	Ctx context.Context
	// ResolveAvatarSource maps an avatar id ("default", a built-in id, or
	// "trained:<jobId>") to a worker-readable portrait path ("" = unknown).
	// The same resolution the session manager uses for SessionSpec.cond_image,
	// so a bake and a live session always agree on the source face. nil when
	// the runtime doesn't wire it.
	ResolveAvatarSource func(id string) string
	// ApplyIdle installs a baked idle loop (a cored-replayable .h264f/.ivf
	// path) as ONE AVATAR's idle material. Takes effect for sessions created
	// afterwards on that avatar; every other avatar is untouched. nil when the
	// runtime doesn't support idle-local hot swap.
	ApplyIdle func(avatarID, assetPath string) error
}

// AdminExtension is the seam for EE-only admin endpoints (voice clone and
// avatar training). The OSS build leaves it nil, so those routes simply do not
// exist. EE supplies an implementation at assembly time in its own cmd/, never
// by patching OSS sources.
type AdminExtension interface {
	// Register mounts the extension's routes. Called once at control-plane
	// assembly, only when the feature gate permits.
	Register(r RouteRegistrar, deps AdminDeps)
}
