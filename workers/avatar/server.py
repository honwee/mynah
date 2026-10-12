#!/usr/bin/env python
"""Mynah AvatarEngine gRPC worker (P2) — wraps FlashHeadEngine behind the
unified streaming contract (proto/avatarengine/v1).

Output model (frame-grid pacing, one video frame + 40ms audio per 25fps tick):

  * SPEAKING — TTS audio is chunked (0.96s) into the engine, which computes in
    its own thread (~0.5s/chunk, pipelined 2 ahead). The pump plays the ready
    chunk out frame-by-frame on the 40ms grid.
  * IDLE — when no chunk is playing, replay the pre-baked LivePortrait idle
    loop (≈0 GPU) + silence audio, also frame-by-frame.
  * IDLE-LOCAL (SessionSpec.idle_local) — the client (cored) replays its own
    baked idle loop, so the worker emits NOTHING while idle: zero GPU, zero
    gRPC traffic per idle session. Each speech turn starts with a forced VP8
    keyframe (the client's decoder is mid-idle-stream) and ends with a
    SpeechEnd marker so the client knows when to cut back to its idle loop.

The pump runs PREROLL frames (~120ms) ahead of the wall clock as a jitter
cushion. The previous design emitted a whole chunk as one burst, which kept a
~0.8s standing queue in cored — speech started behind that queue. Frame-grid
emission caps the look-ahead at PREROLL*40ms.

BOTH MEDIA ARE ENCODED HERE (PyAV): video -> VP8, audio -> Opus (16k mono f32
-> 48k stereo). cored just forwards/paces the compressed packets to the WebRTC
tracks — NO ffmpeg in cored. This matters because ffmpeg's Opus path buffered
~2s before its first packet, which stalled cored's a/v start-gate and built a
huge video backlog; PyAV emits the first Opus packet in ~20ms.

Pacing uses a MONOTONIC clock. Interrupt clears the speech buffer, bumps gen,
drops the in-play chunk and forces a VP8 keyframe (decoder resync).

Runs in the `flashhead` conda env. Loads + warms the engine once at startup.

Launch (see run_avatar_worker.sh):
  cd <SoulX-FlashHead>
  CUDA_VISIBLE_DEVICES=6 python <repo>/workers/avatar/server.py --port 9401
"""
import os
import sys
import glob
import time
import argparse
import logging
import threading
from fractions import Fraction
from concurrent import futures

import numpy as np
import grpc

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("avatar-worker")

# --- locate engine + generated stubs ----------------------------------------
# FLASHHEAD_DIR is a separate checkout, so there is no default worth guessing:
# compose and the launcher scripts pass it explicitly, and a fabricated absolute
# path just turns "you forgot a variable" into a confusing ImportError.
FH_DIR = os.environ.get("FLASHHEAD_DIR", "")
if not FH_DIR:
    raise SystemExit("avatar worker: set FLASHHEAD_DIR to your SoulX-FlashHead checkout")
ENGINE_DIR = os.path.join(FH_DIR, "worker")
GEN = os.environ.get("PERSONALIVE_GEN",
                     os.path.join(os.path.dirname(__file__), "..", "..", "gen", "python"))
for p in (ENGINE_DIR, GEN, FH_DIR):
    if p not in sys.path:
        sys.path.insert(0, p)

# engine's get_pipeline expects cwd == SoulX-FlashHead
os.chdir(FH_DIR)

import av                                               # noqa: E402
import cv2                                              # noqa: E402
from flashhead_engine import FlashHeadEngine            # noqa: E402
from flash_head.inference import get_infer_params       # noqa: E402
from avatarengine.v1 import avatarengine_pb2 as pb      # noqa: E402
from avatarengine.v1 import avatarengine_pb2_grpc as pbg  # noqa: E402


