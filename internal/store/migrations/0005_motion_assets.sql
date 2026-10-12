-- Driving motion skeletons (动作骨架, 控制台三期续). A management shell around
-- LivePortrait motion-template extraction: upload a driving video, queue an
-- extraction, poll progress, get a reusable .pkl whose head/lip/blink trajectory
-- can be transferred onto ANY portrait (person-agnostic — no likeness, no
-- copyright weight, unlike training_jobs).
-- State machine: queued -> extracting -> done/failed (or canceled). The motion
-- processor (a second goroutine alongside training's) claims one job at a time
-- and drives the worker /extract endpoint, which runs the LivePortrait pipeline
-- in a subprocess. A 30-min sweep self-heals jobs orphaned by a crash.
--
-- Unlike training_jobs there is NO single-active pointer: a skeleton is a shared
-- library asset, referenced many-to-many by avatars (config.avatar.motions) and
-- by training jobs (training_jobs.motion_ref). So no is_active column/index.
--
-- Written ONLY by EE builds (gated by core.FeatureTraining, same family as
-- training). Lives in the shared OSS migration set so the one source of schema
-- truth stays in the advisory-locked, transactional migration runner. Additive.

CREATE TABLE motion_assets (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          TEXT NOT NULL,                       -- display name of the skeleton
    status        TEXT NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued','extracting','done','failed','canceled')),
    source_path   TEXT NOT NULL DEFAULT '',            -- <PL_MOTION_UPLOAD_DIR>/{id}/source.mp4
    pkl_path      TEXT NOT NULL DEFAULT '',            -- produced motion-template .pkl; '' until done
    -- Idle-suitability metrics computed at extraction (head°/yaw/pit, lipAvg/lipMax,
    -- blink, loopD, n_frames, fps). Opaque to cored — passed through as raw JSON so
    -- the worker's metric schema can evolve without recompiling the server.
    stats         JSONB NOT NULL DEFAULT '{}'::jsonb,
    n_frames      INT  NOT NULL DEFAULT 0,             -- frame count of the template (shallow-read from stats)
    progress      INT  NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
    error         TEXT NOT NULL DEFAULT '',
    -- The id the worker assigns; cored polls /extract/status/{worker_job_id}.
    worker_job_id TEXT NOT NULL DEFAULT '',
    -- Captured authorization, mirroring training/voice-clone. A driving video is a
    -- real person performing; record that the operator is authorized to use it.
    consent       TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX motion_assets_status_idx ON motion_assets (status);

-- Which driving skeleton a training job should bake into its idle. References a
-- builtin preset ('builtin:d9'/'builtin:d14'/'builtin:d13') or an uploaded asset
-- ('asset:<id>'). Existing rows silently get the lively default. Honored once
-- real baking lands (Phase B/C); the worker /train stub accepts and ignores it.
ALTER TABLE training_jobs ADD COLUMN motion_ref TEXT NOT NULL DEFAULT 'builtin:d9';
