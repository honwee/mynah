# Changelog

## v0.1.0 — first full open-source release

Everything ships in one Apache-2.0 repository — the former enterprise overlay
(avatar training, voice cloning, motion skeletons, idle baking, RAG reranking)
is merged in with no feature wall.

### Engine
- Realtime WebRTC digital-human core (`cored`): frame-grid pacing, barge-in,
  VP8/H.264 + Opus, idle-local replay.
- Pluggable engines behind gRPC/HTTP contracts: FlashHead (default) and
  wav2lipLS avatar engines; SenseVoice ASR; any OpenAI-compatible TTS/LLM;
  Dify/Coze chat adapters.
- Semantic turn-taking with the bundled Smart Turn v3.2 audio end-of-turn
  model (BSD-2, 32MB ONNX, in-repo).

### Avatar pipeline
- **Avatar training from a video**: frame sampling + MediaPipe scoring picks
  the best speaking base (closed mouth / frontal / sharp); artifact becomes a
  selectable likeness (`trained:<id>`) driving the live cond_image.
- **Idle baking**: FlashHead neutral render → LivePortrait motion transfer →
  cored-replayable idle loop, with a deterministic flat cache for instant
  re-selection; hot idle swap on the live engine.
- **Motion-skeleton library**: extract reusable driving templates (.pkl) from
  uploaded videos.
- Consent-gated uploads throughout; see `docs/compliance.md`.

### Voice
- Voice cloning through the reference TTS stack (upstream vLLM-omni +
  Qwen3-TTS CustomVoice); contract documented in
  `docs/api/tts-voice-contract.md`; per-voice seed pinning.

### Control plane
- Admin console (React, no build step): dashboard, KB/RAG with optional
  cross-encoder reranking, playground (chat + live avatar debug), publish
  management (channels with token/domain/CIDR access + concurrency caps),
  avatar & voice studio, session monitor.
- Config center with three-layer merge and hot apply.

### Ops & security
- Setup wizard (one-click component install, CN-mirror-aware) + docker-compose
  stack; TTS provisioning now installs and launches vLLM-omni end to end.
- Per-IP rate limiting on visitor endpoints (`--visitor-rate-limit`);
  SECURITY.md deployment guidance.
- CI: Go build/vet/test + Python compile checks + training-worker contract
  smoke test.
