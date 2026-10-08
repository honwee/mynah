package training

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
	pollInterval = 2 * time.Second  // status poll cadence while a job runs
	jobCeiling   = 30 * time.Minute // a single job may not exceed this
	maxStatusErr = 30               // consecutive worker status errors (~60s) before failing
)

// processor is the single-goroutine job loop. It claims one queued job at a
// time (the GPU is serial), submits it to the worker /train endpoint, and polls
// /train/status until terminal — mirroring knowledge.Ingester's "cored owns the
// loop" shape, but with an async worker so processNext blocks in a poll.
type processor struct {
	db        core.DBConn
	http      *http.Client
	trainHTTP string
	wake      chan struct{}
}

func newProcessor(db core.DBConn, h *http.Client, trainHTTP string) *processor {
	return &processor{db: db, http: h, trainHTTP: trainHTTP, wake: make(chan struct{}, 1)}
}

// Notify nudges the loop; safe from any goroutine.
func (p *processor) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// run drives the queue until ctx is canceled.
func (p *processor) run(ctx context.Context) {
	// Crash recovery: a job left 'running' by a previous process can't be
	// resumed — the worker's in-memory progress is gone and worker_job_id may
	// point at a job a restarted worker no longer knows. Fail it rather than
	// silently re-burn a GPU-hour; re-training is a deliberate operator action.
	if _, err := p.db.Exec(ctx,
		`UPDATE training_jobs SET status='failed', error='interrupted by restart', updated_at=now() WHERE status='running'`); err != nil {
		log.Printf("training: recover stale: %v", err)
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

// sweepTimeouts fails any 'running' job whose updated_at has gone stale —
// belt-and-suspenders for a job orphaned by a crash mid-poll (the in-loop
// ceiling handles the actively-tracked job).
func (p *processor) sweepTimeouts(ctx context.Context) {
	if _, err := p.db.Exec(ctx,
		`UPDATE training_jobs SET status='failed', error='timed out (no progress for 30m)', updated_at=now()
		 WHERE status='running' AND updated_at < now() - interval '30 minutes'`); err != nil {
		log.Printf("training: timeout sweep: %v", err)
	}
}

// processNext claims one queued job and runs it to a terminal state. Returns
// whether a job was claimed.
func (p *processor) processNext(ctx context.Context) bool {
	row := p.db.QueryRow(ctx, `
		UPDATE training_jobs SET status='running', progress=0, error='', updated_at=now()
		WHERE id = (
			SELECT id FROM training_jobs WHERE status='queued'
			ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		RETURNING `+jobCols)
	job, err := scanJob(row)
	if err != nil {
		return false // empty queue (pgx.ErrNoRows) or transient — just wait
	}
	p.train(ctx, job)
	return true
}

// train submits the job to the worker and polls it to completion.
func (p *processor) train(ctx context.Context, job *Job) {
	wjid, err := p.submit(ctx, job)
	if err != nil {
		p.failJob(ctx, job.ID, "submit: "+err.Error())
		return
	}
	if _, err := p.db.Exec(ctx,
		`UPDATE training_jobs SET worker_job_id=$2, updated_at=now() WHERE id=$1`, job.ID, wjid); err != nil {
		log.Printf("training: store worker id job %d: %v", job.ID, err)
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
			p.failJob(ctx, job.ID, "training timed out after 30m")
			return
		}
		// Operator canceled (DELETE on a running job) — stop tracking; the row
		// already reflects 'canceled'.
		var cur string
		if err := p.db.QueryRow(ctx, `SELECT status FROM training_jobs WHERE id=$1`, job.ID).Scan(&cur); err == nil && cur != "running" {
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
			`UPDATE training_jobs SET progress=$2, updated_at=now() WHERE id=$1 AND status='running'`, job.ID, clampPct(st.Progress))
		switch st.Status {
		case "done":
			if _, err := p.db.Exec(ctx,
				`UPDATE training_jobs SET status='done', progress=100, artifact_path=$2, error='', updated_at=now() WHERE id=$1 AND status='running'`,
				job.ID, st.ArtifactPath); err != nil {
				log.Printf("training: finalize job %d: %v", job.ID, err)
			} else {
				log.Printf("training: job %d (%s) done in %dms", job.ID, job.Name, time.Since(start).Milliseconds())
			}
			return
		case "failed":
			msg := st.Error
			if msg == "" {
				msg = "training failed"
			}
			p.failJob(ctx, job.ID, msg)
			return
		}
		sleep(ctx, pollInterval)
	}
}

func (p *processor) failJob(ctx context.Context, id int64, msg string) {
	if _, err := p.db.Exec(ctx,
		`UPDATE training_jobs SET status='failed', error=$2, updated_at=now() WHERE id=$1 AND status='running'`, id, msg); err != nil {
		log.Printf("training: fail job %d: %v", id, err)
	}
}

// submit POSTs the job to the worker /train endpoint and returns its job id.
func (p *processor) submit(ctx context.Context, job *Job) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"job_id":     job.ID,
		"name":       job.Name,
		"video_path": job.SourcePath,
		"motion_ref": job.MotionRef, // which idle skeleton the follow-up bake should use
	})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.trainHTTP+"/train", bytes.NewReader(payload))
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
	Status       string `json:"status"`
	Progress     int    `json:"progress"`
	ArtifactPath string `json:"artifact_path"`
	Error        string `json:"error"`
}

func (p *processor) status(ctx context.Context, wjid string) (*workerStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.trainHTTP+"/train/status/"+wjid, nil)
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
