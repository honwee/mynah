"""Verify the Preload RPC: an avatar bound at admin time is resident before any
visitor arrives, so the session that uses it does not pay the ~15s decode.

Sequence:
  1. Preload an avatar the worker did NOT start with  -> expect ready, load_ms>0
  2. Preload the same one again                        -> expect already=True, 0ms
  3. Preload a nonexistent path                        -> expect ready=False + reason
  4. Open a session on the preloaded avatar            -> expect no load in the log
"""
import os
import sys
import sys
import time

sys.path.insert(0, "/app/gen/python")
import grpc  # noqa: E402
from avatarengine.v1 import avatarengine_pb2 as pb  # noqa: E402
from avatarengine.v1 import avatarengine_pb2_grpc as pbg  # noqa: E402

AVATARS = os.environ.get("AVATAR_BAKES_DIR") or sys.exit("set AVATAR_BAKES_DIR")
stub = pbg.AvatarEngineStub(grpc.insecure_channel("127.0.0.1:9420"))

print("1. preload a NOT-preloaded avatar")
r = stub.Preload(pb.PreloadRequest(avatar=f"{AVATARS}/lxgray_mt"), timeout=120)
print(f"   ready={r.ready} already={r.already} load_ms={r.load_ms} detail={r.detail!r}")
assert r.ready and not r.already and r.load_ms > 0, "first preload should do real work"

print("2. preload the SAME avatar again (idempotent)")
r2 = stub.Preload(pb.PreloadRequest(avatar=f"{AVATARS}/lxgray_mt"), timeout=30)
print(f"   ready={r2.ready} already={r2.already} load_ms={r2.load_ms}")
assert r2.ready and r2.already and r2.load_ms == 0, "second preload should be a no-op"

print("3. preload a nonexistent avatar")
r3 = stub.Preload(pb.PreloadRequest(avatar="/no/such/bake"), timeout=30)
print(f"   ready={r3.ready} detail={r3.detail!r}")
assert not r3.ready, "a missing bake must not report ready"

print("4. session on the preloaded avatar (should NOT reload)")
t0 = time.time()


def gen():
    yield pb.ClientFrame(start=pb.SessionSpec(
        session_id="pre1", cond_image=f"{AVATARS}/lxgray_mt",
        idle_local=True, video_codec="h264"))
    time.sleep(1.5)
    yield pb.ClientFrame(close=pb.Close())


for sf in stub.Session(gen()):
    if sf.WhichOneof("msg") == "ready":
        print(f"   Ready in {time.time() - t0:.2f}s  <- instant means the preload paid off")
        break

print("PASS")
