#!/usr/bin/env python3
"""Temporally smooth + resample a LivePortrait driving template (.pkl) before
the idle bake.

Why: the builtin drivers (d9 & co) were extracted from real footage, so the
per-frame motion carries detector jitter — expression/translation step sizes
swing 20x between neighbouring frames (0.0005 -> 0.011 for d9). LivePortrait
renders that faithfully and the loop reads as "卡" (stutter) even at a clean
25 fps. They are also 30 fps while the idle plays at 25, which both slows the
motion 17% and leaves no clean frame cadence.

What: Gaussian-smooth every motion parameter along time (sigma in SOURCE
frames; default 2 ≈ 67 ms at 30 fps — kills jitter, keeps head sway and
blinks), then linearly resample the sequence to --fps. Rotation matrices are
interpolated element-wise and re-orthonormalised (SVD); angles here are a few
degrees, so that is exact enough.

Usage: smooth_motion.py --in d9.pkl --out d9_smooth.pkl [--sigma 2] [--fps 25]
Prints the before/after step-size spread so the effect is auditable.
"""
import argparse
import pickle

import numpy as np


def _stack(motion, key):
    return np.stack([np.asarray(m[key], dtype=np.float64) for m in motion])  # T x ...


def _gauss1d(x, sigma):
    """Gaussian filter along axis 0 with edge replication (no scipy dependency)."""
    if sigma <= 0:
        return x
    r = int(np.ceil(3 * sigma))
    k = np.exp(-0.5 * (np.arange(-r, r + 1) / sigma) ** 2)
    k /= k.sum()
    flat = x.reshape(x.shape[0], -1)
    pad = np.concatenate([np.repeat(flat[:1], r, 0), flat, np.repeat(flat[-1:], r, 0)])
    out = np.empty_like(flat)
    for j in range(flat.shape[1]):
        out[:, j] = np.convolve(pad[:, j], k, mode="valid")
    return out.reshape(x.shape)


def _resample(x, n_out):
    """Linear resample along axis 0 to n_out samples spanning the same time."""
    n_in = x.shape[0]
    if n_out == n_in:
        return x
    src = np.linspace(0, n_in - 1, n_out)
    i0 = np.floor(src).astype(int)
    i1 = np.minimum(i0 + 1, n_in - 1)
    w = (src - i0).reshape((-1,) + (1,) * (x.ndim - 1))
    return x[i0] * (1 - w) + x[i1] * w


def _orthonormalise(R):
    u, _, vt = np.linalg.svd(R)
    out = u @ vt
    neg = np.linalg.det(out) < 0
    if np.any(neg):
        u[neg, :, -1] *= -1
        out = u @ vt
    return out


def step_spread(motion):
    ex = np.stack([np.asarray(m["exp"]).ravel() for m in motion])
    d = np.linalg.norm(np.diff(ex, axis=0), axis=1)
    return float(np.median(d)), float(np.percentile(d, 95) / max(np.median(d), 1e-9))


def smooth_template(d, sigma=2.0, fps=25):
    motion = d["motion"]
    n_in = len(motion)
    src_fps = float(d.get("output_fps", 30) or 30)
    n_out = int(round(n_in * fps / src_fps))
    keys = [k for k in motion[0] if k != "R"]
    arrays = {k: _resample(_gauss1d(_stack(motion, k), sigma), n_out) for k in keys}
    R = _stack(motion, "R")                      # T x 1 x 3 x 3
    R = _resample(_gauss1d(R, sigma), n_out)
    R = _orthonormalise(R.reshape(n_out, 3, 3)).reshape(n_out, 1, 3, 3)
    out_motion = []
    for i in range(n_out):
        item = {k: arrays[k][i].astype(np.float32) for k in keys}
        item["R"] = R[i].astype(np.float32)
        out_motion.append(item)
    out = dict(d)
    out["motion"] = out_motion
    out["n_frames"] = n_out
    out["output_fps"] = fps
    for k in ("c_eyes_lst", "c_lip_lst"):
        if k in d and isinstance(d[k], list) and len(d[k]) == n_in:
            arr = _resample(_gauss1d(np.stack([np.asarray(v, dtype=np.float64) for v in d[k]]), sigma), n_out)
            out[k] = [a.astype(np.float32) for a in arr]
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--in", dest="inp", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--sigma", type=float, default=2.0, help="Gaussian sigma in source frames (0 = off)")
    ap.add_argument("--fps", type=int, default=25, help="target fps (0 = keep)")
    a = ap.parse_args()
    with open(a.inp, "rb") as f:
        d = pickle.load(f)
    fps = a.fps or int(d.get("output_fps", 30))
    med0, spread0 = step_spread(d["motion"])
    out = smooth_template(d, a.sigma, fps)
    med1, spread1 = step_spread(out["motion"])
    with open(a.out, "wb") as f:
        pickle.dump(out, f)
    print(f"smooth_motion: {len(d['motion'])}f@{d.get('output_fps')}fps -> {out['n_frames']}f@{fps}fps "
          f"sigma={a.sigma}  exp-step median {med0:.4f}->{med1:.4f}  p95/median {spread0:.1f}->{spread1:.1f}")
    print("SMOOTH-OK")


if __name__ == "__main__":
    main()
