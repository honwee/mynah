#!/usr/bin/env python
"""Mynah ASR worker — streaming VAD (fsmn-vad) + per-utterance SenseVoice.

Speaks the personalive.asr.v1 contract: cored forwards browser-mic Opus
packets verbatim; we decode (PyAV), resample to 16k mono f32, run streaming
VAD to segment utterances, emit VadEvent edges (cored uses speech=true for
barge-in) and one final Transcript per utterance.

Runs in the `livetalking` conda env (funasr 1.2.6, grpc, PyAV). Models are
loaded from the local modelscope cache — never downloads (system disk full).
"""
import argparse
import logging
import os
import re
import sys
import threading
import time
import queue

import numpy as np

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "gen", "python"))
from asr.v1 import asr_pb2 as pb
from asr.v1 import asr_pb2_grpc as pb_grpc

import grpc

log = logging.getLogger("asrworker")

MS_HUB = "/root/.cache/modelscope/hub/models/iic"
SENSEVOICE = os.path.join(MS_HUB, "SenseVoiceSmall")
FSMN_VAD = os.path.join(MS_HUB, "speech_fsmn_vad_zh-cn-16k-common-pytorch")

SR = 16000
VAD_CHUNK_MS = 200          # fsmn-vad streaming chunk
MAX_BUF_S = 60              # ring buffer bound
PAD_MS = 200                # context padding around VAD segment for decode

# SenseVoice emits <|zh|><|NEUTRAL|>... tags; strip anything in <|...|>
TAG_RE = re.compile(r"<\|[^|]*\|>")


class OpusDecoder:
    """Decode raw Opus packets (RTP payloads) -> 16k mono float32."""

    def __init__(self):
        import av
        self.av = av
        self.ctx = av.CodecContext.create("opus", "r")
        self.ctx.sample_rate = 48000
        self.ctx.layout = "mono"
        self.rs = av.AudioResampler(format="flt", layout="mono", rate=SR)

    def decode(self, data: bytes) -> np.ndarray:
        pkt = self.av.Packet(data)
        out = []
        for frame in self.ctx.decode(pkt):
            frame.pts = None
            for rf in self.rs.resample(frame):
                out.append(rf.to_ndarray().reshape(-1))
        if not out:
            return np.zeros(0, dtype=np.float32)
        return np.concatenate(out).astype(np.float32)


class Models:
    """Shared VAD + SenseVoice models (loaded once, used by all sessions)."""

    def __init__(self, device: str):
        from funasr import AutoModel

        t0 = time.time()
        # VAD streams on CPU (tiny fsmn); SenseVoice decodes per-utterance on GPU.
        self.vad = AutoModel(model=FSMN_VAD, device="cpu",
                             disable_update=True, disable_pbar=True)
        self.sv = AutoModel(model=SENSEVOICE, device=device,
                            trust_remote_code=True,
                            disable_update=True, disable_pbar=True)
        self.sv_lock = threading.Lock()
        log.info("models loaded in %.1fs (vad=cpu sensevoice=%s)", time.time() - t0, device)
        # warmup so the first real utterance doesn't pay torch init
        self.transcribe(np.zeros(SR, dtype=np.float32), "auto")
        log.info("warmup done")

    def transcribe(self, pcm: np.ndarray, language: str) -> str:
        with self.sv_lock:
            res = self.sv.generate(input=pcm, cache={},
                                   language=language or "auto",
                                   use_itn=True, batch_size_s=60)
        if not res or not res[0].get("text"):
            return ""
        return TAG_RE.sub("", res[0]["text"]).strip()


