#!/usr/bin/env python3
"""Mynah — first-run setup helper.

A tiny standalone provisioning service (stdlib only, no deps) behind the
out-of-the-box SETUP WIZARD. Deliberately NOT part of cored: cored stays a pure,
generic digital-human engine; all "download models / start services / probe
resources" ops live here, outside it.

Each component (LLM / TTS / Avatar) can be provisioned two ways:
  • connect — point at an existing OpenAI-compatible endpoint (remote / cloud /
    another box). No download, no GPU. (matches the engine's pluggable interfaces)
  • local   — download + run on THIS machine, on a chosen GPU, after a VRAM/disk
    preflight against GET /api/resources.

Endpoints:
  GET  /                          wizard page
  GET  /static/<f>                static assets
  GET  /api/resources             GPUs (nvidia-smi) + disk free  → preflight
  GET  /api/status                per-component health + chosen mode
  POST /api/connect/<key>         {base_url,model?,api_key?} → test + remember (remote)
  POST /api/provision/<key>       {gpu?} → run deploy/setup/<key>.sh on that GPU (local)
  GET  /api/provision/<key>/stream SSE of the script's stdout (+ exit)
  POST /api/launch                bring up cored once components are green
"""
import json
import os
import re
import shutil
import socket
import subprocess
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
STATIC = os.path.join(HERE, "static")
SCRIPTS = HERE
COMPOSE_DIR = os.path.normpath(os.path.join(HERE, "..", "compose"))
ENV_PATH = os.path.join(COMPOSE_DIR, ".env")
ENV_EXAMPLE = os.path.join(COMPOSE_DIR, ".env.example")
# The wizard owns a marked block at the END of compose/.env. compose takes the
# LAST value for a repeated key, so this block overrides the example defaults
# above it while leaving the user's own edits untouched.
ENV_BEGIN = "# >>> mynah setup wizard (managed — regenerated on each choice) >>>"
ENV_END = "# <<< mynah setup wizard (managed) <<<"

# key -> registry. `port`/`url`+`path` are the LOCAL probe targets; vram/disk are
# rough needs for the preflight; `kind` picks the connect-form shape.
COMPONENTS = [
    {"key": "llm", "label": "对话大脑 (LLM · ollama)", "kind": "openai",
     "hint": "默认 ollama + qwen2.5:7b，OpenAI 兼容 /v1/chat/completions",
     "url": os.environ.get("LLM_URL", "http://127.0.0.1:11434"), "path": "/api/tags",
     "vram_gb": 6, "disk_gb": 5},
    {"key": "tts", "label": "语音合成 (TTS · 默认 EdgeTTS 免费档)", "kind": "openai",
     "hint": "默认 EdgeTTS：免费、零配置、不占显存（需联网）。最佳实践：本地 Qwen3-TTS（vllm-omni，可克隆音色）或填云端 OpenAI 兼容 /v1/audio/speech",
     "url": os.environ.get("TTS_URL", "http://127.0.0.1:8091"), "path": "/v1/models",
     "vram_gb": 0, "disk_gb": 0},
    {"key": "avatar", "label": "数字人形象 (FlashHead)", "kind": "grpc",
     "hint": "默认 FlashHead（高画质，自带内置形象），或连接已有 avatar worker (host:port)",
     "addr": "127.0.0.1:" + os.environ.get("FLASHHEAD_PORT", "9421"),
     "vram_gb": 10, "disk_gb": 12},
]
COMP_BY_KEY = {c["key"]: c for c in COMPONENTS}

# Runtime choices: key -> {mode:"local"|"remote", url|addr, gpu, model, api_key}
_CONFIG = {}
_JOBS = {}
_LOCK = threading.Lock()


def _probe_tcp(host, port):
    t0 = time.time()
    try:
        with socket.create_connection((host, int(port)), timeout=2):
            return {"status": "ok", "ms": int((time.time() - t0) * 1000)}
    except Exception as e:
        return {"status": "down", "error": str(e)[:120]}


