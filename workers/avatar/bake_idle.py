#!/usr/bin/env python
"""Bake the LivePortrait idle frame library into cored-replayable assets:

  * idle_<name>.ivf      — (--codec vp8) the 300-frame ping-pong loop (598
                           frames) encoded as one VP8 stream. Frame 0 is a
                           keyframe, so cored can (re)enter the loop at frame 0
                           any time and the VP8 reference chain stays valid
                           across loop wraps.
  * idle_<name>.h264f    — (--codec h264) same loop as Annex-B H264 in a tiny
                           custom framed container (magic "PLH264F1", then per
                           frame: u32le size + u8 keyflag + data). Keyframes
                           carry in-band SPS/PPS (repeat-headers).
  * idle_<name>.h264f (PLH264F2) — (--segments dir1 dir2 ...) multi-segment
                           layout: each dir is one motion segment whose frame 1
                           is the shared neutral anchor (see
                           prep_idle_segments.py). Per frame: u32le size + u8
                           flag (0=delta, 1=key, 2=anchor key) + data. cored
                           plays segments in random order — anchors are
                           pixel-identical so hops are seamless.
  * silence_20ms.opus    — one steady-state 20ms Opus silence packet (48k
                           stereo, matching the worker's OpusStreamEncoder) that
                           cored emits while idle so the audio RTP clock and the
                           a/v start gate don't wait on the worker.

Offline encode, so we can afford best-quality settings — unlike the worker's
realtime encoder.

Run in the flashhead env (has av/cv2/numpy):
  python bake_idle.py --idle-dir .../idle_libs/custom_avatar \
                      --out-dir assets
  python bake_idle.py --segments segs/seg00 segs/seg01 segs/seg02 \
                      --out-dir assets --codec h264
"""
import os
import glob
import argparse
from fractions import Fraction

import av
import cv2
import numpy as np


def load_frames(idle_dir, size=None):
    paths = sorted(glob.glob(os.path.join(idle_dir, "*.png")))
    frames = []
    for p in paths:
        bgr = cv2.imread(p)
        if bgr is None:
            continue
        rgb = cv2.cvtColor(bgr, cv2.COLOR_BGR2RGB)
        if size and rgb.shape[:2] != (size[1], size[0]):
            rgb = cv2.resize(rgb, size)
        frames.append(np.ascontiguousarray(rgb, dtype=np.uint8))
    if not frames:
        raise SystemExit(f"no frames in {idle_dir}")
    return frames


def scale_bitrate(base, w, h, ref=512 * 512):
    """1.5 Mbps was tuned on 512x512. On a 720x1280 half-body canvas the same
    bitrate smears P-frames and every IDR snaps sharp again: a 1 Hz pulse
    over the whole frame, static sweater included (the 2026-10-10 "待机有点
    卡"). Scale with pixel count, never below the base."""
    return int(base * max(1.0, (w * h) / float(ref)))


def h264_quality_opts(gop, bitrate, qp=14):
    """Offline H264 settings for an idle loop: CONSTANT QP, not ABR/CRF.

    The loop is mostly static, so P-frames are skip blocks (exact copies) and
    the only thing that moves on still areas is each IDR re-quantising the
    texture differently from the previous one. Any rate-controlled mode
    (ABR 5.5 Mbps: 0.43 grey levels per IDR on a 720x1280 knit sweater;
    crf 12: 0.3-0.7 at some IDRs) leaves that 1 Hz tick. With a fixed
    quantiser every IDR lands on the same values -> measured 0.00 at every
    keyframe, and at qp 14 it is still ~40 dB / ~2.7 Mbps on that canvas
    (2026-10-10). `bitrate` is kept only as a VBV ceiling (2x the size-scaled
    rate) so a pathological loop cannot balloon. keyint==min-keyint keeps
    IDRs exactly where cored expects them."""
    cap = int(bitrate * 2)
    return {
        "preset": "veryslow", "profile": "baseline", "forced-idr": "1",
        "x264-params": f"repeat-headers=1:keyint={gop}:min-keyint={gop}:scenecut=0:qp={qp}:"
                       f"vbv-maxrate={cap // 1000}:vbv-bufsize={cap // 1000}",
    }


