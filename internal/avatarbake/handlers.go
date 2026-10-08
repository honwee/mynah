// Package avatarbake is the admin extension for idle-loop baking (Phase B
// 真烘焙). Each job bakes a living-standby idle loop for one avatar: FlashHead
// renders a neutral frame from the avatar's source portrait, LivePortrait
// transfers a chosen motion skeleton (.pkl) onto it, and the result is encoded
// to a cored-replayable idle_<avatar>.h264f. On completion (with apply=true)
// the live idle loop is hot-swapped via AdminDeps.ApplyIdle — this is what
// makes "换动作 / 换人" take effect on the live engine.
//
//	POST   /api/v1/avatars/bake        {avatar_id, motion_ref, apply} -> queue (or cache-hit done)
//	GET    /api/v1/avatars/bake        list jobs
//	GET    /api/v1/avatars/bake/{id}   one job (poll status/progress)
//	DELETE /api/v1/avatars/bake/{id}   cancel (queued/baking) or remove (terminal)
//
// Server-side paths (source portrait, motion .pkl, optional half-body canvas)
// are resolved and validated at create time, so the processor just submits
// them to the worker /bake endpoint. A finished bake lands in the flat cache
// as idle_<avatar>__<motion>.h264f — the same deterministic name this package
// computes — so a later select of the same (avatar, motion) pair is an instant
// cache hit with no GPU time. Stateful: needs DB; with no --db it mounts
// nothing.
package avatarbake

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mynah/core"

	"github.com/jackc/pgx/v5"
)

