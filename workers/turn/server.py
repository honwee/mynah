#!/usr/bin/env python
"""Mynah turn-detector worker — audio semantic end-of-turn over the
personalive.turn.v1 contract.

cored sends the just-finished utterance as 16 kHz mono float32 LE PCM (the last
<=8 s); we return P(end-of-turn) so cored can hold the turn open and coalesce a
follow-up instead of answering a mid-thought pause. Judging the WAVEFORM
(intonation, pace, trailing fillers like "嗯/那个") is more robust than the
transcript and runs in parallel with ASR.

Model: Smart Turn v3 (Pipecat / Daily, BSD-2) — a Whisper-tiny encoder + a
linear endpoint classifier, exported to ONNX, CPU. Open weights + open data +
open training code, redistributable, so the model is bundled with the repo (no
download, no framework lock-in). The ONNX graph already applies the sigmoid:
its single output IS P(complete) in [0,1] (do NOT sigmoid it again). Per-language
thresholds tune the decision; 0.5 is the model default.

Deps (CPU-only): onnxruntime, transformers (WhisperFeatureExtractor — log-mel
front-end, no torch), numpy.
"""
import argparse
import logging
import os
import sys
import time
from concurrent.futures import ThreadPoolExecutor

import numpy as np

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "gen", "python"))
from turn.v1 import turn_pb2 as pb
from turn.v1 import turn_pb2_grpc as pb_grpc

import grpc

log = logging.getLogger("turnworker")

# Bundled model (BSD-2, redistributable). Override with TURN_MODEL_PATH.
DEFAULT_MODEL = os.path.join(os.path.dirname(__file__), "models", "smart-turn-v3.2.onnx")
MODEL_PATH = os.environ.get("TURN_MODEL_PATH", DEFAULT_MODEL)

SR = 16000
WINDOW_S = 8                 # the model looks at the last 8 s
N = WINDOW_S * SR
DEFAULT_THRESHOLD = 0.5      # Smart Turn default; per-language overrides below

# Per-language end-of-turn thresholds. Smart Turn outputs a calibrated
# P(complete); 0.5 is the model default. zh is tuned at live-mic acceptance —
# keep the default until then (the published zh benchmark uses 0.5).
THRESHOLDS = {}


class TurnModel:
    def __init__(self, model_path: str):
        import onnxruntime as ort
        from transformers import WhisperFeatureExtractor

        t0 = time.time()
        # Whisper-tiny front-end: 80 mel bins, 8 s window -> [1, 80, 800].
        self.fe = WhisperFeatureExtractor(feature_size=80, chunk_length=WINDOW_S)
        so = ort.SessionOptions()
        # The fp32 encoder's matmuls parallelize well: on CPU, inference drops
        # from ~90ms (1 thread) to ~28ms (4) to ~15ms (8). 4 is a good default —
        # fast even on a modest adopter CPU, and turn calls are brief + off the
        # hot media path. Override with TURN_INTRA_OP_THREADS on bigger boxes.
        so.intra_op_num_threads = int(os.environ.get("TURN_INTRA_OP_THREADS", "4"))
        so.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        self.sess = ort.InferenceSession(model_path, so, providers=["CPUExecutionProvider"])
        log.info("turn model loaded in %.1fs (%s)", time.time() - t0, model_path)
        self.predict(np.zeros(SR, dtype=np.float32), "zh")  # warmup
        log.info("warmup done")

    def _features(self, pcm: np.ndarray) -> np.ndarray:
        a = pcm[-N:]
        if a.shape[0] < N:                   # left-pad: audio sits at the END of the window
            a = np.pad(a, (N - a.shape[0], 0))
        feat = self.fe(a, sampling_rate=SR, return_tensors="np", padding="max_length",
                       max_length=N, truncation=True, do_normalize=True).input_features
        return feat.astype(np.float32)

    def predict(self, pcm: np.ndarray, language: str):
        prob = float(self.sess.run(None, {"input_features": self._features(pcm)})[0][0, 0])
        prob = max(0.0, min(1.0, prob))      # already sigmoid'd by the graph; clamp for safety
        thr = THRESHOLDS.get((language or "").lower(), DEFAULT_THRESHOLD)
        return prob, prob >= thr, thr


class TurnServicer(pb_grpc.TurnDetectorServicer):
    def __init__(self, model: TurnModel):
        self.model = model

    def Health(self, request, context):
        return pb.HealthReply(ready=True, detail="smart-turn v3 onnx cpu (audio)")

    def Predict(self, request, context):
        try:
            pcm = np.frombuffer(request.audio_pcm, dtype=np.float32)
            if pcm.size == 0:
                return pb.PredictReply(eot_prob=1.0, end_of_turn=True, threshold=DEFAULT_THRESHOLD)
            t0 = time.time()
            prob, eot, thr = self.model.predict(pcm, request.language)
            log.info("predict lang=%s p=%.4f thr=%.4f eot=%s %.0fms (%.1fs audio)",
                     request.language, prob, thr, eot, (time.time() - t0) * 1000, pcm.size / SR)
            return pb.PredictReply(eot_prob=prob, end_of_turn=eot, threshold=thr)
        except Exception:  # noqa: BLE001
            log.exception("predict error")
            return pb.PredictReply(eot_prob=1.0, end_of_turn=True, threshold=DEFAULT_THRESHOLD)  # fail open


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=9406)
    ap.add_argument("--model", default=MODEL_PATH)
    args = ap.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    servicer = TurnServicer(TurnModel(args.model))
    server = grpc.server(ThreadPoolExecutor(max_workers=4))
    pb_grpc.add_TurnDetectorServicer_to_server(servicer, server)
    server.add_insecure_port(f"[::]:{args.port}")
    server.start()
    log.info("turn worker READY on :%d", args.port)
    server.wait_for_termination()


if __name__ == "__main__":
    main()