def bake_ivf(frames, out_path, fps=25, bitrate=1_500_000, gop=25, order="pingpong"):
    # gop=25 (1s): cored may start replaying before ICE finishes, so early
    # packets are lost — a dense keyframe grid bounds the recovery window.
    h, w = frames[0].shape[:2]
    bitrate = scale_bitrate(bitrate, w, h)
    order = frame_order(frames, order)

    out = av.open(out_path, "w", format="ivf")
    st = out.add_stream("libvpx", rate=fps)
    st.width, st.height = w, h
    st.pix_fmt = "yuv420p"
    st.bit_rate = bitrate
    st.codec_context.gop_size = gop
    st.codec_context.time_base = Fraction(1, fps)
    st.codec_context.options = {
        "deadline": "good", "cpu-used": "0",     # offline: best quality
        "lag-in-frames": "0", "auto-alt-ref": "0",
        "error-resilient": "1",
        "maxrate": str(bitrate), "minrate": str(bitrate // 4),
        "bufsize": str(bitrate),
    }
    pts = 0
    for idx in order:
        fr = av.VideoFrame.from_ndarray(frames[idx], format="rgb24")
        fr = fr.reformat(format="yuv420p")
        fr.pts = pts
        pts += 1
        if pts == 1:
            fr.pict_type = av.video.frame.PictureType.I
        for pkt in st.encode(fr):
            out.mux(pkt)
    for pkt in st.encode(None):
        out.mux(pkt)
    out.close()
    return len(order), os.path.getsize(out_path)


def frame_order(frames, mode):
    """pingpong: 0..N-1,N-2..1 (legacy; reverses motion at the fold).
    loop: 0..N-1 + a short crossfade back to frame 0 appended by the caller —
    here it's just linear order; pair with crossfade_close()."""
    if mode == "pingpong":
        return list(range(len(frames))) + list(range(len(frames) - 2, 0, -1))
    return list(range(len(frames)))


def crossfade_close(frames, ease=8):
    """Append ease frames crossfading the tail back into frame 0, closing the
    loop without the ping-pong 'rewind'. Use with a clip whose end pose is
    already near its start pose (a natural loop closure)."""
    out = list(frames)
    tail = frames[-1]
    head = frames[0]
    for i in range(ease):
        t = (i + 1) / (ease + 1)
        out.append(cv2.addWeighted(tail, 1.0 - t, head, t, 0))
    return out


def flow_close(frames, ease=None, ease_min=3, ease_max=24):
    """Append ease frames morphing the tail back into frame 0 along the
    optical flow between them. Unlike a crossfade (which double-exposes the
    two poses and reads as a blurry hiccup), a flow morph moves pixels along
    the actual motion path, so the wrap looks like ordinary head motion.

    ease=None sizes the morph from the content: (residual gap j->0) / (median
    per-frame step), clamped to [ease_min, ease_max], so each morph frame moves
    about as much as an ordinary frame. A fixed 6 made the wrap a speed bump
    whenever auto_loop landed at the end of a fast head move (2026-10-10:
    steps 2.5/frame into a 6-frame morph of 0.3/frame, then a 0.9 wrap pop)."""
    a = frames[-1]
    b = frames[0]
    if ease is None:
        P = np.stack([cv2.resize(cv2.cvtColor(f, cv2.COLOR_BGR2GRAY), (96, 96)).astype(np.float32)
                      for f in frames])
        steps = np.sqrt(np.mean((P[1:] - P[:-1]) ** 2, axis=(1, 2)))
        local = float(np.mean(steps[-5:]))            # speed arriving at the wrap
        start = float(np.mean(steps[:5]))             # speed leaving frame 0
        gap = float(np.sqrt(np.mean((P[0] - P[-1]) ** 2)))
        # Enough frames to cover the gap while decelerating from the arrival
        # speed to the departure speed (trapezoid: gap ~= ease * (local+start)/2).
        ease = int(np.clip(round(2 * gap / max(local + start, 1e-6)), ease_min, ease_max))
        print(f"flow_close: gap {gap:.2f} arrive {local:.2f} depart {start:.2f} -> ease {ease} frames")
    ga = cv2.cvtColor(a, cv2.COLOR_BGR2GRAY)
    gb = cv2.cvtColor(b, cv2.COLOR_BGR2GRAY)
    fab = cv2.calcOpticalFlowFarneback(ga, gb, None, 0.5, 5, 25, 5, 7, 1.5, 0)
    fba = cv2.calcOpticalFlowFarneback(gb, ga, None, 0.5, 5, 25, 5, 7, 1.5, 0)
    h, w = ga.shape
    gx, gy = np.meshgrid(np.arange(w, dtype=np.float32), np.arange(h, dtype=np.float32))

    def warp(img, flow, t):
        mx = gx + flow[..., 0] * t
        my = gy + flow[..., 1] * t
        return cv2.remap(img, mx, my, cv2.INTER_LINEAR, borderMode=cv2.BORDER_REFLECT)

    out = list(frames)
    for i in range(ease):
        # ease-out spacing: big steps first (matching the arrival speed), then
        # smaller ones into frame 0, so the wrap decelerates instead of jumping
        # from fast motion into a slow morph
        u = (i + 1) / (ease + 1)
        t = 1.0 - (1.0 - u) ** 2
        # warp A forward along A->B and B backward along B->A, then blend the
        # two aligned images — features coincide, so no ghosting
        wa = warp(a, fab, -t)        # remap uses inverse mapping: sample A at +t*flow
        wb = warp(b, fba, -(1 - t))
        out.append(cv2.addWeighted(wa, 1.0 - t, wb, t, 0))
    return out


def auto_loop(frames, min_frac=0.5, vel_w=1.5):
    """Auto loop-closer: anchor the loop at frame 0 (the neutral-driven entry
    frame, kept so the speaking->idle handoff stays pop-free) and search for the
    end frame j whose pose AND outgoing velocity best match frame 0 — i.e. where
    wrapping j -> 0 reads like one more ordinary frame step. Trim to [0..j]; the
    caller then flow_close()s the now-small residual gap into a seamless wrap.

    This is the loop_closure2 scoring (gotalk idle_polish R&D) folded into the
    bake pipeline: pose term = how close frame j looks to frame 0; velocity term
    = how close the implied wrap motion (P[1]-P[j]) is to the natural motion at j.
    Returns (trimmed_frames, (0, j)). Falls back to the full clip when too short.
    """
    n = len(frames)
    if n < 8:
        return frames, (0, n - 1)
    # cheap proxy: 96x96 grayscale per frame (color space irrelevant for matching)
    P = np.stack([
        cv2.resize(cv2.cvtColor(f, cv2.COLOR_RGB2GRAY), (96, 96)).astype(np.float32)
        for f in frames
    ])
    V = P[1:] - P[:-1]                       # per-frame velocity field
    min_span = max(4, int(n * min_frac))
    js = np.arange(min_span, n - 1)
    if len(js) == 0:
        return frames, (0, n - 1)
    pose = np.sqrt(np.mean((P[js] - P[0]) ** 2, axis=(1, 2)))
    implied = P[1][None] - P[js]             # motion implied by wrapping j -> 1
    vel = np.sqrt(np.mean((implied - V[js]) ** 2, axis=(1, 2)))
    # Speed term: the loop leaves frame 0 slowly (neutral entry), so a wrap
    # point in the middle of a fast head move decelerates abruptly however the
    # gap is morphed. Penalise |speed(j) - speed(0)|.
    speed = np.sqrt(np.mean(V ** 2, axis=(1, 2)))
    speed0 = float(np.mean(speed[:5]))
    spd = np.abs(np.array([np.mean(speed[max(0, j - 4):j + 1]) for j in js]) - speed0)
    score = pose + vel_w * vel + vel_w * spd
    j = int(js[int(np.argmin(score))])
    return frames[0:j + 1], (0, j)


def bake_h264(frames, out_path, fps=25, bitrate=1_500_000, gop=25, order="pingpong"):
    """Encode the idle loop as Annex-B H264 into the PLH264F1 framed
    container. Offline -> libx264 veryslow for best quality; repeat-headers
    puts SPS/PPS on every IDR so cored can cut in at any keyframe."""
    h, w = frames[0].shape[:2]
    bitrate = scale_bitrate(bitrate, w, h)
    order = frame_order(frames, order)

    cc = av.CodecContext.create("libx264", "w")
    cc.width, cc.height = w, h
    cc.pix_fmt = "yuv420p"
    cc.time_base = Fraction(1, fps)
    cc.framerate = Fraction(fps, 1)
    cc.gop_size = gop
    cc.max_b_frames = 0
    cc.options = h264_quality_opts(gop, bitrate)
    pkts = []
    for i, idx in enumerate(order):
        fr = av.VideoFrame.from_ndarray(frames[idx], format="rgb24")
        fr = fr.reformat(format="yuv420p")
        fr.pts = i
        if i == 0:
            fr.pict_type = av.video.frame.PictureType.I
        for p in cc.encode(fr):
            pkts.append((bool(p.is_keyframe), bytes(p)))
    for p in cc.encode(None):
        pkts.append((bool(p.is_keyframe), bytes(p)))

    with open(out_path, "wb") as fo:
        fo.write(b"PLH264F1")
        for key, data in pkts:
            fo.write(len(data).to_bytes(4, "little"))
            fo.write(b"\x01" if key else b"\x00")
            fo.write(data)
    keys = [i for i, (k, _) in enumerate(pkts) if k]
    assert len(pkts) == len(order) and keys and keys[0] == 0, "bad h264 bake"
    return len(pkts), os.path.getsize(out_path), keys


def bake_h264_segments(seg_dirs, out_path, fps=25, bitrate=1_500_000, gop=25):
    """Encode anchor-aligned segments (from prep_idle_segments.py) into the
    PLH264F2 container. Every segment is a closed GOP run starting with an IDR
    at the shared neutral anchor frame, so cored can enter any segment cold.
    Flag byte: 0=delta, 1=keyframe, 2=anchor (segment-start) keyframe."""
    all_pkts = []   # (flag, data)
    for d in seg_dirs:
        frames = load_frames(d)
        h, w = frames[0].shape[:2]
        bitrate = scale_bitrate(bitrate, w, h)
        cc = av.CodecContext.create("libx264", "w")
        cc.width, cc.height = w, h
        cc.pix_fmt = "yuv420p"
        cc.time_base = Fraction(1, fps)
        cc.framerate = Fraction(fps, 1)
        cc.gop_size = gop
        cc.max_b_frames = 0
        cc.options = h264_quality_opts(gop, bitrate)
        pkts = []
        for i, fr_np in enumerate(frames):
            fr = av.VideoFrame.from_ndarray(fr_np, format="rgb24")
            fr = fr.reformat(format="yuv420p")
            fr.pts = i
            if i == 0:
                fr.pict_type = av.video.frame.PictureType.I
            for p in cc.encode(fr):
                pkts.append((bool(p.is_keyframe), bytes(p)))
        for p in cc.encode(None):
            pkts.append((bool(p.is_keyframe), bytes(p)))
        assert pkts and pkts[0][0], f"{d}: segment frame 0 not a keyframe"
        for j, (key, data) in enumerate(pkts):
            flag = 2 if j == 0 else (1 if key else 0)
            all_pkts.append((flag, data))
        print(f"  segment {d}: {len(pkts)} frames")

    with open(out_path, "wb") as fo:
        fo.write(b"PLH264F2")
        for flag, data in all_pkts:
            fo.write(len(data).to_bytes(4, "little"))
            fo.write(bytes([flag]))
            fo.write(data)
    anchors = [i for i, (f, _) in enumerate(all_pkts) if f == 2]
    assert len(anchors) == len(seg_dirs) and anchors[0] == 0, "bad segment bake"
    return len(all_pkts), os.path.getsize(out_path), anchors


def bake_silence(out_path):
    cc = av.CodecContext.create("libopus", "w")
    cc.sample_rate = 48000
    cc.format = "s16"
    cc.layout = "stereo"
    cc.bit_rate = 128000  # match the live OpusEncoder; silence barely uses it anyway
    cc.time_base = Fraction(1, 48000)
    pkts = []
    pts = 0
    for _ in range(25):  # 25 x 20ms silence; keep a steady-state packet
        f = av.AudioFrame(format="s16", layout="stereo", samples=960)
        for pl in f.planes:
            pl.update(b"\x00" * pl.buffer_size)
        f.sample_rate = 48000
        f.pts = pts
        pts += 960
        for p in cc.encode(f):
            pkts.append(bytes(p))
    if not pkts:
        raise SystemExit("opus produced no packets")
    pkt = pkts[-1]  # past any encoder priming
    with open(out_path, "wb") as fo:
        fo.write(pkt)
    return len(pkt)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--idle-dir", help="legacy single ping-pong frame library")
    ap.add_argument("--segments", nargs="+",
                    help="anchor-aligned segment dirs (PLH264F2 multi-segment bake)")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--name", default="custom_avatar")
    ap.add_argument("--fps", type=int, default=25)
    ap.add_argument("--codec", default="vp8", choices=["vp8", "h264", "both"])
    ap.add_argument("--order", default="pingpong", choices=["pingpong", "loop", "autoloop", "cut"],
                    help="autoloop = search the best wrap point (anchor frame 0), trim, "
                         "then flow-morph the residual into a seamless forward loop "
                         "(productized loop-closer); loop = linear + flow-morph the full "
                         "tail->head gap (assumes end pose already ~= start); cut = linear "
                         "with a hard wrap")
    args = ap.parse_args()

    os.makedirs(args.out_dir, exist_ok=True)

    if args.segments:
        if args.codec == "vp8":
            raise SystemExit("--segments requires --codec h264 (PLH264F2)")
        h264f = os.path.join(args.out_dir, f"idle_{args.name}.h264f")
        print(f"multi-segment bake: {len(args.segments)} segments")
        n, sz, anchors = bake_h264_segments(args.segments, h264f, fps=args.fps)
        print(f"H264F2: {n} frames -> {h264f} ({sz/1024:.0f} KB, {sz/n:.0f} B/frame), "
              f"anchors at {anchors}")
        sil = os.path.join(args.out_dir, "silence_20ms.opus")
        sl = bake_silence(sil)
        print(f"silence packet: {sl} B -> {sil}")
        print("BAKE-OK")
        return

    if not args.idle_dir:
        raise SystemExit("need --idle-dir or --segments")
    frames = load_frames(args.idle_dir)
    if args.order == "autoloop":
        frames, span = auto_loop(frames)
        frames = flow_close(frames)
        print(f"autoloop: wrap at {span} -> {len(frames)} frames (incl. flow-morph tail)")
    elif args.order == "loop":
        frames = flow_close(frames)
    h, w = frames[0].shape[:2]
    print(f"loaded {len(frames)} frames {w}x{h}")

    if args.codec in ("vp8", "both"):
        ivf = os.path.join(args.out_dir, f"idle_{args.name}.ivf")
        n, sz = bake_ivf(frames, ivf, fps=args.fps, order=args.order)
        print(f"IVF: {n} frames -> {ivf} ({sz/1024:.0f} KB, {sz/n:.0f} B/frame)")

        # sanity: decode the IVF back, check frame count + frame0 is a keyframe
        inp = av.open(ivf)
        ks, total = [], 0
        for pkt in inp.demux(video=0):
            if pkt.size == 0:
                continue
            if pkt.is_keyframe:
                ks.append(total)
            total += 1
        inp.close()
        print(f"verify: {total} packets, keyframes at {ks[:8]}{'...' if len(ks) > 8 else ''}")
        assert total == n and ks and ks[0] == 0, "bad bake"

    if args.codec in ("h264", "both"):
        h264f = os.path.join(args.out_dir, f"idle_{args.name}.h264f")
        n, sz, keys = bake_h264(frames, h264f, fps=args.fps, order=args.order)
        print(f"H264F: {n} frames -> {h264f} ({sz/1024:.0f} KB, {sz/n:.0f} B/frame), "
              f"keyframes at {keys[:8]}{'...' if len(keys) > 8 else ''}")

    sil = os.path.join(args.out_dir, "silence_20ms.opus")
    sl = bake_silence(sil)
    print(f"silence packet: {sl} B -> {sil}")
    print("BAKE-OK")


if __name__ == "__main__":
    main()