def _probe_http(url):
    t0 = time.time()
    try:
        with urllib.request.urlopen(urllib.request.Request(url, method="GET"), timeout=2):
            return {"status": "ok", "ms": int((time.time() - t0) * 1000)}
    except urllib.error.HTTPError:
        return {"status": "ok", "ms": int((time.time() - t0) * 1000)}  # reachable
    except Exception as e:
        return {"status": "down", "error": str(e)[:120]}


def _probe(c):
    """Probe a component using its current mode (remote override or local default)."""
    cfg = _CONFIG.get(c["key"], {})
    if c["kind"] == "grpc":
        addr = cfg.get("addr", c.get("addr"))
        host, _, port = addr.partition(":")
        return _probe_tcp(host, port), addr
    base = cfg.get("url", c.get("url"))
    return _probe_http(base.rstrip("/") + c.get("path", "")), base


def _resources():
    """GPUs via nvidia-smi + disk free. Empty gpus list = CPU-only machine."""
    gpus = []
    try:
        out = subprocess.check_output(
            ["nvidia-smi", "--query-gpu=index,name,memory.total,memory.free,utilization.gpu",
             "--format=csv,noheader,nounits"], text=True, timeout=4)
        for line in out.strip().splitlines():
            p = [x.strip() for x in line.split(",")]
            if len(p) >= 5:
                gpus.append({"index": int(p[0]), "name": p[1],
                             "mem_total_gb": round(int(p[2]) / 1024, 1),
                             "mem_free_gb": round(int(p[3]) / 1024, 1),
                             "util": int(p[4])})
    except Exception:
        pass
    try:
        du = shutil.disk_usage(os.environ.get("PL_MODELS_DIR", "/root"))
        disk_free_gb = round(du.free / 1e9, 1)
    except Exception:
        disk_free_gb = None
    return {"gpus": gpus, "disk_free_gb": disk_free_gb}


def _status():
    out = []
    for c in COMPONENTS:
        with _LOCK:
            job = _JOBS.get(c["key"])
            running = bool(job and not job["done"])
        p, target = _probe(c)
        cfg = _CONFIG.get(c["key"], {})
        st = "provisioning" if running else p["status"]
        out.append({"key": c["key"], "label": c["label"], "hint": c["hint"],
                    "kind": c["kind"], "status": st, "detail": p.get("error", ""),
                    "ms": p.get("ms", 0), "running": running, "target": target,
                    "mode": cfg.get("mode", "local"), "gpu": cfg.get("gpu"),
                    "vram_gb": c["vram_gb"], "disk_gb": c["disk_gb"]})
    return {"components": out}


def _run_script(key, extra_env=None):
    script = os.path.join(SCRIPTS, key + ".sh")
    job = {"lines": [], "done": False, "code": None, "started": time.time()}
    with _LOCK:
        _JOBS[key] = job
    if not os.path.isfile(script):
        job["lines"].append("ERROR: 找不到脚本 %s" % script)
        job["done"], job["code"] = True, 127
        return
    env = dict(os.environ)
    env.update(extra_env or {})
    job["lines"].append("$ bash %s%s" % (script,
                        ("  (GPU=%s)" % env["GPU"]) if env.get("GPU") else ""))
    try:
        proc = subprocess.Popen(["bash", script], stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, text=True, bufsize=1,
                                cwd=SCRIPTS, env=env)
        for line in iter(proc.stdout.readline, ""):
            job["lines"].append(line.rstrip("\n"))
        proc.stdout.close()
        job["code"] = proc.wait()
    except Exception as e:
        job["lines"].append("ERROR: %s" % e)
        job["code"] = 1
    finally:
        job["done"] = True


