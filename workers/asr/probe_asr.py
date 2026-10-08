#!/usr/bin/env python
"""End-to-end probe for the ASR worker: TTS a sentence -> encode to Opus
packets (like browser mic) -> stream into AsrEngine.Session with leading and
trailing silence -> expect VadEvents + a final Transcript."""
import os
import sys
import threading
import time
import queue

import numpy as np
import requests

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "gen", "python"))
from asr.v1 import asr_pb2 as pb
from asr.v1 import asr_pb2_grpc as pb_grpc
import grpc
import av

SR = 16000
TEXT = "今天天气怎么样，适合出去跑步吗？"


def tts_pcm16k(text: str) -> np.ndarray:
    r = requests.post("http://127.0.0.1:8091/v1/audio/speech", json={
        "input": text, "voice": "vivian", "response_format": "pcm",
        "speed": 1.0, "language": "Auto", "task_type": "CustomVoice", "stream": True,
    }, stream=True, timeout=60)
    r.raise_for_status()
    raw = b"".join(r.iter_content(8192))
    s16 = np.frombuffer(raw, dtype=np.int16).astype(np.float32) / 32768.0
    # 24k -> 16k linear resample
    n = int(len(s16) * SR / 24000)
    x = np.linspace(0, len(s16) - 1, n)
    return np.interp(x, np.arange(len(s16)), s16).astype(np.float32)


def opus_packets(pcm16k: np.ndarray):
    """Encode 16k mono f32 to 20ms Opus packets at 48k (browser-like)."""
    enc = av.CodecContext.create("libopus", "w")
    enc.sample_rate = 48000
    enc.layout = "mono"
    enc.format = "flt"
    enc.bit_rate = 32000
    rs = av.AudioResampler(format="flt", layout="mono", rate=48000)
    pkts = []
    step = SR // 50  # 20ms at 16k
    for i in range(0, len(pcm16k) - step + 1, step):
        fr = av.AudioFrame.from_ndarray(pcm16k[i:i + step].reshape(1, -1),
                                        format="flt", layout="mono")
        fr.sample_rate = SR
        fr.pts = None
        for rf in rs.resample(fr):
            rf.pts = None
            for p in enc.encode(rf):
                pkts.append(bytes(p))
    for p in enc.encode(None):
        pkts.append(bytes(p))
    return pkts


def main():
    speech = tts_pcm16k(TEXT)
    sil = np.zeros(SR, dtype=np.float32)  # 1s silence
    stream_pcm = np.concatenate([sil, speech, sil, sil])
    pkts = opus_packets(stream_pcm)
    print(f"tts {len(speech)/SR:.1f}s speech, {len(pkts)} opus packets total")

    chan = grpc.insecure_channel("127.0.0.1:9402")
    stub = pb_grpc.AsrEngineStub(chan)
    q = queue.Queue()

    def gen():
        yield pb.ClientFrame(start=pb.SessionSpec(session_id="probe", codec="opus"))
        for i, p in enumerate(pkts):
            yield pb.ClientFrame(audio=pb.AudioPacket(data=p, seq=i))
            time.sleep(0.02)  # realtime pacing
        time.sleep(1.0)
        yield pb.ClientFrame(close=pb.Close())

    t0 = time.time()
    got_transcript = False
    for sf in stub.Session(gen()):
        which = sf.WhichOneof("msg")
        ts = time.time() - t0
        if which == "vad":
            print(f"+{ts:.2f}s VAD speech={sf.vad.speech} utt={sf.vad.utterance}")
        elif which == "transcript":
            print(f"+{ts:.2f}s TRANSCRIPT utt={sf.transcript.utterance}: {sf.transcript.text!r}")
            got_transcript = True
        elif which == "ready":
            print(f"+{ts:.2f}s ready")
        elif which == "error":
            print(f"+{ts:.2f}s ERROR: {sf.error.message}")
    print("PASS" if got_transcript else "FAIL: no transcript")


if __name__ == "__main__":
    main()
