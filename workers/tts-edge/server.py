#!/usr/bin/env python3
"""Mynah EdgeTTS worker — the zero-cost, zero-config TTS tier.

Speaks the same OpenAI-compatible contract cored already uses for Qwen3-TTS
(docs/api/tts-voice-contract.md), backed by Microsoft Edge's free online
neural voices via the `edge-tts` package. CPU only, no model download, needs
outbound internet. For production voice quality / cloning use the reference
stack (vLLM-omni + Qwen3-TTS) or a cloud key — this tier exists so a first run
needs nothing but `docker compose up`.

Routes
  GET  /v1/models                 readiness probe
  GET  /v1/audio/voices           {"voices":[...], "uploaded_voices":[]}
  POST /v1/audio/speech           {"input","voice","response_format":"pcm"|"mp3"|"wav","speed"}
                                  pcm = s16le mono 24 kHz, streamed as it is synthesized
  POST /v1/audio/voices           501 — cloning is not available on this tier

Env: TTS_EDGE_PORT (8091), TTS_EDGE_DEFAULT_VOICE (zh-CN-XiaoxiaoNeural)
"""
import asyncio
import json
import logging
import os
import shutil
import sys
import time

from aiohttp import web

try:
    import edge_tts
except ImportError:  # pragma: no cover
    sys.exit("pip install edge-tts   (see deploy/compose/requirements-tts-edge.txt)")

log = logging.getLogger("tts-edge")
PORT = int(os.environ.get("TTS_EDGE_PORT", "8091"))
DEFAULT_VOICE = os.environ.get("TTS_EDGE_DEFAULT_VOICE", "zh-CN-XiaoxiaoNeural")
FFMPEG = shutil.which("ffmpeg")
# Voices cored's default config and the wizard point at; listed first so the
# console dropdown opens on something sensible. Everything else Edge offers
# follows (fetched once, cached).
FEATURED = [
    "zh-CN-XiaoxiaoNeural", "zh-CN-XiaoyiNeural", "zh-CN-YunxiNeural", "zh-CN-YunyangNeural",
    "zh-CN-YunjianNeural", "zh-CN-liaoning-XiaobeiNeural", "zh-TW-HsiaoChenNeural", "zh-HK-HiuMaanNeural",
    "en-US-AriaNeural", "en-US-GuyNeural", "en-US-JennyNeural", "en-GB-SoniaNeural",
    "ja-JP-NanamiNeural", "ko-KR-SunHiNeural",
]
_voice_cache = {"at": 0.0, "names": []}


def _rate(speed):
    try:
        pct = int(round((float(speed) - 1.0) * 100))
    except (TypeError, ValueError):
        pct = 0
    pct = max(-50, min(100, pct))
    return f"{pct:+d}%"


async def all_voices():
    if time.time() - _voice_cache["at"] < 3600 and _voice_cache["names"]:
        return _voice_cache["names"]
    names = list(FEATURED)
    try:
        for v in await edge_tts.list_voices():
            n = v.get("ShortName")
            if n and n not in names:
                names.append(n)
        _voice_cache.update(at=time.time(), names=names)
    except Exception as e:  # offline: still serve the featured list
        log.warning("list_voices failed (%s); serving featured list only", e)
    return names


async def models(_req):
    return web.json_response({"object": "list", "data": [{"id": "edge-tts", "object": "model", "owned_by": "microsoft-edge"}]})


async def voices(_req):
    return web.json_response({"voices": await all_voices(), "uploaded_voices": []})


async def not_supported(_req):
    return web.json_response({"error": "voice cloning is not available on the EdgeTTS tier; "
                              "switch tts.base_url to a Qwen3-TTS / cloud server"}, status=501)


async def speech(req):
    try:
        body = await req.json()
    except Exception:
        return web.json_response({"error": "bad json"}, status=400)
    text = (body.get("input") or "").strip()
    if not text:
        return web.json_response({"error": "input is empty"}, status=400)
    voice = body.get("voice") or DEFAULT_VOICE
    # Channels freeze a voice id at publish time; one published against Qwen3-TTS
    # ("vivian", "sohee") must still speak on this tier, so unknown ids fall back
    # to the default voice instead of failing the whole turn.
    known = await all_voices()
    if voice not in known:
        log.warning("voice %r is not an Edge voice; falling back to %s", voice, DEFAULT_VOICE)
        voice = DEFAULT_VOICE
    fmt = (body.get("response_format") or "pcm").lower()
    rate = _rate(body.get("speed", 1.0))

    comm = edge_tts.Communicate(text, voice, rate=rate)

    async def mp3_chunks():
        async for ch in comm.stream():
            if ch["type"] == "audio":
                yield ch["data"]

    resp = web.StreamResponse(status=200)
    if fmt == "mp3":
        resp.content_type = "audio/mpeg"
        await resp.prepare(req)
        async for b in mp3_chunks():
            await resp.write(b)
        await resp.write_eof()
        return resp

    if not FFMPEG:
        return web.json_response({"error": "ffmpeg not found; only response_format=mp3 is available"}, status=500)
    # mp3 -> s16le mono 24k (cored's srcRate) or wav, streamed through ffmpeg.
    out_args = ["-f", "s16le", "-ar", "24000", "-ac", "1"] if fmt == "pcm" else ["-f", "wav", "-ar", "24000", "-ac", "1"]
    proc = await asyncio.create_subprocess_exec(
        FFMPEG, "-loglevel", "error", "-f", "mp3", "-i", "pipe:0", *out_args, "pipe:1",
        stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
    resp.content_type = "audio/pcm" if fmt == "pcm" else "audio/wav"
    await resp.prepare(req)

    async def feed():
        try:
            async for b in mp3_chunks():
                proc.stdin.write(b)
                await proc.stdin.drain()
        except Exception as e:
            log.warning("edge stream ended early: %s", e)
        finally:
            try:
                proc.stdin.close()
            except Exception:
                pass

    feeder = asyncio.create_task(feed())
    try:
        while True:
            chunk = await proc.stdout.read(4800)  # 100 ms of 24k s16le
            if not chunk:
                break
            await resp.write(chunk)
    finally:
        await feeder
        await proc.wait()
    await resp.write_eof()
    return resp


def main():
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    if not FFMPEG:
        log.warning("ffmpeg not on PATH: pcm/wav output disabled, cored will not be able to use this worker")
    app = web.Application()
    app.add_routes([
        web.get("/v1/models", models),
        web.get("/v1/audio/voices", voices),
        web.post("/v1/audio/voices", not_supported),
        web.post("/v1/audio/speech", speech),
    ])
    log.info("EdgeTTS worker on :%d (default voice %s, ffmpeg=%s)", PORT, DEFAULT_VOICE, bool(FFMPEG))
    web.run_app(app, host="0.0.0.0", port=PORT, print=None)


if __name__ == "__main__":
    main()
