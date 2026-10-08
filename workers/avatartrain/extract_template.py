#!/usr/bin/env python3
"""Mynah motion-template extractor (动作骨架真抽取).

Runs the LivePortrait motion-template pipeline on a driving video and dumps a
reusable .pkl whose head/lip/blink trajectory can be transferred onto any
portrait — the same code path inference.py takes when handed a video as the
driving (src/live_portrait_pipeline.py: load_video -> crop_driving_video ->
calc_ratio -> prepare_videos -> make_motion_template -> dump). Person-agnostic:
the .pkl holds only keypoint trajectories, no likeness.

Invoked as a subprocess by workers/avatartrain/server.py (/extract) so the heavy
LivePortrait runtime stays out of the stdlib worker process. Also computes
idle-suitability metrics (head motion range, lip openness, blinks, loop
seamlessness) into a stats JSON for the console's badges.

    python extract_template.py --video drv.mp4 --out motion.pkl \
        --stats stats.json --lp-root /path/to/LivePortrait [--device 0]

Exit 0 on success (pkl + stats written); non-zero with the error on the last
stdout line otherwise (the worker surfaces that as the failure reason).
"""
import argparse
import json
import os
import os.path as osp
import sys


def log(*a):
    print(*a, flush=True)


def matrix_to_euler_angle_deg(R0, Ri):
    """Geodesic angle (deg) of Ri relative to reference R0 — convention-free, so
    it doesn't depend on LivePortrait's pitch/yaw/roll ordering. trace identity:
    angle = arccos((trace(Ri·R0ᵀ) - 1) / 2)."""
    import numpy as np
    rel = Ri @ R0.T
    c = (np.trace(rel) - 1.0) / 2.0
    c = max(-1.0, min(1.0, float(c)))
    return float(np.degrees(np.arccos(c)))


