# Architecture

Mynah is built in **layers**, with **a single interface contract between each adjacent pair**. That is what makes every layer replaceable on its own: the avatar engine does not care which language model drives the conversation, the language model does not care which engine renders the lips, and the admin console talks only to the control plane, never to the inference processes directly.

<img src="/diagrams/architecture-layers.png" alt="Layered view" style="border:1px solid #e5e7eb;border-radius:8px">

```
 ┌──────────────────────────────────────────────────────────────────────────┐
 │  Clients          visitor page /channel/<slug> · web SDK <mynah-avatar>  │
 │                   agents: DeepSeek Harness plugin · Pipecat (face mode)  │
 └───────────────▲──────────────────────────────────────────────▲──────────┘
     WebRTC (A/V + DataChannel)                      REST /human /action …
 ┌───────────────┴──────────────────────────────────────────────┴──────────┐
 │  Realtime core (Go, cored)                                               │
 │   rtc (pion) → session: turn FSM · speaking state · barge-in             │
 │   mediaenc: idle-loop replay · action clips · VP8/H264 · Opus · PLI      │
 │   ┌───────────────┐  ┌──────────────────────┐  ┌───────────────────────┐ │
 │   │ Brains        │  │ Instinct (System One)│  │ Avatar engines        │ │
 │   │ local LLM+TTS │  │ rules | Jev          │  │ gRPC worker contract  │ │
 │   │ Qwen realtime │  │ picks a gesture clip │  │ MuseTalk · FlashHead  │ │
 │   │ Pipecat bot   │  │ while the brain talks│  │ wav2lipLS · (yours)   │ │
 │   └───────────────┘  └──────────────────────┘  └───────────────────────┘ │
 └───────────────▲──────────────────────────────────────────────▲──────────┘
     admin API /api/v1 (JWT)                                   gRPC / HTTP
 ┌───────────────┴──────────────────┐   ┌─────────────────────────┴──────────┐
 │  Control plane (same process)    │   │  Workers (Python, GPU)              │
 │   config layering · channels +   │   │  avatar (per engine) · asr · turn   │
 │   capacity quota · KB/RAG ·      │   │  avatartrain: idle & gesture bakes  │
 │   bake jobs · sessions · console │   │  (LivePortrait) · tts-edge          │
 └───────────────┬──────────────────┘   └─────────────────────────────────────┘
                 ▼
         Postgres + pgvector (config, channels, KB, jobs)
```

## The layers

| Layer | Where | What it owns | Contract to the next layer |
|---|---|---|---|
| **Clients** | `web/user` (visitor page), `web/sdk` (`<mynah-avatar>`), `dsh-plugin-mynah`, `pipecat-ai-mynah` | Rendering the stream, mic capture, subtitles, action bar, agent tools | WebRTC `/offer`, REST (`/human`, `/action`, `/interrupt_talk`), DataChannel events |
| **Realtime core** | `internal/rtc`, `internal/session`, `internal/mediaenc`, `internal/signaling` | One `Session` per visitor: peer connection, conversation FSM (`idle → listening → thinking → speaking`), barge-in, A/V pacing, idle-loop replay, keyframe handling (PLI) | Pulls audio from a *brain*, pushes it to an *engine worker*, forwards frames to the browser |
| **Brains** | `internal/llm` + `internal/tts` (local), `internal/qwenrt` (Qwen realtime), `internal/pipecatrt` (Pipecat) | Turning what the visitor said into reply speech. All three present the same event surface to the session (`ServeEvents`: user text, reply start/delta/done, reply PCM, barge-in) | 16 kHz PCM in both directions |
| **Instinct** | `internal/instinct` | A fast decision model (rules or Jev) that watches the transcript and picks one of the avatar's gesture clips; gated, paced, falls back to rules, never on the critical path | Plays clips through the same `PlayAction` as the visitor action bar |
| **Avatar engines** | `workers/avatar/*`, contract in `proto/avatarengine` | Lip-synced frames from audio: MuseTalk 1.5 (baked avatars), SoulX-FlashHead (single portrait, incl. the half-body paste-back), wav2lipLS | gRPC bidirectional stream: `AudioChunk` in, encoded `VideoFrame` + Opus `AudioFrame` out, `Interrupt`, `SetAction` |
| **Control plane** | `internal/control`, `internal/config`, `internal/channel`, `internal/knowledge`, `internal/avatarbake`, `internal/store` | Admin API behind JWT; hot config; publishable channels with per-engine capacity quota; knowledge base + RAG; idle/gesture bake jobs; session monitor | Postgres; the console (`web/admin`) is a pure client of this API |
| **Workers** | `workers/{avatar,asr,turn,avatartrain,tts-edge}` | GPU inference behind stable contracts; each is a separate container | gRPC (`avatarengine`, `asr`, `turn`) and HTTP (`/bake`, `/train`) |

