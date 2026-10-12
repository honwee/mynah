"""Verify per-session avatar switching on a multi-avatar MuseTalk worker.

Opens three short sessions asking for different avatars via
SessionSpec.cond_image and lets the engine log show which one it activated.
The point is that switching is free: no reload, no I/O, just rebinding the
render lists at session start.
"""
import sys
import time

sys.path.insert(0, "/app/gen/python")
import grpc  # noqa: E402
from avatarengine.v1 import avatarengine_pb2 as pb  # noqa: E402
from avatarengine.v1 import avatarengine_pb2_grpc as pbg  # noqa: E402

stub = pbg.AvatarEngineStub(grpc.insecure_channel("127.0.0.1:9420"))


def open_session(sid, avatar):
    def gen():
        yield pb.ClientFrame(start=pb.SessionSpec(
            session_id=sid, cond_image=avatar, idle_local=True, video_codec="h264"))
        time.sleep(2.0)
        yield pb.ClientFrame(close=pb.Close())

    t0 = time.time()
    frames = 0
    for sf in stub.Session(gen()):
        if sf.WhichOneof("msg") == "video":
            frames += 1
        if frames >= 3:
            break
    return frames, time.time() - t0


for sid, avatar in (("s1", "lxgray"), ("s2", "leiya"), ("s3", "no-such-avatar")):
    n, dt = open_session(sid, avatar)
    print(f"session {sid} avatar={avatar!r}: {n} video frames, first frames in {dt:.2f}s")
    time.sleep(1)
