package mtbake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mynah/core"
)

// maxUploadBytes bounds one uploaded video. Only the first `seconds` are baked,
// so this exists to stop a mis-drag of a 4GB master file from filling the disk,
// not to constrain legitimate input — a 20s clip at any sane bitrate is far
// below it.
const maxUploadBytes = 512 << 20

// Handlers exposes the bake API. Mounted by the control plane, which owns
// authentication; nothing here does its own auth.
type Handlers struct {
	db     core.DBConn
	runner *Runner
}

func NewHandlers(db core.DBConn, r *Runner) *Handlers { return &Handlers{db: db, runner: r} }

// Register mounts the routes on a registrar that has already applied auth.
//
//	POST   /api/v1/avatars/bake-mt       multipart: name + video -> queued job
//	GET    /api/v1/avatars/bake-mt       list jobs
//	GET    /api/v1/avatars/bake-mt/{id}  poll one
//	DELETE /api/v1/avatars/bake-mt/{id}  cancel (running) or remove (terminal)
func (h *Handlers) Register(register func(pattern string, fn http.HandlerFunc)) {
	register("POST /api/v1/avatars/bake-mt", h.create)
	register("GET /api/v1/avatars/bake-mt", h.list)
	register("GET /api/v1/avatars/bake-mt/{id}", h.get)
	register("DELETE /api/v1/avatars/bake-mt/{id}", h.remove)
}

func (h *Handlers) create(w http.ResponseWriter, r *http.Request) {
	cfg := h.runner.Config()
	if err := cfg.Ready(); err != nil {
		fail(w, http.StatusNotImplemented, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	// 32MB in memory, the rest spooled to disk by net/http.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(w, http.StatusBadRequest, "读取上传失败（超过 512MB？）: "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()

	name := strings.TrimSpace(strings.ToLower(r.FormValue("name")))
	if err := ValidateName(name); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}

	file, hdr, err := r.FormFile("video")
	if err != nil {
		fail(w, http.StatusBadRequest, "缺少视频文件（字段名 video）")
		return
	}
	defer file.Close()

	uploads := filepath.Join(cfg.WorkDir, "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		fail(w, http.StatusInternalServerError, "创建上传目录失败: "+err.Error())
		return
	}
	// The stored name is derived from the validated avatar name plus the
	// original extension — never from the client-supplied filename, which is
	// attacker-controlled and would be a path-traversal vector.
	dst := filepath.Join(uploads, name+videoExt(hdr.Filename))
	out, err := os.Create(dst)
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存视频失败: "+err.Error())
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		_ = os.Remove(dst)
		fail(w, http.StatusBadRequest, "保存视频失败: "+err.Error())
		return
	}
	out.Close()

	job := &Job{
		Name:        name,
		SourceVideo: dst,
		BBoxShift:   atoiDefault(r.FormValue("bbox_shift"), 0),
		ExtraMargin: atoiDefault(r.FormValue("extra_margin"), 10),
		Mirror:      r.FormValue("mirror") != "false",
		Seconds:     atoiDefault(r.FormValue("seconds"), DefaultSeconds),
	}
	created, err := Create(r.Context(), h.db, job)
	if err != nil {
		_ = os.Remove(dst)
		fail(w, http.StatusInternalServerError, "创建烘焙任务失败: "+err.Error())
		return
	}
	h.runner.Notify()

	resp := map[string]any{"job": created}
	// An operator re-baking an existing avatar (a different bbox_shift, better
	// source footage) is a normal workflow, so this is a warning rather than a
	// rejection — but it must be visible, because the replacement is silent
	// from the console's point of view.
	if _, err := os.Stat(filepath.Join(cfg.BakesDir, name)); err == nil {
		resp["warning"] = fmt.Sprintf("形象 %q 已存在，烘焙成功后会被替换。"+
			"已加载该形象的 worker 需要重新预热才会看到新素材。", name)
	}
	ok(w, resp)
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	jobs, err := List(r.Context(), h.db, atoiDefault(r.URL.Query().Get("limit"), 50))
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, jobs)
}

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad id")
		return
	}
	job, err := Get(r.Context(), h.db, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if job == nil {
		fail(w, http.StatusNotFound, "任务不存在")
		return
	}
	ok(w, job)
}

func (h *Handlers) remove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad id")
		return
	}
	job, err := Get(r.Context(), h.db, id)
	if err != nil || job == nil {
		fail(w, http.StatusNotFound, "任务不存在")
		return
	}
	// Cancel first, delete second: a running job's container is killed by the
	// runner when it notices the status change, so removing the row outright
	// would leave the container running with nothing tracking it.
	if job.Status == "queued" || job.Status == "running" {
		if err := Cancel(r.Context(), h.db, id); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		ok(w, map[string]any{"canceled": id})
		return
	}
	if err := Delete(r.Context(), h.db, id); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, map[string]any{"deleted": id})
}

// videoExt keeps a recognized container extension so ffmpeg's demuxer probing
// has the usual hint. Anything unexpected becomes .mp4 — ffmpeg probes content
// regardless, and the extension never reaches a shell.
func videoExt(filename string) string {
	switch ext := strings.ToLower(filepath.Ext(filename)); ext {
	case ".mp4", ".mov", ".mkv", ".webm", ".avi":
		return ext
	default:
		return ".mp4"
	}
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

func ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "msg": msg})
}
