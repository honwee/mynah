# Cloud images (AutoDL / UCloud 优云智算) — how the one-command images are built

A visitor picks the **Mynah** image, the instance boots, they run
`~/mynah/mynahctl start`, open an SSH tunnel and talk to Luna in a browser.
Same recipe on both platforms; `bootstrap.sh` is the recipe, `mynahctl` is
what runs on the image.

## Platform facts that shaped the recipe

| | AutoDL | UCloud 优云智算 |
|---|---|---|
| Instance type | Docker container, no Docker inside | container ("基础镜像" line), no Docker inside |
| What an image saves | **system disk only, 30 GB**; `/root/autodl-tmp` (data disk) is not saved | layered image; 100 GB system disk free, size is not the constraint |
| Reaching the browser | no public IP; one HTTP port via 自定义服务; SSH in | public IP with TCP port mapping, or SSH |
| UDP to the instance | none | not relied on |

Consequences:

- **Native install**, one conda env (`mynah`, py3.10) for FlashHead + SenseVoice
  + Smart Turn + EdgeTTS — torch is the bulk of every env, so one copy.
- **FlashHead Lite weights only** (`Model_Lite` + `VAE_LTX` + wav2vec, ~8.4 GB)
  instead of the full 15 GB snapshot the compose setup pulls.
- **PostgreSQL 16 + pgvector from conda-forge** at `/opt/mynah/pg`, run as the
  `mynah` system user (Postgres refuses root; `/root` is 0700 so it lives
  under `/opt`).
- **LLM** = ollama + `qwen2.5:7b-instruct-q4_K_M`. On AutoDL it is pulled on
  the first `mynahctl start` into `/root/autodl-tmp/mynah-llm` (`--llm
  firstrun`, default) because 4.7 GB more does not fit the 30 GB system disk
  next to the engine. On UCloud build with `--llm bundled`.
- **WebRTC over TCP through the SSH tunnel**: cored runs with `--ice-tcp
  :8444 --ice-nat-ip 127.0.0.1`, so the browser gets a passive TCP host
  candidate at 127.0.0.1:8444 and the tunnel carries the media. With a public
  IP and a forwarded TCP port set `ICE_NAT_IP` to that IP instead.

Footprint, measured in the validation container (2026-10-12): conda env
6.6 GB, FlashHead Lite weights ≈ 8.4 GB, ASR models 0.9 GB, ollama runtime
2.1 GB, Postgres 0.15 GB — about 18 GB before the LLM, which is why the 4.7 GB
model goes to the data disk on AutoDL.

Validation also covered the SSH-tunnel path end to end from a laptop: the
browser's selected ICE pair was `tcp/host 127.0.0.1:8444`, 25 fps video, zero
audio loss. Two findings baked into the scripts: the first-run model download
uses a single-file GGUF from ModelScope (registry.ollama.ai throttles to ~200
KB/s from China after a few GB), and pion must gather loopback candidates for
the TCP mux to accept tunnelled connections (`internal/rtc/ice.go`).

## Building the image

On a fresh instance from a plain CUDA 12 / Ubuntu 22.04 base (Miniconda is
detected or installed):

```bash
git clone https://github.com/honwee/mynah.git ~/mynah/src     # or https://gitcode.com/honwee/mynah
bash ~/mynah/src/deploy/cloud/bootstrap.sh                   # AutoDL
bash ~/mynah/src/deploy/cloud/bootstrap.sh --llm bundled     # UCloud
~/mynah/mynahctl start && ~/mynah/mynahctl status             # verify, then:
~/mynah/mynahctl prepare-image
```

`prepare-image` stops everything and scrubs per-instance state: it re-creates an
empty Postgres cluster with a new password (so each instance made from the image
gets its own admin password on first start), deletes the TLS certificate (minted
again on start), logs, pid files, temp and shell history. Then check `~/.ssh`
for your own keys (AutoDL injects `authorized_keys2`; remove it last, from the
session you are already in) and click **保存镜像** (AutoDL) / **制作镜像** (UCloud).
Publish with the text in `autodl.md` / `ucloud.md`.

Knobs: `MYNAH_VERSION` (release to fetch cored + presets from), `MYNAH_CORED_BIN`
(use a local build), `GH_PROXY` (GitHub download prefix, default ghfast.top; empty
= direct), `HF_ENDPOINT`, `PIP_INDEX_URL`, `TORCH_INDEX_URL`,
`MYNAH_SEED_FH_MODELS` / `MYNAH_SEED_MODELSCOPE` (reuse weights already on disk).

## What `mynahctl start` does

pg → tts-edge → asr → turn → avatar (FlashHead, `FLASHHEAD_COMPILE=0` for a
15 s cold start) → ollama (pulls the model on first run) → cored. Each step
waits for its port. Ports, GPU index, LLM, `ICE_NAT_IP` live in `~/mynah/env`.
cored is started with the Luna/Nova idle loops and gesture clips from
`~/mynah/presets` (release asset `mynah-presets-<version>.tar.gz`) and the
console database; service start/stop from the console is disabled
(`--docker-socket=`), `mynahctl` does that here.
