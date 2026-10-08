#!/usr/bin/env bash
# Mynah cloud-image bootstrap (AutoDL / UCloud): native install, no Docker.
# Usage: bash deploy/cloud/bootstrap.sh [--engine wav2lipls|flashhead] [--prefix ~/mynah]
# Idempotent; re-run to resume. UNTESTED on the platforms yet — see README.md.
set -euo pipefail
ENGINE=wav2lipls; PREFIX="$HOME/mynah"
while [ $# -gt 0 ]; do case "$1" in --engine) ENGINE="$2"; shift 2;; --prefix) PREFIX="$2"; shift 2;; *) echo "unknown arg $1"; exit 2;; esac; done
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export HF_ENDPOINT="${HF_ENDPOINT:-https://hf-mirror.com}"
export PL_DATA_DIR="${PL_DATA_DIR:-$PREFIX/data}"
mkdir -p "$PREFIX" "$PL_DATA_DIR"
log(){ echo "[bootstrap] $*"; }

log "1/6 system packages"; (command -v ffmpeg >/dev/null && command -v git >/dev/null) || { apt-get update -qq && apt-get install -y -qq ffmpeg git curl; }

log "2/6 cored binary"
if [ ! -x "$PREFIX/cored" ]; then
  REL="https://github.com/honwee/mynah/releases/latest/download/cored-linux-amd64"
  if curl -fsSL --retry 3 "$REL" -o "$PREFIX/cored" 2>/dev/null; then chmod +x "$PREFIX/cored"
  elif command -v go >/dev/null; then (cd "$REPO" && go build -o "$PREFIX/cored" ./cmd/cored)
  else echo "no release binary and no Go toolchain — install Go 1.22+ or publish a release"; exit 1; fi
fi

log "3/6 conda envs"
eval "$(conda shell.bash hook)"
conda env list | grep -q '^mynah-tts ' || conda create -y -q -n mynah-tts python=3.11
conda run -n mynah-tts pip install -q -r "$REPO/deploy/compose/requirements-tts-edge.txt"
conda env list | grep -q '^mynah-asr ' || conda create -y -q -n mynah-asr python=3.10
conda run -n mynah-asr pip install -q -r "$REPO/deploy/compose/requirements-asr.txt"
conda env list | grep -q '^mynah-avatar ' || conda create -y -q -n mynah-avatar python=3.10
conda run -n mynah-avatar pip install -q -r "$REPO/deploy/compose/requirements-avatar.txt"

log "4/6 models (ASR via ModelScope mirror; avatar assets for $ENGINE)"
conda run -n mynah-asr python - <<'PY'
from modelscope import snapshot_download
for m in ("iic/SenseVoiceSmall","iic/speech_fsmn_vad_zh-cn-16k-common-pytorch"): snapshot_download(m)
PY
case "$ENGINE" in
  wav2lipls) ENGINE=wav2lipls PL_MODELS_DIR="$PL_DATA_DIR/models" NO_DOCKER=1 bash "$REPO/deploy/setup/wav2lipls.sh" --fetch-only || log "wav2lipls assets: see deploy/setup/wav2lipls.sh (fetch step needs the asset URLs)";;
  flashhead) bash "$REPO/deploy/compose/setup-flashhead.sh" --no-docker || log "flashhead setup: see deploy/compose/setup-flashhead.sh";;
esac

log "5/6 TLS (self-signed) + start/stop scripts"
mkdir -p "$PREFIX/tls"; [ -f "$PREFIX/tls/cored.crt" ] || openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj "/CN=mynah" -keyout "$PREFIX/tls/cored.key" -out "$PREFIX/tls/cored.crt" 2>/dev/null
cat > "$PREFIX/start.sh" <<EOS
#!/usr/bin/env bash
set -e; cd "$PREFIX"; eval "\$(conda shell.bash hook)"
export PERSONALIVE_GEN="$REPO/gen/python" PL_DATA_DIR="$PL_DATA_DIR"
wait_port(){ for i in \$(seq 1 180); do (exec 3<>/dev/tcp/127.0.0.1/\$1) 2>/dev/null && return 0; sleep 2; done; echo "timeout waiting :\$1"; return 1; }
conda run -n mynah-tts --no-capture-output env TTS_EDGE_PORT=8091 python "$REPO/workers/tts-edge/server.py" > logs-tts.log 2>&1 &
conda run -n mynah-asr --no-capture-output bash "$REPO/workers/asr/run_asr_worker.sh" 0 9402 > logs-asr.log 2>&1 &
conda run -n mynah-avatar --no-capture-output bash "$REPO/workers/avatar/run_${ENGINE}_worker.sh" 0 9421 > logs-avatar.log 2>&1 &
wait_port 8091; wait_port 9402; wait_port 9421
./cored --listen=:8020 --tls-listen=:8443 --tls-cert=tls/cored.crt --tls-key=tls/cored.key \\
  --worker=127.0.0.1:9421 --tts=http://127.0.0.1:8091 --voice=zh-CN-XiaoxiaoNeural \\
  --asr=127.0.0.1:9402 --web="$REPO/web/user" --admin-web="$REPO/web/admin" \\
  --admin-tls-listen=:9443 --stun=stun:stun.l.google.com:19302 --video-codec=h264 > logs-cored.log 2>&1 &
echo "Mynah is up: visitor https://<host>:8443/  console https://<host>:9443/  (self-signed cert)"
EOS
cat > "$PREFIX/stop.sh" <<'EOS'
#!/usr/bin/env bash
pkill -f "tts-edge/server.py" ; pkill -f "run_asr_worker.sh|asr/server.py" ; pkill -f "wav2lipls_server.py|flashhead|server.py --port 9421" ; pkill -f "cored --listen" ; echo stopped
EOS
chmod +x "$PREFIX/start.sh" "$PREFIX/stop.sh"
log "6/6 done. Start with: $PREFIX/start.sh"
