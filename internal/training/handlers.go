// Package training is the admin extension for the avatar training pipeline
// (形象训练管线). cored persists jobs, streams the uploaded video to disk, and
// drives the worker /train HTTP endpoint, which extracts the best
// speaking-base portrait from the video (workers/avatartrain — mediapipe
// frame scoring, closed-mouth/frontal/sharp preference). The finished
// artifact (portrait.jpg) is resolvable as avatar id "trained:<jobId>"
// (ResolvePortrait) — selecting it drives the live engine's cond_image and
// the idle-bake source.
//
//	POST   /api/v1/avatars/training            multipart (video,name,consent) -> queue a job
//	GET    /api/v1/avatars/training            list jobs
//	GET    /api/v1/avatars/training/{id}       one job (poll status/progress)
//	DELETE /api/v1/avatars/training/{id}       cancel (running/queued) or remove (terminal)
//	POST   /api/v1/avatars/training/{id}/active set a done job as the active avatar
//
// Unlike voice clone, this extension is stateful: it needs a DB (core.AdminDeps.DB)
// and a background processor goroutine. With no --db it mounts nothing.
package training

import (
	"context"
	"errors"
	"io"
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

const (
	maxVideoBytes = 200 << 20 // 200MB source video cap
)

// Job is one training job row. Disk path, worker id, and consent are internal —
// they never leave the server in an API response.
type Job struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	SourcePath   string    `json:"-"`
	ArtifactPath string    `json:"artifact_path"`
	Progress     int       `json:"progress"`
	Error        string    `json:"error"`
	WorkerJobID  string    `json:"-"`
	Consent      string    `json:"-"`
	IsActive     bool      `json:"is_active"`
	// MotionRef is which driving skeleton this avatar's idle should use
	// ("builtin:d9" / "asset:<id>"); the console passes it to the idle-bake
	// extension when the trained avatar is selected.
	MotionRef string    `json:"motion_ref"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const jobCols = `id, name, status, source_path, artifact_path, progress, error, worker_job_id, consent, is_active, motion_ref, created_at, updated_at`

type scannable interface{ Scan(dest ...any) error }

func scanJob(row scannable) (*Job, error) {
	var j Job
	if err := row.Scan(&j.ID, &j.Name, &j.Status, &j.SourcePath, &j.ArtifactPath,
		&j.Progress, &j.Error, &j.WorkerJobID, &j.Consent, &j.IsActive,
		&j.MotionRef, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	return &j, nil
}

// Extension implements core.AdminExtension.
type Extension struct {
	http      *http.Client
	trainHTTP string // avatar-train worker base, e.g. http://127.0.0.1:9405
	uploadDir string // where source videos land (data disk; system disk is full)
	proc      *processor
}

// New builds the avatar-training admin extension. The DB arrives later in
// Register (it isn't open at cmd/ assembly time).
func New() *Extension {
	tb := os.Getenv("PL_AVATAR_TRAIN_HTTP")
	if tb == "" {
		tb = "http://127.0.0.1:9405"
	}
	dir := os.Getenv("PL_TRAIN_UPLOAD_DIR")
	if dir == "" {
		// Under the working dir, not an absolute path from someone else's
		// machine: the old /data/personalive default was the data disk of a box
		// that got rebuilt, and nothing noticed until an upload failed on
		// MkdirAll. NOTE the worker must see this same path — set the variable
		// on both when they are in different containers.
		dir = filepath.Join(".data", "training_uploads")
	}
	return &Extension{
		http:      &http.Client{},
		trainHTTP: strings.TrimRight(tb, "/"),
		uploadDir: dir,
	}
}

// Register mounts the routes and starts the job processor. Avatar training
// needs persistence, so with no DB it mounts nothing (the console's feature
// probe then degrades gracefully instead of hitting 500s).
func (e *Extension) Register(r core.RouteRegistrar, deps core.AdminDeps) {
	if deps.DB == nil {
		log.Printf("training: no DB (started without --db); avatar-training routes disabled")
		return
	}
	e.proc = newProcessor(deps.DB, e.http, e.trainHTTP)
	go e.proc.run(deps.Ctx)
	log.Printf("training: worker=%s uploads=%s", e.trainHTTP, e.uploadDir)
	r.Private("POST /api/v1/avatars/training", e.create(deps.DB))
	r.Private("GET /api/v1/avatars/training", e.list(deps.DB))
	r.Private("GET /api/v1/avatars/training/{id}", e.get(deps.DB))
	r.Private("DELETE /api/v1/avatars/training/{id}", e.del(deps.DB))
	r.Private("POST /api/v1/avatars/training/{id}/active", e.setActive(deps.DB))
}

// create accepts a multipart upload (video + name + consent) and queues a job.
// The video is streamed straight to a temp file on the data disk (never spooled
// to the full system disk, and never fully buffered in RAM), then renamed under
// the job's id once the INSERT assigns one.
func (e *Extension) create(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		mr, err := req.MultipartReader()
		if err != nil {
			fail(w, http.StatusBadRequest, "expected multipart/form-data: "+err.Error())
			return
		}
		if err := os.MkdirAll(e.uploadDir, 0o755); err != nil {
			fail(w, http.StatusInternalServerError, "upload dir: "+err.Error())
			return
		}
		tmp, err := os.CreateTemp(e.uploadDir, "incoming-*.mp4")
		if err != nil {
			fail(w, http.StatusInternalServerError, "temp file: "+err.Error())
			return
		}
		tmpName := tmp.Name()
		cleanupTmp := true
		defer func() {
			if cleanupTmp {
				tmp.Close()
				os.Remove(tmpName)
			}
		}()

		var name, consent string
		var motionRef string
		var videoBytes int64
		gotVideo := false
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				fail(w, http.StatusBadRequest, "read upload: "+err.Error())
				return
			}
			switch part.FormName() {
			case "name":
				b, _ := io.ReadAll(io.LimitReader(part, 4096))
				name = strings.TrimSpace(string(b))
			case "consent":
				b, _ := io.ReadAll(io.LimitReader(part, 4096))
				consent = strings.TrimSpace(string(b))
			case "motion_ref":
				b, _ := io.ReadAll(io.LimitReader(part, 4096))
				motionRef = strings.TrimSpace(string(b))
			case "video":
				gotVideo = true
				if ct := part.Header.Get("Content-Type"); ct != "" &&
					!strings.HasPrefix(ct, "video/") && !looksVideo(part.FileName()) {
					part.Close()
					fail(w, http.StatusBadRequest, "file must be a video (mp4/mov/webm/mkv)")
					return
				}
				n, err := io.Copy(tmp, io.LimitReader(part, maxVideoBytes+1))
				if err != nil {
					part.Close()
					fail(w, http.StatusInternalServerError, "save video: "+err.Error())
					return
				}
				videoBytes = n
			}
			part.Close()
		}
		if err := tmp.Close(); err != nil {
			fail(w, http.StatusInternalServerError, "save video: "+err.Error())
			return
		}

		if name == "" || len([]rune(name)) > 40 {
			fail(w, http.StatusBadRequest, "name is required (<= 40 chars)")
			return
		}
		if consent == "" {
			fail(w, http.StatusBadRequest, "consent is required: confirm you are authorized to train this likeness")
			return
		}
		if !gotVideo || videoBytes == 0 {
			fail(w, http.StatusBadRequest, `missing "video" file`)
			return
		}
		if videoBytes > maxVideoBytes {
			fail(w, http.StatusRequestEntityTooLarge, "video exceeds 200MB")
			return
		}

		if motionRef == "" {
			motionRef = "builtin:d9" // lively default, mirrors the DB column default
		}

		ctx := req.Context()
		var id int64
		if err := db.QueryRow(ctx,
			`INSERT INTO training_jobs (name, consent, motion_ref, status) VALUES ($1, $2, $3, 'queued') RETURNING id`,
			name, consent, motionRef).Scan(&id); err != nil {
			fail(w, http.StatusInternalServerError, "create job: "+err.Error())
			return
		}
		dir := filepath.Join(e.uploadDir, strconv.FormatInt(id, 10))
		dest := filepath.Join(dir, "source.mp4")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			_, _ = db.Exec(ctx, `UPDATE training_jobs SET status='failed', error=$2, updated_at=now() WHERE id=$1`, id, "store video: "+err.Error())
			fail(w, http.StatusInternalServerError, "store video: "+err.Error())
			return
		}
		if err := os.Rename(tmpName, dest); err != nil {
			_, _ = db.Exec(ctx, `UPDATE training_jobs SET status='failed', error=$2, updated_at=now() WHERE id=$1`, id, "store video: "+err.Error())
			fail(w, http.StatusInternalServerError, "store video: "+err.Error())
			return
		}
		cleanupTmp = false // renamed away; don't delete
		if _, err := db.Exec(ctx,
			`UPDATE training_jobs SET source_path=$2, updated_at=now() WHERE id=$1`, id, dest); err != nil {
			fail(w, http.StatusInternalServerError, "create job: "+err.Error())
			return
		}
		e.proc.Notify()

		job, err := scanJob(db.QueryRow(ctx, `SELECT `+jobCols+` FROM training_jobs WHERE id=$1`, id))
		if err != nil {
			ok(w, map[string]any{"id": id})
			return
		}
		ok(w, job)
	}
}

// list returns all jobs, newest first.
func (e *Extension) list(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		rows, err := db.Query(req.Context(), `SELECT `+jobCols+` FROM training_jobs ORDER BY id DESC`)
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

// get returns one job (the console polls this while a job is running).
func (e *Extension) get(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, bad := pathID(w, req)
		if bad {
			return
		}
		job, err := scanJob(db.QueryRow(req.Context(), `SELECT `+jobCols+` FROM training_jobs WHERE id=$1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, http.StatusNotFound, "job not found")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "get: "+err.Error())
			return
		}
		ok(w, job)
	}
}

