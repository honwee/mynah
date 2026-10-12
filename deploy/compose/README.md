# Mynah — dev/debug docker-compose stack

One-command bring-up of the parts of the cored stack you iterate on: **cored**
(Go realtime core), the **avatar worker** (FlashHead by default), and the
**turn-detector**.
TTS, LLM (ollama), ASR and Postgres stay as host-native services — the containers
reach them over the host network. The stack is safe to run **in parallel** to
another cored deployment on the same host: pick fresh ports + a dedicated GPU
in `.env` and it never disturbs it.

## 开箱即用：首启引导向导 (recommended for a fresh clone)

A fresh clone has no LLM / TTS / ASR / avatar provisioned yet. Instead of wiring
them by hand, run the **setup wizard** — a tiny standalone helper (stdlib Python,
no deps) that detects each component, **one-click installs** what's missing (model
downloads + service start, mirror-aware for CN networks, live progress), and lights
up green when ready:

```bash
python3 deploy/setup/helper.py        # → http://127.0.0.1:9500
```

Open it in a browser, click **一键安装** on each red component, then **启动数字人**.
The wizard is intentionally *not* part of cored (cored stays a pure engine) and
needs no DB/admin-console — it serves its own page and drives the mirror-aware
scripts in `deploy/setup/*.sh`. Mirrors are overridable (`HF_ENDPOINT`,
`TTS_MODEL_REPO`, …). Power users can run the scripts directly, e.g.
`bash deploy/setup/setup-flashhead.sh`.

The rest of this doc is the manual/advanced path.

## Prerequisites (one-time, on the host)

