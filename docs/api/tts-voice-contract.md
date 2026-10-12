# TTS Voice Contract (`/v1/audio/*`)

Mynah drives TTS through an **OpenAI-compatible speech API** with a
voice-management extension. The reference implementation is
**[vLLM-omni](https://github.com/vllm-project/vllm-omni)** (upstream, Apache-2.0)
serving **Qwen3-TTS CustomVoice** — that is what `deploy/setup/tts.sh`
provisions and what the voice-clone console feature talks to. Any server
implementing the routes below drops in via the config center's
`tts.base_url`.

## Tiers

| tier | server | cost | GPU | cloning |
|---|---|---|---|---|
| **EdgeTTS (default)** | `workers/tts-edge` — Microsoft Edge online voices | free | none | no (`POST /v1/audio/voices` → 501) |
| Qwen3-TTS (best practice) | vLLM-omni, `deploy/setup/tts.sh` | local GPU | ~3 GB | yes |
| cloud | any OpenAI-compatible `/v1/audio/speech` | per provider | none | per provider |

All three answer on `tts.base_url`; cored is identical across them.

## Synthesis (required)

Standard OpenAI speech surface — this is all the *speaking* path needs:

```
POST /v1/audio/speech
{ "model": "...", "input": "text to speak", "voice": "vivian",
  "response_format": "wav", "seed": 12345 }          → audio bytes
GET  /v1/models                                       → health/readiness probe
```

- `seed` (vLLM-omni extension): pins the sampling seed so a voice keeps one
  consistent rendition across utterances. The console's per-voice seed knob
  writes `tts.seed` / `tts.voice_seeds`.

## Voice management (required for the voice-clone console feature)

These are vLLM-omni's built-in voice routes (`vllm_omni/entrypoints/openai/
api_server.py`); cored's `/api/v1/tts/voices*` admin endpoints proxy to them.

### List voices

```
GET /v1/audio/voices
→ { "voices": ["vivian", "serena", ..., "my_clone"],
    "uploaded_voices": [ { "name": "my_clone", "consent": "...",
                           "created_at": 0, "file_size": 0, "mime_type": "...",
                           "embedding_source": "audio", "embedding_dim": null,
                           "ref_text": "..." } ] }
```

Gotchas the console already handles (keep them if you reimplement):
- `voices` mixes **built-in and uploaded** names in one flat list;
- `uploaded_voices` is `[]` when empty, and a list of **objects** (`{name,...}`)
  when non-empty — not a list of strings.

### Upload (clone) a voice

```
POST /v1/audio/voices          multipart/form-data
  name:          voice id to register (required)
  consent:       authorization statement (required — see docs/compliance.md)
  audio_sample:  reference audio file (wav/mp3/m4a; cored transcodes to WAV
                 mono PCM via ffmpeg before forwarding when available)
  ref_text:      transcript of the reference audio (improves ICL cloning;
                 cored can auto-fill it via the ASR worker /transcribe)
→ 200 { ... }   voice becomes available in /v1/audio/voices immediately
```

### Delete a voice

```
DELETE /v1/audio/voices/{name}                        → 200
```

## Provisioning the reference stack

`deploy/setup/tts.sh` (also reachable from the setup wizard):

1. downloads `Qwen/Qwen3-TTS-12Hz-0.6B-CustomVoice` (HF mirror-aware),
2. installs vLLM-omni (`pip install vllm-omni`, or a source checkout for the
   newest TTS models),
3. serves it:

```bash
vllm serve <model-dir> --omni --port 8091 \
  --served-model-name qwen3-tts-customvoice \
  --deploy-config vllm_omni/deploy/qwen3_tts.yaml --trust-remote-code
```

No Mynah-specific patches are required — the voice routes are upstream
vLLM-omni features.
