#!/usr/bin/env bash
# Mynah setup · TTS (default: Qwen3-TTS served by vLLM, OpenAI /v1/audio/speech).
# Idempotent: skips if the TTS endpoint already answers.
set -euo pipefail
SETUP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_DIR="$SETUP_DIR/../compose"
TTS_URL="${TTS_URL:-http://127.0.0.1:8091}"
export HF_ENDPOINT="${HF_ENDPOINT:-https://hf-mirror.com}"          # China-friendly HF mirror
TTS_MODEL_REPO="${TTS_MODEL_REPO:-Qwen/Qwen3-TTS-12Hz-0.6B-CustomVoice}"
PL_DATA_DIR="${PL_DATA_DIR:-$(cd "$(dirname "$0")/../.." && pwd)/.data}"
TTS_MODEL_DIR="${TTS_MODEL_DIR:-$PL_DATA_DIR/models/qwen3-tts/Qwen3-TTS-12Hz-0.6B-CustomVoice}"

if curl -sf -m3 "$TTS_URL/v1/models" >/dev/null 2>&1; then
  echo "[tts] 已在 $TTS_URL 运行 — 跳过"; exit 0
fi

echo "[tts] 下载 Qwen3-TTS 模型 ($TTS_MODEL_REPO, HF_ENDPOINT=$HF_ENDPOINT) ..."
if [ ! -f "$TTS_MODEL_DIR/config.json" ]; then
  docker run --rm -e HF_ENDPOINT="$HF_ENDPOINT" -e REPO="$TTS_MODEL_REPO" -e DEST="$TTS_MODEL_DIR" \
    -v "$(dirname "$TTS_MODEL_DIR"):$(dirname "$TTS_MODEL_DIR")" python:3.10-slim bash -c '
      pip install -q huggingface_hub
      python - <<PY
import os
from huggingface_hub import snapshot_download
snapshot_download(os.environ["REPO"], local_dir=os.environ["DEST"])
print("downloaded ->", os.environ["DEST"])
PY'
else
  echo "[tts] 模型已就绪"
fi

echo "[tts] 启动 vLLM-omni TTS 服务 ..."
# vLLM-omni runtime: installed by install-vllm-omni.sh into a dedicated venv.
# The voice-clone routes (/v1/audio/voices) are upstream vLLM-omni features —
# see docs/api/tts-voice-contract.md.
VENVDIR="${TTS_VENV_DIR:-$PL_DATA_DIR/venvs/vllm-omni}"
TTS_GPU="${TTS_GPU:-0}"
TTS_LOG="${TTS_LOG:-$PL_DATA_DIR/logs/tts.log}"
if ! { [ -x "$VENVDIR/bin/vllm" ] && "$VENVDIR/bin/python" -c "import vllm_omni" 2>/dev/null; }; then
  echo "[tts] 安装 vLLM-omni 运行时（首次需 ~10-20 分钟）..."
  bash "$SETUP_DIR/install-vllm-omni.sh"
fi
mkdir -p "$(dirname "$TTS_LOG")"
echo "[tts] 启动: vllm serve $TTS_MODEL_DIR --omni --port ${TTS_URL##*:} (GPU $TTS_GPU, log: $TTS_LOG)"
CUDA_VISIBLE_DEVICES="$TTS_GPU" nohup "$VENVDIR/bin/vllm" serve "$TTS_MODEL_DIR" \
  --omni --port "${TTS_URL##*:}" --trust-remote-code \
  --served-model-name qwen3-tts-customvoice \
  > "$TTS_LOG" 2>&1 < /dev/null &

echo "[tts] 等待 $TTS_URL/v1/models（模型加载 ~2-5 分钟）..."
for i in $(seq 1 120); do
  curl -sf -m3 "$TTS_URL/v1/models" >/dev/null 2>&1 && { echo "[tts] READY"; exit 0; }
  sleep 3
done
echo "[tts] 超时 — 查看 $TTS_LOG"; exit 1
