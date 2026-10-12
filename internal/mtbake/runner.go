package mtbake

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mynah/core"
	"mynah/internal/supervisor"
)

// ContainerRunner is the one-shot container capability the runner needs. Narrow
// on purpose: the runner must not be able to stop the engine pool.
type ContainerRunner interface {
	RunOnce(ctx context.Context, spec supervisor.RunSpec, onLine func(string)) (int, error)
}

// Config is the deployment-specific material a bake needs. All paths are HOST
// paths and are bind-mounted into the bake container at the same location, so
// what the runner writes and what the container reads are the same string.
type Config struct {
	// Image is the bake image tag (built from worker-musetalk-bake.Dockerfile).
	Image string
	// WorkDir holds uploads and per-job scratch. Must be writable by cored.
	WorkDir string
	// BakesDir is the avatar catalog directory — where a finished bake lands.
	// cored must have it mounted READ-WRITE (it does the final rename).
	BakesDir string
	// RepoDir / ModelsDir are the upstream MuseTalk checkout and its weights.
	RepoDir   string
	ModelsDir string
	// WorkersDir is the repo's workers/ directory on the host — the bake script
	// lives there and is bind-mounted in rather than baked into the image, so a
	// script fix does not need an image rebuild.
	WorkersDir string
	// TorchCache is the host torch hub cache holding FAN's weights. Optional:
	// without it the first bake downloads ~180MB.
	TorchCache string
	// GPU is the CDI device index the bake runs on.
	GPU int
}

// Ready reports whether enough is configured to run a bake at all.
func (c Config) Ready() error {
	for _, f := range []struct{ name, val string }{
		{"--avatar-bake-work-dir", c.WorkDir},
		{"--avatar-bakes-dir", c.BakesDir},
		{"--musetalk-repo", c.RepoDir},
		{"--musetalk-models-dir", c.ModelsDir},
		{"--pool-workers-dir", c.WorkersDir},
	} {
		if strings.TrimSpace(f.val) == "" {
			return fmt.Errorf("形象烘焙未配置：缺少 %s", f.name)
		}
	}
	return nil
}

// Runner drives the bake queue. One job at a time: baking is GPU work that
// competes with the live engine pool for VRAM on the same card.
type Runner struct {
	db   core.DBConn
	run  ContainerRunner
	cfg  Config
	// rescan republishes the avatar catalog after a bake lands, so the new
	// appearance is bindable without waiting for the console's next list.
	rescan func()
	wake   chan struct{}
}

func NewRunner(db core.DBConn, run ContainerRunner, cfg Config, rescan func()) *Runner {
	return &Runner{db: db, run: run, cfg: cfg, rescan: rescan, wake: make(chan struct{}, 1)}
}

// Config exposes the runner's deployment material (handlers need WorkDir and
// the readiness check before accepting an upload).
func (r *Runner) Config() Config { return r.cfg }

// Notify nudges the loop after a job is queued.
func (r *Runner) Notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Start runs the queue until ctx is canceled.
func (r *Runner) Start(ctx context.Context) {
	// A job left 'running' by a previous process has no container to reattach
	// to — its output stream died with the process. Fail it so the console
	// shows something actionable instead of a bar frozen at 40%.
	if _, err := r.db.Exec(ctx,
		`UPDATE avatar_mt_bake_jobs SET status='failed', error='cored 重启中断了烘焙',
		 updated_at=now() WHERE status='running'`); err != nil {
		log.Printf("mtbake: recover stale: %v", err)
	}
	go func() {
		for {
			if r.processNext(ctx) {
				continue // drain
			}
			select {
			case <-ctx.Done():
				return
			case <-r.wake:
			case <-time.After(30 * time.Second):
			}
		}
	}()
}

// processNext claims one queued job and runs it to a terminal state.
func (r *Runner) processNext(ctx context.Context) bool {
	job, err := scanJob(r.db.QueryRow(ctx, `
		UPDATE avatar_mt_bake_jobs SET status='running', progress=0, error='', updated_at=now()
		WHERE id = (
			SELECT id FROM avatar_mt_bake_jobs WHERE status='queued'
			ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		RETURNING `+jobCols))
	if err != nil {
		return false // empty queue, or transient — the caller waits
	}
	r.bake(ctx, job)
	return true
}

// progressRE matches the bake script's progress protocol.
var progressRE = regexp.MustCompile(`^PROGRESS (\d{1,3})$`)

// bboxRangeRE pulls the per-clip usable bbox_shift window out of the line the
// bake script prints after the landmark stage:
//
//	bbox_shift adjust range: [-12~17], current: 0
//
// It is derived from THIS clip's landmark spacing, so it cannot be a constant
// in the console — and it is the one number needed to re-bake with a better
// mouth amplitude.
var bboxRangeRE = regexp.MustCompile(`bbox_shift adjust range: \[(-?\d+~-?\d+)\]`)

// logTailBytes bounds what is kept from container output. Enough for a Python
// traceback, small enough that a chatty job cannot bloat a row.
const logTailBytes = 8000

