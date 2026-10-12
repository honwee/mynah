// Package motion is the EE admin extension for the driving-motion-skeleton
// library (动作骨架, 控制台三期续). It manages LivePortrait motion-template
// extraction: upload a driving video, queue an extraction job, poll progress,
// get a reusable .pkl whose head/lip/blink trajectory transfers onto any
// portrait. Unlike training, a skeleton carries no likeness — it's a shared
// library asset referenced many-to-many by avatars and training jobs, so there
// is no single-active pointer.
//
//	POST   /api/v1/motions        multipart (video,name,consent) -> queue extraction
//	GET    /api/v1/motions        list skeletons
//	GET    /api/v1/motions/{id}   one skeleton (poll status/progress/stats)
//	DELETE /api/v1/motions/{id}   cancel (queued/extracting) or remove (terminal)
//
// Mirrors internal/training in shape (stateful: needs DB + a processor
// goroutine). The extraction itself is REAL — the worker /extract endpoint runs
// the LivePortrait pipeline in a subprocess.
// With no --db it mounts nothing.
package motion

import (
	"encoding/json"
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
	maxVideoBytes = 200 << 20 // 200MB source driving video cap
)

// Asset is one motion-skeleton row. Disk path, worker id, and consent are
// internal — they never leave the server. Stats is opaque worker-produced JSON
// (idle-suitability metrics), passed through verbatim.
type Asset struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	SourcePath  string          `json:"-"`
	PklPath     string          `json:"pkl_path"`
	Stats       json.RawMessage `json:"stats"`
	NFrames     int             `json:"n_frames"`
	Progress    int             `json:"progress"`
	Error       string          `json:"error"`
	WorkerJobID string          `json:"-"`
	Consent     string          `json:"-"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

const assetCols = `id, name, status, source_path, pkl_path, stats, n_frames, progress, error, worker_job_id, consent, created_at, updated_at`

type scannable interface{ Scan(dest ...any) error }

func scanAsset(row scannable) (*Asset, error) {
	var a Asset
	if err := row.Scan(&a.ID, &a.Name, &a.Status, &a.SourcePath, &a.PklPath,
		&a.Stats, &a.NFrames, &a.Progress, &a.Error, &a.WorkerJobID, &a.Consent,
		&a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// Extension implements core.AdminExtension.
type Extension struct {
	http        *http.Client
	extractHTTP string // avatar-train worker base (shares the worker), e.g. http://127.0.0.1:9405
	uploadDir   string // where source driving videos land (data disk; system disk is full)
	proc        *processor
}

// New builds the motion-skeleton admin extension. The DB arrives later in
// Register (it isn't open at cmd/ assembly time).
func New() *Extension {
	tb := os.Getenv("PL_MOTION_EXTRACT_HTTP")
	if tb == "" {
		tb = "http://127.0.0.1:9405"
	}
	dir := os.Getenv("PL_MOTION_UPLOAD_DIR")
	if dir == "" {
		// Under the working dir — see internal/training for why not an absolute
		// path. The extraction worker must see this same path.
		dir = filepath.Join(".data", "motion_uploads")
	}
	return &Extension{
		http:        &http.Client{},
		extractHTTP: strings.TrimRight(tb, "/"),
		uploadDir:   dir,
	}
}

// Register mounts the routes and starts the extraction processor. Motion needs
// persistence, so with no DB it mounts nothing (the console's feature probe
// then degrades gracefully instead of hitting 500s).
func (e *Extension) Register(r core.RouteRegistrar, deps core.AdminDeps) {
	if deps.DB == nil {
		log.Printf("motion: no DB (started without --db); motion-skeleton routes disabled")
		return
	}
	e.proc = newProcessor(deps.DB, e.http, e.extractHTTP)
	go e.proc.run(deps.Ctx)
	log.Printf("motion: worker=%s uploads=%s", e.extractHTTP, e.uploadDir)
	r.Private("POST /api/v1/motions", e.create(deps.DB))
	r.Private("GET /api/v1/motions", e.list(deps.DB))
	r.Private("GET /api/v1/motions/{id}", e.get(deps.DB))
	r.Private("DELETE /api/v1/motions/{id}", e.del(deps.DB))
}

// create accepts a multipart upload (video + name + consent) and queues an
// extraction. The video is streamed straight to a temp file on the data disk
// (never spooled to the full system disk, never fully buffered in RAM), then
// renamed under the asset's id once the INSERT assigns one.
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
			fail(w, http.StatusBadRequest, "consent is required: confirm you are authorized to use this driving video")
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

		ctx := req.Context()
		var id int64
		if err := db.QueryRow(ctx,
			`INSERT INTO motion_assets (name, consent, status) VALUES ($1, $2, 'queued') RETURNING id`,
			name, consent).Scan(&id); err != nil {
			fail(w, http.StatusInternalServerError, "create asset: "+err.Error())
			return
		}
		dir := filepath.Join(e.uploadDir, strconv.FormatInt(id, 10))
		dest := filepath.Join(dir, "source.mp4")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			_, _ = db.Exec(ctx, `UPDATE motion_assets SET status='failed', error=$2, updated_at=now() WHERE id=$1`, id, "store video: "+err.Error())
			fail(w, http.StatusInternalServerError, "store video: "+err.Error())
			return
		}
		if err := os.Rename(tmpName, dest); err != nil {
			_, _ = db.Exec(ctx, `UPDATE motion_assets SET status='failed', error=$2, updated_at=now() WHERE id=$1`, id, "store video: "+err.Error())
			fail(w, http.StatusInternalServerError, "store video: "+err.Error())
			return
		}
		cleanupTmp = false // renamed away; don't delete
		if _, err := db.Exec(ctx,
			`UPDATE motion_assets SET source_path=$2, updated_at=now() WHERE id=$1`, id, dest); err != nil {
			fail(w, http.StatusInternalServerError, "create asset: "+err.Error())
			return
		}
		e.proc.Notify()

		asset, err := scanAsset(db.QueryRow(ctx, `SELECT `+assetCols+` FROM motion_assets WHERE id=$1`, id))
		if err != nil {
			ok(w, map[string]any{"id": id})
			return
		}
		ok(w, asset)
	}
}

// list returns all skeletons, newest first.
func (e *Extension) list(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		rows, err := db.Query(req.Context(), `SELECT `+assetCols+` FROM motion_assets ORDER BY id DESC`)
		if err != nil {
			fail(w, http.StatusInternalServerError, "list: "+err.Error())
			return
		}
		defer rows.Close()
		assets := []*Asset{}
		for rows.Next() {
			a, err := scanAsset(rows)
			if err != nil {
				fail(w, http.StatusInternalServerError, "scan: "+err.Error())
				return
			}
			assets = append(assets, a)
		}
		if err := rows.Err(); err != nil {
			fail(w, http.StatusInternalServerError, "list: "+err.Error())
			return
		}
		ok(w, map[string]any{"assets": assets})
	}
}

// get returns one skeleton (the console polls this while extraction runs).
func (e *Extension) get(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, bad := pathID(w, req)
		if bad {
			return
		}
		asset, err := scanAsset(db.QueryRow(req.Context(), `SELECT `+assetCols+` FROM motion_assets WHERE id=$1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, http.StatusNotFound, "skeleton not found")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "get: "+err.Error())
			return
		}
		ok(w, asset)
	}
}

