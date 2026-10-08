# syntax=docker/dockerfile:1
# Mynah EdgeTTS worker — zero-cost TTS tier (Microsoft Edge online voices).
# CPU only; needs outbound internet at runtime. Build context = repo root.
FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/*
# China-friendly default like the rest of the stack (HF_ENDPOINT mirror);
# override with --build-arg PIP_INDEX_URL=https://pypi.org/simple elsewhere.
ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple
COPY deploy/compose/requirements-tts-edge.txt /tmp/req.txt
RUN --mount=type=cache,target=/root/.cache/pip pip install -i "$PIP_INDEX_URL" -r /tmp/req.txt
COPY workers/tts-edge /app/workers/tts-edge
ENV PYTHONUNBUFFERED=1
WORKDIR /app/workers/tts-edge
CMD ["python", "server.py"]
