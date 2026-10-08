#!/bin/bash
###############################################################################
# Bake a per-avatar living-standby idle loop from a source portrait + a
# LivePortrait motion template (.pkl). Produces a cored-replayable .h264f.
#
#   Stage A: render a neutral FlashHead frame from the portrait  (flashhead env)
#   Stage B: LivePortrait motion transfer, driven by the .pkl    (liveportrait env)
#            InsightFace BYPASSED (--no-flag_do_crop) so idle frames are
#            pixel-aligned with the speaking engine (pop ~= 0).
#   Stage C: split the animation to a 512x512 BGR PNG frame library  (ffmpeg)
#   Stage D: encode the loop to idle_<name>.h264f + silence_20ms.opus (flashhead env)
#
# This is the productized form of gotalk/.../tools/bake_idle_liveportrait.sh,
# generalized to take an arbitrary motion .pkl as the driving (the .sh hardcoded
# assets/examples/driving/<name>.mp4). LivePortrait's inference.py natively
# loads a .pkl driving via is_template(args.driving).
#
# Usage:
#   bake_idle_asset.sh --cond <portrait.jpg> --pkl <motion.pkl> \
#                      --out-dir <dir> --name <avatar> [--frames 300 --mult 0.65]
#
# Tool paths are env-overridable (mirrors run_avatartrain_worker.sh):
#   PL_LIVEPORTRAIT_ROOT PL_LIVEPORTRAIT_PY PL_FLASHHEAD_PY PL_FLASHHEAD_DIR
#   PL_RENDER_NEUTRAL PL_BAKE_IDLE_PY PL_BAKE_GPU
###############################################################################
set -euo pipefail

# The three stage dependencies are separate checkouts — required, not guessed.
# They used to default under /data/personalive; when that machine was rebuilt the
# defaults silently pointed at nothing and bakes died mid-subprocess.
need() { # need VAR "what it is"
  if [ -z "${!1:-}" ] || [ ! -e "${!1}" ]; then
    echo "bake_idle_asset.sh: set $1 ($2)" >&2; exit 2
  fi
}
need PL_LIVEPORTRAIT_ROOT "the LivePortrait checkout (stage B: motion transfer)"
need PL_FLASHHEAD_DIR "the SoulX-FlashHead checkout (stage A: neutral frame)"
need PL_RENDER_NEUTRAL "LiveTalking's tools/render_neutral.py"
LP=$PL_LIVEPORTRAIT_ROOT
LPPY=${PL_LIVEPORTRAIT_PY:-python}
FHPY=${PL_FLASHHEAD_PY:-python}
FLASHHEAD_DIR=$PL_FLASHHEAD_DIR
RENDER_NEUTRAL=$PL_RENDER_NEUTRAL
# bake_idle.py ships in this repo next to the avatar worker — default relative
# to this script so a checkout works with zero env.
BAKE_IDLE=${PL_BAKE_IDLE_PY:-"$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../avatar/bake_idle.py"}
GPU=${PL_BAKE_GPU:-0}  # 0, not an index from the old 8-GPU box

COND=""; PKL=""; OUTDIR=""; NAME=""; FRAMES=300; MULT=0.65
HALFBODY=""; BLEND=""
while [ $# -gt 0 ]; do case "$1" in
  --cond)     COND=$2;     shift 2;;
  --pkl)      PKL=$2;      shift 2;;
  --out-dir)  OUTDIR=$2;   shift 2;;
  --name)     NAME=$2;     shift 2;;
  --frames)   FRAMES=$2;   shift 2;;
  --mult)     MULT=$2;     shift 2;;
  --halfbody) HALFBODY=$2; shift 2;;   # half-body canvas (== engine cond + paste-back body)
  --blend)    BLEND=$2;    shift 2;;   # precomputed <id>.blend.npz (mask+crop_box+face_box)
  *) echo "unknown arg: $1" >&2; exit 1;;
