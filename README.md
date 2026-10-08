<div align="center">

<img src="assets/brand/mynah-lockup.png" alt="Mynah" width="520">


**Open-source digital humans, ready to deploy.**

<img src="assets/demo/mynah-demo.gif" alt="Mynah demo" width="800">

<sub>Realtime voice conversation on the public demo channel: ask, it answers with lip-synced video; interrupt it any time.</sub>

<img src="assets/demo/mynah-flashhead-demo.gif" alt="Mynah FlashHead demo (Nova)" width="640">

<sub>FlashHead with the built-in <b>Nova</b> half-body preset: one portrait, the 512 face is pasted back onto a head-to-waist canvas; idle breathing is baked from the same canvas. Public demo: <code>/channel/nova</code>.</sub>

A self-hosted digital human you can actually hand to a customer: realtime voice
conversation, admin console, knowledge base, channel publishing — all open source.
One GPU, or none.

Stream a talking avatar over WebRTC — voice in, lip-synced video out — with
swappable avatar / TTS / ASR / LLM engines, avatar training from a video,
voice cloning, and a one-command setup wizard.

[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Avatar engine](https://img.shields.io/badge/avatar-FlashHead%20(Apache--2.0)-green)](LICENSING.md)
&nbsp;·&nbsp; [Quickstart](#quickstart) &nbsp;·&nbsp; [Architecture](#architecture) &nbsp;·&nbsp; [Integration API](#integration--api) &nbsp;·&nbsp; [Licensing](LICENSING.md) &nbsp;·&nbsp; [Commercial support](#commercial-support)

</div>

> **Hardware, up front:** the default FlashHead avatar engine needs an
> **NVIDIA GPU (4090-class)**; the lighter wav2lipLS profile runs on more
> modest GPUs. **One avatar worker serves one live session at a time** — see
> [Capacity & scaling](#capacity--scaling) before planning a deployment.
> No GPU at all? Every component can be a remote endpoint.

> **Demo** &nbsp;_(placeholder — drop a short screen-capture GIF/MP4 of the avatar
> talking here; for a digital-human project the first impression is lip-sync and
> image quality, which text can't convey)._
>
> `docs/demo.gif`

---

## What it is

Mynah is a Go realtime core (**`cored`**) that streams synchronized
video + audio from a Python avatar worker to a browser over **WebRTC**. It
receives a WebRTC offer, opens a bidirectional gRPC session to the avatar
engine, feeds it **TTS audio** (from text, or from an **LLM** chat turn), and
paces the encoded VP8/H.264 + Opus frames back to the browser. Optional **voice
input** (ASR worker) and **semantic turn-taking** (a turn-detector worker that
decides when the user has actually finished speaking) make it a full
voice-to-voice loop. An optional control plane (`--db`) adds an admin API,
config hot-apply, a knowledge base (RAG), and publishable visitor "channels".

It is engine-agnostic by design: avatar, TTS, ASR, LLM and turn-detection are
**pluggable** behind small contracts (a gRPC streaming contract for workers, an
OpenAI-compatible HTTP surface for TTS/LLM). The default engines are chosen to
be **commercially usable** out of the box.

## Highlights

- **Realtime WebRTC pipeline** — frame-grid pacing on a 25fps grid; barge-in
  (interrupt) support; media encoded in the worker (PyAV), cored just paces.
- **Semantic turn-taking** — a bundled audio end-of-turn model (Smart Turn
  v3.2, BSD-2) decides when the user has actually finished speaking, so the
  avatar neither interrupts nor waits awkwardly.
- **Avatar training from a video** — upload a talking video, the pipeline
  picks the best speaking-base frame (closed-mouth/frontal/sharp scoring,
  MediaPipe — no InsightFace), and the new likeness becomes selectable with a
  baked living-standby idle loop. Consent-gated (see `docs/compliance.md`).
- **Voice cloning** — reference-audio voice enrollment through the reference
  TTS stack (upstream vLLM-omni + Qwen3-TTS CustomVoice); the contract is
  documented so any compatible server drops in.
- **Knowledge base (RAG) with reranking** — pgvector retrieval plus an
  optional cross-encoder second stage (self-hosted worker or any standard
  rerank API).
- **Out-of-the-box setup wizard** — a standalone helper that detects each
  component, one-click installs what's missing (mirror-aware model downloads for
  CN networks, live progress), and lights up green when ready.
- **Pluggable engines** — bring your own avatar / TTS / ASR / LLM, locally or as
  a remote OpenAI-compatible endpoint. No GPU? Point at a cloud API.
- **Commercially-clean defaults** — FlashHead (Apache-2.0) avatar and a
  self-trained wav2lipLS alternative. See [LICENSING.md](LICENSING.md).

## Quickstart

**Day one costs nothing.** The default stack uses **EdgeTTS** (free Microsoft Edge
voices, CPU only, needs internet) so you can hear the avatar talk before you
provision any model. Upgrade to Qwen3-TTS (voice cloning) or a cloud key later —
the console switches TTS without a restart.

**No GPU?** Point the avatar engine at a remote worker and the LLM at any
OpenAI-compatible API; cored itself runs on a laptop. See the docs:
https://honwee.github.io/mynah/ (中文: /zh/)

### Quickstart (original)

The fastest path is the **setup wizard** — it provisions LLM / TTS / ASR / avatar
for you (download + start, mirror-aware), then launches the stack:

```bash
python3 deploy/setup/helper.py        # → http://127.0.0.1:9500
```

Open it in a browser, click **一键安装 / Install** on each component (or point it
at an existing endpoint), then **启动 / Launch**.

Prefer Docker Compose by hand? See **[deploy/compose/README.md](deploy/compose/README.md)**:

```bash
cd deploy/compose
cp .env.example .env && $EDITOR .env       # set GPU index, ports, voice
bash setup-flashhead.sh                    # fetch FlashHead engine + weights
docker compose up -d --build               # start cored + flashhead
# open https://<host>:8455/   (mic needs a secure origin / TLS)
```

## Requirements & expectations

**Minimum, by what you want to run** (all figures measured on an RTX 4090
host, steady state per worker; the console recomputes capacity from your GPU):

| profile | avatar engine | VRAM | fits on |
|---|---|---|---|
| **Lightest** | wav2lipLS (384) ≈ **2 GB** + ASR 1.4 GB, EdgeTTS 0 | ≈ 3.5 GB | any 6 GB card (RTX 2060 / 3050 / 4060 class) |
| Quality | FlashHead ≈ 6.6 GB *or* MuseTalk 1.5 ≈ 7.7 GB, + ASR | ≈ 8–9 GB | 12 GB card (RTX 3060 12G / 4070) — one live session |
| Pool | 2 × MuseTalk + ASR, or MuseTalk + FlashHead | ≈ 17–20 GB | 24 GB card (3090 / 4090) — 2–3 concurrent sessions |
| **No GPU** | remote avatar worker + cloud LLM/TTS | 0 | laptop / VPS runs `cored` + console |

- **TTS**: EdgeTTS (default) uses no GPU; Qwen3-TTS via vLLM-omni needs ~3 GB
  more and enables voice cloning.
- **Network / mirrors**: defaults are **China-friendly** — model downloads use
  `HF_ENDPOINT=https://hf-mirror.com` and ModelScope for ASR. Outside CN, set
  `HF_ENDPOINT=https://huggingface.co` (everything is overridable per script).
- **Host**: Docker 25+ / Compose v2, NVIDIA driver + `nvidia-container-toolkit`
  (CDI). No Docker (AutoDL / UCloud style containers)? See `deploy/cloud/`.

## Capacity & scaling

Be honest about this before a deployment plan: **one avatar worker serves one
live session at a time** (a second concurrent offer gets "engine busy"). The
FlashHead reference runs at 1.56× realtime on a 4090, i.e. one GPU ≈ one
concurrent visitor with headroom for TTS. What this means in practice:

- **Single-host demo / kiosk / booth**: works out of the box — that's the
  designed sweet spot.
- **N concurrent visitors**: run N avatar-worker processes on N GPUs (each is
  an independent gRPC endpoint) and route sessions yourself — a thin router in
  front of `/offer` (pick an idle worker, forward) is straightforward since
  workers are stateless between sessions. Built-in multi-worker scheduling is
  on the roadmap, not in the box today.
- **Published channels** enforce `max_concurrent` per channel, and the visitor
  endpoints are rate-limited per IP (`--visitor-rate-limit`); see
  [SECURITY.md](SECURITY.md) before exposing anything to the public internet.
- The admin **playground drives the same worker** as visitors — a debug
  session competes with a live one.

## Architecture

Three planes, cleanly separated:

```
            Browser (WebRTC: video+audio, DataChannel)
                 │  /offer  /human  /interrupt_talk
                 ▼
   ┌─────────────────────────────┐        ┌──────────────────────────────┐
   │  cored  (Go realtime core)  │  gRPC  │  Python workers              │
   │  WebRTC ⇄ session ⇄ pacing  │◀──────▶│  • avatar  (FlashHead/…)     │
   │  TTS · LLM · RAG · turn FSM │ stream │  • asr     (SenseVoice)      │
   └──────────────┬──────────────┘        │  • turn    (Smart Turn, audio)│
                  │ same process (--db)   └──────────────────────────────┘
                  ▼
   ┌─────────────────────────────┐        host-native (OpenAI-compatible):
   │  control plane (admin API)  │            TTS · ollama LLM · Postgres
   │  config · KB/RAG · channels │
   └─────────────────────────────┘
```

| Area | Package(s) | Role |
|---|---|---|
| Realtime core | `internal/session`, `internal/rtc`, `internal/mediaenc` | WebRTC peers, gRPC stream pacing, VP8/H.264/Opus encode, turn-taking FSM |
| Data plane | `internal/signaling` | Visitor HTTP: `/offer`, `/human`, `/interrupt_talk`, channels |
| Control plane | `internal/control`, `internal/store`, `internal/channel` | Admin API, config hot-apply, KB/RAG, publishable channels (needs `--db`) |
| Providers | `internal/llm`, `internal/tts`, `internal/rag`, `internal/asrclient`, `internal/turnclient`, `internal/engineclient` | Pluggable LLM/TTS/RAG/ASR/turn/avatar clients |
| Feature extensions | `internal/{training,voiceclone,motion,avatarbake,rerank}` | Avatar training, voice clone, motion skeletons, idle bake, RAG rerank |
| Assembly seam | `core/` | Interfaces (`AuthProvider`, `Reranker`, `AdminExtension`, …) for composing builds |
| Assembly / entry | `app/`, `cmd/cored` | Flag parsing, wiring, `app.Main(app.Options{...})` |
| Workers | `workers/{avatar,asr,turn,avatartrain,rerank}` | Python inference engines behind the gRPC/HTTP contracts |
| Contracts | `proto/{avatarengine,turn,asr}/v1` | gRPC service definitions (generated into `gen/`) |

## Integration / API

cored exposes a small visitor-facing HTTP surface (default `:8020`, or `:8455`
TLS). Responses are wrapped `{code:0, data:…}` on success.

| Route | Method | Body | Purpose |
|---|---|---|---|
| `/offer` | POST | `{sdp, type}` | WebRTC offer → `{sdp, type:"answer", sessionid}`; starts a session |
| `/human` | POST | `{sessionid, text, type:"chat"\|"echo", interrupt}` | Make the avatar speak — `echo` = verbatim TTS, `chat` = through the LLM |
| `/interrupt_talk` | POST | `{sessionid}` | Barge-in: cancel in-flight speech |
| `/is_speaking` | POST | `{sessionid}` | Poll speaking state |
| `/channel/{slug}` · `/channel/{slug}/config` · `/channel/offer` | GET/POST | — | Published visitor channels (token / domain / CIDR access control) |

The admin/control plane (`/api/v1/...`, requires `--db` + bearer auth) adds
config, knowledge-base/RAG, channel CRUD, live-session introspection and a
playground. See `internal/control` for the full route set.

> **On "embeddable" today:** integration is via this **REST + WebRTC** surface
> (and the gRPC worker contracts), not a Go library import — `module mynah`
> is app-shaped, not yet published for `go get`. A documented HTTP API + a minimal
> web widget example are on the near-term roadmap.

### Pluggable engines (gRPC worker contracts)

Each worker speaks one streaming contract, so you can swap implementations:

- **`AvatarEngine`** (`proto/avatarengine/v1`) — `Session(stream ClientFrame) → stream ServerFrame)`: audio chunks in, encoded video+audio frames out, plus `Interrupt` / `Ready` / `Stats`. FlashHead and wav2lipLS both implement it.
- **`AsrEngine`** (`proto/asr/v1`) — audio packets in, `VadEvent` + `Transcript` (+ the utterance's `UtteranceAudio` for the turn detector) out.
- **`TurnDetector`** (`proto/turn/v1`) — `Predict(audio_pcm) → {eot_prob, end_of_turn}`. The reference worker is **Smart Turn v3.2** (Pipecat, BSD-2), bundled in-repo.

TTS and LLM are **OpenAI-compatible HTTP** (`/v1/audio/speech`, `/v1/chat/completions`),
so any compatible server or cloud endpoint drops in via config / `.env`.

## Commercial use & licensing

Mynah's own code is **Apache-2.0** ([LICENSE](LICENSE)). **The bundled
default avatar engines are usable commercially** — FlashHead is Apache-2.0,
wav2lipLS runs self-trained weights, and the only non-commercial engine (classic
Rudrabha Wav2Lip) has been removed. Full per-component provenance, caveats, and
the two items to verify for your deployment are in **[LICENSING.md](LICENSING.md)**.

If you clone real people's likenesses or voices, read
**[docs/compliance.md](docs/compliance.md)** — the training/cloning endpoints
are consent-gated by design, and deep-synthesis deployments are regulated in
most jurisdictions.

## Everything included, still composable

The full feature set — avatar training, idle baking, voice cloning, motion
skeletons, RAG reranking — ships in this one repository under Apache-2.0.
There is no paid edition and no feature wall. The assembly seam (`core/`
interfaces + `app.Main(app.Options{})`) remains public API: a distribution can
still compose a narrower build (drop an extension from `cmd/cored`, ship a
tighter `FeatureGate`) or inject richer implementations (SSO auth, custom
rerankers) without patching sources.

## Commercial support

Mynah is free and Apache-2.0, and will stay that way. If your company
wants help going to production, we offer paid engagements:

- **Deployment & tuning** — capacity planning, multi-GPU topologies, latency
  tuning for your hardware.
- **Custom avatars & voices** — production-quality likeness pipelines beyond
  the built-in trainer.
- **Integration & feature work** — bespoke engines, platform integrations,
  priority fixes.
- **Hosted / managed** — we run it, you embed it.

Open an issue with the `commercial` label or reach the maintainer via the
repository profile. <!-- TODO: replace with a direct contact (email / site) -->

## Repository layout

```
app/            assembly + flags (app.Main seam)
cmd/cored/      binary entry point (registers the full feature set)
core/           assembly interfaces (compose narrower builds here)
internal/       realtime core, signaling, control plane, providers, features
proto/  gen/    gRPC contracts + generated stubs
web/            visitor widget (web/user) + admin console (web/admin)
workers/        Python engines (avatar / asr / turn / avatartrain / rerank)
deploy/compose/ docker-compose dev/prod stack  (+ README)
deploy/setup/   first-run setup wizard (helper.py + scripts)
tools/probes/   headless acceptance probes (not shipped)
docs/           API contracts, compliance guide
```

## License

Apache-2.0 — see [LICENSE](LICENSE) and [LICENSING.md](LICENSING.md).
