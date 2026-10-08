package motion

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
	pollInterval = 2 * time.Second  // status poll cadence while extraction runs
	jobCeiling   = 30 * time.Minute // a single extraction may not exceed this
	maxStatusErr = 30               // consecutive worker status errors (~60s) before failing
)

// processor is the single-goroutine extraction loop. It claims one queued
// asset at a time (the worker subprocess is serial), submits it to the worker
// /extract endpoint, and polls /extract/status until terminal — same shape as
// training's processor, a second goroutine alongside it.
type processor struct {
	db          core.DBConn
	http        *http.Client
	extractHTTP string
	wake        chan struct{}
}

func newProcessor(db core.DBConn, h *http.Client, extractHTTP string) *processor {
	return &processor{db: db, http: h, extractHTTP: extractHTTP, wake: make(chan struct{}, 1)}
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
	// Crash recovery: an asset left 'extracting' by a previous process can't be
	// resumed — the worker's in-memory progress is gone and worker_job_id may
	// point at a job a restarted worker no longer knows. Fail it rather than
	// leave it stuck; re-extracting is a cheap, deliberate operator action.
	if _, err := p.db.Exec(ctx,
		`UPDATE motion_assets SET status='failed', error='interrupted by restart', updated_at=now() WHERE status='extracting'`); err != nil {
		log.Printf("motion: recover stale: %v", err)
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

// sweepTimeouts fails any 'extracting' asset whose updated_at has gone stale —
// belt-and-suspenders for a job orphaned by a crash mid-poll.
func (p *processor) sweepTimeouts(ctx context.Context) {
	if _, err := p.db.Exec(ctx,
		`UPDATE motion_assets SET status='failed', error='timed out (no progress for 30m)', updated_at=now()
		 WHERE status='extracting' AND updated_at < now() - interval '30 minutes'`); err != nil {
		log.Printf("motion: timeout sweep: %v", err)
	}
}

// processNext claims one queued asset and runs it to a terminal state. Returns
// whether an asset was claimed.
func (p *processor) processNext(ctx context.Context) bool {
	row := p.db.QueryRow(ctx, `
		UPDATE motion_assets SET status='extracting', progress=0, error='', updated_at=now()
		WHERE id = (
			SELECT id FROM motion_assets WHERE status='queued'
			ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		RETURNING `+assetCols)
	asset, err := scanAsset(row)
	if err != nil {
		return false // empty queue (pgx.ErrNoRows) or transient — just wait
	}
	p.extract(ctx, asset)
	return true
}

// extract submits the asset to the worker and polls it to completion.
func (p *processor) extract(ctx context.Context, asset *Asset) {
	wjid, err := p.submit(ctx, asset)
	if err != nil {
		p.failAsset(ctx, asset.ID, "submit: "+err.Error())
		return
	}
	if _, err := p.db.Exec(ctx,
		`UPDATE motion_assets SET worker_job_id=$2, updated_at=now() WHERE id=$1`, asset.ID, wjid); err != nil {
		log.Printf("motion: store worker id asset %d: %v", asset.ID, err)
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
			p.failAsset(ctx, asset.ID, "extraction timed out after 30m")
			return
		}
		// Operator canceled (DELETE on an extracting asset) — stop tracking; the
		// row already reflects 'canceled'.
		var cur string
		if err := p.db.QueryRow(ctx, `SELECT status FROM motion_assets WHERE id=$1`, asset.ID).Scan(&cur); err == nil && cur != "extracting" {
			return
		}

		st, err := p.status(ctx, wjid)
		if err != nil {
			errStreak++
			if errStreak >= maxStatusErr {
				p.failAsset(ctx, asset.ID, "worker unreachable: "+err.Error())
				return
			}
			sleep(ctx, pollInterval)
			continue
		}
		errStreak = 0
		_, _ = p.db.Exec(ctx,
			`UPDATE motion_assets SET progress=$2, updated_at=now() WHERE id=$1 AND status='extracting'`, asset.ID, clampPct(st.Progress))
		switch st.Status {
		case "done":
			stats := st.Stats
			if len(stats) == 0 {
				stats = json.RawMessage(`{}`)
			}
			if _, err := p.db.Exec(ctx,
				`UPDATE motion_assets SET status='done', progress=100, pkl_path=$2, stats=$3, n_frames=$4, error='', updated_at=now() WHERE id=$1 AND status='extracting'`,
				asset.ID, st.PklPath, []byte(stats), st.NFrames); err != nil {
				log.Printf("motion: finalize asset %d: %v", asset.ID, err)
			} else {
				log.Printf("motion: asset %d (%s) extracted in %dms (%d frames)", asset.ID, asset.Name, time.Since(start).Milliseconds(), st.NFrames)
			}
			return
		case "failed":
			msg := st.Error
			if msg == "" {
				msg = "extraction failed"
			}
			p.failAsset(ctx, asset.ID, msg)
			return
		}
		sleep(ctx, pollInterval)
	}
}

func (p *processor) failAsset(ctx context.Context, id int64, msg string) {
	if _, err := p.db.Exec(ctx,
		`UPDATE motion_assets SET status='failed', error=$2, updated_at=now() WHERE id=$1 AND status='extracting'`, id, msg); err != nil {
		log.Printf("motion: fail asset %d: %v", id, err)
	}
}

// submit POSTs the asset to the worker /extract endpoint and returns its job id.
func (p *processor) submit(ctx context.Context, asset *Asset) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"asset_id":   asset.ID,
		"name":       asset.Name,
		"video_path": asset.SourcePath,
	})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.extractHTTP+"/extract", bytes.NewReader(payload))
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
	Status   string          `json:"status"`
	Progress int             `json:"progress"`
	PklPath  string          `json:"pkl_path"`
	Stats    json.RawMessage `json:"stats"`
	NFrames  int             `json:"n_frames"`
	Error    string          `json:"error"`
}

func (p *processor) status(ctx context.Context, wjid string) (*workerStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.extractHTTP+"/extract/status/"+wjid, nil)
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
