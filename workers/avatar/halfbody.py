#!/usr/bin/env python3
"""Half-body presenter compositor — shared by the live worker and the idle bake.

The engine (FlashHead) always face-crops its cond image internally via
``flash_head.utils.facecrop.process_image`` (face_ratio=2.0, target 512x512) and
emits a pure 512x512 face. To turn that into an application-type half-body frame
we paste the generated face back onto a static head-to-waist canvas at *exactly*
the box the engine cropped from — so live speaking frames and baked idle frames
share one canvas and one geometry, and the idle<->speaking switch is pop-free.

Two halves:

  OFFLINE prep (run once per avatar, this file's CLI):
    * build a CANVAS_W x CANVAS_H (720x1280, 9:16) near-white canvas from a
      high-res half-body portrait, with headroom so the 2x face crop never
      clamps at an edge  ->  <id>.halfbody.png   (== the engine cond)
    * compute the engine's scaled face bbox on that canvas by replicating
      get_scaled_bbox, and SELF-VERIFY it is pixel-identical to what
      process_image() would crop  ->  <id>.bbox.json
    * dump the 512 crop for eyeballing  ->  <id>.cond512.png

  RUNTIME composite: paste the 512 face into the canvas at the persisted bbox
  using a precomputed face-parsing blend mask (vectorised, ~1-2 ms/frame).

Run in the flashhead env (mediapipe / cv2 / PIL):
  python halfbody.py prepare --src linxia_halfbody.png --out-dir avatar_src --id linxia
"""
import os
import sys
import json
import argparse

import numpy as np
from PIL import Image

# Separate checkout — no guessed default (see workers/avatar/server.py).
FLASHHEAD_DIR = os.environ.get("FLASHHEAD_DIR", "")
if FLASHHEAD_DIR and FLASHHEAD_DIR not in sys.path:
    sys.path.insert(0, FLASHHEAD_DIR)

CANVAS_W, CANVAS_H = 720, 1280
BG = (251, 251, 253)          # #fbfbfd near-white
FACE_RATIO = 2.0
COND_SIZE = 512


# ---- offline: canvas + bbox -------------------------------------------------

