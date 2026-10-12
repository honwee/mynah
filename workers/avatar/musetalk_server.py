#!/usr/bin/env python
"""Mynah AvatarEngine gRPC worker — MuseTalk 1.5 backend.

Drop-in alternative to wav2lipls_server.py: same proto/avatarengine/v1
contract, model-agnostic pump from avatar_common. Runs in the `musetalk`
conda env (diffusers + transformers whisper).

Model assets are NOT vendored:
  --models-dir    MuseTalk weights root (musetalkV15/{unet.pth,musetalk.json},
                  sd-vae/, whisper/) — the upstream download_weights.sh layout.
  --musetalk-repo upstream MuseTalk repo checkout (the `musetalk` python
                  package is imported from there, not pip-installed).
  --avatar-dir    a MuseTalk v1.5 bake made by scripts.realtime_inference
                  (full_imgs/ coords.pkl latents.pt mask/ mask_coords.pkl).

Launch:
  CUDA_VISIBLE_DEVICES=1 PERSONALIVE_GEN=<repo>/gen/python \
    python musetalk_server.py --port 9415 \
      --models-dir /path/MuseTalk/models --musetalk-repo /path/MuseTalk \
      --avatar-dir /path/MuseTalk/results/v15/avatars/<id>
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
from musetalk_engine import MuseTalkEngine                 # noqa: E402


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=9415)
    ap.add_argument("--models-dir", default=os.environ.get("MUSETALK_MODELS_DIR", ""),
                    help="MuseTalk weights root; else $MUSETALK_MODELS_DIR")
    ap.add_argument("--musetalk-repo", default=os.environ.get("MUSETALK_REPO", ""),
                    help="upstream MuseTalk repo checkout; else $MUSETALK_REPO")
    ap.add_argument("--avatar-dir", action="append", default=None,
                    help="MuseTalk v1.5 baked avatar dir; else $AVATAR_DIR. "
                         "Repeatable to PRELOAD several avatars in one worker: "
                         "--avatar-dir leiya=/path/a --avatar-dir lin=/path/b. A bare "
                         "path uses the directory name as the id. Switching between "
                         "preloaded avatars is free at session start (SessionSpec."
                         "cond_image picks one); the cost is ~2.7GB HOST RAM each and "
                         "only ~8MB VRAM, so it does not eat the per-worker VRAM budget. "
                         "All preloaded avatars must share one resolution.")
    ap.add_argument("--max-avatars", type=int, default=6,
                    help="cap on avatars held in memory (~1.4GB host RAM each). "
                         "Preloaded ones are pinned; on-demand loads evict the "
                         "least-recently-used cached entry past this cap.")
    ap.add_argument("--batch-size", type=int, default=8)
    ap.add_argument("--infer-silence", action="store_true",
                    help="run the model on silent chunks instead of emitting raw base "
                         "frames (required for articulated-mouth bakes)")
    ap.add_argument("--device", default=None)
    ap.add_argument("--h264-codec", default=os.environ.get("PERSONALIVE_H264", "libx264"))
    ap.add_argument("--preroll", type=int, default=int(os.environ.get("PERSONALIVE_PREROLL", "3")))
    args = ap.parse_args()

    for name, p, isdir in (("models-dir", args.models_dir, True),
                           ("musetalk-repo", args.musetalk_repo, True)):
        if not p or not os.path.isdir(p):
            raise SystemExit(f"--{name} not found: {p!r}")

    # --avatar-dir is repeatable; fall back to $AVATAR_DIR when absent so the
    # existing single-avatar launch keeps working unchanged.
    raw_dirs = args.avatar_dir or ([os.environ["AVATAR_DIR"]]
                                   if os.environ.get("AVATAR_DIR") else [])
    if not raw_dirs:
        raise SystemExit("--avatar-dir (or $AVATAR_DIR) is required")
    avatars = {}
    for entry in raw_dirs:
        # "id=path" names the avatar explicitly; a bare path takes the dir name.
        if "=" in entry:
            aid, path = entry.split("=", 1)
            aid = aid.strip()
        else:
            path, aid = entry, os.path.basename(os.path.normpath(entry))
        path = path.strip()
        if not os.path.isdir(path):
            raise SystemExit(f"--avatar-dir not found: {path!r}")
        if aid in avatars:
            raise SystemExit(f"duplicate avatar id {aid!r}")
        avatars[aid] = path

    log.info("loading MuseTalkEngine ...")
    engine = MuseTalkEngine(avatars, args.models_dir, args.musetalk_repo,
                            batch_size=args.batch_size, device=args.device,
                            infer_silence=args.infer_silence,
                            max_avatars=args.max_avatars)
    engine.warmup(n_chunks=2)
    dims = (engine.width, engine.height, engine.fps, engine.SR)
    log.info("dims w=%d h=%d fps=%d sr=%d chunk_samples=%d slice_len=%d",
             *dims, engine.chunk_samples, engine.slice_len)

    opts = [("grpc.max_send_message_length", 16 * 1024 * 1024),
            ("grpc.max_receive_message_length", 16 * 1024 * 1024)]
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4), options=opts)
    pbg.add_AvatarEngineServicer_to_server(
        AvatarSessionServicer(engine, dims, idle_frames=None, preroll=args.preroll,
                              h264_codec=args.h264_codec,
                              health_detail="musetalk 1.5 avatar engine"), server)
    server.add_insecure_port(f"[::]:{args.port}")
    server.start()
    log.info("AvatarEngine (musetalk) gRPC worker READY on :%d", args.port)
    server.wait_for_termination()


if __name__ == "__main__":
    main()
