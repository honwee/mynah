#!/usr/bin/env python3
"""Extract the best speaking-base portrait from a source video.

This is the real "training" step behind the avatar-training worker's /train
endpoint (MVP: avatar preparation, not weight fine-tuning). Given a talking
video, it picks the single frame that makes the best FlashHead cond_image —
the speaking base every generated frame is conditioned on — and writes it as
a square portrait crop.

Selection follows the speech-base rules learned in production: prefer a
neutral CLOSED mouth, a frontal head pose, a large sharp face. Scoring:

    score = sharpness(face crop, Laplacian var)
          × frontal-ness (keypoint symmetry)
          × mouth-closed bonus (FaceMesh inner-lip gap)
          × face-size factor

Detection is MediaPipe (Apache-2.0) on CPU — deliberately NOT InsightFace
(non-commercial). ffmpeg does the frame sampling.

Usage:
    extract_portrait.py --video in.mp4 --out-dir DIR [--frames 30] [--size 1024]

Writes DIR/portrait.jpg + DIR/meta.json. Progress goes to stdout as
"PROGRESS <n>" lines (the worker's /train parser reads them). Exit codes:
0 ok, 2 bad input, 3 no usable face found.
"""
import argparse
import json
import math
import os
import shutil
import subprocess
import sys
import tempfile

import cv2
import numpy as np


def progress(n: int):
    print("PROGRESS %d" % n, flush=True)


def die(code: int, msg: str):
    print(msg, flush=True)
    sys.exit(code)


def video_duration(path: str) -> float:
    out = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration",
         "-of", "default=noprint_wrappers=1:nokey=1", path],
        capture_output=True, text=True, timeout=60)
    try:
        return float(out.stdout.strip())
    except ValueError:
        die(2, "ffprobe could not read the video: %s" % (out.stderr.strip()[-200:] or path))


def sample_frames(video: str, n: int, tmpdir: str) -> list[str]:
    """Evenly sample n frames across the video (skipping the first/last 3%,
    which often carry fade-ins or hand-off motion)."""
    dur = video_duration(video)
    if dur <= 0.1:
        die(2, "video too short: %.2fs" % dur)
    t0, t1 = 0.03 * dur, 0.97 * dur
    stamps = [t0 + (t1 - t0) * i / max(n - 1, 1) for i in range(n)]
    paths = []
    for i, t in enumerate(stamps):
        out = os.path.join(tmpdir, "f%03d.png" % i)
        r = subprocess.run(
            ["ffmpeg", "-hide_banner", "-loglevel", "error", "-ss", "%.3f" % t,
             "-i", video, "-frames:v", "1", "-y", out],
            capture_output=True, text=True, timeout=120)
        if r.returncode == 0 and os.path.exists(out):
            paths.append(out)
        progress(5 + int(25 * (i + 1) / n))  # 5 → 30
    if not paths:
        die(2, "ffmpeg extracted no frames from the video")
    return paths


def sharpness(gray: np.ndarray) -> float:
    return float(cv2.Laplacian(gray, cv2.CV_64F).var())


def detect_candidates(paths: list[str]):
    """MediaPipe FaceDetection over every sampled frame; returns scored
    candidates [{path, img, box(x,y,w,h), kp, score0}]. score0 lacks the
    mouth term (FaceMesh runs later, only on the shortlist)."""
    import mediapipe as mp
    cands = []
    detector = mp.solutions.face_detection.FaceDetection(
        model_selection=1, min_detection_confidence=0.5)
    with detector:
        for i, p in enumerate(paths):
            img = cv2.imread(p)
            if img is None:
                continue
            h, w = img.shape[:2]
            res = detector.process(cv2.cvtColor(img, cv2.COLOR_BGR2RGB))
            det = max(res.detections, key=lambda d: d.score[0]) if res.detections else None
            if det is None:
                progress(30 + int(30 * (i + 1) / len(paths)))
                continue
            rb = det.location_data.relative_bounding_box
            x, y = max(0, int(rb.xmin * w)), max(0, int(rb.ymin * h))
            bw, bh = min(int(rb.width * w), w - x), min(int(rb.height * h), h - y)
            if bw < 64 or bh < 64:  # too small to be a usable base
                progress(30 + int(30 * (i + 1) / len(paths)))
                continue
            kp = det.location_data.relative_keypoints  # Reye Leye nose mouth Rear Lear
            face = img[y:y + bh, x:x + bw]
            sharp = sharpness(cv2.cvtColor(face, cv2.COLOR_BGR2GRAY))
            # Frontal-ness: nose should sit midway between the eyes, eyes level.
            re, le, nose = kp[0], kp[1], kp[2]
            inter = math.hypot(le.x - re.x, le.y - re.y) + 1e-6
            asym = abs(math.hypot(nose.x - re.x, nose.y - re.y) -
                       math.hypot(nose.x - le.x, nose.y - le.y)) / inter
            tilt = abs(le.y - re.y) / inter
            frontal = max(0.0, 1.0 - 1.8 * asym - 1.2 * tilt)
            size_f = min(1.0, (bw * bh) / (0.08 * w * h))  # saturate at ~8% of frame
            cands.append({"path": p, "img": img, "box": (x, y, bw, bh),
                          "sharp": sharp, "frontal": frontal, "size_f": size_f,
                          "score0": sharp * (0.5 + 0.5 * frontal) * size_f})
            progress(30 + int(30 * (i + 1) / len(paths)))  # 30 → 60
    return cands


