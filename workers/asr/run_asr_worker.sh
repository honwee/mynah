#!/bin/bash
# Mynah ASR worker (livetalking conda env: funasr+grpc+PyAV)
# usage: run_asr_worker.sh [GPU] [PORT]
GPU=${1:-0}
PORT=${2:-9402}
cd "$(dirname "$0")"
exec env CUDA_VISIBLE_DEVICES=$GPU \
  python server.py \
  --port "$PORT" --device cuda:0
