# syntax=docker/dockerfile:1
# Mynah FlashHead avatar worker image (512 float-head engine).
# Heavier than the wav2lip image: the SoulX-FlashHead engine pulls diffusers /
# xfuser / mediapipe. Source (workers/avatar, gen/python), the SoulX-FlashHead
# repo, and all model weights are bind-mounted at runtime — this image is just
# the pinned Python deps. torch cu121 wheels carry the CUDA runtime; the GPU is
# provided by the NVIDIA driver via CDI (see docker-compose.yml).
#
# server.py self-inserts $FLASHHEAD_DIR/worker onto sys.path, so the engine code
# (flashhead_engine.py) loads from the bind-mounted repo, not from the image.
#
# Build context = mynah-turn repo root.
FROM python:3.10-slim

# libgl1 + libglib for mediapipe/opencv; libgomp for numpy/scikit-image OpenMP.
# build-essential (gcc) is required at RUNTIME: the engine's torch.compile
# (inductor) JIT-compiles CUDA kernels on warmup and needs a host C compiler —
# without it warmup dies with "Failed to find C compiler".
RUN apt-get update \
 && apt-get install -y --no-install-recommends libgl1 libglib2.0-0 libgomp1 build-essential \
 && rm -rf /var/lib/apt/lists/*

COPY deploy/compose/requirements-flashhead.txt /tmp/req.txt
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install -r /tmp/req.txt

ENV PYTHONUNBUFFERED=1
WORKDIR /app/workers/avatar
ENTRYPOINT ["python"]
