#!/usr/bin/env bash
# Mynah — one-command FlashHead provisioning (out-of-the-box setup).
#
# FlashHead's engine code + weights are intentionally NOT vendored into this repo:
# the model is the separate Apache-2.0 SoulX-FlashHead project and its weights
# (several GB) live on HuggingFace. This script fetches them into $FLASHHEAD_DIR
# so `docker compose --profile flashhead up` then works with zero further steps.
#
# Turnkey by design:
#   - Host needs only `git` + `docker`. NO Python / huggingface-cli / GPU on the
#     host — the weight download runs inside a throwaway python:3.10-slim container.
#   - Weights are public (Apache-2.0) — no HuggingFace token required.
#   - Idempotent + resumable: safe to re-run; finished files are skipped.
#
# Usage:   bash deploy/compose/setup-flashhead.sh
#          (override target dir with FLASHHEAD_DIR=/path bash ...; it also reads
#           FLASHHEAD_DIR from a sibling .env if present.)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Resolve FLASHHEAD_DIR: explicit env > .env file > docker-compose.yml default.
if [ -z "${FLASHHEAD_DIR:-}" ] && [ -f "$SCRIPT_DIR/.env" ]; then
  FLASHHEAD_DIR="$(grep -E '^FLASHHEAD_DIR=' "$SCRIPT_DIR/.env" 2>/dev/null | tail -1 | cut -d= -f2- || true)"
fi
# Where to clone. Under this checkout by default so a fresh box needs no
# pre-provisioned data disk; set FLASHHEAD_DIR (same value the compose stack
# gets) to put it anywhere else.
FLASHHEAD_DIR="${FLASHHEAD_DIR:-$(cd "$(dirname "$0")/../.." && pwd)/.data/SoulX-FlashHead}"
REPO_URL="${FLASHHEAD_REPO:-https://github.com/Soul-AILab/SoulX-FlashHead.git}"

echo "==> FlashHead target: $FLASHHEAD_DIR"

# 1) Clone the engine code (idempotent; the github repo is code-only, ~MB).
if [ -f "$FLASHHEAD_DIR/flash_head/inference.py" ]; then
  echo "==> [1/3] engine code already present — skip clone"
else
  echo "==> [1/3] cloning SoulX-FlashHead ..."
  git clone --depth 1 "$REPO_URL" "$FLASHHEAD_DIR"
fi

# 2) Make the torch.compile gates env-configurable (idempotent, version-tolerant).
#    Equivalent to flashhead-compile-env.patch but applied via sed so it survives
#    upstream line drift. `import os` is already at the top of that file upstream.
PIPE="$FLASHHEAD_DIR/flash_head/src/pipeline/flash_head_pipeline.py"
if [ -f "$PIPE" ] && grep -q "FLASHHEAD_COMPILE_MODEL" "$PIPE"; then
  echo "==> [2/3] compile-env toggle already applied — skip"
elif [ -f "$PIPE" ]; then
  echo "==> [2/3] enabling FLASHHEAD_COMPILE_MODEL/VAE env toggle ..."
  sed -i \
    -e 's/^COMPILE_MODEL = True/COMPILE_MODEL = os.environ.get("FLASHHEAD_COMPILE_MODEL", "1") == "1"/' \
    -e 's/^COMPILE_VAE = True/COMPILE_VAE = os.environ.get("FLASHHEAD_COMPILE_VAE", "1") == "1"/' \
    "$PIPE"
else
  echo "==> [2/3] WARNING: $PIPE not found — upstream layout changed; skipping toggle"
fi

# 3) Download model weights into $FLASHHEAD_DIR/models (resumable; runs in a
#    container so the host needs no Python). Public repos — no token needed.
#    Mirror-aware: HF_ENDPOINT defaults to hf-mirror.com (China-friendly).
export HF_ENDPOINT="${HF_ENDPOINT:-https://hf-mirror.com}"
echo "==> [3/3] downloading weights (several GB, resumable, HF_ENDPOINT=$HF_ENDPOINT) ..."
docker run --rm -e HF_ENDPOINT="$HF_ENDPOINT" -v "$FLASHHEAD_DIR:/fh" python:3.10-slim bash -c '
  set -e
  pip install -q huggingface_hub
  python - <<PY
from huggingface_hub import snapshot_download
snapshot_download("Soul-AILab/SoulX-FlashHead-1_3B", local_dir="/fh/models/SoulX-FlashHead-1_3B")
snapshot_download("facebook/wav2vec2-base-960h", local_dir="/fh/models/wav2vec2-base-960h")
print("weights ready")
PY
'

echo ""
echo "==> FlashHead ready at $FLASHHEAD_DIR"
echo "    FlashHead is the DEFAULT avatar engine — just start the stack:"
echo "    docker compose up -d --build cored flashhead turn"
