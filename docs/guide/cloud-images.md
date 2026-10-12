---
title: Cloud images (AutoDL / UCloud)
---

# Cloud images (AutoDL / UCloud)

For people who do not want to install Docker or drivers: rent a 24 GB GPU instance, pick the Mynah image, run one command, open an SSH tunnel, talk to Luna in the browser.

::: tip Positioning: single-node trial build
The image puts every component on one GPU in one instance, for trials, development and demos. Mynah itself is an enterprise-grade, production-oriented architecture (see [Architecture](/guide/architecture)): containerised deployment, worker pools with concurrency quotas, per-channel publishing, knowledge base and instinct layer all live in the same codebase. For production follow the [Deploy](/guide/deploy) guide and run it multi-instance; the configuration and data from the image carry over.
:::

::: warning Status
The recipe, bootstrap script and `mynahctl` live in `deploy/cloud/` and the whole chain has been run end to end in an AutoDL-like CUDA container. **The platform images are being published**; links land here when they are live. Until then the manual steps below take about fifteen minutes on any CUDA 12 / Ubuntu 22.04 base image.
:::

## What is in the image

| Component | Notes |
|---|---|
| FlashHead (Lite weights) | default avatar engine, half-body canvas, about 6.5 GB VRAM |
| Luna / Nova built-in presets | with baked idle breathing and shy / laugh / shake / wink clips |
| SenseVoice + Smart Turn | speech recognition + semantic end-of-turn |
| EdgeTTS | default voice; switch to Qwen3-TTS for cloning |
| qwen2.5:7b via ollama | the brain. Pulled on first start on AutoDL (data disk); bundled on UCloud |
| PostgreSQL 16 + pgvector | console, knowledge base, channel publishing |
| `cored` + console | realtime core and admin, all native, no Docker |

## Usage

```bash
~/mynah/mynahctl start        # starts everything; prints the first console password at the end
```

Browsers cannot reach these instances directly (AutoDL has no public UDP), so open a tunnel from **your own machine** that forwards the visitor page, the console and the WebRTC TCP port together:

```bash
ssh -N -L 8443:127.0.0.1:8443 -L 9443:127.0.0.1:9443 -L 8444:127.0.0.1:8444 -p <port> root@<instance>
```

Open `https://127.0.0.1:8443/` (self-signed certificate, click through), allow the microphone, talk. Console at `https://127.0.0.1:9443/`.

::: tip Why three ports
WebRTC prefers UDP; an SSH tunnel only carries TCP. The image starts cored with `--ice-tcp :8444 --ice-nat-ip 127.0.0.1`, so the browser receives a passive TCP host candidate at 127.0.0.1:8444 and media flows through the tunnel. On a UCloud instance with a public IP, set `ICE_NAT_IP` in `~/mynah/env` to that IP, open the three TCP ports, and skip the tunnel.
:::

Everyday: `mynahctl status | logs <service> | restart | password`; ports, GPU and model live in `~/mynah/env`.

## GPU

24 GB runs the default set (FlashHead ≈ 6.5 GB + 7B model ≈ 5.5 GB + ASR ≈ 1.4 GB). 16 GB works. On 12 GB set `LLM_MODE=none` and point the console at any OpenAI-compatible cloud model.

## Build it yourself (what the image does for you)

On a fresh CUDA 12 / Ubuntu 22.04 instance:

```bash
git clone https://github.com/honwee/mynah.git ~/mynah/src
bash ~/mynah/src/deploy/cloud/bootstrap.sh                    # add --llm bundled on a big system disk (UCloud)
~/mynah/mynahctl start
```

`bootstrap.sh` installs system packages; downloads the `cored` release binary and the Luna/Nova preset clips; creates one conda env for every worker; installs PostgreSQL+pgvector from conda-forge and initialises it; clones FlashHead and downloads only the Lite weights (~8.4 GB, via hf-mirror); fetches SenseVoice from ModelScope; installs ollama; writes a self-signed certificate and `~/mynah/env`. Downloads go through China mirrors by default and the script resumes when re-run.

## Using the instance as the GPU for a laptop

You can also run only the avatar worker there and keep `cored` and the console on your own machine: forward 9421 (the FlashHead worker's gRPC port) and set `AVATAR_ADDR=127.0.0.1:9421` locally. See [Quickstart, path C](./quickstart#path-c-no-gpu).
