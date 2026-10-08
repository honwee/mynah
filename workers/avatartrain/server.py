#!/usr/bin/env python3
"""Mynah avatar-training worker.

A standalone HTTP service that runs the avatar-preparation pipelines behind
the control plane's training/motion/bake admin flows. It is deliberately a
*separate* process from the realtime AvatarEngine inference worker: these
jobs saturate a GPU (or CPU) for minutes and must never contend with the
live engine cored blocks startup on.

Endpoints (cored's internal/training + internal/motion processors drive this):

    POST /train                {job_id, name, video_path} -> {job_id, status}
    GET  /train/status/{id}    -> {status, progress, artifact_path, error}
    POST /extract              motion-skeleton extraction (LivePortrait .pkl)
    POST /bake                 idle-loop baking (FlashHead + LivePortrait)

status ∈ running | done | failed.

/train runs extract_portrait.py in a subprocess: it samples frames from the
uploaded video, picks the best speaking-base frame (closed mouth, frontal,
sharp — MediaPipe, no InsightFace), and writes the artifact the avatar
catalog serves as cond_image (portrait.jpg + meta.json). Set
PL_TRAIN_MODE=simulate to restore the no-GPU progress-only stub (CI / contract
tests).
"""
import argparse
import json
import logging
import os
import shutil
import subprocess
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

log = logging.getLogger("avatartrain")

# job_id -> {status, progress, artifact_path, error}; guarded by _LOCK.
_JOBS: dict[str, dict] = {}
_LOCK = threading.Lock()

# Output dirs default under this checkout rather than under an absolute path from
# some other machine. They used to default under /data/personalive — the data
# disk of the 8-GPU box — which stopped existing without anything noticing: jobs
# were accepted and then died in a subprocess on a path nobody had typed. In the
# compose stacks every one of these arrives from .env; run_avatartrain_worker.sh
# sets them for bare-host runs. See also PL_BAKE_CACHE_DIR below, which must
# agree with cored's.
_REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
_DATA = os.environ.get("PL_DATA_DIR") or os.path.join(_REPO, ".data")

# Where a trained artifact lands: ARTIFACT_DIR/<worker_job_id>/portrait.jpg.
ARTIFACT_DIR = os.environ.get("PL_TRAIN_ARTIFACT_DIR", os.path.join(_DATA, "idlebake", "training"))

# "real" (default) runs extract_portrait.py; "simulate" restores the progress-
# only stub — no GPU, no deps beyond stdlib — for CI and contract tests.
TRAIN_MODE = os.environ.get("PL_TRAIN_MODE", "real")

# Python with mediapipe + opencv for extract_portrait.py. mediapipe needs
# protobuf<5, so prefer a small dedicated venv over a shared ML env (newer
# protobuf breaks mp.solutions with a GetPrototype AttributeError).
TRAIN_PY = os.environ.get("PL_TRAIN_PORTRAIT_PY", "python3")
# extract_portrait.py sits next to this server.
TRAIN_SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "extract_portrait.py")
# Hard ceiling for one portrait-extraction subprocess.
TRAIN_TIMEOUT = float(os.environ.get("PL_TRAIN_TIMEOUT", "900"))

# Simulated wall-clock for one job in simulate mode, seconds. Override for fast tests.
SIM_SECONDS = float(os.environ.get("PL_TRAIN_SIM_SECONDS", "20"))

# ── Motion-skeleton extraction (REAL, unlike /train) ──────────────────────────
# The /extract endpoint runs the LivePortrait motion-template pipeline in a
# subprocess (its own conda env / sys.path), so it shares this worker process and
# port but not its Python runtime. Produces a reusable .pkl + idle-suitability
# stats. Output lands on the data disk (system disk is full).
MOTION_PKL_DIR = os.environ.get("PL_MOTION_PKL_DIR", os.path.join(_DATA, "idlebake", "motions"))
# Python interpreter with LivePortrait deps installed (torch, the LP repo).
LIVEPORTRAIT_PY = os.environ.get("PL_LIVEPORTRAIT_PY", "python3")
# LivePortrait repo root, prepended to sys.path by extract_template.py.
LIVEPORTRAIT_ROOT = os.environ.get("PL_LIVEPORTRAIT_ROOT", "")
# extract_template.py sits next to this server.
EXTRACT_SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "extract_template.py")
# Hard ceiling for one extraction subprocess (cored also has a 30m sweep).
EXTRACT_TIMEOUT = float(os.environ.get("PL_EXTRACT_TIMEOUT", "1500"))

