# syntax=docker/dockerfile:1
# Mynah avatar-TRAINING worker image (image-training pipeline orchestrator).
#
# workers/avatartrain/server.py is a stdlib HTTP orchestrator (:9405) that drives
# three endpoints — /train (portrait extraction), /extract (LivePortrait
# motion-template .pkl), and /bake (the full living-standby chain: FlashHead
# renders a neutral frame -> LivePortrait motion transfer -> ffmpeg split ->
# bake_idle encode idle_<name>.h264f). /train and /extract and /bake all shell
# out to per-stage pythons (PL_TRAIN_PORTRAIT_PY / PL_LIVEPORTRAIT_PY /
# PL_FLASHHEAD_PY). On the host those are separate envs; HERE /extract + /bake
# share the in-container python — LivePortrait's runtime is dependency-compatible
# with the FlashHead image (identical py3.10 / torch2.5.1+cu121 / diffusers0.36 /
# numpy2.2), so one image serves both stages. /train gets its own tiny venv
# because mediapipe needs protobuf<5 and the ML stack has moved past it.
#
# Built on mynah/flashhead:dev to reuse its heavy layer (torch/diffusers/
# librosa/opencv). Adds: ffmpeg (Stage C frame split) + LivePortrait's
# delta deps (insightface/tyro/face-alignment/...). Source (workers/, gen/), the
# SoulX-FlashHead + LivePortrait repos, model weights, and output dirs are all
# bind-mounted at runtime — this image is just the pinned deps + ffmpeg.
#
# Build context = repo root.
#
# The base is the FlashHead worker image, which the dev and prod stacks tag
# differently (:dev vs :prod). It must be built first — this is a derived image,
# not a self-contained one:
#   docker build -t mynah/flashhead:prod -f deploy/compose/worker-flashhead.Dockerfile .
ARG FLASHHEAD_IMAGE=mynah/flashhead:dev
FROM ${FLASHHEAD_IMAGE}

# ffmpeg binary for Stage C (split animation mp4 -> PNG frame library) and for
# /train's frame sampling.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg \
 && rm -rf /var/lib/apt/lists/*

# LivePortrait's delta deps over the FlashHead image (versions pinned to the
# host liveportrait conda env). Everything else LivePortrait needs (torch,
# diffusers, numpy, opencv, scikit-image, imageio, huggingface_hub) is already
# in the base image.
COPY deploy/compose/requirements-avatartrain.txt /tmp/req.txt
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install -r /tmp/req.txt \
 && pip install "protobuf==3.20.3"
# ^ The LivePortrait deltas drag protobuf up to 7.x, which silently breaks the
# mediapipe face crop that Stage A (render_neutral via flash_head.utils.facecrop)
# relies on: the "neutral face" becomes the whole portrait squashed to 512 and
# the idle bake pastes a body where the face should be. The FlashHead base image
# ships 3.20.3; put it back last so the resolver cannot bump it again.

# Dedicated venv for /train's portrait extraction: mediapipe pins protobuf<5,
# which conflicts with the shared ML stack, so it lives isolated. The server
# reaches it via PL_TRAIN_PORTRAIT_PY.
RUN --mount=type=cache,target=/root/.cache/pip \
    python -m venv /opt/pltrain \
 && /opt/pltrain/bin/pip install "mediapipe==0.10.14"
ENV PL_TRAIN_PORTRAIT_PY=/opt/pltrain/bin/python

ENV PYTHONUNBUFFERED=1
WORKDIR /app/workers/avatartrain
ENTRYPOINT ["python"]
