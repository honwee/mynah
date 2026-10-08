#!/usr/bin/env python
"""Model-agnostic AvatarEngine plumbing shared by every avatar backend.

This is the cored "avatar plugin" base: the gRPC streaming pump + the three
PyAV stream encoders (VP8 / H264 / Opus) are identical no matter which model
generates the frames, so they live here. A backend only has to provide an
*engine* object implementing this duck-typed contract (the same one
FlashHeadEngine already exposes):

    engine.chunk_samples            -> int   audio samples per pushed chunk
    engine.slice_len                -> int   video frames produced per chunk
    engine.start_session(sid, av_chunks=True)
    engine.push_audio(np.float32[chunk_samples])
    engine.read_video_frame(timeout=0) -> (gen, chunk_idx, frames, slice_pcm) | None
                                          frames: np.uint8[slice_len, H, W, 3] RGB
                                          slice_pcm: np.float32[chunk_samples]
    engine.interrupt()
    engine.close_session()

`frame_transform(rgb)->rgb` is an optional per-emitted-frame hook (FlashHead's
half-body compositor plugs in here; wav2lip passes identity because its frames
are already full-canvas).

The pump is copied verbatim from the original FlashHead worker (server.py) with
the FlashHead-specific bits (half-body composite, per-session cond_image avatar
switching) factored out behind the engine/frame_transform seams — so behaviour
is byte-identical for the parts that remain. See proto/avatarengine/v1.
"""
import glob
import logging
import os
import threading
import time
from fractions import Fraction

import numpy as np
import grpc  # noqa: F401  (import kept for parity with the FlashHead worker)

import av
import cv2

from avatarengine.v1 import avatarengine_pb2 as pb
from avatarengine.v1 import avatarengine_pb2_grpc as pbg

log = logging.getLogger("avatar-worker")


class VP8StreamEncoder:
    """Per-session libvpx VP8 encoder (PyAV). Stateful: one per session; feed
    idle + speech RGB frames so the VP8 reference chain stays continuous."""

    def __init__(self, w, h, fps, bitrate=2_000_000):
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
        cc.bit_rate = 128000  # speech at 48k/stereo sounds smeared; source is 16k so this is the cheap half of the fix
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
    default — a few ms/frame and it keeps the GPU free for the generation
    engine (use codec="h264_nvenc" to trade that around)."""

    def __init__(self, w, h, fps, bitrate=2_000_000, codec="libx264"):
        cc = av.CodecContext.create(codec, "w")
        cc.width = w
        cc.height = h
        cc.pix_fmt = "yuv420p"
        cc.bit_rate = bitrate
        cc.time_base = Fraction(1, fps)
        cc.framerate = Fraction(fps, 1)
        cc.gop_size = fps  # ~1s keyframe interval
        cc.max_b_frames = 0
        if codec == "h264_nvenc":
            cc.options = {"preset": "p1", "tune": "ull", "zerolatency": "1",
                          "delay": "0", "forced-idr": "1", "profile": "baseline",
                          "rc": "cbr"}
        else:
            cc.options = {"preset": "ultrafast", "tune": "zerolatency",
                          "profile": "baseline", "forced-idr": "1",
                          "x264-params": f"repeat-headers=1:vbv-maxrate={bitrate//1000}:"
                                         f"vbv-bufsize={bitrate//2000}:nal-hrd=cbr"}
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
    """Load a baked idle loop as a list of RGB uint8 HxWx3 frames (optional)."""
    if not idle_dir or not os.path.isdir(idle_dir):
        if idle_dir:
            log.warning("idle dir not found (%s) — engine drives idle instead", idle_dir)
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


class AvatarSessionServicer(pbg.AvatarEngineServicer):
    """Generic AvatarEngine.Session pump. Model-agnostic: it only speaks the
    engine duck-type contract documented at module top. One session at a time
    (the engine is single-session, guarded by self._lock)."""

    def __init__(self, engine, dims, idle_frames=None, preroll=3,
                 h264_codec="libx264", frame_transform=None,
                 health_detail="avatar engine"):
        self.engine = engine
        self.W, self.H, self.FPS, self.SR = dims
        self.chunk_samples = int(engine.chunk_samples)
        self.slice_len = int(engine.slice_len)
        self.samples_per_frame = self.chunk_samples // self.slice_len
        self.period = self.chunk_samples / float(self.SR)
        self.idle_frames = idle_frames or []
        self.preroll = preroll
        self.h264_codec = h264_codec
        self.frame_transform = frame_transform or (lambda rgb: rgb)
        self.health_detail = health_detail
        self._lock = threading.Lock()

    def Preload(self, request, context):
        """Make an avatar resident before any session asks for it.

        Session loads inline and only emits Ready afterwards, so the first
        visitor to request an unloaded avatar waits ~15s on a blank screen.
        Moving that cost here lets the control plane pay it when an operator
        binds an avatar to a channel — a moment where waiting is expected.

        Engines with a single baked avatar have no preload_avatar and answer
        "not supported" rather than pretending to succeed."""
        avatar = (request.avatar or "").strip()
        if not avatar:
            return pb.PreloadReply(ready=False, detail="empty avatar")
        fn = getattr(self.engine, "preload_avatar", None)
        if fn is None:
            return pb.PreloadReply(
                ready=False,
                detail="engine serves a single baked avatar; preload not supported")
        t0 = time.time()
        try:
            already = fn(avatar)
        except Exception as e:  # noqa: BLE001
            log.warning("preload %s failed: %s", avatar, e)
            return pb.PreloadReply(ready=False, detail=str(e))
        ms = 0 if already else int((time.time() - t0) * 1000)
        log.info("preload %s: %s (%dms)", avatar,
                 "already resident" if already else "loaded", ms)
        return pb.PreloadReply(ready=True, already=bool(already), load_ms=ms,
                               detail="resident")

    def Health(self, request, context):
        return pb.HealthReply(ready=True, detail=self.health_detail)

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
                        # cond_image carries the requested avatar id. Engines
                        # that preload several avatars switch on it; engines
                        # with a single baked avatar ignore the kwarg.
                        try:
                            engine.start_session(cf.start.session_id or "s",
                                                 av_chunks=True,
                                                 avatar=cf.start.cond_image or "")
                        except TypeError:
                            engine.start_session(cf.start.session_id or "s", av_chunks=True)
                        started.set()
                        log.info("session started: %s (av_chunks, idle_local=%s, codec=%s)",
                                 cf.start.session_id, idle_local["v"], vcodec["v"])
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
            except Exception as e:  # noqa: BLE001
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
                # Keep the engine pipelined up to 2 chunks ahead.
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
                    # no idle library: drive the engine with silence (idle frames)
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
                            # new speech turn: client decoder showed local idle
                            force_kf["v"] = True
                            end_sent = False
                    elif ilocal and not end_sent and pending == 0:
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
                    yield from emit_video(self.frame_transform(cur["frames"][i]), cur["gen"])
                    cur["i"] += 1
                    if cur["i"] >= cur["frames"].shape[0]:
                        cur = None
                    prev_idle = False
                elif ilocal:
                    # idle_local: client replays its own idle loop — emit nothing
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
                    # no idle library and no chunk ready: keep audio alive
                    yield from emit_audio(sil_frame)
                    prev_idle = True

                n += 1
                due = t_base + (n - PREROLL) * tick
                slp = due - time.monotonic()
                if slp > 0:
                    time.sleep(slp)
                elif slp < -0.5:
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