# ── Idle-asset baking (REAL) ──────────────────────────────────────────────────
# The /bake endpoint runs the full living-standby pipeline in a subprocess:
# FlashHead renders a neutral frame from the avatar's source portrait, LivePortrait
# transfers a chosen motion template (.pkl) onto it, the animation is split to a
# frame library and encoded to a cored-replayable idle_<name>.h264f. This is the
# productized form of Phase A's bake_idle_liveportrait.sh chain. Heavy (GPU, ~6min
# cold), but baking is a rare admin action. Output lands on the data disk.
BAKE_DIR = os.environ.get("PL_BAKE_DIR", os.path.join(_DATA, "idlebake", "assets"))
# Flat cache of finished idle assets: idle_<avatar>__<motion>.h264f. cored's EE
# avatarbake extension computes the SAME path from (avatar_id, motion_ref) and,
# on a cache hit, applies it instantly without calling /bake at all. So a finished
# bake is atomically moved here under the deterministic name — that's what turns a
# later "select this avatar" into an instant hot-swap instead of a 6-min bake.
BAKE_CACHE_DIR = os.environ.get("PL_BAKE_CACHE_DIR", os.path.join(_DATA, "idlebake", "cache"))
# bake_idle_asset.sh sits next to this server; it reads PL_BAKE_GPU / PL_*_PY env.
BAKE_SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "bake_idle_asset.sh")
# Hard ceiling for one bake subprocess.
BAKE_TIMEOUT = float(os.environ.get("PL_BAKE_TIMEOUT", "1800"))


def _safe_name(s: str) -> str:
    """Filesystem-safe avatar token for idle_<name>.h264f."""
    return "".join(c if (c.isalnum() or c in "-_") else "_" for c in (s or "avatar"))[:48] or "avatar"


def _bake(wjid: str, source_image: str, motion_pkl: str, name: str, motion_ref: str = "",
          half_body: str = "", blend: str = ""):
    """Run the idle-asset bake subprocess for one avatar+motion.

    Drives _JOBS[wjid] to a terminal state. Unlike _extract's progress creeper,
    bake_idle_asset.sh emits `PROGRESS <n>` lines at stage boundaries, so we parse
    them for accurate progress. The bake writes BAKE_DIR/<wjid>/idle_<name>.h264f;
    on success it is atomically moved to the flat cache as
    BAKE_CACHE_DIR/idle_<name>__<motion>.h264f (the deterministic name cored's EE
    side computes), so a future select of the same (avatar, motion) is an instant
    cache hit. When motion_ref is empty the per-job path is kept as-is.
    """
    out_dir = os.path.join(BAKE_DIR, wjid)
    safe = _safe_name(name)
    asset_path = os.path.join(out_dir, "idle_%s.h264f" % safe)
    try:
        os.makedirs(out_dir, exist_ok=True)
    except OSError as e:
        with _LOCK:
            j = _JOBS.get(wjid)
            if j:
                j["status"], j["error"] = "failed", "mkdir: %s" % e
        return
    cmd = ["bash", BAKE_SCRIPT, "--cond", source_image, "--pkl", motion_pkl,
           "--out-dir", out_dir, "--name", safe]
    # Half-body presenter: pass the canvas + precomputed blend so Stage C-prime
    # composites each generated face back onto the 720x1280 canvas. Empty -> the
    # script bakes the legacy 512 face library (full backward compatibility).
    if half_body and blend:
        cmd += ["--halfbody", half_body, "--blend", blend]
    log.info("job %s bake: %s", wjid, " ".join(cmd))
    try:
        proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                text=True, bufsize=1)
    except Exception as e:  # noqa: BLE001
        with _LOCK:
            j = _JOBS.get(wjid)
            if j:
                j["status"], j["error"] = "failed", "spawn: %s" % e
        return
    start = time.time()
    last = ""
    # Read the merged stream line by line; PROGRESS lines move the bar, the rest
    # is kept as the last-line tail for error reporting.
    for line in iter(proc.stdout.readline, ""):
        line = line.rstrip("\n")
        if line.strip():
            last = line.strip()
        if line.startswith("PROGRESS "):
            try:
                p = int(line.split()[1])
            except (ValueError, IndexError):
                p = None
            if p is not None:
                with _LOCK:
                    j = _JOBS.get(wjid)
                    if j is None or j["status"] != "running":
                        proc.kill()  # canceled/removed
                        return
                    j["progress"] = max(j.get("progress", 0), min(99, p))
        if time.time() - start > BAKE_TIMEOUT:
            proc.kill()
            with _LOCK:
                j = _JOBS.get(wjid)
                if j:
                    j["status"], j["error"] = "failed", "bake timed out"
            return
        with _LOCK:
            j = _JOBS.get(wjid)
            if j is None or j["status"] != "running":
                proc.kill()
                return
    proc.wait()
    if proc.returncode != 0 or not os.path.exists(asset_path):
        log.error("job %s bake failed: %s", wjid, last)
        with _LOCK:
            j = _JOBS.get(wjid)
            if j and j["status"] == "running":
                j["status"], j["error"] = "failed", (last or "exit %d" % proc.returncode)[:300]
        return
    # Move the finished asset into the flat cache under the deterministic name so
    # cored's EE side finds it on a later select (instant hot-swap, no re-bake).
    # shutil.move (not os.replace) so it survives the asset dir and cache dir being
    # SEPARATE filesystems — e.g. distinct bind-mounts in the compose worker, where
    # os.replace raises EXDEV (cross-device link).
    if motion_ref:
        try:
            os.makedirs(BAKE_CACHE_DIR, exist_ok=True)
            cache_path = os.path.join(
                BAKE_CACHE_DIR, "idle_%s__%s.h264f" % (safe, _safe_name(motion_ref)))
            shutil.move(asset_path, cache_path)
            asset_path = cache_path
        except OSError as e:
            log.warning("job %s cache move failed (keeping per-job asset): %s", wjid, e)
    try:
        sz = os.path.getsize(asset_path)
    except OSError:
        sz = 0
    with _LOCK:
        j = _JOBS.get(wjid)
        if j and j["status"] == "running":
            j["status"] = "done"
            j["progress"] = 100
            j["asset_path"] = asset_path
            j["bytes"] = sz
    log.info("job %s baked -> %s (%d B)", wjid, asset_path, sz)


