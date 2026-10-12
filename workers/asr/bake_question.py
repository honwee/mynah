#!/usr/bin/env python
"""Bake a spoken question into raw Opus packets for the Go voice probe.
Output format: u32le packet size + packet bytes, repeated. 20ms per packet.
Leading/trailing silence included so VAD sees clean utterance edges."""
import struct
import sys
import os

import numpy as np
import requests
import av

SR = 16000
TEXT = sys.argv[1] if len(sys.argv) > 1 else "请用一句话介绍你自己。"
OUT = sys.argv[2] if len(sys.argv) > 2 else os.path.join(os.path.dirname(os.path.abspath(__file__)), "question.opuspkts")


def tts_pcm16k(text):
    r = requests.post("http://127.0.0.1:8091/v1/audio/speech", json={
        "input": text, "voice": "vivian", "response_format": "pcm",
        "speed": 1.0, "language": "Auto", "task_type": "CustomVoice", "stream": True,
    }, stream=True, timeout=60)
    r.raise_for_status()
    raw = b"".join(r.iter_content(8192))
    s16 = np.frombuffer(raw, dtype=np.int16).astype(np.float32) / 32768.0
    n = int(len(s16) * SR / 24000)
    x = np.linspace(0, len(s16) - 1, n)
    return np.interp(x, np.arange(len(s16)), s16).astype(np.float32)


speech = tts_pcm16k(TEXT)
sil = np.zeros(SR, dtype=np.float32)
pcm = np.concatenate([sil, speech, sil])

enc = av.CodecContext.create("libopus", "w")
enc.sample_rate = 48000
enc.layout = "mono"
enc.format = "flt"
enc.bit_rate = 32000
rs = av.AudioResampler(format="flt", layout="mono", rate=48000)

with open(OUT, "wb") as f:
    count = 0
    step = SR // 50
    for i in range(0, len(pcm) - step + 1, step):
        fr = av.AudioFrame.from_ndarray(pcm[i:i + step].reshape(1, -1),
                                        format="flt", layout="mono")
        fr.sample_rate = SR
        fr.pts = None
        for rf in rs.resample(fr):
            rf.pts = None
            for p in enc.encode(rf):
                b = bytes(p)
                f.write(struct.pack("<I", len(b)) + b)
                count += 1
    for p in enc.encode(None):
        b = bytes(p)
        f.write(struct.pack("<I", len(b)) + b)
        count += 1
print(f"baked {count} opus packets ({len(speech)/SR:.1f}s speech + 2s silence) -> {OUT}")
print(f"question: {TEXT}")
