# Pipecat: Mynah as the face, or Pipecat as the brain

[Pipecat](https://github.com/pipecat-ai/pipecat) is the open-source framework for real-time voice agents. Mynah plugs into it in both directions.

## A. Mynah as a Pipecat video service

`pip install pipecat-ai-mynah` ([repo](https://github.com/honwee/pipecat-ai-mynah)). Put `MynahVideoService` after your TTS:

```python
from pipecat_mynah import MynahVideoService

pipeline = Pipeline([
    transport.input(), stt, context.user(), llm, tts,
    MynahVideoService(worker_addr="127.0.0.1:9415", avatar="/.../assets/nova.halfbody.png"),
    transport.output(), context.assistant(),
])
```

It swallows the TTS audio, streams it to a Mynah avatar worker over the worker gRPC contract, and pushes back lip-synced RGB frames plus the same audio in lockstep. Pipecat's other avatar services are hosted APIs; this one is your GPU, Apache-2.0, no per-minute fee.

## B. A Pipecat bot as Mynah's brain

Mynah's brain mode **`pipecat`** (Console → Config → Brain) sends the visitor's microphone to a Pipecat bot and lip-syncs whatever TTS audio the bot returns. The bot owns VAD, STT, LLM, TTS, tools and Flows; Mynah keeps the face, console, channels and capacity.

Bot side: `WebsocketServerTransport` + `ProtobufFrameSerializer`, 16 kHz in/out recommended. A complete bot: `examples/mynah_brain_bot.py` in the pipecat-ai-mynah repo. Then set the bot's `ws://host:port` as the Pipecat URL in the console; new sessions use it.

What travels on the wire:

| Mynah → bot | bot → Mynah |
|---|---|
| `InputAudioRawFrame` (16 kHz mono PCM) | `OutputAudioRawFrame` (TTS, any rate) → lip-sync |
| `TranscriptionFrame` (typed text from the page) | `TranscriptionFrame` (what the visitor said) → subtitle |
| `InterruptionFrame` (page-side barge-in) | `TextFrame` (reply text) → subtitle |
| | `InterruptionFrame` (bot's VAD heard the visitor) → cut playback |

Limits in this mode: Mynah's knowledge base and local voices do not apply (configure them in the bot); scripted lines (channel greeting, `mynah_speak`) go through Mynah's local TTS.