- Docker 25+ with Compose v2 (verified: Docker 28.5, Compose v2.40).
- NVIDIA driver + `nvidia-container-toolkit`.
- **Enable GPU-in-docker via CDI** (does *not* restart the Docker daemon, so it
  won't bounce other running containers):
  ```bash
  sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml
  # sanity check: should print one GPU
  docker run --rm --device nvidia.com/gpu=2 ubuntu:22.04 nvidia-smi -L
  ```
  Re-run the `generate` step whenever GPUs are added/removed.

## Configure

```bash
cd deploy/compose
cp .env.example .env
$EDITOR .env        # set AVATAR_GPU, ports, model paths, voice
```

Key knobs (`.env`): `AVATAR_GPU` (CDI index), `CORED_HTTP_PORT`/`CORED_TLS_PORT`,
`FLASHHEAD_PORT`/`TURN_PORT`, and the service URLs (`TTS_URL`/`LLM_URL`/`LLM_KEY`
for a cloud LLM/`ASR_ADDR`).

The default avatar engine is **FlashHead** (Apache-2.0). It speaks the **bundled
default portrait** (`workers/avatar/assets/mynah-default.jpg`) — no baked
asset to host. Its engine repo + weights are **not vendored**; fetch them once:

```bash
bash deploy/compose/setup-flashhead.sh   # clones SoulX-FlashHead + downloads weights (mirror-aware) into FLASHHEAD_DIR
```

Set `FLASHHEAD_AVATAR` to use your own portrait, or `FLASHHEAD_IDLE_DIR` to a baked
idle loop (optional — blank falls back to generated-silence idle). The setup wizard
runs this for you.

## Run

```bash
bash setup-flashhead.sh                 # one-time: fetch FlashHead engine + weights
docker compose up -d --build           # build images + start cored, flashhead, turn
docker compose ps
docker compose logs -f cored           # cored should log "worker healthy: ..."
```

> **Turn-detection** runs by default: the `turn` worker is **Smart Turn v3.2**
> (Pipecat, BSD-2), an audio end-of-turn model bundled in-repo
> (`workers/turn/models/smart-turn-v3.2.onnx`) — nothing to download. It judges
> the just-finished utterance's waveform in parallel with ASR (~28ms on CPU).
> The slot stays pluggable: any worker speaking `proto/turn/v1` works, or unset
> `--turn-detector` and cored answers each utterance.

Open `https://<host>:${CORED_TLS_PORT}/` (mic needs a secure origin / TLS).

Bring up a subset (e.g. cored against an already-running host worker):
```bash
docker compose up -d --no-deps cored
```

## Alternative avatar engine: wav2lipLS (opt-in profile)

cored dials **one** avatar worker. The default `up` runs **FlashHead** (`flashhead`,
:9421). A lighter engine is available behind a compose **profile** — not started
unless you ask for it:

| Engine | Profile | Port | Image | Notes |
|---|---|---|---|---|
| **FlashHead** (512 float head) | *(default)* | 9421 | `mynah/flashhead:dev` | best quality; heavier (diffusers/xfuser/mediapipe); needs `setup-flashhead.sh` |
| **wav2lipLS** (192/384) | `wav2lipls` | 9420 | `mynah/avatar:dev` | light/low-VRAM, high frame-rate; `FACE_SIZE=192`/`384`; assets via `deploy/setup/wav2lipls.sh` (`WAV2LIPLS_CKPT_URL`/`DEFAULT_AVATAR_URL`) |

To use wav2lipLS **instead of** FlashHead, name the services so they don't both
grab a GPU, and point cored at its port:

```bash
# wav2lipLS (light)
AVATAR_ADDR=127.0.0.1:9420 docker compose --profile wav2lipls up -d --build cored avatar turn
```

Each engine has its own `*_GPU` knob in `.env`; they default to the same GPU as
`avatar` since you normally run one at a time. Build an engine's image without
starting it: `docker compose --profile flashhead build flashhead`.

### FlashHead cold start (torch.compile)

FlashHead's cold start is dominated by `torch.compile`, not model load. Measured
on a 4090 (lite model, 4 sample steps, `chunk_dur` ≈ 0.96s):

| `COMPILE_MODEL`/`VAE` | cold start | steady-state rtf | |
|---|---|---|---|
| `1`/`1` (compile on) | ~139s warm cache / ~300s cold cache | **2.28×** | max headroom; matches prod `:9412` |
| `1`/`0` (VAE off) | ~115s | 2.24× | barely helps — VAE compile is a tiny share |
| **`0`/`0` (compile off — default)** | **~14s** | **1.56×** | 10× faster cold start, still comfortably realtime |

Model load alone is ~15s; the rest is Dynamo trace + inductor codegen + autotune.
The inductor **kernel** cache (persisted on the `avatar-cache` volume) does *not*
cover the trace/guards, so even a warm cache stays ~124s with compile on.

The compose default is **compile OFF** (`FLASHHEAD_COMPILE_MODEL=0` /
`FLASHHEAD_COMPILE_VAE=0`): a 14s cold start at 1.56× realtime beats a 139s one at
2.28× for a dev/demo stack. The gates need the `flashhead-compile-env.patch` applied
to the bind-mounted SoulX-FlashHead repo:

```bash
cd "$FLASHHEAD_DIR" && patch -p1 < /path/to/deploy/compose/flashhead-compile-env.patch
```

Set both to `1` in `.env` to restore max throughput headroom (e.g. if you need the
2.28× margin for heavy interrupt/jitter). The `avatartrain` worker defaults compile
**off** too — baking renders one neutral frame offline and never needs the compiled
steady state, which cuts each bake's Stage A by ~265s.

### Avatar-training worker (profile `avatartrain`)

The image-training pipeline orchestrator (`:9425`) — driven by the control plane,
**not** part of the realtime path. One unified image (`mynah/avatartrain:dev`,
built `FROM mynah/flashhead:dev` + ffmpeg + LivePortrait's delta deps) runs
all three endpoints; `/extract` and `/bake` shell out to LivePortrait + FlashHead
stages that on the host are two separate conda envs but here collapse to the single
in-container python (deps are compatible).

```bash
docker compose --profile avatartrain up -d --build avatartrain
```

| Endpoint | Method | Contract | Cost |
|---|---|---|---|
| `/train` | POST `{job_id,name,video_path}` | stub → `{status}` (poll `/train/status/{id}`) | stdlib, seconds, no GPU |
| `/extract` | POST | LivePortrait motion template → `.pkl` in `MOTION_PKL_DIR` | GPU, ~1–2 min |
| `/bake` | POST | FlashHead neutral → LivePortrait transfer → ffmpeg → `idle_<name>.h264f` | GPU, ~6 min |

Repos (`LIVEPORTRAIT_DIR`/`FLASHHEAD_DIR`) + model weights are
bind-mounted read-only; output dirs (`TRAIN_ARTIFACT_DIR`/`MOTION_PKL_DIR`/`BAKE_DIR`/
`BAKE_CACHE_DIR`) read-write. Idle baking also needs an external `render_neutral.py`
(`RENDER_NEUTRAL`, not shipped here) and, for half-body mode, face-parse weights
(`FACEPARSE_DIR`). ⚠️ InsightFace (LivePortrait) + FlashHead + Wav2Lip are
research/non-commercial — the image only *references* the bind-mounted weights, it
does not vendor them.

## Dev loop

| You changed… | Do this |
|---|---|
| Go (`cmd/`, `internal/`, `app/`) | `docker compose up -d --build cored` |
| Python worker code (`workers/…`) | `docker compose restart avatar` (code is bind-mounted, no rebuild) |
| Python deps (`requirements-*.txt`) | `docker compose up -d --build avatar` |
| Ports / model paths / voice (`.env`) | `docker compose up -d` |

Logs: `docker compose logs -f avatar`. Shell in: `docker compose exec avatar bash`.

## Services & ports (defaults)

| Service | Port | GPU | Talks to |
|---|---|---|---|
| `cored` | 8050 http / 8455 tls | — | avatar, turn, host TTS/LLM/ASR |
| `avatar` (wav2lipLS) | 9420 | CDI `AVATAR_GPU` (→ `cuda:0` in-container) | — |
| `turn` (detector) | 9416 | CPU | — |

Host-native (not containerized) **in this dev stack**: TTS `:8091`, ollama
`:11434`, ASR `:9402`, Postgres `:5432`.

## Production stack (`docker-compose.prod.yml`)

The dev stack above containerizes only the parts you iterate on. A second,
fully-containerized compose file mirrors what production actually runs —
**MuseTalk 1.5** (not FlashHead), a two-worker pool, and containerized ASR + TTS:

```bash
cp .env.prod.example .env.prod          # fill in QWEN_RT_KEY + PL_DB_DSN
docker compose --env-file .env.prod -f docker-compose.prod.yml up -d
```

| Service | Port | GPU | Notes |
|---|---|---|---|
| `cored` | 8020 http / 8443 visitor tls / 9443 admin tls | — | `--worker` takes BOTH musetalk addrs |
| `musetalk` | 9414 | `MUSETALK_GPU` (1) | one session per worker |
| `musetalk2` | 9417 | `MUSETALK_GPU` (1) | same image+avatar, port differs |
| `asr` | 9402 grpc / 9404 http | `ASR_GPU` (1) | SenseVoice + fsmn-VAD |
| `tts` | 8091 | `TTS_GPU` (0) | vllm-omni, built from ITS OWN upstream `docker/Dockerfile.cuda` |

Still host-native: ollama `:11434`, Postgres `:5432`.

Things worth knowing before you touch it:

- **The pool size IS the concurrency quota.** cored dispatches a new session to
  whichever worker is free and returns 503「数字人正忙」when both are busy; the
  console enforces `∑(enabled channel max_concurrent) ≤ pool size`. Adding a
  worker raises the quota — but a third does not fit on GPU1 (~7.7GB each,
  ~15.3/24GB used) and GPU0 is TTS territory.
- **ASR cannot be switched off in cloud-brain mode.** With `brain=qwen` +
  cloud VAD the SenseVoice model never runs, but the worker is still the only
  Opus→PCM decoder in the stack (`internal/session/cloudvad.go`).
- **ASR model paths are hardcoded** in `workers/asr/server.py` to
  `/root/.cache/modelscope/hub/models/iic/...`, so `MODELSCOPE_CACHE` must mount
  at exactly that in-container path, and the weights must be pre-provisioned
  (the worker runs with `disable_update=True` and never downloads).
- **TTS is not dead weight in cloud-brain mode** — the console voice list,
  voice-clone upload/preview and every local-brain session still need it, and a
  cold start costs minutes. Prefer leaving it up.
- MuseTalk needs three read-only mounts at **identical host/container paths**
  (repo, weights, baked avatar dir) because they are passed as CLI args;
  `musetalk_server.py` hard-fails if any is missing.


## Notes

- **Host networking** is used because cored does pion WebRTC (ICE needs UDP
  ranges) and all worker/service addresses are `127.0.0.1`. There is no compose
  port isolation as a result — the ports above are bound directly on the host.
- The `tls/` certs and `web/`, `assets/` dirs are bind-mounted into `cored`;
  they are intentionally kept out of the image (`*.Dockerfile.dockerignore`).
- **License — the bundled avatar engines are usable commercially.** See the
  repo-root `LICENSING.md` for the full per-component table.
  - **FlashHead** (default engine): Apache-2.0 model + code
    ([SoulX-FlashHead](https://github.com/Soul-AILab/SoulX-FlashHead), on
    Apache-2.0 Wan 2.1). Footnotes: the *lite* model uses the LTX-Video VAE
    (verify Lightricks' LTX terms if you ship lite); the VividHead training
    *dataset* is CC-BY-4.0, which binds the dataset, not the released weights.
  - **wav2lipLS** (`wav2lipls` profile): an MIT-family architecture driving HuBERT
    audio features (`hubert-large-ls960-ft`, Apache-2.0); the downloaded weights
    are self-trained, so commercial use is the operator's to grant (subject only
    to your own training-data license). It is **not** the research-only Rudrabha
    Wav2Lip — that non-commercial engine has been removed from this repo.
  - cored and LivePortrait-based idle baking are permissively
    licensed (LivePortrait MIT; bypass InsightFace's non-commercial crop with
    `flag_do_crop=False` / MediaPipe). The turn-detector is **Smart Turn v3.2**
    (Pipecat, **BSD-2** — model + code, bundled in-repo; see repo-root `LICENSING.md`).
