-- Avatar training pipeline (形象训练管线, 控制台三期). A management shell around
-- a training step: upload a video, queue a job, poll progress, mark active.
-- State machine: queued -> running -> done/failed (or canceled). An async
-- processor claims one job at a time (single GPU), drives a worker /train stub,
-- and a 30-min sweep self-heals jobs orphaned by a crash.
--
-- Written ONLY by EE builds (gated by core.FeatureTraining); an OSS deployment
-- leaves this table empty and inert. It lives in the shared OSS migration set
-- so the one source of schema truth stays in the advisory-locked, transactional
-- migration runner (channels/0002 set the same precedent for a product table).
-- Additive only — no existing table is touched.

CREATE TABLE training_jobs (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          TEXT NOT NULL,                       -- display name of the trained avatar
    status        TEXT NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued','running','done','failed','canceled')),
    source_path   TEXT NOT NULL DEFAULT '',            -- <PL_TRAIN_UPLOAD_DIR>/{id}/source.mp4
    artifact_path TEXT NOT NULL DEFAULT '',            -- produced avatar asset dir; '' until done
    progress      INT  NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
    error         TEXT NOT NULL DEFAULT '',
    -- The id the worker stub assigns; cored polls /train/status/{worker_job_id}.
    -- Persisted so a poll can resume — without it a restart loses correlation.
    worker_job_id TEXT NOT NULL DEFAULT '',
    -- Captured authorization, mirroring voice clone's consent gate. Training a
    -- real person's likeness carries equal-or-greater authorization weight.
    consent       TEXT NOT NULL DEFAULT '',
    is_active     BOOLEAN NOT NULL DEFAULT false,      -- 「设为当前形象」 (metadata for now)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX training_jobs_status_idx ON training_jobs (status);
-- At most one active avatar: set-active flips this bit on exactly one row.
CREATE UNIQUE INDEX training_jobs_active_idx ON training_jobs (is_active) WHERE is_active;
