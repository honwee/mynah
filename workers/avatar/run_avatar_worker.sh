#!/bin/bash
# Run the Mynah AvatarEngine gRPC worker (FlashHead).
#   FLASHHEAD_DIR=/path/to/SoulX-FlashHead ./run_avatar_worker.sh [GPU] [PORT]
#
# FLASHHEAD_DIR is required: it is a separate checkout, so there is no path
# worth guessing — a fabricated default is wrong on every machine but the one it
# was written on, and stays invisible until an import fails somewhere deep.
# Scratch/cache dirs live under PL_DATA_DIR: one knob to move them onto a big
# disk (torch inductor + triton caches are GBs), default inside this checkout.
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
if [ -z "${FLASHHEAD_DIR:-}" ] || [ ! -d "$FLASHHEAD_DIR" ]; then
	echo "run_avatar_worker.sh: set FLASHHEAD_DIR to your SoulX-FlashHead checkout" >&2
	exit 1
fi
export FLASHHEAD_DIR
FH=${FLASHHEAD_PY:-python}
export PERSONALIVE_GEN="$ROOT/gen/python"
export CUDA_VISIBLE_DEVICES=${1:-0}
DATA=${PL_DATA_DIR:-$ROOT/.data}
export XDG_CACHE_HOME=$DATA/.cache
export TORCHINDUCTOR_CACHE_DIR=$DATA/.cache/inductor
export TRITON_CACHE_DIR=$DATA/.cache/triton
export TMPDIR=$DATA/tmp
mkdir -p "$XDG_CACHE_HOME" "$TMPDIR"
cd "$FLASHHEAD_DIR"
exec $FH "$ROOT/workers/avatar/server.py" --port ${2:-9401}