// del cancels a queued/running job (the processor bails on its next poll) or
// hard-removes a terminal one (row + uploaded video).
func (e *Extension) del(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, bad := pathID(w, req)
		if bad {
			return
		}
		ctx := req.Context()
		var status string
		if err := db.QueryRow(ctx, `SELECT status FROM training_jobs WHERE id=$1`, id).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
			fail(w, http.StatusNotFound, "job not found")
			return
		} else if err != nil {
			fail(w, http.StatusInternalServerError, "delete: "+err.Error())
			return
		}
		if status == "queued" || status == "running" {
			if _, err := db.Exec(ctx, `UPDATE training_jobs SET status='canceled', updated_at=now() WHERE id=$1`, id); err != nil {
				fail(w, http.StatusInternalServerError, "cancel: "+err.Error())
				return
			}
			ok(w, map[string]any{"canceled": id})
			return
		}
		if _, err := db.Exec(ctx, `DELETE FROM training_jobs WHERE id=$1`, id); err != nil {
			fail(w, http.StatusInternalServerError, "delete: "+err.Error())
			return
		}
		_ = os.RemoveAll(filepath.Join(e.uploadDir, strconv.FormatInt(id, 10)))
		ok(w, map[string]any{"deleted": id})
	}
}

// setActive marks a done job as the active avatar (DB-side pointer; the
// console pairs this with PUT /config/avatar {current:"trained:<id>"}, which
// the resolver maps to the artifact portrait for cond_image + bake source).
// The partial unique index guarantees at most one active; this flips exactly one.
func (e *Extension) setActive(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, bad := pathID(w, req)
		if bad {
			return
		}
		ctx := req.Context()
		var status string
		if err := db.QueryRow(ctx, `SELECT status FROM training_jobs WHERE id=$1`, id).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
			fail(w, http.StatusNotFound, "job not found")
			return
		} else if err != nil {
			fail(w, http.StatusInternalServerError, "set active: "+err.Error())
			return
		}
		if status != "done" {
			fail(w, http.StatusBadRequest, "only a completed avatar can be set active")
			return
		}
		if _, err := db.Exec(ctx,
			`UPDATE training_jobs SET is_active = (id = $1), updated_at = now() WHERE is_active OR id = $1`, id); err != nil {
			fail(w, http.StatusInternalServerError, "set active: "+err.Error())
			return
		}
		ok(w, map[string]any{"active": id})
	}
}

// looksVideo is a filename-extension fallback when a part carries no Content-Type.
func looksVideo(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mov", ".webm", ".mkv", ".m4v", ".avi":
		return true
	}
	return false
}

// ResolvePortrait maps a "trained:<jobId>" avatar id to the job's artifact
// portrait (the cond_image the /train pipeline produced). Returns "" for
// non-trained ids, unfinished jobs, and missing files — the caller falls back
// to the built-in catalog / no-override behavior. Shared by the session
// manager (speaking face) and the idle-bake extension (bake source), so both
// always agree on the portrait.
func ResolvePortrait(ctx context.Context, db core.DBConn, id string) string {
	idStr, isTrained := strings.CutPrefix(strings.TrimSpace(id), "trained:")
	if !isTrained || db == nil {
		return ""
	}
	jobID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || jobID <= 0 {
		return ""
	}
	var artifact, status string
	if err := db.QueryRow(ctx,
		`SELECT artifact_path, status FROM training_jobs WHERE id=$1`, jobID).Scan(&artifact, &status); err != nil {
		return ""
	}
	if status != "done" || artifact == "" {
		return ""
	}
	p := filepath.Join(artifact, "portrait.jpg")
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return ""
	}
	return p
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