def compute_stats(template_dct):
    """Objective idle-suitability metrics from a motion template.

    head_deg  : max angular deviation of head pose from the first frame (range
                of head movement; LOW = calm, HIGH = lively).
    lip_avg/max: mean/peak lip openness (LOW = silent/listening idle — good;
                HIGH = talking motion — bad for a silent idle).
    blink     : approximate blink count (eye-open ratio dips).
    loop_deg  : head-pose gap between first and last frame (LOW = seamless loop).
    """
    import numpy as np
    motion = template_dct.get("motion", [])
    n = len(motion)
    stats = {
        "n_frames": int(template_dct.get("n_frames", n)),
        "fps": int(template_dct.get("output_fps", 25)),
    }
    if n == 0:
        return stats

    def R_of(i):
        R = np.asarray(motion[i]["R"], dtype=np.float64).reshape(3, 3)
        return R

    have_R = "R" in motion[0]
    if have_R:
        R0 = R_of(0)
        head = [matrix_to_euler_angle_deg(R0, R_of(i)) for i in range(n)]
        stats["head_deg"] = round(float(np.ptp(head)), 2)
        stats["loop_deg"] = round(matrix_to_euler_angle_deg(R0, R_of(n - 1)), 2)

    # Lip openness from c_lip_lst (each entry an array; reduce to a scalar).
    lips = template_dct.get("c_lip_lst", [])
    if lips:
        lv = np.array([float(np.mean(np.asarray(x, dtype=np.float64))) for x in lips])
        stats["lip_avg"] = round(float(lv.mean()), 4)
        stats["lip_max"] = round(float(lv.max()), 4)

    # Blink count from c_eyes_lst: normalize the eye-open series and count
    # falling-edge crossings below a relative threshold.
    eyes = template_dct.get("c_eyes_lst", [])
    if eyes:
        ev = np.array([float(np.mean(np.asarray(x, dtype=np.float64))) for x in eyes])
        lo, hi = ev.min(), ev.max()
        if hi - lo > 1e-6:
            norm = (ev - lo) / (hi - lo)
            thr = 0.35
            below = norm < thr
            blinks = int(np.sum((~below[:-1]) & (below[1:])))  # rising->below edges
            stats["blink"] = blinks
        else:
            stats["blink"] = 0
    return stats


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--video", required=True)
    ap.add_argument("--out", required=True, help="output .pkl path")
    ap.add_argument("--stats", required=True, help="output stats JSON path")
    ap.add_argument("--lp-root", required=True, help="LivePortrait repo root")
    ap.add_argument("--device", type=int, default=0)
    args = ap.parse_args()

    if not osp.exists(args.video):
        log("ERROR: driving video not found: %s" % args.video)
        sys.exit(2)

    lp_root = osp.abspath(args.lp_root)
    if lp_root not in sys.path:
        sys.path.insert(0, lp_root)
    # Model weight paths in the configs are relative to the repo root.
    os.chdir(lp_root)

    try:
        import cv2
        from src.config.inference_config import InferenceConfig
        from src.config.crop_config import CropConfig
        from src.live_portrait_pipeline import LivePortraitPipeline
        from src.utils.io import load_video, dump
        from src.utils.camera import get_rotation_matrix  # noqa: F401 (ensures repo importable)
    except Exception as e:  # noqa: BLE001
        log("ERROR: import LivePortrait failed: %s" % e)
        sys.exit(3)

    # Helpers live in the pipeline module's namespace; import what we branch on.
    try:
        from src.utils.helper import is_square_video
    except Exception:
        is_square_video = None  # fall back to always-crop

    try:
        from src.utils.video import get_fps
    except Exception:
        get_fps = None

    inf_cfg = InferenceConfig()
    crop_cfg = CropConfig()
    inf_cfg.device_id = args.device
    log("Loading LivePortrait pipeline (device %d)..." % args.device)
    pipeline = LivePortraitPipeline(inference_cfg=inf_cfg, crop_cfg=crop_cfg)
    wrapper = pipeline.live_portrait_wrapper
    cropper = pipeline.cropper

    output_fps = int(get_fps(args.video)) if get_fps else 25
    log("Loading driving video: %s (fps=%d)" % (args.video, output_fps))
    driving_rgb_lst = load_video(args.video)
    n_frames = len(driving_rgb_lst)
    if n_frames == 0:
        log("ERROR: driving video has no frames")
        sys.exit(4)

    square = is_square_video(args.video) if is_square_video else False
    if inf_cfg.flag_crop_driving_video or (not square):
        ret_d = cropper.crop_driving_video(driving_rgb_lst)
        log("Driving video cropped, %d frames." % len(ret_d["frame_crop_lst"]))
        driving_rgb_crop_lst, driving_lmk_crop_lst = ret_d["frame_crop_lst"], ret_d["lmk_crop_lst"]
        driving_rgb_crop_256x256_lst = [cv2.resize(_, (256, 256)) for _ in driving_rgb_crop_lst]
    else:
        driving_lmk_crop_lst = cropper.calc_lmks_from_cropped_video(driving_rgb_lst)
        driving_rgb_crop_256x256_lst = [cv2.resize(_, (256, 256)) for _ in driving_rgb_lst]

    c_d_eyes_lst, c_d_lip_lst = wrapper.calc_ratio(driving_lmk_crop_lst)
    I_d_lst = wrapper.prepare_videos(driving_rgb_crop_256x256_lst)
    template_dct = pipeline.make_motion_template(I_d_lst, c_d_eyes_lst, c_d_lip_lst, output_fps=output_fps)

    os.makedirs(osp.dirname(osp.abspath(args.out)), exist_ok=True)
    dump(args.out, template_dct)
    log("Dumped motion template -> %s" % args.out)

    stats = compute_stats(template_dct)
    os.makedirs(osp.dirname(osp.abspath(args.stats)), exist_ok=True)
    with open(args.stats, "w") as f:
        json.dump(stats, f)
    log("Stats: %s" % json.dumps(stats))
    log("OK")


if __name__ == "__main__":
    main()