def scale_bitrate(base, w, h, ref=512 * 512):
    """Bitrates below were tuned on 512x512 faces. A half-body canvas
    (720x1280) has 3.5x the pixels: at the same bitrate the P-frames smear and
    every IDR snaps back sharp — a visible 1 Hz pulse on static areas (knit
    sweater). Scale with pixel count, never below the base."""
    return int(base * max(1.0, (w * h) / float(ref)))


class VP8StreamEncoder:
    """Per-session libvpx VP8 encoder (PyAV). Stateful: one per session; feed
    idle + speech RGB frames so the VP8 reference chain stays continuous."""

    def __init__(self, w, h, fps, bitrate=2_000_000):
        bitrate = scale_bitrate(bitrate, w, h)
        cc = av.CodecContext.create("libvpx", "w")
        cc.width = w
        cc.height = h
        cc.pix_fmt = "yuv420p"
        cc.bit_rate = bitrate
        cc.time_base = Fraction(1, fps)
        cc.gop_size = fps  # ~1s keyframe interval
        cc.options = {
            "deadline": "realtime", "cpu-used": "8",
            "lag-in-frames": "0", "auto-alt-ref": "0", "error-resilient": "1",
            # CBR-ish so high-flicker generative frames can't balloon the bitrate
            "maxrate": str(bitrate), "minrate": str(bitrate), "bufsize": str(bitrate // 2),
        }
        self.cc = cc
        self.pts = 0

    def encode(self, rgb, force_kf=False):
        fr = av.VideoFrame.from_ndarray(np.ascontiguousarray(rgb), format="rgb24")
        fr = fr.reformat(format="yuv420p")
        fr.pts = self.pts
        self.pts += 1
        if force_kf:
            fr.pict_type = av.video.frame.PictureType.I
        return [bytes(p) for p in self.cc.encode(fr)]

    def close(self):
        try:
            for _ in self.cc.encode(None):
                pass
        except Exception:
            pass


class OpusStreamEncoder:
    """Per-session libopus encoder (PyAV). Resamples 16k mono f32 -> 48k stereo
    and emits standalone 20ms Opus packets that cored forwards to its pion audio
    track. First packet emerges in ~20ms (vs ~2s for the ffmpeg/ogg path)."""

    def __init__(self, in_rate=16000):
        cc = av.CodecContext.create("libopus", "w")
        cc.sample_rate = 48000
        cc.format = "s16"
        cc.layout = "stereo"
        cc.bit_rate = 128000  # keep in sync with avatar_common.OpusEncoder
        cc.time_base = Fraction(1, 48000)
        self.cc = cc
        self.res = av.AudioResampler(format="s16", layout="stereo", rate=48000)
        self.in_rate = in_rate
        self.pts = 0

    def encode(self, pcm_f32_16k):
        i16 = np.clip(pcm_f32_16k * 32767.0, -32768, 32767).astype(np.int16).reshape(1, -1)
        f = av.AudioFrame.from_ndarray(i16, format="s16", layout="mono")
        f.sample_rate = self.in_rate
        f.pts = self.pts
        f.time_base = Fraction(1, self.in_rate)
        self.pts += i16.shape[1]
        out = []
        for rf in self.res.resample(f):
            for p in self.cc.encode(rf):
                out.append(bytes(p))
        return out

    def close(self):
        try:
            for p in self.cc.encode(None):
                pass
        except Exception:
            pass


class H264StreamEncoder:
    """Per-session H264 encoder (PyAV). Annex-B output; every keyframe carries
    in-band SPS/PPS (repeat-headers), so a browser can join mid-stream and the
    idle->speech cut can resync on any IDR. libx264 ultrafast/zerolatency by
    default — a few ms/frame at 512px and it keeps the GPU free for the
    generation engine (use codec="h264_nvenc" to trade that around)."""

    def __init__(self, w, h, fps, bitrate=2_000_000, codec="libx264"):
        scaled = scale_bitrate(bitrate, w, h)
        # Canvas-sized streams (half-body 720x1280): IDRs every second
        # re-quantise the big static areas (visible 1 Hz tick on the sweater),
        # so space them 4 s apart and give the VBV a full second of buffer so
        # the IDR itself is not starved. Speech start / barge-in still force
        # an IDR explicitly (force_kf), so resync does not depend on the GOP.
        # 512 faces keep the 1 s GOP they were tuned with.
        big = scaled > bitrate
        # Canvas streams: cap the rate and keep the VBV buffer short so an IDR
        # is at most ~0.4 s of bitrate (~250 KB). A 1 s buffer let IDRs burst
        # to ~900 KB, which a lossy last mile drops wholesale -> NACK storm ->
        # PLI -> another huge IDR (the reproducible ~2 s freeze right after a
        # speech turn started, 2026-10-11). GOP 2 s bounds the stall if a PLI
        # round trip fails; cored now relays viewer PLIs so it rarely matters.
        bitrate = min(scaled, 5_000_000) if big else scaled
        gop = fps * 2 if big else fps
        bufsize = int(bitrate * 0.4) if big else bitrate // 2
        cc = av.CodecContext.create(codec, "w")
        cc.width = w
        cc.height = h
        cc.pix_fmt = "yuv420p"
        cc.bit_rate = bitrate
        cc.time_base = Fraction(1, fps)
        cc.framerate = Fraction(fps, 1)
        cc.gop_size = gop
        cc.max_b_frames = 0
        if codec == "h264_nvenc":
            cc.options = {"preset": "p1", "tune": "ull", "zerolatency": "1",
                          "delay": "0", "forced-idr": "1", "profile": "baseline",
                          "rc": "cbr"}
        else:
            cc.options = {"preset": "ultrafast", "tune": "zerolatency",
                          "profile": "baseline", "forced-idr": "1",
                          "x264-params": f"repeat-headers=1:keyint={gop}:min-keyint={gop}:scenecut=0:"
                                         f"vbv-maxrate={bitrate//1000}:"
                                         f"vbv-bufsize={bufsize//1000}:nal-hrd=cbr"}
        self.cc = cc
        self.pts = 0

    def encode(self, rgb, force_kf=False):
        fr = av.VideoFrame.from_ndarray(np.ascontiguousarray(rgb), format="rgb24")
        fr = fr.reformat(format="yuv420p")
        fr.pts = self.pts
        self.pts += 1
        if force_kf:
            fr.pict_type = av.video.frame.PictureType.I
        return [bytes(p) for p in self.cc.encode(fr)]

    def close(self):
        try:
            for _ in self.cc.encode(None):
                pass
        except Exception:
            pass


def load_idle_frames(idle_dir, w, h):
    """Load the baked LivePortrait idle loop as a list of RGB uint8 HxWx3 frames."""
    if not idle_dir or not os.path.isdir(idle_dir):
        log.warning("idle dir not found (%s) — idle will fall back to engine silence", idle_dir)
        return []
    paths = sorted(glob.glob(os.path.join(idle_dir, "*.png")))
    frames = []
    for p in paths:
        bgr = cv2.imread(p)
        if bgr is None:
            continue
        rgb = cv2.cvtColor(bgr, cv2.COLOR_BGR2RGB)
        if rgb.shape[:2] != (h, w):
            rgb = cv2.resize(rgb, (w, h))
        frames.append(np.ascontiguousarray(rgb, dtype=np.uint8))
    log.info("loaded %d idle frames from %s", len(frames), idle_dir)
    return frames


class AvatarEngineServicer(pbg.AvatarEngineServicer):
    def __init__(self, engine, dims, idle_frames, preroll=3, h264_codec="libx264",
                 avatar_path="", use_face_crop=True, halfbody_png="", blend_path="",
                 face_dims=None):
        self.engine = engine
        self.W, self.H, self.FPS, self.SR = dims
        self.chunk_samples = int(engine.chunk_samples)
        self.slice_len = int(engine.slice_len)
        self.samples_per_frame = self.chunk_samples // self.slice_len
        self.period = self.chunk_samples / float(self.SR)
        self.idle_frames = idle_frames
        self.preroll = preroll
        self.h264_codec = h264_codec
        self._lock = threading.Lock()
        # Per-session avatar switching (Phase B 换人): SessionSpec.cond_image picks
        # which portrait the engine speaks as. The engine boots on avatar_path; a
        # session whose cond_image differs triggers a reload BEFORE it starts (never
        # mid-session — the engine is single-session, guarded by self._lock). Same
        # cond_image as already-loaded = no-op, so steady-state sessions pay nothing.
        self.loaded_avatar = avatar_path
        self.use_face_crop = use_face_crop
        # Half-body presenter: the engine still generates a 512 face; we paste it
        # back onto a static head-to-waist canvas (e.g. 720x1280) at the persisted
        # bbox, so live speaking frames match the half-body idle bake pixel-for-
        # pixel outside the face. self.W/self.H are already the canvas dims (set
        # by main). Idle frames are pre-composited by the bake -> only SPEECH
        # frames composite at runtime. Empty -> legacy 512 path (no composite).
        self.halfbody_png = halfbody_png
        self.body_bgr = None
        self._composite = None
        self._blend = None
        # Engine's native face dims (512) — the legacy fallback size when a session
        # switches to a non-half-body avatar.
        self._face_dims = tuple(face_dims) if face_dims else (self.W, self.H)
        if halfbody_png:
            self._reconfigure_halfbody(halfbody_png, blend_path, boot=True)

    def _reconfigure_halfbody(self, cond_image, blend_path=None, boot=False):
        """(Re)configure half-body compositing for a cond image. A half-body cond
        is <base>.halfbody.png with a sibling <base>.blend.npz: when present we
        paste speech faces onto that canvas and the wire/encoder dims become the
        canvas size. Anything else clears half-body mode (legacy 512 output).
        Called under self._lock at session start (before the encoder is built), so
        self.W/self.H are stable for the whole session. This is what lets ONE prod
        worker serve any avatar — 512 or half-body — by cond_image alone."""
        if blend_path is None and cond_image.endswith(".halfbody.png"):
            blend_path = cond_image[:-len(".halfbody.png")] + ".blend.npz"
        if blend_path and os.path.exists(blend_path):
            from halfbody import load_blend, composite_halfbody
            body = cv2.imread(cond_image)
            if body is not None:
                self.body_bgr = body
                self._blend = load_blend(blend_path)
                self._composite = composite_halfbody
                self.H, self.W = body.shape[0], body.shape[1]
                log.info("half-body avatar: canvas=%dx%d cond=%s face_box=%s",
                         self.W, self.H, cond_image, self._blend["face_box"])
                return
            if boot:
                raise SystemExit(f"--halfbody not readable: {cond_image}")
        # legacy 512 face avatar (or missing/unreadable blend): clear half-body
        self.body_bgr = None
        self._composite = None
        self._blend = None
        self.W, self.H = self._face_dims

    def _speech_frame(self, rgb_face):
        """Speech frames are raw 512 engine faces -> paste into the canvas in
        half-body mode; pass through (already canvas-sized) otherwise."""
        if self.body_bgr is not None:
            return self._composite(rgb_face, self.body_bgr, self._blend)
        return rgb_face

    @staticmethod
    def engine_cond(cond_image, use_face_crop):
        """What the ENGINE conditions on for a catalog cond. A half-body cond
        (<id>.halfbody.png) with a sibling <id>.cond.png (halfbody.py recrop)
        feeds the engine that pre-cut square with face-crop OFF, so the crop
        covers the whole head (crown included) instead of the engine's
        hairline-tight face_ratio-2.0 box. Otherwise the cond itself."""
        if cond_image.endswith(".halfbody.png"):
            pre = cond_image[:-len(".halfbody.png")] + ".cond.png"
            if os.path.exists(pre):
                return pre, False
        return cond_image, use_face_crop

    def _ensure_avatar(self, cond_image):
        """Load cond_image as the speaking avatar if it differs from the current
        one. Called at session start under self._lock. Best-effort: a bad path is
        logged and the current avatar is kept (the session still runs)."""
        cond_image = (cond_image or "").strip()
        if not cond_image or cond_image == self.loaded_avatar:
            return
        if not os.path.exists(cond_image):
            log.warning("cond_image not found, keeping current avatar: %s", cond_image)
            return
        t = time.time()
        try:
            ec, fc = self.engine_cond(cond_image, self.use_face_crop)
            self.engine.load_avatar(ec, use_face_crop=fc)
            self.engine.warmup(n_chunks=1)
        except Exception as e:  # noqa: BLE001
            log.warning("load_avatar(%s) failed, keeping current: %s", cond_image, e)
            return
        self.loaded_avatar = cond_image
        # Lockstep: a half-body cond brings its own canvas+blend (and dims). Switch
        # them together so speech frames composite onto the right body. Runs under
        # the same self._lock as the engine reload, at session start.
        self._reconfigure_halfbody(cond_image)
        log.info("avatar switched to %s in %.1fs", cond_image, time.time() - t)

    def Health(self, request, context):
        return pb.HealthReply(ready=True, detail="flashhead avatar engine")

    def Session(self, request_iterator, context):
        if not self._lock.acquire(blocking=False):
            yield pb.ServerFrame(error=pb.EngineError(message="engine busy"))
            return
        engine = self.engine
        started = threading.Event()
        closing = threading.Event()

        spk_lock = threading.Lock()
        spk = {"buf": np.zeros(0, dtype=np.float32), "arrived": 0.0, "last_rx": 0.0}
        force_kf = {"v": False}
        intr = {"epoch": 0, "t": 0.0}
        idle_local = {"v": False}
        vcodec = {"v": "vp8"}

        def consume():
            try:
                for cf in request_iterator:
                    which = cf.WhichOneof("msg")
                    if which == "start":
                        idle_local["v"] = bool(cf.start.idle_local)
                        if cf.start.video_codec:
                            vcodec["v"] = cf.start.video_codec
                        # Phase B 换人: switch the speaking avatar if this session
                        # asks for a different cond_image (before the engine starts).
                        self._ensure_avatar(cf.start.cond_image)
                        engine.start_session(cf.start.session_id or "s", av_chunks=True)
                        started.set()
                        log.info("session started: %s (av_chunks, idle_local=%s, codec=%s, avatar=%s)",
                                 cf.start.session_id, idle_local["v"], vcodec["v"], self.loaded_avatar)
                    elif which == "audio":
                        pcm = np.frombuffer(cf.audio.pcm_f32le_16k, dtype=np.float32)
                        now = time.monotonic()
                        with spk_lock:
                            if spk["buf"].shape[0] == 0:
                                spk["arrived"] = now
                            spk["buf"] = np.concatenate([spk["buf"], pcm])
                            spk["last_rx"] = now
                    elif which == "interrupt":
                        engine.interrupt()
                        with spk_lock:
                            spk["buf"] = np.zeros(0, dtype=np.float32)
                        intr["t"] = time.monotonic()
                        intr["epoch"] += 1
                        force_kf["v"] = True
                        log.info("interrupt")
                    elif which == "set_action":
                        act = cf.set_action.action
                        if act == "keyframe":
                            # viewer lost packets (cored relays its PLI): re-key
                            # the next speech frame instead of waiting for the GOP
                            force_kf["v"] = True
                        else:
                            try:
                                engine.set_action(act)
                            except Exception as e:  # noqa: BLE001
                                log.warning("set_action(%s): %s", act, e)
                    elif which == "close":
                        break
            except Exception as e:
                log.warning("consume ended: %s", e)
            finally:
                closing.set()

        ct = threading.Thread(target=consume, daemon=True)
        ct.start()
        venc = aenc = None
        try:
            if not started.wait(timeout=30):
                yield pb.ServerFrame(error=pb.EngineError(message="no SessionSpec received"))
                return

            yield pb.ServerFrame(ready=pb.Ready(width=self.W, height=self.H,
                                                fps=self.FPS, sample_rate=self.SR))

            if vcodec["v"] == "h264":
                venc = H264StreamEncoder(self.W, self.H, self.FPS, codec=self.h264_codec)
            else:
                venc = VP8StreamEncoder(self.W, self.H, self.FPS)
            aenc = OpusStreamEncoder(self.SR)
            idle = self.idle_frames
            n_idle = len(idle)
            idle_idx, idle_dir = 0, 1
            tick = 1.0 / self.FPS
            spf = self.samples_per_frame
            sil_frame = np.zeros(spf, dtype=np.float32)
            vframes, aseq = 0, 0
            t0 = None

            PREROLL = self.preroll
            cur = None        # chunk being played out: {"frames","pcm","i","gen"}
            pending = 0       # chunks pushed to the engine, not yet picked up
            last_push = 0.0
            seen_epoch = 0
            prev_idle = True
            ilocal = idle_local["v"]
            end_sent = True   # idle_local: SpeechEnd already delivered for the turn

            def emit_audio(pcm):
                nonlocal aseq
                for op in aenc.encode(pcm):
                    yield pb.ServerFrame(audio=pb.AudioFrame(seq=aseq, pcm_f32le_16k=op))
                    aseq += 1

            def emit_video(rgb, gen):
                nonlocal vframes
                kf = force_kf["v"]
                force_kf["v"] = False
                for enc in venc.encode(rgb, force_kf=kf):
                    yield pb.ServerFrame(video=pb.VideoFrame(
                        idx=vframes, gen=int(gen), width=self.W, height=self.H,
                        pix_fmt=vcodec["v"], data=enc))
                    vframes += 1

            def feed():
                # Keep the engine pipelined up to 2 chunks ahead: chunk N+1
                # computes (~0.5s) while chunk N plays out (0.96s).
                nonlocal pending, last_push
                if pending >= 2:
                    return
                take = None
                now = time.monotonic()
                with spk_lock:
                    avail = spk["buf"].shape[0]
                    if avail >= self.chunk_samples:
                        take = spk["buf"][:self.chunk_samples].copy()
                        spk["buf"] = spk["buf"][self.chunk_samples:]
                    elif avail > 0 and now - spk["last_rx"] > 0.12:
                        # turn tail: TTS stopped streaming, pad the remainder
                        take = np.zeros(self.chunk_samples, dtype=np.float32)
                        take[:avail] = spk["buf"]
                        spk["buf"] = np.zeros(0, dtype=np.float32)
                if take is None and not ilocal and n_idle == 0 and pending == 0 and cur is None:
                    # no idle library: drive the engine with silence instead
                    take = np.zeros(self.chunk_samples, dtype=np.float32)
                if take is not None:
                    engine.push_audio(take)
                    pending += 1
                    last_push = now

            n = 0
            t_base = time.monotonic()
            while not closing.is_set():
                if seen_epoch != intr["epoch"]:
                    # Barge-in: drop the in-play chunk + anything already computed.
                    seen_epoch = intr["epoch"]
                    cur = None
                    pending = 0
                    while engine.read_video_frame(timeout=0) is not None:
                        pass
                    if ilocal and not end_sent:
                        yield pb.ServerFrame(speech_end=pb.SpeechEnd(gen=intr["epoch"]))
                        end_sent = True

                feed()

                if cur is None:
                    pkt = engine.read_video_frame(timeout=0)
                    if pkt is not None:
                        if intr["t"] > 0 and last_push < intr["t"]:
                            # computed from pre-interrupt audio that raced past
                            # the engine's queue clear — stale, drop it
                            pkt = None
                        else:
                            pending = max(0, pending - 1)
                    if pkt is not None:
                        gen, _chunk_idx, frames, slice_pcm = pkt
                        cur = {"frames": frames, "pcm": slice_pcm, "i": 0, "gen": gen}
                        if t0 is None:
                            t0 = time.monotonic()
                        if prev_idle:
                            with spk_lock:
                                arr = spk["arrived"]
                            log.info("speech start: arrival->first emit %.3fs (compute %.3fs)",
                                     time.monotonic() - arr, time.monotonic() - last_push)
                        if ilocal and end_sent:
                            # new speech turn: the client's decoder has been
                            # showing its local idle stream — resync with a KF
                            force_kf["v"] = True
                            end_sent = False
                    elif ilocal and not end_sent and pending == 0:
                        # speech turn drained (engine empty, nothing queued,
                        # TTS gone quiet) -> tell the client to cut to idle
                        with spk_lock:
                            quiet = (spk["buf"].shape[0] == 0
                                     and time.monotonic() - spk["last_rx"] > 0.25)
                        if quiet:
                            yield pb.ServerFrame(speech_end=pb.SpeechEnd(gen=intr["epoch"]))
                            end_sent = True
                            log.info("speech end")

                if cur is not None:
                    i = cur["i"]
                    yield from emit_audio(cur["pcm"][i * spf:(i + 1) * spf])
                    yield from emit_video(self._speech_frame(cur["frames"][i]), cur["gen"])
                    cur["i"] += 1
                    if cur["i"] >= cur["frames"].shape[0]:
                        cur = None
                    prev_idle = False
                elif ilocal:
                    # idle_local: the client replays its own idle loop —
                    # emit nothing at all (zero GPU, zero gRPC while idle)
                    prev_idle = True
                elif n_idle > 0:
                    yield from emit_audio(sil_frame)
                    yield from emit_video(idle[idle_idx], 0)
                    idle_idx += idle_dir
                    if idle_idx <= 0:
                        idle_idx, idle_dir = 0, 1
                    elif idle_idx >= n_idle - 1:
                        idle_idx, idle_dir = n_idle - 1, -1
                    prev_idle = True
                else:
                    # no idle library and no chunk ready yet: keep audio alive;
                    # cored's video pacer holds cadence via skipped ticks
                    yield from emit_audio(sil_frame)
                    prev_idle = True

                n += 1
                due = t_base + (n - PREROLL) * tick
                slp = due - time.monotonic()
                if slp > 0:
                    time.sleep(slp)
                elif slp < -0.5:
                    # stalled badly — resync the grid instead of bursting to catch up
                    t_base -= slp

            dur = (time.monotonic() - t0) if t0 else 0.0
            yield pb.ServerFrame(stats=pb.Stats(
                rtf=(vframes / self.FPS / dur) if dur > 0 else 0.0,
                first_frame_ms=0.0, frames=vframes))
            log.info("session done: %d video frames, %d audio packets", vframes, aseq)
        finally:
            if venc is not None:
                venc.close()
            if aenc is not None:
                aenc.close()
            engine.close_session()
            self._lock.release()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=9401)
    ap.add_argument("--ckpt", default=os.path.join(FH_DIR, "models/SoulX-FlashHead-1_3B"))
    ap.add_argument("--w2v", default=os.path.join(FH_DIR, "models/wav2vec2-base-960h"))
    ap.add_argument("--avatar", default=os.path.join(FH_DIR, "examples/custom_avatar.jpg"))
    ap.add_argument("--type", default="lite")
    ap.add_argument("--no-face-crop", action="store_true")
    ap.add_argument("--idle-dir",
                    default=os.environ.get("FLASHHEAD_IDLE_DIR", ""),
                    help="dir of pre-baked LivePortrait idle frames (PNG); "
                         "empty = fall back to generated silence idle")
    ap.add_argument("--preroll", type=int,
                    default=int(os.environ.get("PERSONALIVE_PREROLL", "3")),
                    help="frames of look-ahead cushion (latency vs jitter tolerance)")
    ap.add_argument("--h264-codec", default=os.environ.get("PERSONALIVE_H264", "libx264"),
                    help="h264 encoder when the session asks for h264 (libx264 / h264_nvenc)")
    ap.add_argument("--halfbody", default="",
                    help="half-body canvas PNG (== engine cond); speech faces are "
                         "composited back onto it. Empty -> legacy 512 face output.")
    ap.add_argument("--blend", default="",
                    help="precomputed <id>.blend.npz (mask+crop_box+face_box), required with --halfbody")
    args = ap.parse_args()

    log.info("loading FlashHeadEngine ...")
    t = time.time()
    # Half-body: the engine cond IS the canvas (it face-crops internally), so the
    # boot avatar defaults to --halfbody unless --avatar was given explicitly.
    if args.halfbody and not args.blend:
        raise SystemExit("--halfbody requires --blend <id>.blend.npz")
    boot_avatar = args.avatar
    if args.halfbody and args.avatar == ap.get_default("avatar"):
        boot_avatar = args.halfbody
    engine = FlashHeadEngine(args.ckpt, args.w2v, args.type)
    boot_cond, boot_fc = AvatarEngineServicer.engine_cond(boot_avatar, not args.no_face_crop)
    engine.load_avatar(boot_cond, use_face_crop=boot_fc)
    log.info("engine loaded in %.1fs; warming up (torch.compile) ...", time.time() - t)
    t = time.time()
    engine.warmup(n_chunks=2)
    log.info("warmup done in %.1fs", time.time() - t)

    ip = get_infer_params()
    face_dims = (int(ip["width"]), int(ip["height"]))
    dims = (face_dims[0], face_dims[1], int(ip["tgt_fps"]), int(ip["sample_rate"]))
    # Half-body output is the CANVAS, not the 512 face: override the wire/encoder
    # dims to the canvas size so idle (pre-composited) and speech (composited at
    # runtime) share one track size.
    if args.halfbody:
        hb = cv2.imread(args.halfbody)
        if hb is None:
            raise SystemExit(f"--halfbody not readable: {args.halfbody}")
        dims = (hb.shape[1], hb.shape[0], dims[2], dims[3])
    log.info("dims w=%d h=%d fps=%d sr=%d chunk_samples=%d slice_len=%d",
             *dims, int(engine.chunk_samples), int(engine.slice_len))

    idle_frames = load_idle_frames(args.idle_dir, dims[0], dims[1])
    # live==idle invariant: idle frames must already be the canvas size (the bake
    # composites them), so the encoder never sees a size change mid-stream.
    if args.halfbody and idle_frames:
        assert idle_frames[0].shape[:2] == (dims[1], dims[0]), (
            f"idle frame {idle_frames[0].shape[:2]} != canvas {(dims[1], dims[0])} "
            f"— bake the idle with --halfbody so live and idle match")

    opts = [("grpc.max_send_message_length", 16 * 1024 * 1024),
            ("grpc.max_receive_message_length", 16 * 1024 * 1024)]
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4), options=opts)
    pbg.add_AvatarEngineServicer_to_server(
        AvatarEngineServicer(engine, dims, idle_frames, preroll=args.preroll,
                             h264_codec=args.h264_codec,
                             avatar_path=boot_avatar, use_face_crop=not args.no_face_crop,
                             halfbody_png=args.halfbody, blend_path=args.blend,
                             face_dims=face_dims), server)
    server.add_insecure_port(f"[::]:{args.port}")
    server.start()
    log.info("AvatarEngine gRPC worker READY on :%d", args.port)
    server.wait_for_termination()


if __name__ == "__main__":
    main()
