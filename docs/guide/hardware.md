---
title: Hardware & engines
---

# Hardware & engines

Measured on an RTX 4090 host, steady state per worker. The console's **Local
services** page recomputes capacity from *your* GPU, so treat these as floors.

| profile | avatar engine | VRAM | fits on |
|---|---|---|---|
| Lightest | wav2lipLS (384) ≈ 2 GB + ASR 1.4 GB, EdgeTTS 0 | ≈ 3.5 GB | any 6 GB card |
| Quality | FlashHead ≈ 6.6 GB or MuseTalk 1.5 ≈ 7.7 GB, + ASR | ≈ 8–9 GB | 12 GB card, one live session |
| Pool | 2 × MuseTalk + ASR | ≈ 17 GB | 24 GB card, 2–3 sessions |
| No GPU | remote avatar worker + cloud LLM/TTS | 0 | laptop / VPS |

Pick by what matters: wav2lipLS is the cheapest to run and the lowest fidelity;
MuseTalk is the most robust to arbitrary source videos; FlashHead needs only a
single portrait (no bake) but is a 512² head-and-shoulders model.

## Latency (measured)

Public demo host, one RTX 4090, Qwen realtime brain, 2026-10-11. "Turn end" is when turn-taking commits the visitor's utterance.

| Metric | MuseTalk 1.5 (512) | FlashHead half-body (720×1280) |
|---|---|---|
| Turn end → first reply audio from the brain | 1–170 ms | 1–170 ms |
| Turn end → first lip-synced audio from the engine | 0.40–0.72 s | 0.86–1.15 s |
| Engine: audio arrival → first frame (of which compute) | 0.32–0.55 s (0.24–0.48 s) | 0.79–0.90 s (0.62–0.76 s) |
| Local brain (LLM + EdgeTTS): request → first audio / first frame | 0.37–0.52 s / 0.73–1.07 s | — |
| Instinct decision: Jev through a proxy / local rules | 0.4–0.85 s / 0 ms | same |
| Viewer packet loss → keyframe (PLI relayed) | ≤ 0.5 s | ≤ 0.5 s |

Rules of thumb: the engine's first frame is ~1 s behind the first audio on FlashHead and ~0.5 s on MuseTalk, so the audio gate holds the first packets until video is ready (lip-sync from frame 0). Idle costs no GPU at all; the only steady cost per session is the engine while it speaks.
