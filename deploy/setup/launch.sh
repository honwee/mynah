#!/usr/bin/env bash
# Mynah setup · Launch — bring up cored once components are green.
set -euo pipefail
SETUP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SETUP_DIR/../compose"
echo "[launch] 启动 cored ..."
docker compose up -d cored
echo "[launch] cored 已启动 — 打开 https://localhost:8455/"
