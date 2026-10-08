#!/usr/bin/env python
"""Bake a MuseTalk v1.5 avatar dir from a frames dir (Mynah lab tool).

Port of scripts/realtime_inference.py Avatar.prepare_material with one
substitution: 68-point face landmarks come from face_alignment (FAN, iBUG-68)
instead of mmpose COCO-WholeBody[23:91] — same 68-point convention, and FAN is
already installed in the flashhead env (mmpose is not installed anywhere).
Everything else (bbox math incl. bbox_shift, v1.5 extra_margin, jaw parsing
mask, VAE latents, forward+reverse cycle) mirrors upstream.

Must RUN WITH THE MuseTalk REPO AS CWD: FaceParsing resolves its weights
through relative paths (./models/face-parse-bisent/...) that upstream never
made configurable. --musetalk-repo only puts the `musetalk` package on
sys.path; it does not remove the cwd requirement.

  cd /path/to/MuseTalk && CUDA_VISIBLE_DEVICES=0 \
    python bake_musetalk_avatar.py --frames /path/to/talk_frames \
      --out ./results/v15/avatars/my_avatar --bbox-shift 0

Progress: emits `PROGRESS <0-100>` lines on stdout at sub-stage boundaries,
the same convention bake_idle_asset.sh uses, so cored's bake processor can
drive a real progress bar instead of a spinner. Baking a 15s clip is minutes
of GPU work; without these the console could only say "还在跑".
"""
import argparse
import glob
import json
import os
import pickle
import shutil
import sys

import cv2
import numpy as np
import torch
from tqdm import tqdm

# Stage boundaries as percentages of the whole bake. Landmarks (FAN, one
# forward pass per frame) and masks (face parsing + PNG writes) dominate;
# VAE latents are comparatively cheap. Rough, but the shape is right — a bar
# that stalls at 30% for minutes is worse than a coarse one that keeps moving.
_STAGES = {"landmarks": (5, 45), "latents": (45, 60), "masks": (60, 99)}


def progress(stage, done, total):
    """Emit PROGRESS for `done/total` within `stage`'s slice of the bake."""
    lo, hi = _STAGES[stage]
    pct = lo if total <= 0 else lo + int((hi - lo) * min(done, total) / total)
    print("PROGRESS %d" % pct, flush=True)


