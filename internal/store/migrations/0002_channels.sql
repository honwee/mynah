-- Channel publish (控制台二期发布管理): a published channel is a frozen config
-- snapshot served to visitors at /channel/<slug> with access control and a
-- transactional concurrency cap. Additive only — no existing table is touched.

CREATE TABLE channels (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug           TEXT NOT NULL UNIQUE,
    name           TEXT NOT NULL,
    -- Frozen snapshot of the config at publish time: {llm, tts, rag, greeting}.
    -- Later config-center edits do NOT affect a published channel until it is
    -- re-published (which bumps version and evicts the cached client bundle).
    config         JSONB NOT NULL,
    version        INT  NOT NULL DEFAULT 1,
    enabled        BOOLEAN NOT NULL DEFAULT true,
    access_mode    TEXT NOT NULL DEFAULT 'public'
                   CHECK (access_mode IN ('public', 'token')),
    -- ?k= secret for token mode; rotating it revokes every old share link.
    access_token   TEXT NOT NULL DEFAULT '',
    -- Allowed Origin/Referer hosts; [] = any. Defense-in-depth (forgeable).
    domains        JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- Allowed client CIDRs; [] = any. Trustworthy only without a reverse proxy.
    cidrs          JSONB NOT NULL DEFAULT '[]'::jsonb,
    max_concurrent INT  NOT NULL DEFAULT 1 CHECK (max_concurrent >= 1),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX channels_enabled_idx ON channels (enabled);

-- Live concurrency ledger: one row per active visitor session on a channel.
-- Reserve = INSERT under a FOR UPDATE lock on the channel row (anti-oversell);
-- release = DELETE on session teardown. Single-instance deployment: the
-- registry truncates this table on startup, since any rows left behind belong
-- to a dead process.
CREATE TABLE channel_sessions (
    channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, session_id)
);
CREATE INDEX channel_sessions_channel_idx ON channel_sessions (channel_id);