def _extract(wjid: str, video_path: str):
    """Run the LivePortrait extraction subprocess for one driving video.

    Drives _JOBS[wjid] to a terminal state. Progress is coarse: the subprocess
    is opaque, so a creeper nudges 5 -> 90 while it runs, then 100 on success.
    extract_template.py writes the .pkl and a stats JSON (head/lip/blink metrics
    + n_frames + fps); we read the stats back and hand them to cored verbatim.
    """
    pkl_path = os.path.join(MOTION_PKL_DIR, wjid, "motion.pkl")
    stats_path = os.path.join(MOTION_PKL_DIR, wjid, "stats.json")
    # LivePortrait is a separate checkout, so there is no default worth guessing:
    # say which variable is missing instead of failing later inside the subprocess.
    if not LIVEPORTRAIT_ROOT:
        with _LOCK:
            j = _JOBS.get(wjid)
            if j:
                j["status"] = "failed"
                j["error"] = "PL_LIVEPORTRAIT_ROOT is not set (path to the LivePortrait checkout)"
        return
    try:
        os.makedirs(os.path.dirname(pkl_path), exist_ok=True)
    except OSError as e:
        with _LOCK:
            j = _JOBS.get(wjid)
            if j:
                j["status"], j["error"] = "failed", "mkdir: %s" % e
        return
    cmd = [LIVEPORTRAIT_PY, EXTRACT_SCRIPT,
           "--video", video_path, "--out", pkl_path, "--stats", stats_path,
           "--lp-root", LIVEPORTRAIT_ROOT]
    log.info("job %s extract: %s", wjid, " ".join(cmd))
    try:
        proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    except Exception as e:  # noqa: BLE001
        with _LOCK:
            j = _JOBS.get(wjid)
            if j:
                j["status"], j["error"] = "failed", "spawn: %s" % e
        return
    with _LOCK:
        j = _JOBS.get(wjid)
        if j:
            j["progress"] = 5
    start = time.time()
    while proc.poll() is None:
        time.sleep(1.0)
        if time.time() - start > EXTRACT_TIMEOUT:
            proc.kill()
            with _LOCK:
                j = _JOBS.get(wjid)
                if j:
                    j["status"], j["error"] = "failed", "extraction timed out"
            return
        with _LOCK:
            j = _JOBS.get(wjid)
            if j is None or j["status"] != "running":
                proc.kill()  # canceled/removed
                return
            j["progress"] = min(90, j["progress"] + 2)
    out = (proc.stdout.read() if proc.stdout else "") or ""
    if proc.returncode != 0 or not os.path.exists(pkl_path):
        tail = out.strip().splitlines()[-1] if out.strip() else "exit %d" % proc.returncode
        log.error("job %s extract failed: %s", wjid, tail)
        with _LOCK:
            j = _JOBS.get(wjid)
            if j and j["status"] == "running":
                j["status"], j["error"] = "failed", tail[:300]
        return
    stats, n_frames = {}, 0
    try:
        with open(stats_path) as f:
            stats = json.load(f)
        n_frames = int(stats.get("n_frames") or 0)
    except Exception as e:  # noqa: BLE001
        log.warning("job %s stats read: %s", wjid, e)
    with _LOCK:
        j = _JOBS.get(wjid)
        if j and j["status"] == "running":
            j["status"] = "done"
            j["progress"] = 100
            j["pkl_path"] = pkl_path
            j["stats"] = stats
            j["n_frames"] = n_frames
    log.info("job %s extracted (%d frames) -> %s", wjid, n_frames, pkl_path)


