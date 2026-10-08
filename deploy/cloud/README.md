# Cloud images (AutoDL / UCloud) — how the one-click images are built

Goal: a visitor picks the **Mynah** image on AutoDL (codewithgpu) or UCloud
(compshare), the instance boots, they run **one command**, and within minutes
they are talking to the avatar in a browser. Same recipe for both platforms.

> Status: recipe + bootstrap script are written; **the images have not been
> built on the platforms yet** — that step needs an account on each platform and
> a GPU instance to snapshot from (see "Publishing" below).

## Why not just `docker compose up` there

Both platforms give you a *container* with a GPU, not a VM: no Docker daemon
inside. So the image runs Mynah **natively**: a prebuilt `cored` binary + the
Python workers in conda envs. Everything else (ports, TLS, STUN) is identical.

## Recipe (what `bootstrap.sh` does)

1. System: `ffmpeg`, `git`, `curl`; conda is already present on both platforms.
2. `cored` — download the linux/amd64 release binary from GitHub Releases
   (falls back to `go build ./cmd/cored` if Go is available).
3. TTS — **EdgeTTS tier** in a tiny conda env (`edge-tts`, `aiohttp`); zero
   model download, needs outbound internet. Qwen3-TTS is a later upgrade.
4. ASR — SenseVoice small (ModelScope mirror) in the `mynah-asr` env.
5. Avatar — **wav2lipLS** (lightest engine) with the bundled default avatar; or
   FlashHead if the instance has ≥ 12 GB VRAM free (flag `--engine flashhead`).
6. Postgres — not required for the demo path; the console runs with the
   embedded config and no knowledge base until `--db` is configured.
7. Writes `~/mynah/start.sh` (starts tts-edge, asr, avatar, cored in that order,
   waits for READY on each) and `~/mynah/stop.sh`.

Ports: cored HTTP `8020`, HTTPS `8443` (self-signed), admin `9443`. On AutoDL
expose them via the instance's custom-service port mapping; on UCloud open them
in the image's port list ("支持开放任意端口").

## Publishing

AutoDL: create an instance from a PyTorch 2.x / CUDA 12.x base → run
`bash deploy/cloud/bootstrap.sh` → verify `~/mynah/start.sh` → **保存镜像** →
publish on codewithgpu with the README snippet in `autodl.md`.

UCloud compshare: same flow from their PyTorch base → **制作镜像** → publish.

Keep the image lean: no Qwen3-TTS weights, no FlashHead weights unless you build
the "GPU" variant. First start downloads nothing on the EdgeTTS + wav2lipLS path.