// bake drives one job: frames, then the avatar, then publish.
func (r *Runner) bake(ctx context.Context, job *Job) {
	work := filepath.Join(r.cfg.WorkDir, fmt.Sprintf("job-%d", job.ID))
	framesDir := filepath.Join(work, "frames")
	// Stage the output INSIDE the catalog dir so the final publish is a rename
	// on one filesystem — atomic, and impossible to leave half-copied. The
	// leading dot keeps it out of the catalog scan.
	stage := filepath.Join(r.cfg.BakesDir, fmt.Sprintf(".staging-%d", job.ID))

	defer func() {
		// Frames for a 20s clip are ~1GB; the video is tens of MB. Neither is
		// worth keeping once the avatar directory exists.
		_ = os.RemoveAll(work)
		_ = os.RemoveAll(stage)
		if job.SourceVideo != "" {
			_ = os.Remove(job.SourceVideo)
		}
	}()

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		r.fail(ctx, job.ID, "创建工作目录失败: "+err.Error())
		return
	}

	var tail strings.Builder
	record := func(line string) {
		if tail.Len() < logTailBytes {
			tail.WriteString(line)
			tail.WriteByte('\n')
		}
	}

	// ── frames ────────────────────────────────────────────────────────────
	// -vf fps=25: MuseTalk plays at 25fps, and source material is routinely 24
	// (Kling) or 30 — resampling here means the avatar never runs slow or fast.
	// -t caps duration; see MaxSeconds for why that is a hard requirement.
	ffmpeg := supervisor.RunSpec{
		Name:       supervisor.OneshotName(fmt.Sprintf("mtframes-%d", job.ID)),
		Image:      r.cfg.Image,
		Entrypoint: []string{"ffmpeg"},
		Cmd: []string{"-y", "-hide_banner", "-loglevel", "error",
			"-i", job.SourceVideo, "-t", strconv.Itoa(job.Seconds),
			"-vf", "fps=25", "-start_number", "0",
			filepath.Join(framesDir, "%08d.png")},
		Binds:      r.videoBinds(),
		WorkingDir: work,
		GPUIndex:   -1, // CPU decode: no reason to hold the GPU for this
	}
	code, err := r.run.RunOnce(ctx, ffmpeg, record)
	if err != nil || code != 0 {
		r.fail(ctx, job.ID, "抽帧失败: "+describe(err, code, tail.String()))
		return
	}
	n, _ := filepath.Glob(filepath.Join(framesDir, "*.png"))
	if len(n) == 0 {
		r.fail(ctx, job.ID, "抽帧后没有任何画面——视频可能损坏或没有视频轨")
		return
	}
	r.setProgress(ctx, job.ID, 5)

	// ── bake ──────────────────────────────────────────────────────────────
	cmd := []string{"/app/workers/avatar/bake_musetalk_avatar.py",
		"--musetalk-repo", r.cfg.RepoDir,
		"--models-dir", r.cfg.ModelsDir,
		"--frames", framesDir,
		"--out", stage,
		"--bbox-shift", strconv.Itoa(job.BBoxShift),
		"--extra-margin", strconv.Itoa(job.ExtraMargin)}
	if !job.Mirror {
		cmd = append(cmd, "--no-mirror")
	}
	spec := supervisor.RunSpec{
		Name:  supervisor.OneshotName(fmt.Sprintf("mtbake-%d", job.ID)),
		Image: r.cfg.Image,
		Cmd:   cmd,
		Env: []string{
			"CUDA_VISIBLE_DEVICES=0", // CDI maps the requested card to index 0
			// tqdm redraws several times a second; the PROGRESS lines carry the
			// same information in a form the runner can actually parse.
			"TQDM_DISABLE=1",
			"PYTHONUNBUFFERED=1",
		},
		Binds: r.bakeBinds(),
		// FaceParsing resolves its weights through paths relative to the repo
		// root. Upstream never made them configurable, so cwd is load-bearing.
		WorkingDir: r.cfg.RepoDir,
		GPUIndex:   r.cfg.GPU,
	}

	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	last := 0
	code, err = r.run.RunOnce(bctx, spec, func(line string) {
		record(line)
		if m := bboxRangeRE.FindStringSubmatch(line); m != nil {
			// Stored as it arrives rather than at completion: this lands around
			// 45%, and knowing the window mid-bake is what tells an operator
			// whether the current shift was even in range.
			r.setBBoxRange(ctx, job.ID, m[1])
			return
		}
		m := progressRE.FindStringSubmatch(line)
		if m == nil {
			return
		}
		p, _ := strconv.Atoi(m[1])
		if p <= last {
			return
		}
		last = p
		// The same write that reports progress detects cancellation: 0 rows
		// updated means the row is no longer 'running', i.e. someone canceled.
		if !r.setProgress(ctx, job.ID, p) {
			cancel() // kills the container via RunOnce's cleanup
		}
	})
	if bctx.Err() != nil && ctx.Err() == nil {
		return // canceled by the operator; status already terminal
	}
	if err != nil || code != 0 {
		r.fail(ctx, job.ID, "烘焙失败: "+describe(err, code, tail.String()))
		return
	}

	// ── publish ───────────────────────────────────────────────────────────
	frames, err := publish(stage, filepath.Join(r.cfg.BakesDir, job.Name))
	if err != nil {
		r.fail(ctx, job.ID, "发布形象失败: "+err.Error())
		return
	}
	if _, err := r.db.Exec(ctx,
		`UPDATE avatar_mt_bake_jobs SET status='done', progress=100, frames=$2,
		 log=$3, error='', updated_at=now() WHERE id=$1 AND status='running'`,
		job.ID, frames, tailOf(tail.String())); err != nil {
		log.Printf("mtbake: finalize job %d: %v", job.ID, err)
	}
	if r.rescan != nil {
		r.rescan()
	}
	log.Printf("mtbake: job %d baked %q (%d frames)", job.ID, job.Name, frames)
}

