---
title: Cloud images (AutoDL / UCloud)
---

# Cloud images (AutoDL / UCloud)

For people who do not want to install Docker or drivers: rent a GPU instance, pick the Mynah image, run one command.

::: warning Status
The recipe and bootstrap script are in the repo (`deploy/cloud/`), and the bootstrap has been written against the platforms' container layout. **The images themselves have not been published yet.** Until they are, follow the manual steps below on any PyTorch / CUDA 12 base image. This page will carry the image links when they go live.
:::

## Why these platforms need a different recipe

AutoDL and UCloud give you a container with a GPU, not a virtual machine: there is no Docker daemon inside. So the image runs Mynah **natively**: a prebuilt `cored` binary plus the Python workers in conda environments. Ports, TLS and the console are identical to the Docker deployment.

## Manual bootstrap (what the image does for you)

On a fresh instance with conda and a GPU:

```bash
git clone https://github.com/honwee/mynah.git
bash mynah/deploy/cloud/bootstrap.sh            # add --engine flashhead on a 12 GB+ card
~/mynah/start.sh
```

`bootstrap.sh` installs ffmpeg, downloads the `cored` release binary, creates envs for EdgeTTS, SenseVoice ASR and the wav2lipLS avatar engine (no large downloads on this path), and writes `~/mynah/start.sh` / `stop.sh` that bring services up in order and wait for each to report ready.

Then map two ports in the platform's custom-service panel:

| Port | What |
|---|---|
| 8443 | visitor page: `https://<mapped-host>:8443/` |
| 9443 | admin console |

The certificate is self-signed; click through the browser warning. The first admin password is in `~/mynah/logs-cored.log` (search `initial admin`).

## What you get

- EdgeTTS voice (free, needs the instance to reach the internet)
- wav2lipLS avatar engine with the bundled default avatar (about 2 GB VRAM)
- SenseVoice ASR
- Console without a database: configuration is embedded; knowledge base and channel publishing light up once you point `PL_DB_DSN` at a Postgres

Upgrade paths from here are the same as everywhere else: switch to MuseTalk or FlashHead in 本地服务 on a 12 GB+ card, switch TTS tier in 配置中心.

## Using a cloud instance as the GPU for a laptop

You can also use the instance only as the **avatar worker** and run `cored` plus the console on your own machine. Map the worker's gRPC port (9420 for wav2lipLS) and set `AVATAR_ADDR=<mapped-host>:<port>` locally. See [Quickstart, path C](./quickstart#path-c-no-gpu).

## Publishing the image (maintainers)

AutoDL: instance from a PyTorch 2.x / CUDA 12.x base → run `bootstrap.sh` → verify `start.sh` → 保存镜像 → publish on codewithgpu with the text in `deploy/cloud/autodl.md`. UCloud compshare: same flow → 制作镜像. Keep the image lean: no Qwen3-TTS or FlashHead weights unless you build the "GPU" variant.
