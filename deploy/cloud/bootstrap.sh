#!/usr/bin/env bash
# Mynah cloud-image bootstrap — native install for GPU-rental containers
# (AutoDL, UCloud 优云智算) where there is no Docker daemon.
#
#   git clone https://github.com/honwee/mynah.git ~/mynah/src
#   bash ~/mynah/src/deploy/cloud/bootstrap.sh [--llm firstrun|bundled|none] [--prefix ~/mynah]
#   ~/mynah/mynahctl start
#
# What it builds (see deploy/cloud/README.md):
#   <prefix>/bin/cored            release binary (MYNAH_CORED_BIN=<file> to use your own)
#   conda env "mynah"             FlashHead + SenseVoice + Smart Turn + EdgeTTS, one interpreter
#   /opt/mynah/pg + pgdata        PostgreSQL 16 + pgvector (conda-forge), runs as user "mynah"
#   <prefix>/SoulX-FlashHead      upstream repo + Lite weights only (~8.4 GB, not 15)
#   <prefix>/presets              baked idle + gesture clips for Luna / Nova
#   <prefix>/ollama + LLM         qwen2.5 7B via ollama: pulled now (--llm bundled) or on
#                                 first start (--llm firstrun, default: AutoDL's 30 GB system
#                                 disk cannot hold it; the data disk is not part of the image)
#   <prefix>/env, mynahctl        ports / paths / GPU in one file; start|stop|status|logs
#
# Idempotent: every step checks before it downloads, re-run to resume.
# Mirrors (all overridable): PIP_INDEX_URL tuna, TORCH_INDEX_URL sjtu cu121,
# HF_ENDPOINT hf-mirror.com, GH_PROXY = ordered GitHub proxy prefixes ("_" =
# direct), conda channels tuna.
set -euo pipefail

PREFIX="${MYNAH_PREFIX:-$HOME/mynah}"
LLM_MODE="${MYNAH_LLM:-firstrun}"
while [ $# -gt 0 ]; do
  case "$1" in
    --prefix) PREFIX="$2"; shift 2;;
    --llm) LLM_MODE="$2"; shift 2;;
    -h|--help) sed -n 2,25p "$0"; exit 0;;
    *) echo "unknown arg: $1 (see --help)"; exit 2;;
  esac
done
case "$LLM_MODE" in firstrun|bundled|none) ;; *) echo "--llm must be firstrun|bundled|none"; exit 2;; esac

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MYNAH_VERSION="${MYNAH_VERSION:-v0.1.0}"
# "_" stands for a direct attempt; order = preference.
GH_PROXY="${GH_PROXY-https://gh-proxy.com/ https://ghfast.top/ https://ghproxy.net/ _}"
export HF_ENDPOINT="${HF_ENDPOINT:-https://hf-mirror.com}"
export PIP_INDEX_URL="${PIP_INDEX_URL:-https://pypi.tuna.tsinghua.edu.cn/simple}"
TORCH_INDEX_URL="${TORCH_INDEX_URL:-https://mirror.sjtu.edu.cn/pytorch-wheels/cu121}"
CONDA_FORGE="${CONDA_FORGE:-https://mirrors.tuna.tsinghua.edu.cn/anaconda/cloud/conda-forge}"
CONDA_MAIN="${CONDA_MAIN:-https://mirrors.tuna.tsinghua.edu.cn/anaconda/pkgs/main}"
OLLAMA_VERSION="${OLLAMA_VERSION:-v0.32.14}"
LLM_MODEL="${LLM_MODEL:-qwen2.5:7b-instruct-q4_K_M}"
GPU="${MYNAH_GPU:-0}"
PG_PREFIX=/opt/mynah/pg
PG_DATA=/opt/mynah/pgdata
FH_DIR="$PREFIX/SoulX-FlashHead"
DATA="$PREFIX/data"
log(){ printf '\n[bootstrap %s] %s\n' "$(date +%H:%M:%S)" "$*"; }
die(){ echo "[bootstrap] ERROR: $*" >&2; exit 1; }