def _env_lines_from_config():
    """Translate the wizard's per-component choices (_CONFIG) into the compose
    .env keys cored reads. Only overrides are emitted: a component left in its
    default 'local' mode contributes nothing, so the example defaults apply."""
    lines = []
    llm = _CONFIG.get("llm", {})
    if llm.get("mode") == "remote":
        if llm.get("url"):
            lines.append("LLM_URL=%s" % llm["url"])
        if llm.get("model"):
            lines.append("LLM_MODEL=%s" % llm["model"])
        if llm.get("api_key"):
            lines.append("LLM_KEY=%s" % llm["api_key"])
    tts = _CONFIG.get("tts", {})
    if tts.get("mode") == "remote" and tts.get("url"):
        lines.append("TTS_URL=%s" % tts["url"])
    av = _CONFIG.get("avatar", {})
    if av.get("mode") == "remote" and av.get("addr"):
        lines.append("AVATAR_ADDR=%s" % av["addr"])
    elif av.get("mode") == "local" and av.get("gpu") is not None:
        lines.append("AVATAR_GPU=%s" % av["gpu"])
    return lines


def _write_env():
    """Persist the wizard's choices into compose/.env so `launch` (docker compose
    up) picks them up — this is cored's existing config surface, so cored itself
    stays untouched. Idempotent: rewrites only the managed block. Non-fatal."""
    try:
        if not os.path.exists(ENV_PATH):
            if os.path.exists(ENV_EXAMPLE):
                shutil.copyfile(ENV_EXAMPLE, ENV_PATH)
            else:
                open(ENV_PATH, "a").close()
        with open(ENV_PATH, "r", encoding="utf-8") as f:
            content = f.read()
        content = re.sub(re.escape(ENV_BEGIN) + r".*?" + re.escape(ENV_END) + r"\n?",
                         "", content, flags=re.S).rstrip("\n")
        block = _env_lines_from_config()
        out = content + "\n"
        if block:
            out += "\n" + ENV_BEGIN + "\n" + "\n".join(block) + "\n" + ENV_END + "\n"
        with open(ENV_PATH, "w", encoding="utf-8") as f:
            f.write(out)
    except Exception as e:
        print("setup: .env write failed:", e)


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _token_ok(self):
        tok = os.environ.get("SETUP_TOKEN", "")
        if not tok:
            return True, False
        from urllib.parse import urlparse, parse_qs
        if (parse_qs(urlparse(self.path).query).get("t") or [""])[0] == tok:
            return True, True
        if ("pl_setup=" + tok) in (self.headers.get("Cookie", "") or ""):
            return True, False
        return False, False

    def _body(self):
        n = int(self.headers.get("Content-Length", "0") or 0)
        try:
            return json.loads(self.rfile.read(n) or b"{}") if n else {}
        except Exception:
            return {}

    def _send(self, code, ctype, body, set_cookie=False):
        if isinstance(body, str):
            body = body.encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-cache")
        if set_cookie:
            self.send_header("Set-Cookie",
                             "pl_setup=%s; Path=/; Max-Age=3600" % os.environ.get("SETUP_TOKEN", ""))
        self.end_headers()
        self.wfile.write(body)

    def _json(self, code, obj):
        self._send(code, "application/json; charset=utf-8", json.dumps(obj, ensure_ascii=False))

    def _file(self, fp):
        if not os.path.isfile(fp):
            return self._json(404, {"error": "not found"})
        ctype = ("text/html; charset=utf-8" if fp.endswith(".html") else
                 "application/javascript" if fp.endswith(".js") else
                 "text/css" if fp.endswith(".css") else "application/octet-stream")
        with open(fp, "rb") as f:
            self._send(200, ctype, f.read())

    def do_GET(self):
        ok, setck = self._token_ok()
        if not ok:
            return self._send(403, "text/plain; charset=utf-8", "forbidden: missing setup token")
        path = self.path.split("?", 1)[0]
        if path in ("/", "/index.html"):
            with open(os.path.join(STATIC, "wizard.html"), "rb") as f:
                return self._send(200, "text/html; charset=utf-8", f.read(), set_cookie=setck)
        if path == "/api/resources":
            return self._json(200, _resources())
        if path == "/api/status":
            return self._json(200, _status())
        if path.startswith("/api/provision/") and path.endswith("/stream"):
            return self._sse(path[len("/api/provision/"):-len("/stream")])
        if path.startswith("/static/"):
            return self._file(os.path.join(STATIC, os.path.normpath(path[len("/static/"):]).lstrip("/.")))
        return self._json(404, {"error": "not found"})

    def do_POST(self):
        ok, _ = self._token_ok()
        if not ok:
            return self._json(403, {"error": "forbidden"})
        path = self.path.split("?", 1)[0]
        if path.startswith("/api/connect/"):
            key = path[len("/api/connect/"):]
            return self._connect(key)
        if path.startswith("/api/provision/"):
            key = path[len("/api/provision/"):]
            if key not in COMP_BY_KEY:
                return self._json(404, {"error": "unknown component"})
            body = self._body()
            with _LOCK:
                job = _JOBS.get(key)
                if job and not job["done"]:
                    return self._json(409, {"error": "already running"})
            env = {}
            if body.get("gpu") is not None:
                env["GPU"] = str(body["gpu"])
                _CONFIG[key] = {"mode": "local", "gpu": body["gpu"]}
                _write_env()
            threading.Thread(target=_run_script, args=(key, env), daemon=True).start()
            return self._json(202, {"started": key})
        if path == "/api/launch":
            _write_env()
            threading.Thread(target=_run_script, args=("launch", {}), daemon=True).start()
            return self._json(202, {"started": "launch"})
        return self._json(404, {"error": "not found"})

    def _connect(self, key):
        c = COMP_BY_KEY.get(key)
        if not c:
            return self._json(404, {"error": "unknown component"})
        b = self._body()
        if c["kind"] == "grpc":
            addr = (b.get("addr") or "").strip()
            if not addr or ":" not in addr:
                return self._json(400, {"error": "需要 host:port"})
            host, _, port = addr.partition(":")
            pr = _probe_tcp(host, port)
            if pr["status"] != "ok":
                return self._json(502, {"error": "连不上 %s: %s" % (addr, pr.get("error", ""))})
            _CONFIG[key] = {"mode": "remote", "addr": addr}
            _write_env()
            return self._json(200, {"ok": True})
        url = (b.get("base_url") or "").strip().rstrip("/")
        if not url:
            return self._json(400, {"error": "需要 base_url"})
        pr = _probe_http(url + c.get("path", ""))
        if pr["status"] != "ok":
            pr = _probe_http(url)
        if pr["status"] != "ok":
            return self._json(502, {"error": "连不上 %s: %s" % (url, pr.get("error", ""))})
        _CONFIG[key] = {"mode": "remote", "url": url,
                        "model": b.get("model", ""), "api_key": b.get("api_key", "")}
        _write_env()
        return self._json(200, {"ok": True})

    def _sse(self, key):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "keep-alive")
        self.end_headers()
        sent = 0
        try:
            while True:
                with _LOCK:
                    job = _JOBS.get(key)
                if not job:
                    self.wfile.write(b"event: error\ndata: no such job\n\n")
                    self.wfile.flush()
                    return
                while sent < len(job["lines"]):
                    self.wfile.write(("data: %s\n\n" % json.dumps(
                        {"line": job["lines"][sent]}, ensure_ascii=False)).encode("utf-8"))
                    sent += 1
                self.wfile.flush()
                if job["done"] and sent >= len(job["lines"]):
                    self.wfile.write(("event: done\ndata: %s\n\n" %
                                      json.dumps({"code": job["code"]})).encode("utf-8"))
                    self.wfile.flush()
                    return
                time.sleep(0.3)
        except (BrokenPipeError, ConnectionResetError):
            return


if __name__ == "__main__":
    port = int(os.environ.get("SETUP_PORT", "9500"))
    bind = os.environ.get("SETUP_BIND", "127.0.0.1")
    httpd = ThreadingHTTPServer((bind, port), H)
    cert, key = os.environ.get("SETUP_TLS_CERT"), os.environ.get("SETUP_TLS_KEY")
    scheme = "http"
    if cert and key:
        import ssl
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(cert, key)
        httpd.socket = ctx.wrap_socket(httpd.socket, server_side=True)
        scheme = "https"
    print("Mynah setup wizard  →  %s://%s:%d" % (scheme, bind, port))
    httpd.serve_forever()
