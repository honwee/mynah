# syntax=docker/dockerfile:1
# Mynah MuseTalk 1.5 avatar worker image — the engine production serves
# from (:9414 / :9417, one session per worker). Source (workers/avatar,
# gen/python), the upstream MuseTalk repo, the weights and the baked avatar dir
# are all bind-mounted at runtime; this image is just the pinned Python deps.
#
# musetalk_server.py hard-fails unless --models-dir / --musetalk-repo /
# --avatar-dir all exist, so nothing can be baked in as a default — see the
# musetalk service in docker-compose.prod.yml for the three required mounts.
#
# Unlike FlashHead this engine does no torch.compile, so there is no
# build-essential / inductor-cache machinery and no multi-minute warmup.
#
# Build context = repo root.
FROM python:3.10-slim

# libglib2.0-0 + libgomp1: opencv-headless and the numpy/torch OpenMP runtime.
# No libgl1 — the headless opencv build doesn't link it.
RUN apt-get update && apt-get install -y --no-install-recommends \
      libglib2.0-0 libgomp1 \
    && rm -rf /var/lib/apt/lists/*

COPY deploy/compose/requirements-musetalk.txt /tmp/req.txt
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install -r /tmp/req.txt

ENV PYTHONUNBUFFERED=1
WORKDIR /app/workers/avatar
ENTRYPOINT ["python"]
