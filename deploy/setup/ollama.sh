#!/usr/bin/env bash
# Mynah setup · LLM (default: ollama + qwen2.5:7b, OpenAI-compatible).
# Idempotent: skips if ollama is up and the model is already pulled.
set -euo pipefail
LLM_URL="${LLM_URL:-http://127.0.0.1:11434}"
LLM_MODEL="${LLM_MODEL:-qwen2.5:7b-instruct-q4_K_M}"
# China note: ollama's registry can be slow; OLLAMA_HOST/registry mirror is overridable
# by exporting OLLAMA_* before running this script.

up() { curl -sf -m3 "$LLM_URL/api/tags" >/dev/null 2>&1; }
has_model() { curl -sf -m3 "$LLM_URL/api/tags" 2>/dev/null | grep -q "\"$LLM_MODEL\""; }

if ! up; then
  echo "[llm] ollama 未运行 — 启动容器 ..."
  docker run -d --name pl-ollama --restart unless-stopped \
    -p 11434:11434 -v pl-ollama:/root/.ollama --gpus all ollama/ollama >/dev/null 2>&1 \
    || docker start pl-ollama >/dev/null 2>&1 || true
  for i in $(seq 1 30); do up && break; sleep 2; done
fi
up || { echo "[llm] 无法连上 ollama ($LLM_URL)"; exit 1; }

if has_model; then
  echo "[llm] 模型 $LLM_MODEL 已就绪 — 跳过下载"; exit 0
fi

echo "[llm] 拉取 $LLM_MODEL（数 GB，断点续传，进度如下）..."
# Stream pull progress from the ollama API (works whether ollama is host-native or our container).
curl -sN "$LLM_URL/api/pull" -d "{\"name\":\"$LLM_MODEL\"}" | while IFS= read -r line; do
  echo "$line" | python3 -c "import sys,json
for l in sys.stdin:
    try: d=json.loads(l)
    except Exception: continue
    s=d.get('status','')
    if 'total' in d and d.get('completed'):
        pct=100*d['completed']/d['total']; print('  %s  %5.1f%%'%(s,pct))
    elif s: print('  '+s)" 2>/dev/null || echo "  $line"
done
has_model && { echo "[llm] READY"; exit 0; } || { echo "[llm] 拉取后仍未见模型"; exit 1; }
