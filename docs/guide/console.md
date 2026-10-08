---
title: Console tour
---

# Console tour

The console is a single-page app served by cored on the admin port (`https://<host>:9443/`). It talks to the [Admin API](/api/admin-api); everything you can click, you can also script.

<img src="/screens/dashboard.png" alt="Dashboard" style="border:1px solid #e5e7eb;border-radius:8px">


## Sign in

One admin account. The initial password is printed once in the cored log (`initial admin`); the first login forces a change. Tokens are JWTs valid for 24 hours. Change the password any time under the account menu or `POST /api/v1/auth/password`.

## Pages

| Page | What you do there | Backed by |
|---|---|---|
| **仪表盘 Dashboard** | One glance at component health: avatar engine, ASR, TTS, LLM, embeddings, Postgres. | `GET /api/v1/health` |
| **配置中心 Config** | Conversation model (any OpenAI-compatible endpoint, with a "test connection" that runs a real turn), system prompt, streaming, TTS base URL and voice, conversation brain (local pipeline or Qwen realtime cloud), turn-taking knobs, RAG parameters. Changes apply hot. | `GET/PUT /api/v1/config/{group}` |
| **形象与声音 Avatars & voices** | Pick the current avatar, upload a talking video to bake a new one, preload avatars so visitors never see a cold start, pick and audition voices, clone a voice from reference audio when the TTS tier supports it. | `/api/v1/avatars*`, `/api/v1/tts/voices` |
| **知识库 Knowledge base** | Create knowledge bases, upload documents, watch ingestion, run retrieval tests against live settings. | `/api/v1/kb*`, `/api/v1/documents*` |
| **发布管理 Publish** | Freeze the current configuration into a channel: a shareable link with access control, concurrency cap and branding. Republish, rotate tokens, enable and disable. | `/api/v1/channels*` |
| **本地服务 Services** | Engine pool: which engines run on which GPU, VRAM budget and what is still allocatable, start / stop / restart workers, per-engine concurrency quota and which channels are bound to it. | `/api/v1/services*`, `/api/v1/avatar/*` |
| **对话调试 Playground** | Talk to the exact pipeline visitors get: text or voice, see retrieved chunks and latency, drive the avatar with text, trigger gestures, tune green-screen keying live. | `/api/v1/playground/*` |
| **会话监控 Sessions** | Who is connected, since when, how many turns, whether the avatar is speaking; kick a session. | `/api/v1/sessions*` |

## Three ideas that explain the console

**Config is layered.** Built-in defaults, then startup flags, then the database. Whatever you save in the console wins and survives restarts. The API returns the merged view.

**Channels are snapshots.** When you publish, the current model, voice, brain mode and knowledge settings are frozen into the channel. You can keep experimenting in the console without changing what visitors see; press "republish" to roll the new settings out. Branding fields (name, logo, background) are live metadata and change immediately.

**Capacity is explicit.** One avatar worker serves one session. The Services page computes how many workers fit in your VRAM from measured per-engine figures and refuses pool compositions that do not fit. Channel concurrency caps are allocated out of that pool, so you can promise a department "three simultaneous visitors" and have the system enforce it.

<img src="/screens/services.png" alt="Services: engine pool and VRAM budget" style="border:1px solid #e5e7eb;border-radius:8px">


<img src="/screens/playground.png" alt="Playground" style="border:1px solid #e5e7eb;border-radius:8px">


## Themes and languages

Light and dark themes, Chinese and English UI, switchable from the top bar. The visitor page has its own theme per channel.
