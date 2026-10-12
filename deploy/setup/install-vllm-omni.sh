#!/usr/bin/env bash
# Mynah setup · vLLM-omni TTS runtime (the reference TTS engine).
#
# Installs the upstream vLLM-omni package (Apache-2.0, vllm-project/vllm-omni)
# into a dedicated venv and leaves a ready-to-run `vllm serve` launcher. The
# voice-clone contract (/v1/audio/voices upload/list/delete) is an upstream
# vLLM-omni feature — no Mynah patches. See docs/api/tts-voice-contract.md.
#
# Requirements: an NVIDIA GPU with CUDA 12 driver, python3.10+, ~20GB disk.
# Idempotent: skips work that is already done.
set -euo pipefail

PL_DATA_DIR="${PL_DATA_DIR:-$(cd "$(dirname "$0")/../.." && pwd)/.data}"
VENVDIR="${TTS_VENV_DIR:-$PL_DATA_DIR/venvs/vllm-omni}"
VLLM_OMNI_VERSION="${VLLM_OMNI_VERSION:-}"   # empty = latest release
TTS_PORT="${TTS_PORT:-8091}"
TTS_MODEL_DIR="${TTS_MODEL_DIR:-$PL_DATA_DIR/models/qwen3-tts/Qwen3-TTS-12Hz-0.6B-CustomVoice}"
# PyPI mirror for CN networks; unset PIP_INDEX_URL to use pypi.org.
export PIP_INDEX_URL="${PIP_INDEX_URL:-https://pypi.tuna.tsinghua.edu.cn/simple}"

if [ -x "$VENVDIR/bin/vllm" ] && "$VENVDIR/bin/python" -c "import vllm_omni" 2>/dev/null; then
  echo "[vllm-omni] 已安装于 $VENVDIR — 跳过安装"
else
  echo "[vllm-omni] 创建 venv $VENVDIR ..."
  mkdir -p "$(dirname "$VENVDIR")"
  python3 -m venv "$VENVDIR"
  "$VENVDIR/bin/pip" install -q --upgrade pip
  PKG="vllm-omni${VLLM_OMNI_VERSION:+==$VLLM_OMNI_VERSION}"
  echo "[vllm-omni] pip install $PKG (拉取匹配的 cu12 vLLM + torch, 需要一段时间) ..."
  "$VENVDIR/bin/pip" install "$PKG"
  echo "[vllm-omni] 验证 ..."
  "$VENVDIR/bin/python" - <<'PY'
import torch, vllm, vllm_omni
print("torch", torch.__version__, "| vllm", vllm.__version__, "| vllm_omni OK")
PY
fi

cat <<EOF

[vllm-omni] READY. 启动 TTS 服务:

  CUDA_VISIBLE_DEVICES=<gpu> $VENVDIR/bin/vllm serve $TTS_MODEL_DIR \\
    --omni --port $TTS_PORT --trust-remote-code \\
    --served-model-name qwen3-tts-customvoice

(模型下载见 tts.sh; deploy-config 可选: vllm_omni/deploy/qwen3_tts.yaml)
EOF