class _Session:
    def __init__(self, models: Models, spec, emit):
        self.m = models
        self.spec = spec
        self.emit = emit  # callback(ServerFrame)
        self.codec = spec.codec or "opus"
        self.dec = OpusDecoder() if self.codec == "opus" else None
        # These two use getattr defaults so an older cored can still drive a
        # newer worker. The flip side bites hard: with STALE generated stubs the
        # field is absent from the descriptor, protobuf silently drops it on the
        # wire, and want_pcm reads False — the worker then does normal ASR while
        # a cloud-VAD cored waits forever for PcmChunks that never come. Symptom
        # is "video flows, no reply, no transcript in cored". So fail loudly at
        # session start instead of degrading silently (see the check below).
        self.want_audio = bool(getattr(spec, "want_utterance_audio", False))
        # PCM passthrough (cloud-VAD mode): decode-and-forward only, no local
        # VAD/transcription — the cloud model owns segmentation.
        self.want_pcm = bool(getattr(spec, "want_pcm", False))
        self.pcm_seq = 0

        self.buf = np.zeros(0, dtype=np.float32)  # absolute stream pcm (trimmed)
        self.buf_off = 0                          # samples trimmed off the front
        self.pend = np.zeros(0, dtype=np.float32) # awaiting a full VAD chunk
        self.vad_cache = {}
        self.utt = 0
        self.speech_beg = -1  # ms, -1 = not in speech

    def feed(self, data: bytes):
        if self.dec is not None:
            pcm = self.dec.decode(data)
        else:
            pcm = np.frombuffer(data, dtype=np.float32)
        if pcm.size == 0:
            return
        if self.want_pcm:
            self.pcm_seq += 1
            self.emit(pb.ServerFrame(pcm=pb.PcmChunk(
                pcm=pcm.astype(np.float32).tobytes(), seq=self.pcm_seq)))
            return
        self.buf = np.concatenate([self.buf, pcm])
        self.pend = np.concatenate([self.pend, pcm])
        chunk = int(SR * VAD_CHUNK_MS / 1000)
        while self.pend.size >= chunk:
            piece, self.pend = self.pend[:chunk], self.pend[chunk:]
            self._vad(piece)
        # bound the buffer (keep speech-in-progress intact)
        keep_from_ms = self.speech_beg - PAD_MS if self.speech_beg >= 0 else None
        max_keep = MAX_BUF_S * SR
        if self.buf.size > max_keep:
            cut = self.buf.size - max_keep
            if keep_from_ms is not None:
                cut = min(cut, max(0, int(keep_from_ms * SR / 1000) - self.buf_off))
            if cut > 0:
                self.buf = self.buf[cut:]
                self.buf_off += cut

    def _vad(self, piece: np.ndarray):
        res = self.m.vad.generate(input=piece, cache=self.vad_cache,
                                  is_final=False, chunk_size=VAD_CHUNK_MS)
        for seg in (res[0]["value"] if res else []):
            beg, end = seg
            if beg != -1 and self.speech_beg < 0:
                self.speech_beg = beg
                self.utt += 1
                log.info("[%s] vad: speech start @%dms utt=%d",
                         self.spec.session_id, beg, self.utt)
                self.emit(pb.ServerFrame(vad=pb.VadEvent(speech=True, utterance=self.utt)))
            if end != -1 and self.speech_beg >= 0:
                self._finish(self.speech_beg, end)
                self.speech_beg = -1

    def _finish(self, beg_ms: int, end_ms: int):
        self.emit(pb.ServerFrame(vad=pb.VadEvent(speech=False, utterance=self.utt)))
        a = max(0, int((beg_ms - PAD_MS) * SR / 1000) - self.buf_off)
        b = min(self.buf.size, int((end_ms + PAD_MS) * SR / 1000) - self.buf_off)
        pcm = self.buf[a:b]
        dur = (end_ms - beg_ms) / 1000.0
        if pcm.size < SR // 5:  # <200ms — noise blip
            log.info("[%s] vad: segment too short (%.2fs), dropped",
                     self.spec.session_id, dur)
            return
        # Hand the utterance audio to cored FIRST (cheap, ~tens of KB) so its
        # turn-detector call overlaps the SenseVoice decode below. Smart Turn
        # only looks at the last 8 s, so cap here to bound the payload.
        if self.want_audio:
            clip = pcm[-8 * SR:]
            self.emit(pb.ServerFrame(utterance_audio=pb.UtteranceAudio(
                pcm=clip.astype(np.float32).tobytes(), utterance=self.utt)))
        t0 = time.time()
        text = self.m.transcribe(pcm, self.spec.language)
        ms = (time.time() - t0) * 1000
        log.info("[%s] utt=%d %.1fs audio -> %.0fms decode: %r",
                 self.spec.session_id, self.utt, dur, ms, text)
        if text:
            self.emit(pb.ServerFrame(transcript=pb.Transcript(
                text=text, final=True, utterance=self.utt)))


class AsrServicer(pb_grpc.AsrEngineServicer):
    def __init__(self, device: str, decode_only: bool = False):
        # decode_only: skip loading SenseVoice + fsmn-VAD entirely and serve as
        # a pure Opus->PCM decoder. That is all a cloud-VAD session ever uses
        # this worker for (the server owns segmentation and transcription), and
        # the models otherwise sit idle holding ~1.4GB of VRAM. See
        # internal/session/cloudvad.go.
        self.decode_only = decode_only
        if decode_only:
            self.models = None
            log.info("decode-only mode: Opus->PCM passthrough only, "
                     "no SenseVoice/VAD loaded (no GPU used)")
        else:
            self.models = Models(device)

    def Health(self, request, context):
        if self.decode_only:
            return pb.HealthReply(ready=True, detail="opus decoder (decode-only)")
        return pb.HealthReply(ready=True, detail="sensevoice+fsmn-vad")

    def Session(self, request_iterator, context):
        out = queue.Queue()
        DONE = object()

        def emit(sf):
            out.put(sf)

        def pump():
            sess = None
            try:
                for cf in request_iterator:
                    which = cf.WhichOneof("msg")
                    if which == "start":
                        # A decode-only worker cannot transcribe. Reject the
                        # session loudly instead of accepting it and going
                        # silent — that failure mode (video fine, never
                        # answers) is miserable to diagnose from the client.
                        if self.decode_only and not cf.start.want_pcm:
                            msg = ("worker runs in --decode-only mode "
                                   "(no SenseVoice loaded) but the session asked "
                                   "for transcription. Restart the ASR worker "
                                   "without --decode-only, or switch 语音轮次 to "
                                   "a cloud mode.")
                            log.error("[%s] %s", cf.start.session_id, msg)
                            emit(pb.ServerFrame(error=pb.EngineError(message=msg)))
                            break
                        sess = _Session(self.models, cf.start, emit)
                        log.info("[%s] session start codec=%s lang=%r pcm_passthrough=%s",
                                 cf.start.session_id, sess.codec, cf.start.language,
                                 sess.want_pcm)
                        emit(pb.ServerFrame(ready=pb.Ready()))
                    elif which == "audio" and sess is not None:
                        sess.feed(cf.audio.data)
                    elif which == "close":
                        break
            except Exception as e:  # noqa: BLE001
                log.exception("session pump error")
                emit(pb.ServerFrame(error=pb.EngineError(message=str(e))))
            finally:
                out.put(DONE)

        t = threading.Thread(target=pump, daemon=True)
        t.start()
        while True:
            sf = out.get()
            if sf is DONE:
                break
            yield sf
        log.info("session ended")