// Job is one bake row. Resolved server paths are internal — they never leave
// the server in an API response.
type Job struct {
	ID          int64     `json:"id"`
	AvatarID    string    `json:"avatar_id"`
	MotionRef   string    `json:"motion_ref"`
	SourceImage string    `json:"-"`
	MotionPkl   string    `json:"-"`
	Status      string    `json:"status"`
	AssetPath   string    `json:"-"`
	Apply       bool      `json:"apply"`
	Progress    int       `json:"progress"`
	Error       string    `json:"error"`
	WorkerJobID string    `json:"-"`
	HalfBody    string    `json:"-"`
	Blend       string    `json:"-"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const jobCols = `id, avatar_id, motion_ref, source_image, motion_pkl, status, asset_path, apply, progress, error, worker_job_id, half_body, blend, created_at, updated_at`

type scannable interface{ Scan(dest ...any) error }

func scanJob(row scannable) (*Job, error) {
	var j Job
	if err := row.Scan(&j.ID, &j.AvatarID, &j.MotionRef, &j.SourceImage, &j.MotionPkl,
		&j.Status, &j.AssetPath, &j.Apply, &j.Progress, &j.Error, &j.WorkerJobID,
		&j.HalfBody, &j.Blend, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	return &j, nil
}

// Extension implements core.AdminExtension.
type Extension struct {
	http       *http.Client
	bakeHTTP   string // avatar-train worker base (shares the worker with /train + /extract)
	cacheDir   string // flat idle-asset cache; must match the worker's PL_BAKE_CACHE_DIR
	builtinDir string // builtin motion skeleton .pkl dir (d9.pkl, d13.pkl, d14.pkl, ...)
	// resolveSource maps an avatar id to its portrait path — the same
	// resolution the session manager uses for cond_image, wired in Register
	// from AdminDeps so a bake and a live session agree on the source face.
	resolveSource func(id string) string
	proc          *processor
}

// New builds the idle-bake admin extension. The DB and the runtime hooks
// (ResolveAvatarSource / ApplyIdle) arrive later in Register.
//
// The two directories below have NO defaults on purpose. They used to default to
// /data/personalive/{idle_cache,motions/builtin} — a path from one particular
// machine, which that machine then stopped having. Nothing complained: bakes
// were accepted, queued, and died deep inside the worker subprocess with a
// stat error on a path nobody had typed. An absolute default is wrong on every
// machine except the one it was written on, and the wrongness is invisible until
// someone bakes. Empty + a startup line naming the variable is the honest
// version; Register turns a missing one into a refusal at the door.
func New() *Extension {
	tb := os.Getenv("PL_AVATAR_BAKE_HTTP")
	if tb == "" {
		tb = os.Getenv("PL_AVATAR_TRAIN_HTTP") // same worker process by default
	}
	if tb == "" {
		tb = "http://127.0.0.1:9405"
	}
	return &Extension{
		http:       &http.Client{},
		bakeHTTP:   strings.TrimRight(tb, "/"),
		cacheDir:   os.Getenv("PL_BAKE_CACHE_DIR"),
		builtinDir: os.Getenv("PL_MOTION_BUILTIN_DIR"),
	}
}

// Register mounts the routes and starts the bake processor. Baking needs
// persistence and the runtime idle hook; with no DB it mounts nothing.
func (e *Extension) Register(r core.RouteRegistrar, deps core.AdminDeps) {
	if deps.DB == nil {
		log.Printf("avatarbake: no DB (started without --db); idle-bake routes disabled")
		return
	}
	// Say out loud where this instance will look, and refuse to pretend the
	// chain works when it can't. Both dirs are consumed by a DIFFERENT process
	// (the bake worker) or resolved here and handed to it, so a wrong value
	// surfaces minutes later inside a subprocess if we don't check it now.
	if e.cacheDir == "" || e.builtinDir == "" {
		log.Printf("avatarbake: idle-bake routes disabled — set PL_BAKE_CACHE_DIR (finished-bake cache, must match the worker's) and PL_MOTION_BUILTIN_DIR (builtin:<name> skeleton .pkl dir; LivePortrait ships them under assets/examples/driving)")
		return
	}
	for _, d := range [][2]string{{"PL_BAKE_CACHE_DIR", e.cacheDir}, {"PL_MOTION_BUILTIN_DIR", e.builtinDir}} {
		if _, err := os.Stat(d[1]); err != nil {
			log.Printf("avatarbake: %s=%s is not readable (%v) — bakes will fail until it is", d[0], d[1], err)
		}
	}
	log.Printf("avatarbake: worker=%s cache=%s builtin-motions=%s", e.bakeHTTP, e.cacheDir, e.builtinDir)
	e.proc = newProcessor(deps.DB, e.http, e.bakeHTTP, deps.ApplyIdle)
	go e.proc.run(deps.Ctx)
	e.resolveSource = deps.ResolveAvatarSource
	r.Private("POST /api/v1/avatars/bake", e.create(deps.DB))
	r.Private("GET /api/v1/avatars/bake", e.list(deps.DB))
	r.Private("GET /api/v1/avatars/bake/{id}", e.get(deps.DB))
	r.Private("DELETE /api/v1/avatars/bake/{id}", e.del(deps.DB))
}

func (e *Extension) create(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			AvatarID  string `json:"avatar_id"`
			MotionRef string `json:"motion_ref"`
			Apply     *bool  `json:"apply"`
		}
		if !decode(w, req, &body) {
			return
		}
		avatarID := strings.TrimSpace(body.AvatarID)
		if avatarID == "" {
			avatarID = "default"
		}
		motionRef := strings.TrimSpace(body.MotionRef)
		if motionRef == "" {
			motionRef = "builtin:d9"
		}
		apply := body.Apply == nil || *body.Apply
		ctx := req.Context()

		// Resolve + validate every server path up front, so a queued job can
		// never fail on a typo'd ref after burning queue position.
		var source string
		if e.resolveSource != nil {
			source = e.resolveSource(avatarID)
		}
		if source == "" {
			fail(w, http.StatusBadRequest, "source image for avatar "+strconv.Quote(avatarID)+" is not resolvable")
			return
		}
		st, err := os.Stat(source)
		if err != nil {
			fail(w, http.StatusBadRequest, "source image for avatar "+strconv.Quote(avatarID)+" not found on server: "+source)
			return
		}
		// LivePortrait animates ONE portrait image. A baked avatar resolves to a
		// directory of pre-encoded frames instead, which passes Stat and then
		// blows up deep inside the worker — the resolver was widened to cover
		// bakes for session dispatch, and this path inherited that. Reject it
		// here where the operator can read the reason.
		if !st.Mode().IsRegular() {
			fail(w, http.StatusBadRequest, "avatar "+strconv.Quote(avatarID)+
				" is a baked avatar (frame directory), which has no source portrait to animate; "+
				"idle bakes need an avatar backed by a single image")
			return
		}
		pkl, err := e.resolveMotion(ctx, db, motionRef)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		// Half-body presenter: a <id>.halfbody.png canvas + <id>.blend.npz mask
		// alongside the source upgrade the bake (and the live worker) to the
		// head-to-waist canvas. Both-or-neither; missing = legacy 512 face.
		halfBody, blend := "", ""
		dir := filepath.Dir(source)
		hb, bl := filepath.Join(dir, avatarID+".halfbody.png"), filepath.Join(dir, avatarID+".blend.npz")
		if fileExists(hb) && fileExists(bl) {
			halfBody, blend = hb, bl
		}

		// Cache hit: the flat cache holds idle_<avatar>__<motion>.h264f under the
		// SAME deterministic name the worker moves a finished bake to. Present =
		// apply instantly, record a done job, no GPU time.
		cachePath := filepath.Join(e.cacheDir, "idle_"+safeName(avatarID)+"__"+safeName(motionRef)+".h264f")
		if fileExists(cachePath) {
			var id int64
			if err := db.QueryRow(ctx, `
				INSERT INTO avatar_bake_jobs (avatar_id, motion_ref, source_image, motion_pkl, status, asset_path, apply, progress, half_body, blend)
				VALUES ($1,$2,$3,$4,'done',$5,$6,100,$7,$8) RETURNING id`,
				avatarID, motionRef, source, pkl, cachePath, apply, halfBody, blend).Scan(&id); err != nil {
				fail(w, http.StatusInternalServerError, "record bake: "+err.Error())
				return
			}
			if apply {
				e.proc.applyAsset(id, avatarID, cachePath)
			}
			job, err := scanJob(db.QueryRow(ctx, `SELECT `+jobCols+` FROM avatar_bake_jobs WHERE id=$1`, id))
			if err != nil {
				ok(w, map[string]any{"id": id, "status": "done"})
				return
			}
			ok(w, job)
			return
		}

		var id int64
		if err := db.QueryRow(ctx, `
			INSERT INTO avatar_bake_jobs (avatar_id, motion_ref, source_image, motion_pkl, apply, half_body, blend)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			avatarID, motionRef, source, pkl, apply, halfBody, blend).Scan(&id); err != nil {
			fail(w, http.StatusInternalServerError, "create bake: "+err.Error())
			return
		}
		e.proc.Notify()
		job, err := scanJob(db.QueryRow(ctx, `SELECT `+jobCols+` FROM avatar_bake_jobs WHERE id=$1`, id))
		if err != nil {
			ok(w, map[string]any{"id": id, "status": "queued"})
			return
		}
		ok(w, job)
	}
}

