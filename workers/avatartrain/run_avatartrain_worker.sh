#!/bin/bash
# Run the Mynah avatar-training worker (host launcher).
#   ./run_avatartrain_worker.sh [PORT]
#
# In the compose stacks this worker runs as the `avatartrain` service and gets
# every path below from .env — this script is the bare-host path.
#
# /train extracts the best speaking-base portrait from an uploaded video
# (extract_portrait.py — mediapipe+opencv, CPU-friendly). /extract shells out
# to extract_template.py in the LivePortrait runtime to produce motion-skeleton
# .pkl files. /bake is the real living-standby pipeline. Give this its OWN GPU
# (a distinct CUDA_VISIBLE_DEVICES from the inference worker's) so
# extraction/baking never contends with the live engine.
#
# ⚠ 2026-07-30: every path here used to default under /data/personalive — the
# data disk of the 8-GPU machine. When that machine was rebuilt and the trees
# moved, nothing here followed, and nothing complained: bake jobs were accepted,
# queued, and then died inside a subprocess looking for a directory no operator
# had ever typed. So: paths that CAN be derived from this checkout now are, and
# paths that can't (other people's repos) are required rather than guessed. A
# fabricated absolute default is wrong everywhere except the machine it was
# written on, and stays invisible until someone bakes.
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)" # repo root

# need VAR "what it is" — for the external repos this worker shells into.
need() {
	if [ -z "${!1:-}" ]; then
		echo "run_avatartrain_worker.sh: set $1 ($2)" >&2
		exit 1
	fi
	if [ ! -e "${!1}" ]; then
		echo "run_avatartrain_worker.sh: $1=${!1} does not exist ($2)" >&2
		exit 1
	fi
}

export CUDA_VISIBLE_DEVICES=${CUDA_VISIBLE_DEVICES:-0}

# Artifact dirs, under this checkout by default so a fresh clone works.
DATA=${PL_DATA_DIR:-$ROOT/.data}
export PL_TRAIN_ARTIFACT_DIR=${PL_TRAIN_ARTIFACT_DIR:-$DATA/idlebake/training}
export PL_MOTION_PKL_DIR=${PL_MOTION_PKL_DIR:-$DATA/idlebake/motions}
export PL_BAKE_DIR=${PL_BAKE_DIR:-$DATA/idlebake/assets}
# The flat finished-bake cache is the rendezvous point with cored: it MUST be
# the same directory cored got as PL_BAKE_CACHE_DIR, or every bake looks like a
# miss and re-burns the GPU.
export PL_BAKE_CACHE_DIR=${PL_BAKE_CACHE_DIR:-$DATA/idlebake/cache}
mkdir -p "$PL_TRAIN_ARTIFACT_DIR" "$PL_MOTION_PKL_DIR" "$PL_BAKE_DIR" "$PL_BAKE_CACHE_DIR"

# Portrait extraction (/train): a python with mediapipe + opencv.
# ⚠ mediapipe needs protobuf<5 — newer protobuf in a shared ML env silently
# breaks it (MessageFactory/GetPrototype AttributeError). Give it a small
# dedicated venv (pip install "mediapipe==0.10.14") rather than reusing the
# FlashHead env. PL_TRAIN_MODE=simulate falls back to the stdlib-only progress
# stub (CI / no-GPU contract tests).
export PL_TRAIN_MODE=${PL_TRAIN_MODE:-real}
export PL_TRAIN_PORTRAIT_PY=${PL_TRAIN_PORTRAIT_PY:-python3}

# Idle baking pins each stage to PL_BAKE_GPU. Distinct from the live engine's
# GPU and from TTS, so bakes never contend; index 0 is only right on a
# single-GPU box (the compose service gets a CDI-mapped 0 for real).
export PL_BAKE_GPU=${PL_BAKE_GPU:-0}
export PL_BAKE_IDLE_PY=${PL_BAKE_IDLE_PY:-$ROOT/workers/avatar/bake_idle.py}

# The two external runtimes bake_idle_asset.sh shells across, plus the neutral
# renderer. Not derivable — they are separate checkouts, and render_neutral.py
# comes from LiveTalking, which isn't vendored here at all.
need PL_LIVEPORTRAIT_ROOT "the LivePortrait checkout; /extract and bake stage B run in it"
need PL_FLASHHEAD_DIR "the SoulX-FlashHead checkout; bake stage A renders the neutral frame there"
need PL_RENDER_NEUTRAL "path to LiveTalking's tools/render_neutral.py"
export PL_LIVEPORTRAIT_ROOT PL_FLASHHEAD_DIR PL_RENDER_NEUTRAL
# Interpreters for those two runtimes. Same-python is fine when one env has both
# sets of deps (that is what the container does); override for split conda envs.
export PL_LIVEPORTRAIT_PY=${PL_LIVEPORTRAIT_PY:-python}
export PL_FLASHHEAD_PY=${PL_FLASHHEAD_PY:-python}

PY=${PL_TRAIN_PY:-python3}
exec $PY "$DIR/server.py" --port ${1:-9405}