def _start_http_transcribe(models, host, port):
    """One-shot transcription over HTTP, alongside the streaming gRPC service.

    cored's voice-clone upload posts the reference audio here (decoded to 16k
    mono s16le PCM) to auto-fill the ICL reference text. Additive — the gRPC
    voice-input path is untouched. Reuses the already-loaded SenseVoice model
    (Models.transcribe is lock-protected, so it serializes safely with live
    sessions).
    """
    import json as _json
    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *a):  # keep the worker log clean
            pass

        def _reply(self, code, obj):
            body = _json.dumps(obj).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self):
            if self.path.rstrip("/") != "/transcribe":
                self._reply(404, {"error": "not found"})
                return
            try:
                n = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(n) if n > 0 else b""
                if not raw:
                    self._reply(400, {"error": "empty body"})
                    return
                lang = self.headers.get("X-Language") or "auto"
                pcm = np.frombuffer(raw, dtype=np.int16).astype(np.float32) / 32768.0
                if pcm.size < SR // 2:  # < 0.5s of audio
                    self._reply(400, {"error": "audio too short"})
                    return
                text = models.transcribe(pcm, lang)
                self._reply(200, {"text": text})
            except Exception as e:  # noqa: BLE001
                log.exception("http transcribe error")
                self._reply(500, {"error": str(e)})

    srv = ThreadingHTTPServer((host, port), Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    log.info("ASR HTTP transcribe on %s:%d/transcribe", host, port)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=9402)
    ap.add_argument("--http-port", type=int, default=9404,
                    help="one-shot HTTP transcribe port (voice-clone ref text)")
    ap.add_argument("--device", default="cuda:0")
    ap.add_argument("--decode-only", action="store_true",
                    help="serve as a pure Opus->PCM decoder: skip loading "
                         "SenseVoice + fsmn-VAD entirely (frees ~1.4GB VRAM). "
                         "Valid only when every session is cloud-VAD "
                         "(brain=qwen + turn=server_vad/smart_turn); a session "
                         "that asks for transcription is rejected with an error.")
    args = ap.parse_args()

    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(levelname)s %(name)s: %(message)s")

    # Guard against stale generated stubs. asr.proto's SessionSpec fields are
    # read with getattr defaults for forward-compat, which means an out-of-date
    # gen/python turns "cloud-VAD passthrough requested" into a silent no-op:
    # the worker transcribes normally, cored waits for PcmChunks that never
    # arrive, and the avatar simply never answers. That failure is invisible in
    # both logs, so refuse to start instead.
    missing = [f for f in ("want_pcm", "want_utterance_audio")
               if f not in {fd.name for fd in pb.SessionSpec.DESCRIPTOR.fields}]
    if missing:
        raise SystemExit(
            f"generated stubs are stale: SessionSpec is missing {missing}. "
            f"Regenerate gen/python from proto/asr/v1/asr.proto (buf generate) — "
            f"running anyway would silently break cloud-VAD mode.")

    servicer = AsrServicer(args.device, decode_only=args.decode_only)
    if args.http_port > 0:
        if args.decode_only:
            # No models to transcribe with. Voice-clone reference-text autofill
            # is the only caller; it degrades to manual entry.
            log.info("decode-only: HTTP /transcribe not served "
                     "(voice-clone reference text must be typed manually)")
        else:
            _start_http_transcribe(servicer.models, "127.0.0.1", args.http_port)

    server = grpc.server(
        __import__("concurrent.futures", fromlist=["ThreadPoolExecutor"])
        .ThreadPoolExecutor(max_workers=8),
        options=[("grpc.max_receive_message_length", 16 * 1024 * 1024)])
    pb_grpc.add_AsrEngineServicer_to_server(servicer, server)
    server.add_insecure_port(f"[::]:{args.port}")
    server.start()
    log.info("ASR worker READY on :%d", args.port)
    server.wait_for_termination()


if __name__ == "__main__":
    main()