// del cancels a queued/extracting job (the processor bails on its next poll) or
// hard-removes a terminal one (row + uploaded video + produced pkl dir).
func (e *Extension) del(db core.DBConn) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, bad := pathID(w, req)
		if bad {
			return
		}
		ctx := req.Context()
		var status string
		if err := db.QueryRow(ctx, `SELECT status FROM motion_assets WHERE id=$1`, id).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
			fail(w, http.StatusNotFound, "skeleton not found")
			return
		} else if err != nil {
			fail(w, http.StatusInternalServerError, "delete: "+err.Error())
			return
		}
		if status == "queued" || status == "extracting" {
			if _, err := db.Exec(ctx, `UPDATE motion_assets SET status='canceled', updated_at=now() WHERE id=$1`, id); err != nil {
				fail(w, http.StatusInternalServerError, "cancel: "+err.Error())
				return
			}
			ok(w, map[string]any{"canceled": id})
			return
		}
		if _, err := db.Exec(ctx, `DELETE FROM motion_assets WHERE id=$1`, id); err != nil {
			fail(w, http.StatusInternalServerError, "delete: "+err.Error())
			return
		}
		_ = os.RemoveAll(filepath.Join(e.uploadDir, strconv.FormatInt(id, 10)))
		ok(w, map[string]any{"deleted": id})
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

// pathID parses the {id} path value as a positive int64.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, "invalid id")
		return 0, true
	}
	return id, false
}
