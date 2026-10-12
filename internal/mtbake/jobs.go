// Package mtbake turns an uploaded talking-head video into a MuseTalk avatar
// directory, without anyone touching the host.
//
// Before this, baking was a documented ssh procedure: scp a video up, run
// ffmpeg, run bake_musetalk_avatar.py inside the right conda env from the right
// cwd, then wait for cored to rescan. Every one of those steps is a place to
// get it wrong, and none of them are visible to an operator using the console.
//
// The pipeline is two one-shot containers driven from here:
//
//	video --[ffmpeg]--> frames/*.png --[bake_musetalk_avatar.py]--> <name>/
//
// Both run from the same image (deploy/compose/worker-musetalk-bake.Dockerfile)
// so the bake environment is pinned and reproducible rather than being whatever
// the host conda env happens to contain.
//
// NOT the same thing as internal/avatarbake, which bakes an idle LOOP. See the
// 0009 migration for why the two coexist.
package mtbake

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"mynah/core"

	"github.com/jackc/pgx/v5"
)

// Job is one avatar bake.
type Job struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Engine string `json:"engine"`
	Status string `json:"status"`
	// Progress is 0-100, parsed from the bake script's PROGRESS lines.
	Progress int `json:"progress"`
	// SourceVideo and WorkDir are server paths — internal, never serialized.
	SourceVideo string `json:"-"`
	WorkDir     string `json:"-"`
	BBoxShift   int    `json:"bbox_shift"`
	// BBoxRange is the usable bbox_shift window the bake script derived from
	// this clip's own landmarks, e.g. "-12~17". Empty until the landmark stage
	// finishes. Per-clip, so it cannot be a constant in the UI.
	BBoxRange   string `json:"bbox_range,omitempty"`
	ExtraMargin int    `json:"extra_margin"`
	Mirror      bool   `json:"mirror"`
	Seconds     int    `json:"seconds"`
	Frames      int    `json:"frames"`
	// Log is the tail of container output, shown when a job fails.
	Log       string    `json:"log,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AvatarID is the catalog id this job produces once it lands.
func (j *Job) AvatarID() string { return "bake:" + j.Name }

const jobCols = `id, name, engine, status, progress, source_video, work_dir,
	bbox_shift, bbox_range, extra_margin, mirror, seconds, frames, log, error, created_at, updated_at`

type scannable interface{ Scan(dest ...any) error }

func scanJob(row scannable) (*Job, error) {
	var j Job
	if err := row.Scan(&j.ID, &j.Name, &j.Engine, &j.Status, &j.Progress,
		&j.SourceVideo, &j.WorkDir, &j.BBoxShift, &j.BBoxRange, &j.ExtraMargin,
		&j.Mirror, &j.Seconds, &j.Frames, &j.Log, &j.Error,
		&j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	return &j, nil
}

// nameRE constrains what becomes a directory name AND a catalog id. Lowercase
// only, so two avatars cannot differ by case alone on a case-insensitive mount.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// ValidateName rejects anything that must not become a path component.
func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("形象名 %q 不合法：只能用小写字母、数字、下划线和连字符，"+
			"以字母开头，长度 2-32", name)
	}
	return nil
}

// MaxSeconds caps how much video one bake may consume.
//
// This is a MEMORY bound, not a patience bound: bake_musetalk_avatar.py decodes
// every frame into a list and keeps it there (then doubles it for the mirror
// cycle), so at 720x1280x3 bytes a 60s clip needs ~16GB of host RAM and dies.
// 20s ≈ 500 frames ≈ 1000 after mirroring ≈ 2.7GB, which is comfortable.
// Longer source material is not more expressive anyway — the avatar is a loop.
const MaxSeconds = 20

// DefaultSeconds is what the console proposes: long enough for a varied loop,
// short enough that a bake is minutes rather than tens of minutes.
const DefaultSeconds = 15

// Create inserts a queued job. Paths must already be written to disk.
func Create(ctx context.Context, db core.DBConn, j *Job) (*Job, error) {
	if err := ValidateName(j.Name); err != nil {
		return nil, err
	}
	if j.Seconds <= 0 || j.Seconds > MaxSeconds {
		j.Seconds = DefaultSeconds
	}
	row := db.QueryRow(ctx, `
		INSERT INTO avatar_mt_bake_jobs
			(name, engine, source_video, work_dir, bbox_shift, extra_margin, mirror, seconds)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING `+jobCols,
		j.Name, "musetalk", j.SourceVideo, j.WorkDir,
		j.BBoxShift, j.ExtraMargin, j.Mirror, j.Seconds)
	return scanJob(row)
}

// List returns recent jobs, newest first.
func List(ctx context.Context, db core.DBConn, limit int) ([]*Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query(ctx,
		`SELECT `+jobCols+` FROM avatar_mt_bake_jobs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Get returns one job, or nil when it does not exist.
func Get(ctx context.Context, db core.DBConn, id int64) (*Job, error) {
	j, err := scanJob(db.QueryRow(ctx,
		`SELECT `+jobCols+` FROM avatar_mt_bake_jobs WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// Cancel marks a job canceled. A running job's container is killed by the
// runner, which notices the status change on its next progress write.
func Cancel(ctx context.Context, db core.DBConn, id int64) error {
	_, err := db.Exec(ctx,
		`UPDATE avatar_mt_bake_jobs SET status='canceled', updated_at=now()
		 WHERE id=$1 AND status IN ('queued','running')`, id)
	return err
}

// Delete removes a terminal job row. Running jobs must be canceled first, so a
// delete can never orphan a container.
func Delete(ctx context.Context, db core.DBConn, id int64) error {
	_, err := db.Exec(ctx,
		`DELETE FROM avatar_mt_bake_jobs WHERE id=$1 AND status NOT IN ('queued','running')`, id)
	return err
}
