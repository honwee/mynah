-- Avatar idle-bake jobs (Phase B 真烘焙). Each row is one bake of a living-standby
-- idle loop for an avatar: FlashHead renders a neutral frame from the avatar's
-- source portrait, LivePortrait transfers a chosen motion skeleton (.pkl) onto it,
-- and the result is encoded to a cored-replayable idle_<avatar>.h264f. The worker
-- /bake endpoint runs this real cross-env pipeline in a subprocess.
-- State machine: queued -> baking -> done/failed (or canceled). The avatar-bake
-- processor (a third goroutine alongside training + motion) claims one job at a
-- time and drives the worker /bake endpoint. A 30-min sweep self-heals orphans.
--
-- On done, if apply is true, the processor calls AdminDeps.ApplyIdle(asset_path)
-- to hot-swap the live idle loop (and persist it to config.system.idle_asset so it
-- survives a restart) — this is what makes "换动作 / 换人" take effect on the live
-- engine. avatar_id is the config.avatar.current token ("default" / a builtin id);
-- motion_ref is a skeleton ref ("builtin:d9" / "asset:<id>").
--
-- Written ONLY by EE builds (gated by core.FeatureMotion/Training, avatar family).
-- Lives in the shared OSS migration set (single source of schema truth). Additive.

CREATE TABLE avatar_bake_jobs (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    avatar_id     TEXT NOT NULL,                       -- config.avatar.current token ("default"/builtin id)
    motion_ref    TEXT NOT NULL DEFAULT 'builtin:d9',  -- skeleton to drive the idle ("builtin:d9"/"asset:<id>")
    -- Server-side paths resolved at create time (validated to exist before queueing),
    -- so the processor just submits them. Internal — never leave the server.
    source_image  TEXT NOT NULL DEFAULT '',            -- avatar FlashHead source portrait
    motion_pkl    TEXT NOT NULL DEFAULT '',            -- resolved motion-template .pkl
    status        TEXT NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued','baking','done','failed','canceled')),
    asset_path    TEXT NOT NULL DEFAULT '',            -- produced idle_<avatar>.h264f; '' until done
    apply         BOOLEAN NOT NULL DEFAULT TRUE,       -- hot-swap the live idle on completion
    progress      INT  NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
    error         TEXT NOT NULL DEFAULT '',
    -- The id the worker assigns; cored polls /bake/status/{worker_job_id}.
    worker_job_id TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX avatar_bake_jobs_status_idx ON avatar_bake_jobs (status);