def mouth_gap(img: np.ndarray) -> float | None:
    """Normalized inner-lip gap via FaceMesh; None when no mesh."""
    import mediapipe as mp
    with mp.solutions.face_mesh.FaceMesh(
            static_image_mode=True, max_num_faces=1, refine_landmarks=False,
            min_detection_confidence=0.5) as mesh:
        res = mesh.process(cv2.cvtColor(img, cv2.COLOR_BGR2RGB))
    if not res.multi_face_landmarks:
        return None
    lm = res.multi_face_landmarks[0].landmark
    gap = math.hypot(lm[13].x - lm[14].x, lm[13].y - lm[14].y)      # inner lips
    face_h = math.hypot(lm[10].x - lm[152].x, lm[10].y - lm[152].y) + 1e-6  # forehead→chin
    return gap / face_h


def crop_portrait(img: np.ndarray, box, size: int) -> np.ndarray:
    """Square head-and-shoulders crop around the face (cond_image framing:
    a generous portrait, not a tight face box), padded with edge replication
    when the expansion runs past the frame."""
    x, y, bw, bh = box
    h, w = img.shape[:2]
    cx, cy = x + bw / 2, y + bh / 2 + 0.12 * bh  # bias down: include shoulders
    side = 2.6 * max(bw, bh)
    x0, y0 = int(cx - side / 2), int(cy - side / 2)
    x1, y1 = int(cx + side / 2), int(cy + side / 2)
    pl, pt = max(0, -x0), max(0, -y0)
    pr, pb = max(0, x1 - w), max(0, y1 - h)
    if any((pl, pt, pr, pb)):
        img = cv2.copyMakeBorder(img, pt, pb, pl, pr, cv2.BORDER_REPLICATE)
        x0, y0, x1, y1 = x0 + pl, y0 + pt, x1 + pl, y1 + pt
    crop = img[y0:y1, x0:x1]
    return cv2.resize(crop, (size, size), interpolation=cv2.INTER_AREA)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--video", required=True)
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--frames", type=int, default=30)
    ap.add_argument("--size", type=int, default=1024)
    args = ap.parse_args()

    if not os.path.isfile(args.video):
        die(2, "video not found: %s" % args.video)
    os.makedirs(args.out_dir, exist_ok=True)
    progress(2)

    tmpdir = tempfile.mkdtemp(prefix="plportrait_")
    try:
        frames = sample_frames(args.video, args.frames, tmpdir)
        cands = detect_candidates(frames)
        if not cands:
            die(3, "no usable face detected in the video (need a clear, "
                   ">=64px frontal face; check lighting and framing)")

        # FaceMesh mouth check only on the shortlist — it is the slow part.
        cands.sort(key=lambda c: c["score0"], reverse=True)
        short = cands[:8]
        for i, c in enumerate(short):
            x, y, bw, bh = c["box"]
            pad = int(0.25 * max(bw, bh))
            y0, y1 = max(0, y - pad), min(c["img"].shape[0], y + bh + pad)
            x0, x1 = max(0, x - pad), min(c["img"].shape[1], x + bw + pad)
            g = mouth_gap(c["img"][y0:y1, x0:x1])
            closed = (g is not None and g < 0.035)
            c["mouth_gap"] = g
            c["score"] = c["score0"] * (1.15 if closed else 0.85)
            progress(60 + int(25 * (i + 1) / len(short)))  # 60 → 85

        best = max(short, key=lambda c: c["score"])
        portrait = crop_portrait(best["img"], best["box"], args.size)
        out_jpg = os.path.join(args.out_dir, "portrait.jpg")
        if not cv2.imwrite(out_jpg, portrait, [cv2.IMWRITE_JPEG_QUALITY, 95]):
            die(2, "failed to write %s" % out_jpg)
        progress(92)

        meta = {
            "source_frame": os.path.basename(best["path"]),
            "sharpness": round(best["sharp"], 1),
            "frontal": round(best["frontal"], 3),
            "mouth_gap": (round(best["mouth_gap"], 4)
                          if best.get("mouth_gap") is not None else None),
            "candidates": len(cands),
            "frames_sampled": len(frames),
            "size": args.size,
        }
        with open(os.path.join(args.out_dir, "meta.json"), "w") as f:
            json.dump(meta, f, indent=2)
        progress(100)
        print("portrait -> %s" % out_jpg, flush=True)
    finally:
        shutil.rmtree(tmpdir, ignore_errors=True)


if __name__ == "__main__":
    main()