def build_canvas(src_img, canvas_w=CANVAS_W, canvas_h=CANVAS_H, bg="auto"):
    """Fit the portrait inside the canvas (prefer filling width, never overflow
    height) and center it. Centering a head-to-waist figure leaves headroom
    above the head, which keeps the 2x face crop clear of the top edge (no
    clamp -> no aspect distortion in the cond).

    bg="auto" fills the letterbox bands by stretching + blurring the image's
    own top/bottom edge rows, so a dark or coloured scene continues instead of
    getting a white bar (a 3:4 night portrait on a near-white 9:16 field read
    as a framed photo, 2026-10-11). Any PIL colour keeps the old flat fill."""
    from PIL import ImageFilter
    w, h = src_img.size
    scale = min(canvas_w / w, canvas_h / h)
    nw, nh = max(1, round(w * scale)), max(1, round(h * scale))
    fitted = src_img.resize((nw, nh), Image.LANCZOS)
    x0 = (canvas_w - nw) // 2
    y0 = (canvas_h - nh) // 2
    if bg == "auto":
        canvas = Image.new("RGB", (canvas_w, canvas_h), (0, 0, 0))
        band = max(8, nh // 40)
        blur = ImageFilter.GaussianBlur(radius=max(6, canvas_w // 40))
        if y0 > 0:  # top band: stretch the top rows of the picture up to the edge
            top = fitted.crop((0, 0, nw, band)).resize((nw, y0), Image.BILINEAR).filter(blur)
            canvas.paste(top, (x0, 0))
        if y0 + nh < canvas_h:
            bh = canvas_h - (y0 + nh)
            bot = fitted.crop((0, nh - band, nw, nh)).resize((nw, bh), Image.BILINEAR).filter(blur)
            canvas.paste(bot, (x0, y0 + nh))
        if x0 > 0:  # side bands (portrait narrower than 9:16): stretch edge columns
            left = fitted.crop((0, 0, band, nh)).resize((x0, nh), Image.BILINEAR).filter(blur)
            right = fitted.crop((nw - band, 0, nw, nh)).resize((canvas_w - x0 - nw, nh), Image.BILINEAR).filter(blur)
            canvas.paste(left, (0, y0))
            canvas.paste(right, (x0 + nw, y0))
    else:
        canvas = Image.new("RGB", (canvas_w, canvas_h), bg)
    canvas.paste(fitted, (x0, y0))
    return canvas


def _scaled_bbox(bbox, img_w, img_h, ratio=FACE_RATIO):
    """Byte-for-byte the geometry of facecrop.get_scaled_bbox (which only returns
    the crop, not the box). Square crop centered on the face, biased upward
    (0.55 above / 0.45 below)."""
    x1, y1, x2, y2 = bbox
    cx = (x1 + x2) / 2
    cy = (y1 + y2) / 2
    width = x2 - x1
    nw = width * ratio
    nh = nw
    dxl = nw * 0.5
    dxr = nw - dxl
    dyu = nh * 0.55
    dyd = nh - dyu
    nx1 = int(max(0, cx - dxl))
    ny1 = int(max(0, cy - dyu))
    nx2 = int(min(img_w, cx + dxr))
    ny2 = int(min(img_h, cy + dyd))
    return [nx1, ny1, nx2, ny2]


def face_bbox(canvas_img, ratio=FACE_RATIO):
    """Run the same detector + crop geometry the engine uses, return the box."""
    from flash_head.utils.cpu_face_handler import CPUFaceHandler
    arr = np.array(canvas_img.convert("RGB"))
    h, w = arr.shape[:2]
    det = CPUFaceHandler()
    boxes, scores = det(arr)
    if len(boxes) == 0:
        raise SystemExit("no face detected in canvas")
    b = boxes[0]
    abs_box = [b[0] * w, b[1] * h, b[2] * w, b[3] * h]
    return _scaled_bbox(abs_box, w, h, ratio)


def load_bbox(path):
    with open(path) as f:
        m = json.load(f)
    return m


def prepare(src_path, out_dir, avatar_id, ratio=FACE_RATIO):
    os.makedirs(out_dir, exist_ok=True)
    src = Image.open(src_path).convert("RGB")
    canvas = build_canvas(src)

    hb_path = os.path.join(out_dir, f"{avatar_id}.halfbody.png")
    canvas.save(hb_path)                        # lossless: cond + paste-back bg

    box = face_bbox(canvas, ratio)
    cond512 = canvas.crop(box).resize((COND_SIZE, COND_SIZE))
    cond_path = os.path.join(out_dir, f"{avatar_id}.cond512.png")
    cond512.save(cond_path)

    # SELF-VERIFY: the box must reproduce exactly what the engine's process_image
    # crops from this same canvas, or paste-back would be misaligned.
    from flash_head.utils.facecrop import process_image
    ref = process_image(hb_path)                # engine's real crop, default args
    diff = int(np.abs(np.array(cond512).astype(int) - np.array(ref).astype(int)).max())

    meta = {
        "x1": box[0], "y1": box[1], "x2": box[2], "y2": box[3],
        "img_w": CANVAS_W, "img_h": CANVAS_H,
        "face_ratio": ratio, "size": COND_SIZE,
        "cond": os.path.basename(hb_path),      # what to feed the engine
    }
    bbox_path = os.path.join(out_dir, f"{avatar_id}.bbox.json")
    with open(bbox_path, "w") as f:
        json.dump(meta, f, indent=2)

    print(f"canvas  {hb_path} ({CANVAS_W}x{CANVAS_H})")
    print(f"bbox    {bbox_path} {box}  (w={box[2]-box[0]} h={box[3]-box[1]})")
    print(f"cond512 {cond_path}")
    print(f"VERIFY  max|cond - process_image(canvas)| = {diff}  "
          f"({'ALIGNED' if diff == 0 else 'MISALIGNED'})")
    assert diff == 0, "bbox does not match engine process_image crop"
    print("PREPARE-OK")


# ---- offline: blend mask precompute (face parsing, once per avatar) ----------

def prepare_blend(canvas_bgr, face_box, expand=1.5, upper_boundary_ratio=0.0,
                  mode="full", feather=0.06):
    """Precompute the paste-back mask for a half-body canvas. Depends only on the
    static body, so it runs ONCE per avatar (offline).

    mode="full" (default): keep the ENTIRE engine output. FlashHead renders the
        whole 512 head-and-shoulders crop — hair, head pose, neck, shoulder
        line — not just the mouth. The mask is the face box itself, feathered
        inward over `feather` x box-size so it fades to the static canvas at
        the box edge. Anything narrower freezes the head outline on the canvas
        while the inner face moves: a talking mask (the 2026-10-10 "头和身体
        不动" report).
    mode="face": MuseTalk-style face-parsing skin mask (needs faceparse +
        mediapipe). Only the inner face is replaced; right for lip-sync-only
        engines, wrong for FlashHead.

    canvas_bgr: HxWx3 BGR (blending.py convention; the net needs true RGB after
                its internal [:,:,::-1], so color order matters HERE).
    Returns (mask_array uint8 [crop_h,crop_w], crop_box [x_s,y_s,x_e,y_e]).
    """
    import cv2
    from faceparse.blending import get_crop_box
    face_box = list(face_box)
    crop_box, _ = get_crop_box(face_box, expand)
    if mode == "face":
        from faceparse import FaceParsing
        from faceparse.blending import get_image_prepare_material
        fp = FaceParsing()
        mask_array, crop_box = get_image_prepare_material(
            canvas_bgr, face_box, upper_boundary_ratio=upper_boundary_ratio,
            expand=expand, fp=fp, mode="presenter")
        return mask_array, crop_box
    if mode != "full":
        raise ValueError(f"unknown blend mode {mode!r} (full|face)")
    x, y, x1, y1 = face_box
    xs, ys, xe, ye = crop_box
    mask = np.zeros((ye - ys, xe - xs), dtype=np.uint8)
    m = max(2, int(round(feather * max(x1 - x, y1 - y))))
    # solid core inset by the feather width, then blur so the ramp ends ~at
    # the box edge (engine pixels exist only inside the box)
    mask[y - ys + m:y1 - ys - m, x - xs + m:x1 - xs - m] = 255
    k = 2 * m + 1
    mask = cv2.GaussianBlur(mask, (k, k), sigmaX=m / 2.0)
    # never bleed outside the face box (no engine pixels there)
    outer = np.zeros_like(mask)
    outer[y - ys:y1 - ys, x - xs:x1 - xs] = 1
    mask = mask * outer
    return mask, crop_box


def save_blend(path, mask_array, crop_box, face_box):
    np.savez_compressed(path, mask=mask_array,
                        crop_box=np.asarray(crop_box, dtype=np.int32),
                        face_box=np.asarray(face_box, dtype=np.int32))


def load_blend(path):
    """Returns dict(mask=uint8 HxW, crop_box=[4], face_box=[4]) for runtime paste."""
    z = np.load(path)
    return {"mask": z["mask"], "crop_box": z["crop_box"].tolist(),
            "face_box": z["face_box"].tolist()}


# ---- bake: colour-match rendered faces back to the engine cond ----------------

def fit_color_match(frames_bgr, ref_bgr, ring=0.12):
    """Per-channel affine (gain, offset) that maps rendered 512 frames onto the
    reference cond, fitted on the static outer ring (background / sweater,
    where nothing moves). LivePortrait's renderer dims its output ~5-6 grey
    levels across the whole frame (2026-10-10 measurement: neutral -1.6 vs
    cond, LivePortrait -5.8), so a baked idle sits darker than the live
    FlashHead speech frames and the first spoken word reads as the whole
    head brightening. Fit once per bake (median over frames), apply to all.
    Returns (gain[3], offset[3]) as float32 arrays in BGR order."""
    import cv2
    h, w = ref_bgr.shape[:2]
    m = np.zeros((h, w), dtype=bool)
    ry, rx = int(h * ring), int(w * ring)
    m[:ry, :] = m[-ry:, :] = True
    m[:, :rx] = m[:, -rx:] = True
    ref = ref_bgr[m].astype(np.float32)
    gains, offs = [], []
    for f in frames_bgr:
        if f.shape[:2] != (h, w):
            f = cv2.resize(f, (w, h))
        x = f[m].astype(np.float32)
        g, o = [], []
        for ch in range(3):
            A = np.stack([x[:, ch], np.ones_like(x[:, ch])], 1)
            sol, *_ = np.linalg.lstsq(A, ref[:, ch], rcond=None)
            g.append(sol[0]); o.append(sol[1])
        gains.append(g); offs.append(o)
    gain = np.median(np.array(gains), axis=0).astype(np.float32)
    off = np.median(np.array(offs), axis=0).astype(np.float32)
    # sanity: keep it a gentle correction (renderer drift, not a relight)
    gain = np.clip(gain, 0.9, 1.1)
    off = np.clip(off, -16, 16)
    return gain, off


def apply_color_match(frame_bgr, gain, off):
    return np.clip(frame_bgr.astype(np.float32) * gain + off + 0.5, 0, 255).astype(np.uint8)


# ---- runtime: composite the 512 face into the half-body canvas ---------------

def _runtime_cache(body_bgr, blend):
    """Precompute, once per (canvas, blend), everything the per-frame paste
    needs: the RGB canvas, the active blend region (where the feathered mask
    is non-zero — the face slot plus its blur margin) and its float weights.
    Keyed on the canvas array identity so a cond switch (new body_bgr / new
    blend dict) rebuilds it."""
    c = blend.get("_rt")
    if c is not None and c["body_id"] == id(body_bgr):
        return c
    import cv2
    # cv2 defaults to one thread per core (112 here): for ~400px ops the
    # fork/join overhead dominates (resize 6.5 ms -> <1 ms with 4 threads).
    if cv2.getNumThreads() > 4:
        cv2.setNumThreads(4)
    H, W = body_bgr.shape[:2]
    x, y, x1, y1 = blend["face_box"]
    xs, ys, xe, ye = blend["crop_box"]
    mask = blend["mask"]
    # Active region in canvas coordinates: rows/cols where the mask is > 0,
    # clamped to the canvas (PIL zero-padded out-of-canvas crops; the engine's
    # headroom rule keeps the box inside, so clamping is a no-op in practice).
    rows = np.where(mask.max(axis=1) > 0)[0]
    cols = np.where(mask.max(axis=0) > 0)[0]
    if rows.size == 0:
        ry0, ry1, rx0, rx1 = 0, 1, 0, 1
    else:
        ry0, ry1 = ys + int(rows[0]), ys + int(rows[-1]) + 1
        rx0, rx1 = xs + int(cols[0]), xs + int(cols[-1]) + 1
    ry0, rx0 = max(ry0, 0), max(rx0, 0)
    ry1, rx1 = min(ry1, H), min(rx1, W)
    alpha = mask[ry0 - ys:ry1 - ys, rx0 - xs:rx1 - xs].astype(np.float32) / 255.0
    body_rgb = np.ascontiguousarray(body_bgr[:, :, ::-1])
    # Face slot clipped to the region (the slot is always inside it, since the
    # mask was painted inside the face box, but stay safe).
    fy0, fy1 = max(y, ry0), min(y1, ry1)
    fx0, fx1 = max(x, rx0), min(x1, rx1)
    c = {
        "body_id": id(body_bgr),
        "body_rgb": body_rgb,
        "region": (ry0, ry1, rx0, rx1),
        "region_rgb": np.ascontiguousarray(body_rgb[ry0:ry1, rx0:rx1]),
        "alpha": np.ascontiguousarray(alpha),
        "inv_alpha": np.ascontiguousarray(1.0 - alpha),
        # where the (face-box-sized) face lands inside the region, and which
        # part of the face that is
        "slot": (fy0 - ry0, fy1 - ry0, fx0 - rx0, fx1 - rx0),
        "face_sub": (fy0 - y, fy1 - y, fx0 - x, fx1 - x),
        "face_wh": (x1 - x, y1 - y),
        # Output ring: a fresh 2.7 MB allocation page-faults (~2.4 ms); reusing
        # two preallocated canvases cuts the copy to ~0.3 ms. Two slots so the
        # caller may still hold the previous frame while we build the next
        # (the live worker encodes each frame before asking for another).
        "ring": [np.empty_like(body_rgb), np.empty_like(body_rgb)],
        "ri": 0,
    }
    blend["_rt"] = c
    return c


def composite_halfbody(face_rgb, body_bgr, blend):
    """Paste a generated 512 face back onto the static body canvas at the
    persisted bbox, blended with the precomputed feathered mask. Deterministic
    (same inputs -> same bytes): the no-pop guarantee across idle<->speaking.

    Vectorised (cv2, no PIL): the live worker runs this on every speech frame
    in the same process as the diffusion engine, and the PIL version
    (whole-canvas Image.fromarray/paste/np.array round trips, ~75 ms/frame
    holding the GIL) throttled the engine's kernel launches ~4x (denoise step
    0.10s -> 0.45s, RTF 0.7 -> 2.3: audio/video fell further behind every
    chunk, i.e. "the avatar doesn't talk"). Now only the mask's active region
    is blended (cv2.blendLinear) and the canvas is one memcpy.

    face_rgb: HxWx3 RGB from the engine (any size; resized to the face box).
    body_bgr: the static canvas, BGR (cv2.imread of <id>.halfbody.png), reused
              across frames — never mutated.
    blend:    load_blend() dict (a runtime cache is memoised into it).
    Returns the composited frame as RGB uint8 (CANVAS_H x CANVAS_W x 3). The
    buffer is owned by a 2-slot ring: consume (encode) it before calling twice
    more, or .copy() it if you need to keep it.
    """
    import cv2
    c = _runtime_cache(body_bgr, blend)
    fw, fh = c["face_wh"]
    if face_rgb.shape[1] != fw or face_rgb.shape[0] != fh:
        face_rgb = cv2.resize(np.ascontiguousarray(face_rgb), (fw, fh),
                              interpolation=cv2.INTER_LINEAR)
    sy0, sy1, sx0, sx1 = c["slot"]
    gy0, gy1, gx0, gx1 = c["face_sub"]
    # region with the face pasted in (PIL face_large.paste semantics)
    fl = c["region_rgb"].copy()
    fl[sy0:sy1, sx0:sx1] = face_rgb[gy0:gy1, gx0:gx1]
    blended = cv2.blendLinear(fl, c["region_rgb"], c["alpha"], c["inv_alpha"])
    c["ri"] ^= 1
    out = c["ring"][c["ri"]]
    np.copyto(out, c["body_rgb"])
    ry0, ry1, rx0, rx1 = c["region"]
    out[ry0:ry1, rx0:rx1] = blended
    return out


def composite_halfbody_pil(face_rgb, body_bgr, blend):
    """Reference (original) PIL implementation, kept for the equivalence test
    in ``selftest`` only — do not use on the live path."""
    from faceparse.blending import get_image_blending
    x, y, x1, y1 = blend["face_box"]
    face_bgr = face_rgb[:, :, ::-1]
    if face_bgr.shape[1] != (x1 - x) or face_bgr.shape[0] != (y1 - y):
        import cv2
        face_bgr = cv2.resize(np.ascontiguousarray(face_bgr), (x1 - x, y1 - y))
    out_bgr = get_image_blending(body_bgr, np.ascontiguousarray(face_bgr),
                                 blend["face_box"], blend["mask"], blend["crop_box"])
    return np.ascontiguousarray(out_bgr[:, :, ::-1])


def recrop_cmd(out_dir, avatar_id, scale=1.25, up=0.7):
    """Loosen the engine's conditioning crop so the WHOLE head fits.

    FlashHead's built-in face crop (face_ratio 2.0) starts about at the
    hairline: the crown sits outside the box and stays frozen on the static
    canvas while the head below it moves (2026-10-10 "颅顶头发不动"). So we
    pick the square ourselves — the engine box scaled by `scale`, with `up`
    of the extra height going above (hair) and the rest below (collar) — and
    hand the engine that crop as <id>.cond.png with face-crop OFF.

    Updates <id>.bbox.json in place (x1..y2 = the new box, "cond" = the png,
    "face_crop": false; the original engine box is kept as "engine_box").
    Re-run `blend` afterwards — the mask follows the box.
    """
    bbox_path = os.path.join(out_dir, f"{avatar_id}.bbox.json")
    meta = load_bbox(bbox_path)
    hb_path = os.path.join(out_dir, f"{avatar_id}.halfbody.png")
    canvas = Image.open(hb_path).convert("RGB")
    W, H = canvas.size
    eb = meta.get("engine_box") or [meta["x1"], meta["y1"], meta["x2"], meta["y2"]]
    x1, y1, x2, y2 = eb
    size = x2 - x1
    new = int(round(size * scale))
    new = min(new, W, H)
    extra = new - size
    cx = (x1 + x2) // 2
    nx1 = int(round(cx - new / 2))
    ny1 = int(round(y1 - extra * up))
    nx1 = min(max(nx1, 0), W - new)
    ny1 = min(max(ny1, 0), H - new)
    box = [nx1, ny1, nx1 + new, ny1 + new]
    cond = canvas.crop(box).resize((COND_SIZE, COND_SIZE), Image.LANCZOS)
    cond_path = os.path.join(out_dir, f"{avatar_id}.cond.png")
    cond.save(cond_path)
    meta.update({
        "x1": box[0], "y1": box[1], "x2": box[2], "y2": box[3],
        "engine_box": list(eb),
        "cond": os.path.basename(cond_path),
        "face_crop": False,
        "recrop": {"scale": scale, "up": up},
    })
    with open(bbox_path, "w") as f:
        json.dump(meta, f, indent=2)
    print(f"recrop  {cond_path}  box={box} ({new}px, engine box was {size}px) "
          f"-> bbox.json updated; now re-run blend")
    print("RECROP-OK")


def blend_cmd(out_dir, avatar_id, expand=1.5, upper_boundary_ratio=0.0, mode="full", feather=0.06):
    import cv2
    hb_path = os.path.join(out_dir, f"{avatar_id}.halfbody.png")
    bbox = load_bbox(os.path.join(out_dir, f"{avatar_id}.bbox.json"))
    face_box = [bbox["x1"], bbox["y1"], bbox["x2"], bbox["y2"]]
    canvas_bgr = cv2.imread(hb_path)
    mask_array, crop_box = prepare_blend(canvas_bgr, face_box, expand, upper_boundary_ratio,
                                         mode=mode, feather=feather)
    npz_path = os.path.join(out_dir, f"{avatar_id}.blend.npz")
    save_blend(npz_path, mask_array, crop_box, face_box)
    cov = int((mask_array > 8).sum())
    print(f"blend   {npz_path}  mode={mode} mask{mask_array.shape} crop_box={crop_box} "
          f"covered={cov}px ({100*cov/mask_array.size:.1f}% of crop)")
    print("BLEND-OK")


def selftest_cmd(out_dir, avatar_id, face_png=None):
    """Paste a face (default: the cond512, i.e. the engine's own crop) back onto
    the canvas through the blend, to eyeball the seam BEFORE wiring the engine.
    A correct mask makes the cond512 paste indistinguishable from the source."""
    import cv2
    blend = load_blend(os.path.join(out_dir, f"{avatar_id}.blend.npz"))
    body_bgr = cv2.imread(os.path.join(out_dir, f"{avatar_id}.halfbody.png"))
    face_path = face_png or next((q for q in (os.path.join(out_dir, f"{avatar_id}.cond.png"),
                                              os.path.join(out_dir, f"{avatar_id}.cond512.png"))
                                  if os.path.exists(q)), None)
    if not face_path:
        raise SystemExit("selftest: no <id>.cond.png / <id>.cond512.png; pass --face")
    face_rgb = cv2.imread(face_path)[:, :, ::-1]
    out_rgb = composite_halfbody(np.ascontiguousarray(face_rgb), body_bgr, blend)
    prev = os.path.join(out_dir, f"{avatar_id}.selftest.png")
    cv2.imwrite(prev, out_rgb[:, :, ::-1])
    # residual outside the head must be ~0 (static body preserved); inside differs
    diff = np.abs(out_rgb.astype(int) - cv2.imread(
        os.path.join(out_dir, f"{avatar_id}.halfbody.png"))[:, :, ::-1].astype(int))
    print(f"selftest {prev}  max_resid={int(diff.max())} mean_resid={diff.mean():.2f}")
    # vectorised paste must match the PIL reference (+-1 rounding) and be fast
    import time
    ref = composite_halfbody_pil(np.ascontiguousarray(face_rgb), body_bgr, blend)
    dv = np.abs(out_rgb.astype(int) - ref.astype(int))
    n = 50
    t = time.perf_counter()
    for _ in range(n):
        composite_halfbody(face_rgb, body_bgr, blend)
    fast_ms = (time.perf_counter() - t) * 1000 / n
    t = time.perf_counter()
    for _ in range(5):
        composite_halfbody_pil(face_rgb, body_bgr, blend)
    pil_ms = (time.perf_counter() - t) * 1000 / 5
    print(f"vs-pil   max_diff={int(dv.max())} px>1={int((dv > 1).sum())}  "
          f"fast={fast_ms:.2f}ms/frame  pil={pil_ms:.1f}ms/frame")
    if dv.max() > 1:
        raise SystemExit("SELFTEST-FAIL: vectorised composite diverges from PIL reference")
    print("SELFTEST-OK")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("prepare", help="build canvas + bbox from a half-body portrait")
    p.add_argument("--src", required=True)
    p.add_argument("--out-dir", required=True)
    p.add_argument("--id", required=True)
    p.add_argument("--face-ratio", type=float, default=FACE_RATIO)

    b = sub.add_parser("blend", help="precompute the face-parsing paste-back mask (.blend.npz)")
    b.add_argument("--out-dir", required=True)
    b.add_argument("--id", required=True)
    b.add_argument("--expand", type=float, default=1.5)
    b.add_argument("--upper-boundary-ratio", type=float, default=0.0)
    b.add_argument("--mask", choices=["full", "face"], default="full",
                   help="full = whole engine crop, feathered at the box edge (FlashHead); "
                        "face = face-parsing skin mask (lip-sync-only engines)")
    b.add_argument("--feather", type=float, default=0.06,
                   help="full mode: feather width as a fraction of the face box")

    r = sub.add_parser("recrop", help="loosen the engine crop so the whole head (crown) fits; "
                                      "writes <id>.cond.png + updates bbox.json (then re-run blend)")
    r.add_argument("--out-dir", required=True)
    r.add_argument("--id", required=True)
    r.add_argument("--scale", type=float, default=1.25, help="box size vs the engine box")
    r.add_argument("--up", type=float, default=0.7, help="share of the extra height placed above")

    s = sub.add_parser("selftest", help="paste cond512 back via the blend to eyeball the seam")
    s.add_argument("--out-dir", required=True)
    s.add_argument("--id", required=True)
    s.add_argument("--face", default=None)

    args = ap.parse_args()
    if args.cmd == "prepare":
        prepare(args.src, args.out_dir, args.id, args.face_ratio)
    elif args.cmd == "blend":
        blend_cmd(args.out_dir, args.id, args.expand, args.upper_boundary_ratio,
                  args.mask, args.feather)
    elif args.cmd == "recrop":
        recrop_cmd(args.out_dir, args.id, args.scale, args.up)
    elif args.cmd == "selftest":
        selftest_cmd(args.out_dir, args.id, args.face)


if __name__ == "__main__":
    main()
