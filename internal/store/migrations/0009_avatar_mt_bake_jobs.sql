-- MuseTalk avatar-bake jobs. Distinct from avatar_bake_jobs (0006), which bakes
-- an IDLE LOOP (a video the visitor sees while nobody is talking) — this one
-- bakes an AVATAR: the frame library + face crops + VAE latents a MuseTalk
-- worker replays and drives with audio. Two unrelated pipelines that English
-- unhelpfully calls the same thing; they share no columns and no worker.
--
-- One row = one uploaded talking-head video turned into
-- <avatar-bakes-dir>/<name>/ (full_imgs/, mask/, coords.pkl, mask_coords.pkl,
-- latents.pt). Once the directory lands, avatarscan picks it up on the next
-- catalog rescan and it becomes bindable — the row exists only to track the
-- job, not to own the artifact.
--
-- State machine: queued -> running -> done/failed. The runner claims one job at
-- a time (baking is GPU-bound; two at once would contend with the live engine
-- pool for VRAM) and drives two one-shot containers: ffmpeg for frames, then
-- the bake image. A restart fails whatever was 'running' — container output is
-- not resumable, and re-queueing is one click.
CREATE TABLE avatar_mt_bake_jobs (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Target avatar directory name; also the catalog id once baked
    -- ("bake:<name>"). Validated against a strict pattern before insert: it
    -- becomes a filesystem path.
    name         TEXT NOT NULL,
    engine       TEXT NOT NULL DEFAULT 'musetalk',
    status       TEXT NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued','running','done','failed','canceled')),
    progress     INT  NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
    -- Server-side only: the uploaded video and the work directory, both deleted
    -- when the job reaches a terminal state (frames for a 20s clip are ~1GB).
    source_video TEXT NOT NULL DEFAULT '',
    work_dir     TEXT NOT NULL DEFAULT '',
    -- Bake parameters. bbox_shift is MuseTalk's mouth-opening-amplitude knob:
    -- the single most useful dial when a bake comes out with a stiff mouth, so
    -- it is per-job rather than a global setting.
    bbox_shift   INT  NOT NULL DEFAULT 0,
    extra_margin INT  NOT NULL DEFAULT 10,
    -- mirror appends the reversed frame sequence so playback loops seamlessly.
    -- Turn it off only when the source video already ends where it starts.
    mirror       BOOLEAN NOT NULL DEFAULT TRUE,
    -- Seconds of video actually baked. Capped: the bake holds every decoded
    -- frame in host RAM at once (720x1280x3 each), so an unbounded clip is an
    -- OOM, not a slow job.
    seconds      INT  NOT NULL DEFAULT 15,
    frames       INT  NOT NULL DEFAULT 0,   -- frames in the finished avatar
    -- Tail of the container output, kept for diagnosing a failure without
    -- shelling into the host. Bounded by the runner.
    log          TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX avatar_mt_bake_jobs_status_idx ON avatar_mt_bake_jobs (status);
