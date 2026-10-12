# syntax=docker/dockerfile:1
# Mynah ASR worker image (SenseVoice + fsmn-VAD). Source (workers/asr,
# gen/python) and the model cache are bind-mounted at runtime; image = deps only.
#
# The model paths are hardcoded in workers/asr/server.py to
# /root/.cache/modelscope/hub/models/iic/{SenseVoiceSmall,speech_fsmn_vad_*}
# and the worker runs with disable_update=True (never downloads), so the compose
# service MUST bind-mount a provisioned modelscope cache at exactly that path.
#
# Build context = repo root.
FROM python:3.10-slim

# libgomp1: OpenMP runtime for numpy/scipy/torch. PyAV ships its own FFmpeg, so
# no system ffmpeg/libopus is needed for the Opus decode path.
RUN apt-get update && apt-get install -y --no-install-recommends \
      libgomp1 \
    && rm -rf /var/lib/apt/lists/*

COPY deploy/compose/requirements-asr.txt /tmp/req.txt
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install -r /tmp/req.txt

ENV PYTHONUNBUFFERED=1
WORKDIR /app/workers/asr
ENTRYPOINT ["python"]
