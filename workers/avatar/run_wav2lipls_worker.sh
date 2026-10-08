#!/bin/bash
# Run the Mynah Wav2LipLS AvatarEngine gRPC worker.
# Uses the `flashhead` env: it has torch 2.5.1 + transformers 4.57 (the
# livetalking env's transformers 5.x needs torch>=2.4 and can't load HubertModel).
#   ./run_wav2lipls_worker.sh [GPU] [PORT] [FACE_SIZE] [AVATAR_DIR]
DIR="$(cd "$(dirname "$0")" && pwd)"
PY=python
export PERSONALIVE_GEN="$DIR/../../gen/python"
export CUDA_VISIBLE_DEVICES=${1:-2}
# Scratch/cache under PL_DATA_DIR — one knob to move it onto a big disk;
# default inside this checkout rather than an absolute path from another machine.
DATA=${PL_DATA_DIR:-$(cd "$DIR/../.." && pwd)/.data}
export XDG_CACHE_HOME=$DATA/.cache
export TMPDIR=$DATA/tmp
mkdir -p "$XDG_CACHE_HOME" "$TMPDIR"
cd "$DIR"
ARGS="--port ${2:-9414} --face-size ${3:-192}"
[ -n "$4" ] && ARGS="$ARGS --avatar-dir $4"
exec $PY "$DIR/wav2lipls_server.py" $ARGS
