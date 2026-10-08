package avatarbake

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"mynah/core"
)

const (
	pollInterval = 3 * time.Second  // status poll cadence while a bake runs
	jobCeiling   = 30 * time.Minute // a single bake may not exceed this
	maxStatusErr = 30               // consecutive worker status errors before failing
)

// processor is the single-goroutine bake loop — a third goroutine alongside
// training's and motion's, same claim-one-poll-to-terminal shape. On done it
// optionally installs the asset as the job's avatar's idle via the ApplyIdle hook.
type processor struct {
	db        core.DBConn
	http      *http.Client
	bakeHTTP  string
	applyIdle func(avatarID, assetPath string) error // nil = runtime without idle hot-swap
	wake      chan struct{}
}

func newProcessor(db core.DBConn, h *http.Client, bakeHTTP string, applyIdle func(string, string) error) *processor {
	return &processor{db: db, http: h, bakeHTTP: bakeHTTP, applyIdle: applyIdle, wake: make(chan struct{}, 1)}
}

// Notify nudges the loop; safe from any goroutine.
func (p *processor) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// applyAsset installs assetPath as avatarID's idle loop. Failures are recorded
// on the job row but do not fail the bake — the asset itself is good.
//
// avatarID matters: the job row has carried it since migration 0006, but this
// used to drop it and swap the ONE process-global loop, so baking an idle for
// one avatar silently replaced everyone else's.
func (p *processor) applyAsset(jobID int64, avatarID, assetPath string) {
	if p.applyIdle == nil {
		return
	}
	if err := p.applyIdle(avatarID, assetPath); err != nil {
		log.Printf("avatarbake: apply idle for job %d (avatar %s): %v", jobID, avatarID, err)
		_, _ = p.db.Exec(context.Background(),
			`UPDATE avatar_bake_jobs SET error=$2, updated_at=now() WHERE id=$1`,
			jobID, "baked ok; hot-apply failed: "+err.Error())
	}
}

// run drives the queue until ctx is canceled.
func (p *processor) run(ctx context.Context) {
	// Crash recovery: a job left 'baking' by a previous process can't be
	// resumed. Fail it rather than leave it stuck; re-baking is cheap to queue.
	if _, err := p.db.Exec(ctx,
		`UPDATE avatar_bake_jobs SET status='failed', error='interrupted by restart', updated_at=now() WHERE status='baking'`); err != nil {
		log.Printf("avatarbake: recover stale: %v", err)
	}
	for {
		p.sweepTimeouts(ctx)
		if p.processNext(ctx) {
			continue // drain the queue without waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-time.After(30 * time.Second): // safety-net poll
		}
	}
}

// sweepTimeouts fails any 'baking' job whose updated_at has gone stale.
func (p *processor) sweepTimeouts(ctx context.Context) {
	if _, err := p.db.Exec(ctx,
		`UPDATE avatar_bake_jobs SET status='failed', error='timed out (no progress for 30m)', updated_at=now()
		 WHERE status='baking' AND updated_at < now() - interval '30 minutes'`); err != nil {
		log.Printf("avatarbake: timeout sweep: %v", err)
	}
}

// processNext claims one queued job and runs it to a terminal state. Returns
// whether a job was claimed.
func (p *processor) processNext(ctx context.Context) bool {
	row := p.db.QueryRow(ctx, `
		UPDATE avatar_bake_jobs SET status='baking', progress=0, error='', updated_at=now()
		WHERE id = (
			SELECT id FROM avatar_bake_jobs WHERE status='queued'
			ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		RETURNING `+jobCols)
	job, err := scanJob(row)
	if err != nil {
		return false // empty queue (pgx.ErrNoRows) or transient — just wait
	}
	p.bake(ctx, job)
	return true
}