def _train(wjid: str, video_path: str):
    """Run the portrait-extraction pipeline for one training job.

    Shells out to extract_portrait.py (mediapipe + opencv runtime), parses its
    PROGRESS lines into _JOBS[wjid], and lands portrait.jpg + meta.json under
    ARTIFACT_DIR/<wjid>. Exit 3 = no usable face (user-fixable input problem);
    the script's last stdout line is surfaced as the error either way.
    """
    if TRAIN_MODE == "simulate":
        _simulate(wjid, video_path)
        return
    artifact = os.path.join(ARTIFACT_DIR, wjid)
    try:
        os.makedirs(artifact, exist_ok=True)
    except OSError as e:
        with _LOCK:
            j = _JOBS.get(wjid)
            if j:
                j["status"], j["error"] = "failed", "mkdir: %s" % e
        return
    cmd = [TRAIN_PY, TRAIN_SCRIPT, "--video", video_path, "--out-dir", artifact]
    log.info("job %s train: %s", wjid, " ".join(cmd))
    try:
        proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                text=True, bufsize=1)
    except Exception as e:  # noqa: BLE001
        with _LOCK:
            j = _JOBS.get(wjid)
            if j:
                j["status"], j["error"] = "failed", "spawn: %s" % e
        return
    start = time.time()
    last = ""
    for line in iter(proc.stdout.readline, ""):
        line = line.rstrip("\n")
        if line.strip():
            last = line.strip()
        if line.startswith("PROGRESS "):
            try:
                pct = max(0, min(99, int(line.split()[1])))
            except (IndexError, ValueError):
                continue
            with _LOCK:
                j = _JOBS.get(wjid)
                if j is None or j["status"] != "running":
                    proc.kill()  # canceled from the console
                    return
                j["progress"] = pct
        if time.time() - start > TRAIN_TIMEOUT:
            proc.kill()
            with _LOCK:
                j = _JOBS.get(wjid)
                if j and j["status"] == "running":
                    j["status"], j["error"] = "failed", "timeout after %ds" % int(TRAIN_TIMEOUT)
            return
    rc = proc.wait()
    portrait = os.path.join(artifact, "portrait.jpg")
    if rc != 0 or not os.path.isfile(portrait):
        log.error("job %s train failed rc=%d: %s", wjid, rc, last)
        with _LOCK:
            j = _JOBS.get(wjid)
            if j and j["status"] == "running":
                j["status"] = "failed"
                j["error"] = (last or "extract_portrait exited %d" % rc)[:300]
        return
    with _LOCK:
        j = _JOBS.get(wjid)
        if j and j["status"] == "running":
            j["status"] = "done"
            j["progress"] = 100
            j["artifact_path"] = artifact
    log.info("job %s trained -> %s (%.1fs)", wjid, portrait, time.time() - start)