esac; done
# Half-body: the engine cond IS the half-body canvas (it face-crops internally),
# so default --cond to it when the caller only gave --halfbody.
[ -n "$HALFBODY" ] && [ -z "$COND" ] && COND="$HALFBODY"
# Loosened crop (halfbody.py recrop): <id>.cond.png is the pre-cut square the
# live worker conditions on with face-crop OFF — stage A must do the same, or
# the idle head would be framed differently from the speaking head.
FACE_CROP=1
if [ -n "$HALFBODY" ] && [ -f "${HALFBODY%.halfbody.png}.cond.png" ]; then
  COND="${HALFBODY%.halfbody.png}.cond.png"; FACE_CROP=0
fi
[ -z "$COND" ]   && { echo "need --cond <portrait>" >&2; exit 1; }
[ -z "$PKL" ]    && { echo "need --pkl <motion.pkl>" >&2; exit 1; }
[ -z "$OUTDIR" ] && { echo "need --out-dir <dir>" >&2; exit 1; }
[ -z "$NAME" ]   && { echo "need --name <avatar>" >&2; exit 1; }
[ -f "$COND" ]   || { echo "cond not found: $COND" >&2; exit 1; }
[ -f "$PKL" ]    || { echo "pkl not found: $PKL" >&2; exit 1; }
if [ -n "$HALFBODY" ]; then
  [ -f "$HALFBODY" ] || { echo "halfbody not found: $HALFBODY" >&2; exit 1; }
  [ -f "$BLEND" ]    || { echo "need --blend <id>.blend.npz with --halfbody" >&2; exit 1; }
fi

WORK="$OUTDIR/_bake_tmp"
FRAMES_DIR="$WORK/frames"
HB_DIR="$WORK/frames_hb"
rm -rf "$WORK"
mkdir -p "$WORK" "$FRAMES_DIR" "$OUTDIR"

# ---- Stage A: neutral FlashHead frame ----
echo "PROGRESS 5"
echo "[A] render neutral FlashHead frame from: $COND (gpu $GPU)"
FLASHHEAD_DIR="$FLASHHEAD_DIR" CUDA_VISIBLE_DEVICES="$GPU" \
  "$FHPY" "$RENDER_NEUTRAL" --cond_image "$COND" --out "$WORK/neutral.png" --use_face_crop "$FACE_CROP"
[ -f "$WORK/neutral.png" ] || { echo "stage A produced no neutral.png" >&2; exit 1; }
echo "PROGRESS 30"

# ---- Stage B: LivePortrait motion transfer driven by the .pkl ----
# The builtin drivers carry detector jitter and run at 30 fps; smooth them in
# time and resample to the idle's 25 fps first (smooth_motion.py), or the loop
# stutters no matter how well it is encoded. PL_BAKE_MOTION_SIGMA=0 disables.
SIGMA="${PL_BAKE_MOTION_SIGMA:-2}"
SMOOTH_PKL="$WORK/motion_smooth.pkl"
"$FHPY" "$(dirname "$0")/smooth_motion.py" --in "$PKL" --out "$SMOOTH_PKL" --sigma "$SIGMA" --fps 25
[ -f "$SMOOTH_PKL" ] && PKL="$SMOOTH_PKL"
echo "[B] LivePortrait transfer: pkl=$PKL mult=$MULT (no crop, lip normalized)"
( cd "$LP" && CUDA_VISIBLE_DEVICES="$GPU" "$LPPY" inference.py \
    -s "$WORK/neutral.png" -d "$PKL" -o "$WORK/anim" \
    --no-flag_do_crop --no-flag_crop_driving_video --flag_normalize_lip \
    --driving_multiplier "$MULT" --no-flag_do_torch_compile )
