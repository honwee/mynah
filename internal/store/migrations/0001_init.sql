-- P4 baseline schema: single-admin auth, grouped settings, single-tenant RAG.
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE admins (
    id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username             TEXT NOT NULL UNIQUE,
    password_hash        TEXT NOT NULL,
    must_change_password BOOLEAN NOT NULL DEFAULT true,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per config group (llm / tts / rag / system / internal), value is the
-- whole group as JSONB. "internal" holds machine state like the JWT secret.
CREATE TABLE settings (
    grp        TEXT PRIMARY KEY,
    value      JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE knowledge_bases (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    -- Locked to the embedding model that produced this KB's vectors; changing
    -- models with ready documents present is rejected (dimensions would mix).
    embed_model TEXT NOT NULL,
    embed_dim   INT  NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE documents (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kb_id      BIGINT NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    filename   TEXT NOT NULL,
    -- Parsed plain text is kept so reindexing never needs the original upload.
    raw_text   TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending', 'processing', 'ready', 'failed')),
    error      TEXT NOT NULL DEFAULT '',
    chunk_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX documents_kb_status_idx ON documents (kb_id, status);

CREATE TABLE chunks (
    id        BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    doc_id    BIGINT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    kb_id     BIGINT NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    seq       INT NOT NULL,
    content   TEXT NOT NULL,
    embedding vector(1024) NOT NULL
);
CREATE INDEX chunks_doc_idx ON chunks (doc_id);
CREATE INDEX chunks_embedding_idx ON chunks
    USING hnsw (embedding vector_cosine_ops);