// bake submits the job to the worker /bake endpoint and polls it to completion.
func (p *processor) bake(ctx context.Context, job *Job) {
	wjid, err := p.submit(ctx, job)
	if err != nil {
		p.failJob(ctx, job.ID, "submit: "+err.Error())
		return
	}
	if _, err := p.db.Exec(ctx,
		`UPDATE avatar_bake_jobs SET worker_job_id=$2, updated_at=now() WHERE id=$1`, job.ID, wjid); err != nil {
		log.Printf("avatarbake: store worker id job %d: %v", job.ID, err)
	}

	start := time.Now()
	errStreak := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if time.Since(start) > jobCeiling {
			p.failJob(ctx, job.ID, "bake timed out after 30m")
			return
		}
		// Operator canceled (DELETE on a baking job) — stop tracking.
		var cur string
		if err := p.db.QueryRow(ctx, `SELECT status FROM avatar_bake_jobs WHERE id=$1`, job.ID).Scan(&cur); err == nil && cur != "baking" {
			return
		}

		st, err := p.status(ctx, wjid)
		if err != nil {
			errStreak++
			if errStreak >= maxStatusErr {
				p.failJob(ctx, job.ID, "worker unreachable: "+err.Error())
				return
			}
			sleep(ctx, pollInterval)
			continue
		}
		errStreak = 0
		_, _ = p.db.Exec(ctx,
			`UPDATE avatar_bake_jobs SET progress=$2, updated_at=now() WHERE id=$1 AND status='baking'`, job.ID, clampPct(st.Progress))
		switch st.Status {
		case "done":
			if _, err := p.db.Exec(ctx,
				`UPDATE avatar_bake_jobs SET status='done', progress=100, asset_path=$2, error='', updated_at=now() WHERE id=$1 AND status='baking'`,
				job.ID, st.AssetPath); err != nil {
				log.Printf("avatarbake: finalize job %d: %v", job.ID, err)
				return
			}
			log.Printf("avatarbake: job %d (%s + %s) done in %dms", job.ID, job.AvatarID, job.MotionRef, time.Since(start).Milliseconds())
			if job.Apply && st.AssetPath != "" {
				p.applyAsset(job.ID, job.AvatarID, st.AssetPath)
			}
			return
		case "failed":
			msg := st.Error
			if msg == "" {
				msg = "bake failed"
			}
			p.failJob(ctx, job.ID, msg)
			return
		}
		sleep(ctx, pollInterval)
	}
}

func (p *processor) failJob(ctx context.Context, id int64, msg string) {
	if _, err := p.db.Exec(ctx,
		`UPDATE avatar_bake_jobs SET status='failed', error=$2, updated_at=now() WHERE id=$1 AND status='baking'`, id, msg); err != nil {
		log.Printf("avatarbake: fail job %d: %v", id, err)
	}
}

// submit POSTs the job to the worker /bake endpoint and returns its job id.
func (p *processor) submit(ctx context.Context, job *Job) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"avatar_id":    job.AvatarID,
		"name":         job.AvatarID,
		"source_image": job.SourceImage,
		"motion_pkl":   job.MotionPkl,
		"motion_ref":   job.MotionRef,
		"half_body":    job.HalfBody,
		"blend":        job.Blend,
	})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.bakeHTTP+"/bake", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", errFromBody(resp.StatusCode, body)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.JobID == "" {
		return "", errString("worker returned no job_id")
	}
	return out.JobID, nil
}

type workerStatus struct {
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	AssetPath string `json:"asset_path"`
	Error     string `json:"error"`
}

func (p *processor) status(ctx context.Context, wjid string) (*workerStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.bakeHTTP+"/bake/status/"+wjid, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, errFromBody(resp.StatusCode, body)
	}
	var st workerStatus
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// sleep waits d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func clampPct(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

type errString string

func (e errString) Error() string { return string(e) }

func errFromBody(status int, body []byte) error {
	s := string(bytes.TrimSpace(body))
	if len(s) > 200 {
		s = s[:200]
	}
	if s == "" {
		return errString("worker HTTP " + strconv.Itoa(status))
	}
	return errString(s)
}

// ─── response helpers (mirror the control plane's {code,data}/{code,msg}) ───

func ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"code": status, "msg": msg})
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		fail(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
