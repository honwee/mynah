# syntax=docker/dockerfile:1
# Mynah avatar worker image (wav2lipLS / flashhead-class env).
# Source (workers/avatar, gen/python) and model weights are bind-mounted at
# runtime — this image is just the pinned Python deps, so editing the worker
# code never needs a rebuild. torch cu121 wheels carry the CUDA runtime; the
# GPU itself is provided by the NVIDIA driver via CDI (see docker-compose.yml).
#
# Build context = mynah-turn repo root.
FROM python:3.10-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends libglib2.0-0 libgomp1 \
 && rm -rf /var/lib/apt/lists/*

COPY deploy/compose/requirements-avatar.txt /tmp/req.txt
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install -r /tmp/req.txt

ENV PYTHONUNBUFFERED=1
WORKDIR /app/workers/avatar
ENTRYPOINT ["python"]
