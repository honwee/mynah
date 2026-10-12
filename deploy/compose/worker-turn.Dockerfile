# syntax=docker/dockerfile:1
# Mynah turn-detector worker image (CPU/ONNX). Source (workers/turn,
# gen/python) and the model dir are bind-mounted at runtime; image = deps only.
#
# Build context = mynah-turn repo root.
FROM python:3.12-slim

COPY deploy/compose/requirements-turn.txt /tmp/req.txt
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install -r /tmp/req.txt

ENV PYTHONUNBUFFERED=1
WORKDIR /app/workers/turn
ENTRYPOINT ["python"]