## One voice turn, end to end

<img src="/diagrams/voice-turn.png" alt="One voice turn" style="border:1px solid #e5e7eb;border-radius:8px">


1. Browser mic → WebRTC Opus → `session.listenMic*` → ASR worker decodes (and transcribes in local mode).
2. **Turn-taking** decides the visitor is done: local Smart Turn, or the brain's own VAD (Qwen `server_vad`/`smart_turn`, Pipecat).
3. The **brain** produces reply text and speech: local = LLM stream → sentence flush → TTS; Qwen = speech-to-speech; Pipecat = whatever the bot's pipeline does. Either way the session gets `OnReplyDelta` (subtitles) and `OnPCM` (16 kHz reply audio).
4. Reply PCM → engine worker (`AudioChunk`) → lip-synced frames come back → `mediaenc` paces them onto the WebRTC track, forcing a keyframe at speech start and on viewer PLI.
5. In parallel the **instinct** layer asks "which gesture fits?"; a confident pick plays right after the line (or during listening if the avatar is idle).
6. Speech end → the track cuts back to the baked **idle loop** at a keyframe; the worker is free (idle costs no GPU).

## Where state lives

- **Config** is layered `defaults < flags < database`; the console writes the database layer and groups marked *hot* apply to new sessions immediately (`internal/config`).
- **Channels** freeze a snapshot (persona, brain, voice, avatar) at publish time, so console edits do not change a live channel until you republish.
- **Capacity** is a per-engine quota: the sum of enabled channels' concurrency must fit the workers of the engine their avatar needs (`internal/control/quota.go`); the console shows it and offers a swap when full.
- **Avatar material** (idle loops, gesture clips, half-body canvases) is baked once into files and registered per avatar; sessions replay, never regenerate.

## Extension points

| You want to… | Touch |
|---|---|
| Add a lip-sync engine | A worker speaking `proto/avatarengine`; register its label in `internal/avatarengine` |
| Add a brain | Implement the `cloudConn` surface (`internal/session/qwen.go`) like `pipecatrt`, add a `brain.mode` |
| Add an instinct provider | Implement `instinct.Provider` (two methods) |
| Add a gesture | Bake it: `POST /api/v1/avatars/bake {kind:"action", action, motion_ref}` |
| Add an admin feature | An `AdminExtension` (`core/extension.go`) mounted by the control plane |
| Drive it from an agent | REST `/human` `/action` or the dsh plugin; from Pipecat, `MynahVideoService` |

## Design rules we keep

- **Contracts over imports.** Workers, brains and clients are replaceable because the only coupling is a proto or an HTTP shape.
- **Bake, don't generate, when nobody is talking.** Idle and gestures are pre-rendered; a session that is listening costs zero GPU.
- **The model that talks does not move.** Speech comes from the brain, gestures from the instinct layer, frames from the engine — three timescales, three components.
- **Fail open to the cheaper tier.** Jev → rules; cloud VAD → local; half-body → face; a missing capability degrades, it never blocks a turn.

## Exact wiring (Graphviz source: `docs/diagrams/architecture.dot`)

<img src="/diagrams/architecture-graphviz.svg" alt="Exact wiring (Graphviz source: `docs/diagrams/architecture.dot`)" style="background:#fff;border:1px solid #e5e7eb;border-radius:8px">