# GitHub downloads: try each proxy prefix in GH_PROXY (space-separated, "" =
# direct), abandoning any source that stalls under 50 KB/s for 30 s. Measured
# from a China host 2026-10: gh-proxy.com ~2 MB/s, ghfast.top stalls after
# its redirect, direct github.com 0 B/s.
gh_fetch(){ # url dest
  local url="$1" dest="$2" tmp="$2.part" pfx
  for pfx in $GH_PROXY ""; do
    [ "$pfx" = _ ] && pfx=""
    curl -fL --retry 1 -C - --connect-timeout 15 --speed-limit 51200 --speed-time 30 -o "$tmp" "${pfx}${url}" && { mv "$tmp" "$dest"; return 0; }
    echo "  (source ${pfx:-direct} failed, trying the next)"
  done
  return 1
}
gh_clone(){ # repo_url dest
  [ -d "$2/.git" ] && return 0
  local pfx
  for pfx in $GH_PROXY ""; do
    [ "$pfx" = _ ] && pfx=""
    rm -rf "$2"; git clone --depth 1 "${pfx}$1" "$2" && return 0
  done
  return 1
}

[ "$(id -u)" = 0 ] || die "run as root (the platforms' containers are root; Postgres gets its own user below)"
command -v nvidia-smi >/dev/null || die "nvidia-smi not found — this must run on a GPU instance"
mkdir -p "$PREFIX/bin" "$PREFIX/logs" "$PREFIX/run" "$PREFIX/tls" "$PREFIX/presets" "$DATA"/{cache,tmp,modelscope}
# AutoDL academic acceleration for GitHub/HF if present (harmless elsewhere).
[ -f /etc/network_turbo ] && { set +u; . /etc/network_turbo; set -u; } || true

