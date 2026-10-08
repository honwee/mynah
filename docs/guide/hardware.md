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
