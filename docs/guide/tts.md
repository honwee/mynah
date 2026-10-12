---
title: TTS tiers
---

# TTS tiers: EdgeTTS → Qwen3-TTS → cloud

Mynah talks to TTS over one contract: OpenAI-compatible `POST /v1/audio/speech` streaming 16-bit PCM at 24 kHz, plus `GET /v1/audio/voices`. Anything that speaks it plugs in. The repo ships three tiers that do.

| Tier | Cost | GPU | Voice cloning | Latency to first audio | When |
|---|---|---|---|---|---|
| **EdgeTTS** (default) | free | none | no | ~0.3–0.6 s, depends on internet | day one, demos, kiosks with internet |
| **Qwen3-TTS** via vLLM-omni | your GPU, ≈ 3 GB | yes | yes | ~0.3 s local | offline deployments, branded voices |
| **Cloud key** | per character | none | provider-dependent | provider-dependent | no GPU, or you already pay a vendor |

There is a fourth option that bypasses TTS entirely: the **Qwen realtime brain** (speech-to-speech in the cloud). It replaces ASR + LLM + TTS with one model and is what the public demo channel runs. Measured on the demo: request to first cloud audio 0.37–0.52 s, to the first lip-synced frame 0.73–1.07 s.

## EdgeTTS (default)

The `tts-edge` container wraps Microsoft Edge's neural voices behind the contract. Nothing to download, CPU only, needs outbound internet to Microsoft. Voice ids look like `zh-CN-XiaoxiaoNeural`; the console pins a featured set to the top of the dropdown and caches the full list. Speed is honored; style instructions are ignored.

```yaml
# .env
TTS_URL=http://127.0.0.1:8091
VOICE=zh-CN-XiaoxiaoNeural
```

## Qwen3-TTS (best practice when you have a GPU)

```bash
docker compose --env-file .env.prod -f docker-compose.full.yml --profile qwen-tts up -d tts   # stop tts-edge first, same port
```

Serves Qwen3-TTS CustomVoice through vLLM-omni on the same contract. Adds voice cloning: upload a 10–30 s reference clip in the console, it is transcribed for you, and the new voice appears everywhere. A per-voice **seed** keeps the timbre consistent across sentences; **instructions** such as "更沉稳" steer the style. Budget about 3 GB of VRAM alongside the avatar engine.

## Cloud providers

Point `TTS_URL` at any server that implements the contract with PCM streaming. Self-hosted wrappers around the open Qwen3-TTS weights (for example Qwen3-TTS-X) do; so do several OpenAI-compatible gateways. Vendors that only return MP3 files, or only expose a vendor SDK, need a small adapter; the [TTS voice contract](/api/tts-voice-contract) page specifies exactly what cored expects so the adapter is a page of code.

## Switching tiers


<img src="/screens/config.png" alt="Config center: TTS, brain, LLM" style="border:1px solid #e5e7eb;border-radius:8px">

配置中心 → TTS: change `base_url` and `voice`, press 试听, save. New sessions use it; running sessions finish on the old one. Channels keep the voice they were published with until you republish.

## Qwen realtime brain

配置中心 → 对话大脑 → Qwen realtime. One cloud model listens, thinks and speaks; cored still drives the avatar from the returned audio, so lip-sync and gestures work the same. Pick a cloud voice from the dropdown. Note the [knowledge base caveat](./knowledge#where-it-applies) when combining it with cloud turn detection.
