#!/usr/bin/env bash
# Mynah setup · wav2lipLS (light/low-VRAM avatar engine, opt-in).
# Fetches the three assets wav2lipLS needs into $PL_MODELS_DIR (bind-mounted into
# the worker at the same path), then builds + starts the worker:
#   1. wav2lipLS checkpoint   ($WAV2LIPLS_CKPT_URL  -> wav2lipls/wav2lipls-<size>.pth)
#   2. default baked avatar   ($DEFAULT_AVATAR_URL  -> avatars/personalive-default/)
#   3. HuBERT feature model   (facebook/hubert-large-ls960-ft, via HF mirror)
# All idempotent + mirror-aware (China-friendly). Host needs only docker.
set -euo pipefail
SETUP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_DIR="$SETUP_DIR/../compose"
AVATAR_PORT="${AVATAR_PORT:-9420}"
FACE_SIZE="${FACE_SIZE:-384}"
export HF_ENDPOINT="${HF_ENDPOINT:-https://hf-mirror.com}"   # China-friendly HF mirror

PL_MODELS_DIR="${PL_MODELS_DIR:-$(cd "$(dirname "$0")/../.." && pwd)/.data/models}"
CKPT="${WAV2LIPLS_CKPT:-$PL_MODELS_DIR/wav2lipls/wav2lipls-$FACE_SIZE.pth}"
AVATAR_DIR="${AVATAR_DIR:-$PL_MODELS_DIR/avatars/personalive-default}"
HUBERT_DIR="${WAV2LIPLS_HUBERT_DIR:-$PL_MODELS_DIR/hubert-large-ls960-ft}"

if (exec 3<>/dev/tcp/127.0.0.1/"$AVATAR_PORT") 2>/dev/null; then
  echo "[avatar] 已在 :$AVATAR_PORT 运行 — 跳过"; exit 0
fi

# Fetch a plain URL (tarball or file) into a target. Resumable. Containerized so
# the host needs no curl/python. Tarballs (.tar.gz/.tgz) are unpacked into a dir.
fetch_url() {  # $1=url  $2=dest (file or dir)  $3=kind(file|tar)
  local url="$1" dest="$2" kind="$3"
  docker run --rm -e URL="$url" -e DEST="$dest" -e KIND="$kind" \
    -v "$PL_MODELS_DIR:$PL_MODELS_DIR" python:3.10-slim bash -c '
      set -e
      if [ "$KIND" = tar ]; then
        mkdir -p "$DEST"
        echo "  下载并解包 -> $DEST"
        curl -fL --retry 3 -C - "$URL" -o /tmp/a.tgz
        tar -xzf /tmp/a.tgz -C "$DEST" --strip-components=0
      else
        mkdir -p "$(dirname "$DEST")"
        echo "  下载 -> $DEST"
        curl -fL --retry 3 -C - "$URL" -o "$DEST"
      fi'
}

# 1) wav2lipLS checkpoint
if [ -f "$CKPT" ]; then
  echo "[avatar] checkpoint 已就绪: $CKPT"
elif [ -n "${WAV2LIPLS_CKPT_URL:-}" ]; then
  echo "[avatar] 下载 wav2lipLS checkpoint ..."
  fetch_url "$WAV2LIPLS_CKPT_URL" "$CKPT" file
else
  echo "[avatar] ✗ 缺少 checkpoint 且未配置 WAV2LIPLS_CKPT_URL。"
  echo "         在 deploy/compose/.env 设置 WAV2LIPLS_CKPT_URL，或把 .pth 放到 $CKPT"
  exit 1
fi

# 2) default baked avatar (full_imgs/face_imgs/coords.pkl)
if [ -f "$AVATAR_DIR/coords.pkl" ]; then
  echo "[avatar] 默认形象已就绪: $AVATAR_DIR"
elif [ -n "${DEFAULT_AVATAR_URL:-}" ]; then
  echo "[avatar] 下载默认数字人形象 ..."
  fetch_url "$DEFAULT_AVATAR_URL" "$AVATAR_DIR" tar
else
  echo "[avatar] ✗ 缺少默认形象且未配置 DEFAULT_AVATAR_URL。"
  echo "         在 .env 设置 DEFAULT_AVATAR_URL，或把已烘形象放到 $AVATAR_DIR"
  echo "         （需 full_imgs/ face_imgs/ coords.pkl）"
  exit 1
fi

# 3) HuBERT feature extractor (Apache-2.0) — via HF mirror
if [ -f "$HUBERT_DIR/config.json" ]; then
  echo "[avatar] HuBERT 已就绪"
else
  echo "[avatar] 下载 HuBERT 特征模型 (HF_ENDPOINT=$HF_ENDPOINT) ..."
  docker run --rm -e HF_ENDPOINT="$HF_ENDPOINT" -e REPO=facebook/hubert-large-ls960-ft -e DEST="$HUBERT_DIR" \
    -v "$(dirname "$HUBERT_DIR"):$(dirname "$HUBERT_DIR")" python:3.10-slim bash -c '
      pip install -q huggingface_hub
      python - <<PY
import os
from huggingface_hub import snapshot_download
snapshot_download(os.environ["REPO"], local_dir=os.environ["DEST"])
print("downloaded ->", os.environ["DEST"])
PY'
fi

echo "[avatar] 构建并启动 wav2lipLS 服务 ..."
cd "$COMPOSE_DIR"
export AVATAR_GPU="${GPU:-${AVATAR_GPU:-2}}"   # GPU picked in the wizard
echo "[avatar] GPU=$AVATAR_GPU"
docker compose --profile wav2lipls up -d --build avatar

echo "[avatar] 等待 :$AVATAR_PORT 就绪 ..."
for i in $(seq 1 90); do
  (exec 3<>/dev/tcp/127.0.0.1/"$AVATAR_PORT") 2>/dev/null && { echo "[avatar] READY"; exit 0; }
  sleep 2
done
echo "[avatar] 超时未就绪 — 查看: docker compose -f $COMPOSE_DIR/docker-compose.yml logs avatar"; exit 1