// resolveMotion maps a skeleton ref to a worker-readable .pkl path.
// "builtin:<name>" resolves under the builtin dir; "asset:<id>" looks up a
// done motion_assets row.
func (e *Extension) resolveMotion(ctx context.Context, db core.DBConn, ref string) (string, error) {
	if name, okB := strings.CutPrefix(ref, "builtin:"); okB {
		p := filepath.Join(e.builtinDir, safeName(name)+".pkl")
		if !fileExists(p) {
			return "", errors.New("motion skeleton not found (builtin " + strconv.Quote(name) + " pkl not found on server)")
		}
		return p, nil
	}
	if idStr, okA := strings.CutPrefix(ref, "asset:"); okA {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			return "", errors.New("invalid motion asset ref " + strconv.Quote(ref))
		}
		var pkl, status string
		err = db.QueryRow(ctx, `SELECT pkl_path, status FROM motion_assets WHERE id=$1`, id).Scan(&pkl, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New("motion skeleton not found")
		}
		if err != nil {
			return "", errors.New("lookup motion skeleton: " + err.Error())
		}
		if status != "done" || pkl == "" {
			return "", errors.New("motion skeleton not found (extraction not finished)")
		}
		if !fileExists(pkl) {
			return "", errors.New("motion skeleton " + strconv.Quote(ref) + " pkl not found on server")
		}
		return pkl, nil
	}
	return "", errors.New("invalid motion asset ref " + strconv.Quote(ref) + ` (want "builtin:<name>" or "asset:<id>")`)
}

func (e *Extension) list(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		rows, err := db.Query(req.Context(), `SELECT `+jobCols+` FROM avatar_bake_jobs ORDER BY id DESC LIMIT 50`)
		if err != nil {
			fail(w, http.StatusInternalServerError, "list: "+err.Error())
			return
		}
		defer rows.Close()
		jobs := []*Job{}
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				fail(w, http.StatusInternalServerError, "scan: "+err.Error())
				return
			}
			jobs = append(jobs, j)
		}
		if err := rows.Err(); err != nil {
			fail(w, http.StatusInternalServerError, "list: "+err.Error())
			return
		}
		ok(w, map[string]any{"jobs": jobs})
	}
}

func (e *Extension) get(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, bad := pathID(w, req)
		if bad {
			return
		}
		job, err := scanJob(db.QueryRow(req.Context(), `SELECT `+jobCols+` FROM avatar_bake_jobs WHERE id=$1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, http.StatusNotFound, "bake job not found")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "get: "+err.Error())
			return
		}
		ok(w, job)
	}
}

// del cancels a queued/baking job (the processor bails on its next poll) or
// hard-removes a terminal one. The produced asset is kept — it IS the cache.
func (e *Extension) del(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, bad := pathID(w, req)
		if bad {
			return
		}
		ctx := req.Context()
		var status string
		if err := db.QueryRow(ctx, `SELECT status FROM avatar_bake_jobs WHERE id=$1`, id).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
			fail(w, http.StatusNotFound, "bake job not found")
			return
		} else if err != nil {
			fail(w, http.StatusInternalServerError, "delete: "+err.Error())
			return
		}
		if status == "queued" || status == "baking" {
			if _, err := db.Exec(ctx, `UPDATE avatar_bake_jobs SET status='canceled', updated_at=now() WHERE id=$1`, id); err != nil {
				fail(w, http.StatusInternalServerError, "cancel: "+err.Error())
				return
			}
			ok(w, map[string]any{"canceled": id})
			return
		}
		if _, err := db.Exec(ctx, `DELETE FROM avatar_bake_jobs WHERE id=$1`, id); err != nil {
			fail(w, http.StatusInternalServerError, "delete: "+err.Error())
			return
		}
		ok(w, map[string]any{"deleted": id})
	}
}

// safeName mirrors the worker's _safe_name (workers/avatartrain/server.py):
// alnum plus -_ kept, everything else _, max 48 chars, fallback "avatar".
// Both sides MUST agree — the flat-cache filename is the contract.
func safeName(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c == '-' || c == '_' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 48 {
		out = out[:48]
	}
	if out == "" {
		return "avatar"
	}
	return out
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// pathID parses the {id} path value as a positive int64.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, "invalid id")
		return 0, true
	}
	return id, false
}
