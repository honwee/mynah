#!/bin/bash
# Run the Mynah MuseTalk 1.5 AvatarEngine gRPC worker.
#   ./run_musetalk_worker.sh [GPU] [PORT] [AVATAR_DIR]
# Env overrides: MUSETALK_MODELS_DIR MUSETALK_REPO MUSETALK_ENV_PY
DIR="$(cd "$(dirname "$0")" && pwd)"
PY=${MUSETALK_ENV_PY:-python}
export PERSONALIVE_GEN="$DIR/../../gen/python"
export CUDA_VISIBLE_DEVICES=${1:-1}
# Scratch/cache under PL_DATA_DIR — one knob to move it onto a big disk;
# default inside this checkout rather than an absolute path from another machine.
DATA=${PL_DATA_DIR:-$(cd "$DIR/../.." && pwd)/.data}
export XDG_CACHE_HOME=$DATA/.cache
export TMPDIR=$DATA/tmp
mkdir -p "$XDG_CACHE_HOME" "$TMPDIR"
cd "$DIR"
ARGS="--port ${2:-9415}"
[ -n "$MUSETALK_MODELS_DIR" ] && ARGS="$ARGS --models-dir $MUSETALK_MODELS_DIR"
[ -n "$MUSETALK_REPO" ] && ARGS="$ARGS --musetalk-repo $MUSETALK_REPO"
[ -n "$3" ] && ARGS="$ARGS --avatar-dir $3"
exec $PY "$DIR/musetalk_server.py" $ARGS
