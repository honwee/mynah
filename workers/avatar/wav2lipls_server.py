#!/usr/bin/env python
"""Mynah AvatarEngine gRPC worker — Wav2LipLS (high-quality) backend.

Drop-in alternative to server.py / wav2lip_server.py: same
proto/avatarengine/v1 contract, model-agnostic pump from avatar_common. Runs in
the `livetalking` conda env. face_size 192 or 384 chosen by --face-size; the
checkpoint must match the chosen size (probed from the ckpt's audioBlocks.fc shape).

Model assets (checkpoint + baked avatar dir) are NOT vendored — they are fetched
by deploy/setup/avatar.sh into $PL_MODELS_DIR and passed here via --ckpt /
--avatar-dir (or the WAV2LIPLS_CKPT / AVATAR_DIR env vars). No private paths are
baked into this file.

Launch (see run_wav2lipls_worker.sh):
  CUDA_VISIBLE_DEVICES=2 PERSONALIVE_GEN=<repo>/gen/python \
    python wav2lipls_server.py --port 9414 --face-size 384 \
      --ckpt $PL_MODELS_DIR/wav2lipls/wav2lipls-384.pth \
      --avatar-dir $PL_MODELS_DIR/avatars/personalive-default
"""
import argparse
import logging
import os
import sys
from concurrent import futures

import grpc

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("avatar-worker")

HERE = os.path.dirname(os.path.abspath(__file__))
GEN = os.environ.get("PERSONALIVE_GEN", os.path.join(HERE, "..", "..", "gen", "python"))
for p in (HERE, GEN):
    if p not in sys.path:
        sys.path.insert(0, p)

from avatarengine.v1 import avatarengine_pb2_grpc as pbg   # noqa: E402
from avatar_common import AvatarSessionServicer            # noqa: E402
from wav2lipls_engine import Wav2LipLSEngine               # noqa: E402

LTPRO = ""  # no private path baked in; assets come from env/flags (see deploy/setup/avatar.sh)
# Checkpoint <-> face_size: probed from each ckpt's audioBlocks.fc shape
# (out_size 3 => face_size%3==0). Asset paths come from env/flags, never baked in:
# deploy/setup/avatar.sh downloads them and the wizard passes --ckpt/--avatar-dir.
CKPT = {192: os.environ.get("WAV2LIPLS_CKPT_192", ""),
        384: os.environ.get("WAV2LIPLS_CKPT_384", "")}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=9414)
    ap.add_argument("--face-size", type=int, default=384, choices=[192, 384])
    ap.add_argument("--ckpt", default=os.environ.get("WAV2LIPLS_CKPT", ""),
                    help="LS checkpoint (.pth); else $WAV2LIPLS_CKPT / $WAV2LIPLS_CKPT_<size>")
    ap.add_argument("--avatar-dir", default=os.environ.get("AVATAR_DIR", ""),
                    help="baked avatar dir (full_imgs/face_imgs/coords.pkl); else $AVATAR_DIR")
    ap.add_argument("--hubert-dir", default=os.environ.get("WAV2LIPLS_HUBERT_DIR", ""),
                    help="HuBERT feature extractor dir; else $WAV2LIPLS_HUBERT_DIR")
    ap.add_argument("--batch-size", type=int, default=16)
    ap.add_argument("--infer-silence", action="store_true",
                    help="run the model on silent chunks instead of emitting raw base "
                         "frames (required for articulated-mouth bakes)")
    ap.add_argument("--device", default=None)
    ap.add_argument("--h264-codec", default=os.environ.get("PERSONALIVE_H264", "libx264"))
    ap.add_argument("--preroll", type=int, default=int(os.environ.get("PERSONALIVE_PREROLL", "3")))
    args = ap.parse_args()

    ckpt = args.ckpt or CKPT.get(args.face_size)
    if not ckpt or not os.path.exists(ckpt):
        raise SystemExit(
            f"wav2lipLS checkpoint not found for face_size={args.face_size}: {ckpt!r}\n"
            f"  run  deploy/setup/avatar.sh  (or the setup wizard) to fetch it, or pass "
            f"--ckpt / set $WAV2LIPLS_CKPT.")
    if not args.avatar_dir or not os.path.isdir(args.avatar_dir):
        raise SystemExit(
            f"avatar dir not found: {args.avatar_dir!r}\n"
            f"  run  deploy/setup/avatar.sh  (or the setup wizard) to fetch the default "
            f"avatar, or pass --avatar-dir / set $AVATAR_DIR.")

    log.info("loading Wav2LipLSEngine (face_size=%d) ...", args.face_size)
    engine = Wav2LipLSEngine(args.avatar_dir, ckpt, args.face_size,
                             hubert_dir=args.hubert_dir or None, batch_size=args.batch_size,
                             device=args.device, infer_silence=args.infer_silence)
    engine.warmup(n_chunks=2)
    dims = (engine.width, engine.height, engine.fps, engine.SR)
    log.info("dims w=%d h=%d fps=%d sr=%d chunk_samples=%d slice_len=%d face_size=%d",
             *dims, engine.chunk_samples, engine.slice_len, engine.face_size)

    opts = [("grpc.max_send_message_length", 16 * 1024 * 1024),
            ("grpc.max_receive_message_length", 16 * 1024 * 1024)]
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4), options=opts)
    pbg.add_AvatarEngineServicer_to_server(
        AvatarSessionServicer(engine, dims, idle_frames=None, preroll=args.preroll,
                              h264_codec=args.h264_codec,
                              health_detail=f"wav2lipLS avatar engine (face_size={args.face_size})"), server)
    server.add_insecure_port(f"[::]:{args.port}")
    server.start()
    log.info("AvatarEngine (wav2lipLS) gRPC worker READY on :%d", args.port)
    server.wait_for_termination()


if __name__ == "__main__":
    main()