def _tick(stage, total, every=None):
    """Return a callback that emits PROGRESS at most ~20 times per stage.

    Per-frame lines would flood the log the bake processor reads line by line.
    """
    every = every or max(1, total // 20)
    def cb(i):
        if i % every == 0 or i + 1 == total:
            progress(stage, i + 1, total)
    return cb


def load_musetalk(repo):
    """Import the upstream MuseTalk symbols, adding `repo` to sys.path first.

    Deferred (not a module-level import) because the repo path is a runtime
    flag: the serving worker gets it the same way, and hardcoding the lab
    layout is what made this script un-runnable outside one machine.
    """
    if repo and repo not in sys.path:
        sys.path.insert(0, repo)
    from musetalk.utils.face_parsing import FaceParsing
    from musetalk.utils.blending import get_image_prepare_material
    from musetalk.models.vae import VAE
    return FaceParsing, get_image_prepare_material, VAE


def fan_landmarks_and_bbox(img_list, bbox_shift, device="cuda"):
    """get_landmark_and_bbox with FAN landmarks. Returns (coords, frames)."""
    import face_alignment
    fa = face_alignment.FaceAlignment(face_alignment.LandmarksType.TWO_D,
                                      flip_input=False, device=device)
    coords, frames = [], []
    rng_minus, rng_plus = [], []
    tick = _tick("landmarks", len(img_list))
    for i, p in enumerate(tqdm(img_list, desc="landmarks")):
        tick(i)
        frame = cv2.imread(p)
        frames.append(frame)
        lms = fa.get_landmarks(cv2.cvtColor(frame, cv2.COLOR_BGR2RGB))
        if not lms:
            coords.append((0.0, 0.0, 0.0, 0.0))
            continue
        lm = lms[0].astype(np.int32)
        half_face_coord = lm[29].copy()
        rng_minus.append(int((lm[30] - lm[29])[1]))
        rng_plus.append(int((lm[29] - lm[28])[1]))
        if bbox_shift != 0:
            half_face_coord[1] += bbox_shift
        half_face_dist = int(np.max(lm[:, 1]) - half_face_coord[1])
        upper_bond = max(0, half_face_coord[1] - half_face_dist)
        x1, y1 = int(np.min(lm[:, 0])), int(upper_bond)
        x2, y2 = int(np.max(lm[:, 0])), int(np.max(lm[:, 1]))
        if y2 - y1 <= 0 or x2 - x1 <= 0 or x1 < 0:
            coords.append((0.0, 0.0, 0.0, 0.0))
        else:
            coords.append((x1, y1, x2, y2))
    if rng_minus:
        print(f"bbox_shift adjust range: [-{int(np.mean(rng_minus))}~{int(np.mean(rng_plus))}], "
              f"current: {bbox_shift}")
    return coords, frames


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--frames", required=True, help="input frames dir (png)")
    ap.add_argument("--out", required=True, help="avatar output dir")
    ap.add_argument("--bbox-shift", type=int, default=0)
    ap.add_argument("--extra-margin", type=int, default=10)
    ap.add_argument("--parsing-mode", default="jaw")
    ap.add_argument("--left-cheek-width", type=int, default=90)
    ap.add_argument("--right-cheek-width", type=int, default=90)
    ap.add_argument("--no-mirror", action="store_true",
                    help="skip the forward+reverse cycle (base video already loops)")
    ap.add_argument("--musetalk-repo", default=os.environ.get("MUSETALK_REPO", ""),
                    help="upstream MuseTalk checkout (provides the `musetalk` package). "
                         "Default: cwd, which must be the repo root either way.")
    ap.add_argument("--models-dir", default="./models",
                    help="MuseTalk models dir; the VAE is read from <dir>/sd-vae")
    args = ap.parse_args()

    FaceParsing, get_image_prepare_material, VAE = load_musetalk(args.musetalk_repo)

    out = args.out
    full_dir = os.path.join(out, "full_imgs")
    mask_dir = os.path.join(out, "mask")
    if os.path.exists(out):
        shutil.rmtree(out)
    os.makedirs(full_dir)
    os.makedirs(mask_dir)

    with open(os.path.join(out, "avator_info.json"), "w") as f:
        json.dump({"avatar_id": os.path.basename(out), "video_path": args.frames,
                   "bbox_shift": args.bbox_shift, "extra_margin": args.extra_margin,
                   "parsing_mode": args.parsing_mode, "version": "v15",
                   "landmarks": "face_alignment-FAN"}, f)

    img_list = sorted(glob.glob(os.path.join(args.frames, "*.png")))
    if not img_list:
        raise SystemExit(f"no frames in {args.frames}")
    print(f"{len(img_list)} input frames", flush=True)
    progress("landmarks", 0, len(img_list))

    coords, frames = fan_landmarks_and_bbox(img_list, args.bbox_shift)

    vae = VAE(model_path=os.path.join(args.models_dir, "sd-vae"), use_float16=False)
    fp = FaceParsing(left_cheek_width=args.left_cheek_width,
                     right_cheek_width=args.right_cheek_width)

    placeholder = (0.0, 0.0, 0.0, 0.0)
    latents = []
    kept_coords, kept_frames = [], []
    tick = _tick("latents", len(coords))
    for i, (bbox, frame) in enumerate(zip(tqdm(coords, desc="latents"), frames)):
        tick(i)
        if bbox == placeholder:
            continue
        x1, y1, x2, y2 = bbox
        y2 = min(y2 + args.extra_margin, frame.shape[0])
        crop = frame[y1:y2, x1:x2]
        crop = cv2.resize(crop, (256, 256), interpolation=cv2.INTER_LANCZOS4)
        latents.append(vae.get_latents_for_unet(crop))
        kept_coords.append([x1, y1, x2, y2])
        kept_frames.append(frame)

    # Every frame failed detection (wrong crop, no face, a slide deck): the
    # avatar dir would be structurally valid but render nothing. Fail loudly
    # here rather than let it reach the catalog as a selectable appearance.
    if not kept_frames:
        raise SystemExit(f"no face detected in any of {len(img_list)} frames — "
                         "check the source video framing")

    if args.no_mirror:
        frame_cycle, coord_cycle, latent_cycle = kept_frames, kept_coords, latents
    else:
        frame_cycle = kept_frames + kept_frames[::-1]
        coord_cycle = kept_coords + kept_coords[::-1]
        latent_cycle = latents + latents[::-1]

    mask_coords = []
    tick = _tick("masks", len(frame_cycle))
    for i, frame in enumerate(tqdm(frame_cycle, desc="masks")):
        tick(i)
        cv2.imwrite(os.path.join(full_dir, f"{i:08d}.png"), frame)
        x1, y1, x2, y2 = coord_cycle[i]
        mask, crop_box = get_image_prepare_material(
            frame, [x1, y1, x2, y2], fp=fp, mode=args.parsing_mode)
        cv2.imwrite(os.path.join(mask_dir, f"{i:08d}.png"), mask)
        mask_coords.append(crop_box)

    with open(os.path.join(out, "mask_coords.pkl"), "wb") as f:
        pickle.dump(mask_coords, f)
    with open(os.path.join(out, "coords.pkl"), "wb") as f:
        pickle.dump(coord_cycle, f)
    torch.save(latent_cycle, os.path.join(out, "latents.pt"))
    print("PROGRESS 100", flush=True)
    print(f"baked {len(frame_cycle)} frames -> {out}", flush=True)


if __name__ == "__main__":
    main()