def _simulate(wjid: str, video_path: str):
    """Progress-only stub (PL_TRAIN_MODE=simulate): drives one mock job
    0 -> 100 with no GPU and no deps beyond stdlib — CI / contract tests."""
    steps = 20
    try:
        for i in range(1, steps + 1):
            time.sleep(max(SIM_SECONDS / steps, 0.01))
            with _LOCK:
                j = _JOBS.get(wjid)
                if j is None or j["status"] != "running":
                    return  # removed/canceled
                j["progress"] = int(i * 100 / steps)
        artifact = os.path.join(ARTIFACT_DIR, wjid)
        try:
            os.makedirs(artifact, exist_ok=True)
        except OSError:
            pass  # stub: artifact dir is best-effort
        with _LOCK:
            j = _JOBS.get(wjid)
            if j and j["status"] == "running":
                j["status"] = "done"
                j["progress"] = 100
                j["artifact_path"] = artifact
        log.info("job %s done (stub, video=%s)", wjid, video_path)
    except Exception as e:  # noqa: BLE001
        log.exception("job %s failed", wjid)
        with _LOCK:
            j = _JOBS.get(wjid)
            if j:
                j["status"] = "failed"
                j["error"] = str(e)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):  # keep the worker log clean
        pass

    def _reply(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        path = self.path.rstrip("/")
        if path not in ("/train", "/extract", "/bake"):
            self._reply(404, {"error": "not found"})
            return
        try:
            n = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(n) if n > 0 else b""
            req = json.loads(raw or b"{}")
        except Exception as e:  # noqa: BLE001
            self._reply(400, {"error": "bad json: %s" % e})
            return
        video_path = str(req.get("video_path") or "")
        wjid = uuid.uuid4().hex
        if path == "/extract":
            with _LOCK:
                _JOBS[wjid] = {"status": "running", "progress": 0, "pkl_path": "",
                               "stats": {}, "n_frames": 0, "error": ""}
            threading.Thread(target=_extract, args=(wjid, video_path), daemon=True).start()
            log.info("accepted extract %s (cored asset_id=%s, video=%s)", wjid, req.get("asset_id"), video_path)
            self._reply(200, {"job_id": wjid, "status": "running"})
            return
        if path == "/bake":
            source_image = str(req.get("source_image") or "")
            motion_pkl = str(req.get("motion_pkl") or "")
            name = str(req.get("name") or req.get("avatar_id") or "avatar")
            motion_ref = str(req.get("motion_ref") or "")
            half_body = str(req.get("half_body") or "")
            blend = str(req.get("blend") or "")
            if not source_image or not motion_pkl:
                self._reply(400, {"error": "need source_image and motion_pkl"})
                return
            with _LOCK:
                _JOBS[wjid] = {"status": "running", "progress": 0, "asset_path": "", "error": ""}
            threading.Thread(target=_bake,
                             args=(wjid, source_image, motion_pkl, name, motion_ref, half_body, blend),
                             daemon=True).start()
            log.info("accepted bake %s (cored avatar_id=%s, src=%s, pkl=%s, ref=%s, halfbody=%s)",
                     wjid, req.get("avatar_id"), source_image, motion_pkl, motion_ref, bool(half_body))
            self._reply(200, {"job_id": wjid, "status": "running"})
            return
        with _LOCK:
            _JOBS[wjid] = {"status": "running", "progress": 0, "artifact_path": "", "error": ""}
        if TRAIN_MODE != "simulate" and not os.path.isfile(video_path):
            with _LOCK:
                _JOBS[wjid].update(status="failed", error="video not found: %s" % video_path)
            self._reply(200, {"job_id": wjid, "status": "failed"})
            return
        threading.Thread(target=_train, args=(wjid, video_path), daemon=True).start()
        log.info("accepted job %s (cored job_id=%s, video=%s, mode=%s)",
                 wjid, req.get("job_id"), video_path, TRAIN_MODE)
        self._reply(200, {"job_id": wjid, "status": "running"})

    def do_GET(self):
        path = self.path.split("?", 1)[0].rstrip("/")
        for prefix in ("/train/status/", "/extract/status/", "/bake/status/"):
            if path.startswith(prefix):
                wjid = path[len(prefix):]
                with _LOCK:
                    j = _JOBS.get(wjid)
                    if j is None:
                        self._reply(404, {"error": "unknown job"})
                        return
                    self._reply(200, dict(j))
                return
        self._reply(404, {"error": "not found"})


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=9405)
    args = ap.parse_args()
    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    srv = ThreadingHTTPServer((args.host, args.port), Handler)
    log.info("avatar-train worker READY on %s:%d (train mode=%s)", args.host, args.port, TRAIN_MODE)
    srv.serve_forever()


if __name__ == "__main__":
    main()
