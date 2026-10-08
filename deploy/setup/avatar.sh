#!/usr/bin/env bash
# Mynah setup · Avatar (DEFAULT engine: FlashHead).
# FlashHead (Apache-2.0 SoulX-FlashHead) speaks the bundled default portrait, so
# the only provisioning is: fetch the engine repo + weights, then start the
# worker. Host needs only git + docker. Idempotent + mirror-aware (China-friendly).
set -euo pipefail
SETUP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_DIR="$SETUP_DIR/../compose"
FLASHHEAD_PORT="${FLASHHEAD_PORT:-9421}"
export HF_ENDPOINT="${HF_ENDPOINT:-https://hf-mirror.com}"   # China-friendly HF mirror

if (exec 3<>/dev/tcp/127.0.0.1/"$FLASHHEAD_PORT") 2>/dev/null; then
  echo "[avatar] FlashHead 已在 :$FLASHHEAD_PORT 运行 — 跳过"; exit 0
fi

# 1) Engine repo + weights (clone SoulX-FlashHead, apply compile toggle, download
#    Apache-2.0 weights into FLASHHEAD_DIR). Idempotent + resumable; mirror-aware.
echo "[avatar] 准备 FlashHead 引擎与权重 ..."
bash "$COMPOSE_DIR/setup-flashhead.sh"

# 2) Build + start the FlashHead worker on the chosen GPU.
echo "[avatar] 构建并启动 FlashHead 服务 ..."
cd "$COMPOSE_DIR"
export FLASHHEAD_GPU="${GPU:-${FLASHHEAD_GPU:-${AVATAR_GPU:-2}}}"   # GPU picked in the wizard
echo "[avatar] GPU=$FLASHHEAD_GPU"
docker compose up -d --build flashhead

echo "[avatar] 等待 :$FLASHHEAD_PORT 就绪（首次含模型加载，请耐心）..."
for i in $(seq 1 150); do
  (exec 3<>/dev/tcp/127.0.0.1/"$FLASHHEAD_PORT") 2>/dev/null && { echo "[avatar] READY"; exit 0; }
  sleep 2
done
echo "[avatar] 超时未就绪 — 查看: docker compose -f $COMPOSE_DIR/docker-compose.yml logs flashhead"; exit 1