log "1/9 system packages"
# binaries to check -> only used to decide whether apt needs to run at all;
# the install line names *packages* (setfacl lives in `acl`).
need=0; for b in ffmpeg git curl openssl setfacl zstd; do command -v "$b" >/dev/null || need=1; done
dpkg -s libgl1 >/dev/null 2>&1 || need=1
if [ "$need" = 1 ]; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq && apt-get install -y -qq --no-install-recommends ffmpeg git curl openssl acl zstd libgl1 libglib2.0-0 libgomp1 ca-certificates
  rm -rf /var/lib/apt/lists/*
fi
id mynah >/dev/null 2>&1 || useradd -r -m -d /opt/mynah/home -s /bin/bash mynah
mkdir -p /opt/mynah && chmod 755 /opt/mynah

log "2/9 source tree + cored $MYNAH_VERSION"
[ "$SRC" = "$PREFIX/src" ] || ln -sfn "$SRC" "$PREFIX/src"
if [ -n "${MYNAH_CORED_BIN:-}" ]; then
  install -m 755 "$MYNAH_CORED_BIN" "$PREFIX/bin/cored"
elif [ ! -x "$PREFIX/bin/cored" ]; then
  gh_fetch "https://github.com/honwee/mynah/releases/download/$MYNAH_VERSION/cored-linux-amd64" "$PREFIX/bin/cored" \
    || { command -v go >/dev/null && (cd "$SRC" && go build -o "$PREFIX/bin/cored" ./cmd/cored); } \
    || die "could not download cored $MYNAH_VERSION and no Go toolchain to build it"
  chmod +x "$PREFIX/bin/cored"
fi
# (capture, don't pipe into grep -q: with pipefail an early-closing grep turns
# cored's SIGPIPE exit into a spurious failure)
cored_help="$("$PREFIX/bin/cored" -h 2>&1 || true)"
case "$cored_help" in *-ice-tcp*) ;; *) die "$PREFIX/bin/cored is older than the cloud recipe needs (no --ice-tcp); set MYNAH_VERSION or MYNAH_CORED_BIN";; esac
if [ ! -f "$PREFIX/presets/idle_luna__builtin_d9.h264f" ]; then
  gh_fetch "https://github.com/honwee/mynah/releases/download/$MYNAH_VERSION/mynah-presets-$MYNAH_VERSION.tar.gz" /tmp/mynah-presets.tgz
  tar xzf /tmp/mynah-presets.tgz -C "$PREFIX/presets" && rm -f /tmp/mynah-presets.tgz
fi

log "3/9 conda"
CONDA_ROOT="${CONDA_ROOT:-}"
for c in "$CONDA_ROOT" "$HOME/miniconda3" /root/miniconda3 /opt/conda /opt/miniconda3; do [ -n "$c" ] && [ -x "$c/bin/conda" ] && { CONDA_ROOT="$c"; break; }; done
if [ -z "$CONDA_ROOT" ] && command -v conda >/dev/null; then CONDA_ROOT="$(conda info --base)"; fi
if [ -z "$CONDA_ROOT" ]; then
  CONDA_ROOT="$HOME/miniconda3"
  curl -fL --retry 3 -o /tmp/mc.sh https://mirrors.tuna.tsinghua.edu.cn/anaconda/miniconda/Miniconda3-py310_24.9.2-0-Linux-x86_64.sh
  bash /tmp/mc.sh -b -p "$CONDA_ROOT" && rm -f /tmp/mc.sh
fi
. "$CONDA_ROOT/etc/profile.d/conda.sh"
conda config --set always_yes true --set auto_activate_base false >/dev/null 2>&1 || true

log "4/9 conda env mynah (torch cu121 + FlashHead + SenseVoice + Smart Turn + EdgeTTS)"
conda env list | grep -q "^mynah " || conda create -q -n mynah -c "$CONDA_MAIN" --override-channels python=3.10
conda activate mynah
if ! python -c "import torch, funasr, onnxruntime, edge_tts, diffusers" 2>/dev/null; then
  # One merged requirements file: the FlashHead pins with their PyTorch index
  # replaced by the mirror (pip would otherwise happily fetch torch from
  # download.pytorch.org at ~100 KB/s from China), then the cloud extras.
  { sed "s#--extra-index-url https://download.pytorch.org/whl/cu121#--extra-index-url $TORCH_INDEX_URL#" "$SRC/deploy/compose/requirements-flashhead.txt"
    grep -v '^#' "$SRC/deploy/cloud/requirements-cloud.txt"; } > /tmp/mynah-requirements.txt
  pip install -q -r /tmp/mynah-requirements.txt
fi
python -c "import torch; assert torch.cuda.is_available(), 'torch cannot see the GPU'; print('torch', torch.__version__, torch.cuda.get_device_name(0))"
conda deactivate

log "5/9 PostgreSQL 16 + pgvector at $PG_PREFIX (user mynah)"
if [ ! -x "$PG_PREFIX/bin/postgres" ]; then
  conda create -q -p "$PG_PREFIX" -c "$CONDA_FORGE" --override-channels "postgresql=16" pgvector
fi
# Postgres runs as "mynah": the prefix must be world-readable. Some hosts put
# default ACLs on new files (seen on a Docker data-root with inherited ACLs),
# which mode bits alone do not undo — strip them, then verify as that user.
setfacl -R -b /opt/mynah 2>/dev/null || true
chmod -R o+rX "$PG_PREFIX" 2>/dev/null || true
su -s /bin/bash mynah -c "$PG_PREFIX/bin/initdb --version" >/dev/null 2>&1 || die "user mynah cannot run $PG_PREFIX/bin/initdb — check permissions/ACLs under /opt/mynah"
PG_PASS_FILE="$PREFIX/.pgpass"
if [ ! -f "$PG_PASS_FILE" ]; then (umask 077; openssl rand -hex 12 > "$PG_PASS_FILE"); fi
if [ ! -f "$PG_DATA/PG_VERSION" ]; then
  mkdir -p "$PG_DATA" && chown mynah:mynah "$PG_DATA" && chmod 700 "$PG_DATA"
  cp "$PG_PASS_FILE" /tmp/.mynah-pgpw && chown mynah /tmp/.mynah-pgpw
  su -s /bin/bash mynah -c "$PG_PREFIX/bin/initdb -D $PG_DATA -U mynah --auth=scram-sha-256 --pwfile=/tmp/.mynah-pgpw -E UTF8 >/dev/null"
  rm -f /tmp/.mynah-pgpw
fi

log "6/9 FlashHead repo + Lite weights (~8.4 GB)"
gh_clone https://github.com/Soul-AILab/SoulX-FlashHead.git "$FH_DIR"
PIPE="$FH_DIR/flash_head/src/pipeline/flash_head_pipeline.py"
if [ -f "$PIPE" ] && ! grep -q FLASHHEAD_COMPILE_MODEL "$PIPE"; then
  sed -i -e 's/^COMPILE_MODEL = True/COMPILE_MODEL = os.environ.get("FLASHHEAD_COMPILE_MODEL", "1") == "1"/' \
         -e 's/^COMPILE_VAE = True/COMPILE_VAE = os.environ.get("FLASHHEAD_COMPILE_VAE", "1") == "1"/' "$PIPE"
fi
if [ -n "${MYNAH_SEED_FH_MODELS:-}" ]; then
  [ -e "$FH_DIR/models" ] || ln -s "$MYNAH_SEED_FH_MODELS" "$FH_DIR/models"
elif [ ! -f "$FH_DIR/models/SoulX-FlashHead-1_3B/Model_Lite/diffusion_pytorch_model.safetensors" ] || [ ! -f "$FH_DIR/models/wav2vec2-base-960h/config.json" ]; then
  conda activate mynah
  python - "$FH_DIR/models" <<'PY'
import sys; from huggingface_hub import snapshot_download
root = sys.argv[1]
snapshot_download("Soul-AILab/SoulX-FlashHead-1_3B", local_dir=f"{root}/SoulX-FlashHead-1_3B",
                  allow_patterns=["Model_Lite/*", "VAE_LTX/*", "config.json", "model_index.json", "README.md"])
snapshot_download("facebook/wav2vec2-base-960h", local_dir=f"{root}/wav2vec2-base-960h")
print("flashhead weights ready")
PY
  conda deactivate
fi

log "7/9 ASR models (SenseVoice small + fsmn-vad, ModelScope)"
# modelscope with an explicit MODELSCOPE_CACHE writes $CACHE/models/<org>/
# (no hub/); the ASR worker accepts either layout.
has_asr_models(){ [ -d "$DATA/modelscope/models/iic/SenseVoiceSmall" ] || [ -d "$DATA/modelscope/hub/models/iic/SenseVoiceSmall" ]; }
if [ -n "${MYNAH_SEED_MODELSCOPE:-}" ] && ! has_asr_models; then
  mkdir -p "$DATA/modelscope/models/iic"
  cp -r "$MYNAH_SEED_MODELSCOPE/hub/models/iic/SenseVoiceSmall" "$MYNAH_SEED_MODELSCOPE/hub/models/iic/speech_fsmn_vad_zh-cn-16k-common-pytorch" "$DATA/modelscope/models/iic/"
fi
if ! has_asr_models; then
  conda activate mynah
  MODELSCOPE_CACHE="$DATA/modelscope" python - <<'PY'
from modelscope import snapshot_download
for m in ("iic/SenseVoiceSmall", "iic/speech_fsmn_vad_zh-cn-16k-common-pytorch"):
    print(snapshot_download(m))
PY
  conda deactivate
fi

log "8/9 LLM runtime (ollama $OLLAMA_VERSION, model: $LLM_MODE)"
LLM_DIR="${MYNAH_LLM_DIR:-$DATA/llm}"
if [ -d /root/autodl-tmp ] && [ -z "${MYNAH_LLM_DIR:-}" ]; then LLM_DIR=/root/autodl-tmp/mynah-llm; fi
if [ "$LLM_MODE" != none ]; then
  if [ ! -x "$PREFIX/ollama/bin/ollama" ]; then
    gh_fetch "https://github.com/ollama/ollama/releases/download/$OLLAMA_VERSION/ollama-linux-amd64.tar.zst" /tmp/ollama.tar.zst
    mkdir -p "$PREFIX/ollama" && tar --use-compress-program=unzstd -xf /tmp/ollama.tar.zst -C "$PREFIX/ollama" && rm -f /tmp/ollama.tar.zst
  fi
  mkdir -p "$LLM_DIR"
fi

log "9/9 TLS, env, mynahctl"
[ -f "$PREFIX/tls/cored.crt" ] || openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj "/CN=mynah" \
  -keyout "$PREFIX/tls/cored.key" -out "$PREFIX/tls/cored.crt" 2>/dev/null
if [ ! -f "$PREFIX/env" ]; then
cat > "$PREFIX/env" <<ENV
# Mynah cloud image — everything mynahctl needs. Edit, then: mynahctl restart
PREFIX=$PREFIX
SRC=$PREFIX/src
CONDA_ROOT=$CONDA_ROOT
FH_DIR=$FH_DIR
PG_PREFIX=$PG_PREFIX
PG_DATA=$PG_DATA
GPU=$GPU

# Ports. Through an SSH tunnel forward TLS_PORT, ADMIN_PORT and ICE_TCP_PORT.
HTTP_PORT=8020
TLS_PORT=8443
ADMIN_PORT=9443
ADMIN_HTTP_PORT=9080
ICE_TCP_PORT=8444
TTS_PORT=8091
ASR_PORT=9402
ASR_HTTP_PORT=9404
TURN_PORT=9406
AVATAR_PORT=9421
PG_PORT=5432
LLM_PORT=11434

# WebRTC reachability. 127.0.0.1 = SSH-tunnel mode (AutoDL default: the
# instance has no public UDP). Put the instance's public IP here when it has a
# forwarded TCP port, or leave empty on a machine browsers can reach directly.
ICE_NAT_IP=127.0.0.1
STUN=

# Brain. LLM_MODE firstrun pulls LLM_MODEL into LLM_DIR on the first start.
LLM_MODE=$LLM_MODE
LLM_MODEL=$LLM_MODEL
LLM_DIR=$LLM_DIR
# Single-file GGUF imported on first start (ModelScope is fast from China); empty = ollama pull from the registry
LLM_GGUF_URL=${LLM_GGUF_URL:-https://modelscope.cn/models/lmstudio-community/Qwen2.5-7B-Instruct-GGUF/resolve/master/Qwen2.5-7B-Instruct-Q4_K_M.gguf}
VOICE=zh-CN-XiaoxiaoNeural

# FlashHead cold start: 0 = ~15 s, 1 = torch.compile (~2 min, higher steady fps)
FLASHHEAD_COMPILE=0
ENV
fi
install -m 755 "$SRC/deploy/cloud/mynahctl" "$PREFIX/mynahctl"

if [ "$LLM_MODE" = bundled ]; then
  log "pulling $LLM_MODEL now (--llm bundled)"
  "$PREFIX/mynahctl" pull-llm
fi

conda clean -afy >/dev/null 2>&1 || true
"$CONDA_ROOT/envs/mynah/bin/pip" cache purge >/dev/null 2>&1 || true
rm -rf "$DATA/tmp"/* 2>/dev/null || true
log "done. Disk used by the image parts:"
du -sh "$CONDA_ROOT/envs/mynah" "$PG_PREFIX" "$FH_DIR/models/" "$DATA/modelscope" "$PREFIX/ollama" "$LLM_DIR" 2>/dev/null || true
echo
echo "Start:   $PREFIX/mynahctl start"
echo "Status:  $PREFIX/mynahctl status"
echo "Tunnel:  ssh -N -L 8443:127.0.0.1:8443 -L 9443:127.0.0.1:9443 -L 8444:127.0.0.1:8444 <this instance>"
echo "Then:    https://127.0.0.1:8443/   console https://127.0.0.1:9443/"
