-- Channel display/branding config (访客频道页对标 public.vue): presentation fields
-- for the polished visitor page at /channel/<slug>. These are channel metadata,
-- NOT part of the frozen persona snapshot (channels.config) — editing a
-- description or theme should not force a re-publish of the LLM/TTS/RAG config.
-- They are served by the public GET /channel/<slug>/config and editable via a
-- normal PATCH. Additive only; defaults keep every existing channel valid.

ALTER TABLE channels
    ADD COLUMN description         TEXT  NOT NULL DEFAULT '',
    ADD COLUMN suggested_questions JSONB NOT NULL DEFAULT '[]'::jsonb, -- quick-ask buttons
    ADD COLUMN brand_name          TEXT  NOT NULL DEFAULT '',          -- top-bar brand; '' = Mynah default
    ADD COLUMN theme_color         TEXT  NOT NULL DEFAULT '',          -- '' or #RGB/#RRGGBB accent
    ADD COLUMN avatar_preview      TEXT  NOT NULL DEFAULT '';          -- welcome-screen avatar image URL