MP4=$(ls "$WORK/anim"/neutral--*.mp4 2>/dev/null | grep -v concat | head -1 || true)
[ -z "$MP4" ] && MP4=$(ls "$WORK/anim"/*.mp4 2>/dev/null | grep -v concat | head -1 || true)
[ -z "$MP4" ] && { echo "stage B produced no animation mp4" >&2; exit 1; }
echo "[B] animated = $MP4"
echo "PROGRESS 75"

# ---- Stage C: split to a BGR PNG frame library ----
echo "[C] split first $FRAMES frames -> $FRAMES_DIR"
rm -f "$FRAMES_DIR"/f*.png
ffmpeg -y -loglevel error -i "$MP4" -frames:v "$FRAMES" "$FRAMES_DIR/f%04d.png"
N=$(ls "$FRAMES_DIR"/f*.png 2>/dev/null | wc -l)
[ "$N" -lt 2 ] && { echo "stage C produced $N frames" >&2; exit 1; }
echo "[C] $N frames"
echo "PROGRESS 88"

# ---- Stage C-prime: composite each 512 face onto the half-body canvas ----
# Only when --halfbody is given. The live worker and this bake share the SAME
# compositor (workers/avatar/halfbody.composite_halfbody) + the same canvas and
# precomputed blend, so idle frames and speaking frames are pixel-identical
# outside the face -> the idle<->speaking switch is pop-free. No model here: the
# face-parsing mask is already baked into the .blend.npz (cheap crop+paste).
BAKE_DIR="$FRAMES_DIR"
if [ -n "$HALFBODY" ]; then
  echo "[C'] composite $N faces -> half-body canvas"
  rm -rf "$HB_DIR"; mkdir -p "$HB_DIR"
  HALFBODY="$HALFBODY" BLEND="$BLEND" COND="$COND" FRAMES_DIR="$FRAMES_DIR" HB_DIR="$HB_DIR" \
  PYTHONPATH="$(dirname "$BAKE_IDLE")" CUDA_VISIBLE_DEVICES="" \
    "$FHPY" - <<'PYEOF'
import os, glob, numpy as np, cv2, halfbody
blend = halfbody.load_blend(os.environ["BLEND"])
body  = cv2.imread(os.environ["HALFBODY"])
src, dst = os.environ["FRAMES_DIR"], os.environ["HB_DIR"]
paths = sorted(glob.glob(os.path.join(src, "f*.png")))
# LivePortrait dims its output a few grey levels across the whole frame; match
# the rendered faces back to the engine cond (what live speech frames look
# like) so the idle<->speech switch does not read as a brightness step.
ref = cv2.imread(os.environ["COND"])
gain, off = halfbody.fit_color_match([cv2.imread(p) for p in paths[::10]], ref)
print(f"[C'] colour match to cond: gain={np.round(gain,3).tolist()} offset={np.round(off,1).tolist()}")
n = 0
for p in paths:
    face_bgr = halfbody.apply_color_match(cv2.imread(p), gain, off)
    out_rgb = halfbody.composite_halfbody(
        np.ascontiguousarray(face_bgr[:, :, ::-1]), body, blend)
    cv2.imwrite(os.path.join(dst, os.path.basename(p)), out_rgb[:, :, ::-1])
    n += 1
print(f"[C'] composited {n} half-body frames {body.shape[1]}x{body.shape[0]}")
PYEOF
  M=$(ls "$HB_DIR"/f*.png 2>/dev/null | wc -l)
  [ "$M" -eq "$N" ] || { echo "stage C-prime composited $M != $N frames" >&2; exit 1; }
  BAKE_DIR="$HB_DIR"
fi

# ---- Stage D: encode idle_<name>.h264f + silence ----
# --order autoloop: search the best forward wrap point (anchor frame 0), trim to
# it, then flow-morph the residual gap into a seamless loop — natural forward
# motion, no ping-pong rewind. The loop-closer is face-agnostic (runs on the
# rendered frame library), so every avatar's idle wraps cleanly. Overridable.
ORDER="${PL_BAKE_ORDER:-autoloop}"
echo "[D] bake_idle.py -> idle_$NAME.h264f (order=$ORDER)"
FLASHHEAD_DIR="$FLASHHEAD_DIR" "$FHPY" "$BAKE_IDLE" \
  --idle-dir "$BAKE_DIR" --out-dir "$OUTDIR" --name "$NAME" --codec h264 --order "$ORDER"
H264F="$OUTDIR/idle_$NAME.h264f"
[ -f "$H264F" ] || { echo "stage D produced no $H264F" >&2; exit 1; }
SZ=$(stat -c%s "$H264F")
echo "PROGRESS 100"
echo "BAKE-ASSET-OK name=$NAME frames=$N path=$H264F bytes=$SZ"
