#!/usr/bin/env python
"""Prep LivePortrait-rendered clips into anchor-aligned idle segments.

The anchor is the RENDERED first frame of the first clip — NOT the raw
FlashHead neutral PNG. LivePortrait reconstruction shifts appearance slightly
(RMS ~4.6 vs the raw source), so mixing the raw frame into the stream pops on
every hop. All clips start from the same normalized driving pose, so their
rendered first frames agree within RMS ~1.7 — hopping between segments at that
shared pose is invisible.

Segments are kept LONG (default 10-26s): a hop back to the anchor pose every
few seconds reads as robotic; every ~20s with randomized order reads as a
person settling. Long clips are split at their most anchor-like interior
frames so every span starts and ends near the anchor; a short 4-frame blend
on each end snaps it exactly (4 frames at near-identical poses shows no
ghosting, unlike the long crossfades this replaces).

Run in the flashhead env (cv2/numpy):
  python prep_idle_segments.py --clips a.mp4 b.mp4 c.mp4 --out-dir segs/
Outputs segs/seg00/f0001.png..., ready for
  bake_idle.py --segments segs/seg00 segs/seg01 ... --codec h264
"""
import os
import glob
import argparse

import cv2
import numpy as np


def read_clip(path):
    cap = cv2.VideoCapture(path)
    frames = []
    while True:
        ok, fr = cap.read()
        if not ok:
            break
        frames.append(fr)
    cap.release()
    if not frames:
        raise SystemExit(f"no frames in {path}")
    return frames


def anchor_dist(frames, anchor):
    """Per-frame RMS distance to the anchor on a downscaled grayscale proxy."""
    def proxy(img):
        g = cv2.cvtColor(img, cv2.COLOR_BGR2GRAY)
        return cv2.resize(g, (96, 96)).astype(np.float32)
    a = proxy(anchor)
    return np.array([np.sqrt(np.mean((proxy(f) - a) ** 2)) for f in frames])


def blend(a, b, t):
    return cv2.addWeighted(a, 1.0 - t, b, t, 0)


def snap_ends(span, anchor, ease=4):
    """Anchor frame + short blends pinning both ends of the span to the anchor.
    The span already starts/ends anchor-like, so 4 blended frames carry no
    visible ghosting — they just zero out the residual offset."""
    out = [anchor.copy()]
    for i in range(min(ease, len(span))):
        out.append(blend(anchor, span[i], (i + 1) / (ease + 1)))
    out.extend(span[ease:len(span) - ease])
    tail = span[-ease:]
    for i, fr in enumerate(tail):
        out.append(blend(fr, anchor, (i + 1) / (ease + 1)))
    return out


def split_spans(dist, max_len, min_len):
    """Split [0..N) into spans of min_len..max_len cutting at the most
    anchor-like frames, so every span boundary is a low-seam hop point."""
    n = len(dist)
    spans, start = [], 0
    while n - start > max_len:
        lo, hi = start + min_len, min(start + max_len, n - min_len)
        if lo >= hi:
            break
        cut = lo + int(np.argmin(dist[lo:hi]))
        spans.append((start, cut))
        start = cut
    spans.append((start, n))
    return spans


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--clips", nargs="+", required=True,
                    help="LivePortrait-rendered mp4s; anchor = first clip's frame 0")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--max-len", type=int, default=650)
    ap.add_argument("--min-len", type=int, default=250)
    ap.add_argument("--tail-window", type=float, default=0.25,
                    help="search the last fraction of a span for its end cut")
    ap.add_argument("--max-seam", type=float, default=12.0,
                    help="drop spans whose end frame is farther than this from the anchor")
    args = ap.parse_args()

    os.makedirs(args.out_dir, exist_ok=True)
    anchor = None
    seg_i = 0
    for path in args.clips:
        clip = read_clip(path)
        if anchor is None:
            anchor = clip[0].copy()  # rendered-domain anchor
        if clip[0].shape != anchor.shape:
            clip = [cv2.resize(f, (anchor.shape[1], anchor.shape[0])) for f in clip]
        dist = anchor_dist(clip, anchor)
        print(f"{os.path.basename(path)}: {len(clip)} frames, "
              f"f0-to-anchor {dist[0]:.2f}")

        for a, b in split_spans(dist, args.max_len, args.min_len):
            span, sdist = clip[a:b], dist[a:b]
            if len(span) < args.min_len:
                print(f"  skip [{a}:{b}]: only {len(span)} frames")
                continue
            # end the span at its most anchor-like frame in the tail window
            w0 = int(len(span) * (1.0 - args.tail_window))
            cut = w0 + int(np.argmin(sdist[w0:]))
            seam = sdist[cut]
            if seam > args.max_seam:
                print(f"  skip [{a}:{b}]: tail seam {seam:.2f} > {args.max_seam}")
                continue
            seg = snap_ends(span[:cut + 1], anchor)
            d = os.path.join(args.out_dir, f"seg{seg_i:02d}")
            os.makedirs(d, exist_ok=True)
            for j, fr in enumerate(seg):
                cv2.imwrite(os.path.join(d, f"f{j + 1:04d}.png"), fr)
            print(f"  seg{seg_i:02d}: [{a}:{a + cut + 1}] -> {len(seg)} frames "
                  f"({len(seg) / 25:.1f}s, head {sdist[0]:.2f} tail {seam:.2f})")
            seg_i += 1
    if seg_i < 2:
        raise SystemExit("need at least 2 segments for a multi-segment bake")
    print(f"PREP-OK: {seg_i} segments in {args.out_dir}")


if __name__ == "__main__":
    main()