// publish replaces destDir with stage atomically-ish and reports the frame
// count. Rename cannot overwrite a non-empty directory, so an existing avatar
// is moved aside first and only deleted once the new one is in place — a failed
// rename therefore leaves the OLD avatar intact rather than nothing at all.
func publish(stage, dest string) (int, error) {
	imgs, err := filepath.Glob(filepath.Join(stage, "full_imgs", "*.png"))
	if err != nil || len(imgs) == 0 {
		return 0, fmt.Errorf("烘焙产物为空（%s）", stage)
	}
	backup := dest + ".replaced"
	_ = os.RemoveAll(backup)
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, backup); err != nil {
			return 0, err
		}
	}
	if err := os.Rename(stage, dest); err != nil {
		_ = os.Rename(backup, dest) // put the old one back
		return 0, err
	}
	_ = os.RemoveAll(backup)
	return len(imgs), nil
}

// videoBinds mounts what the frame-extraction step touches. One bind for the
// whole work dir: the upload and the scratch dir both live under it, and two
// overlapping binds would be a needless way to get the paths subtly wrong.
func (r *Runner) videoBinds() []string {
	return []string{r.cfg.WorkDir + ":" + r.cfg.WorkDir}
}

// bakeBinds mounts what the bake step touches. The catalog dir is read-write
// because the bake writes its staging directory inside it.
func (r *Runner) bakeBinds() []string {
	b := []string{
		r.cfg.WorkersDir + ":/app/workers:ro",
		r.cfg.RepoDir + ":" + r.cfg.RepoDir + ":ro",
		// ModelsDir is a symlink in the reference deployment; bind-mounting it
		// explicitly is what makes it resolve inside the container, where the
		// symlink target does not exist.
		r.cfg.ModelsDir + ":" + r.cfg.ModelsDir + ":ro",
		r.cfg.BakesDir + ":" + r.cfg.BakesDir,
		r.cfg.WorkDir + ":" + r.cfg.WorkDir,
	}
	if r.cfg.TorchCache != "" {
		b = append(b, r.cfg.TorchCache+":/root/.cache/torch:ro")
	}
	return b
}

// setProgress records progress and reports whether the job is still running.
func (r *Runner) setProgress(ctx context.Context, id int64, pct int) bool {
	tag, err := r.db.Exec(ctx,
		`UPDATE avatar_mt_bake_jobs SET progress=$2, updated_at=now()
		 WHERE id=$1 AND status='running'`, id, pct)
	if err != nil {
		return true // a transient DB error is not a cancellation
	}
	return tag.RowsAffected() > 0
}

// setBBoxRange records the usable shift window for this clip. Not gated on
// status: the window stays useful on a job that later failed or was canceled,
// because the next attempt needs it.
func (r *Runner) setBBoxRange(ctx context.Context, id int64, rng string) {
	if _, err := r.db.Exec(ctx,
		`UPDATE avatar_mt_bake_jobs SET bbox_range=$2, updated_at=now() WHERE id=$1`,
		id, rng); err != nil {
		log.Printf("mtbake: store bbox range job %d: %v", id, err)
	}
}

func (r *Runner) fail(ctx context.Context, id int64, msg string) {
	if _, err := r.db.Exec(ctx,
		`UPDATE avatar_mt_bake_jobs SET status='failed', error=$2, updated_at=now()
		 WHERE id=$1 AND status='running'`, id, msg); err != nil {
		log.Printf("mtbake: fail job %d: %v", id, err)
	}
}

// describe turns a run failure into one operator-readable line: the transport
// error if there was one, otherwise the exit code plus the tail of the output
// (where a Python traceback's actual message lives).
func describe(err error, code int, out string) string {
	if err != nil {
		return err.Error()
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if n := len(lines); n > 0 && lines[n-1] != "" {
		return fmt.Sprintf("退出码 %d：%s", code, lines[n-1])
	}
	return fmt.Sprintf("退出码 %d", code)
}

func tailOf(s string) string {
	if len(s) <= logTailBytes {
		return s
	}
	return s[len(s)-logTailBytes:]
}
