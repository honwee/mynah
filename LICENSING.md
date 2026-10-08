# Licensing & model provenance

Mynah's own code (the `cored` engine, the control plane, the gRPC worker
wrappers, the setup wizard) is licensed under **Apache-2.0** — see [LICENSE](LICENSE).

This file is the single source of truth for the **third-party models and engines**
the stack downloads at provision time. None of these weights are vendored into the
repository; the setup scripts fetch them from their upstream sources. The table
below is what to hand your legal/compliance team.

## TL;DR

**The default stack is commercially usable.** The default avatar engine
(FlashHead) is Apache-2.0; the optional wav2lipLS engine runs self-trained
weights. The only genuinely non-commercial component (the original Rudrabha
Wav2Lip) has been **removed** from this repo. Two items are yours to verify for
your own deployment — see the ⚠️ notes in the table.

## Component table

| Component | Role | License | Weights / source | Commercial use | Notes & caveats |
|---|---|---|---|---|---|
| **cored** + control plane | Go realtime engine, signaling, admin API | **Apache-2.0** | — (this repo) | ✅ | Mynah's own code. |
| gRPC worker wrappers (`workers/*`), setup wizard | Python service shells | **Apache-2.0** | — (this repo) | ✅ | Mynah's own code. |
| **FlashHead** (default avatar engine) | Diffusion talking-head | **Apache-2.0** | [`Soul-AILab/SoulX-FlashHead`](https://github.com/Soul-AILab/SoulX-FlashHead) code + HF weights; built on Apache-2.0 Wan 2.1 | ✅ | ⚠️ The *lite* model uses the **LTX-Video VAE** (Lightricks) — verify Lightricks' LTX terms if you ship the lite variant. The VividHead training *dataset* is CC-BY-4.0, which binds the dataset, not the released weights. |
| **wav2lipLS** (optional, `wav2lipls` profile) | Lightweight lip-sync head | MIT-family architecture | **Self-trained** weights (operator-provided); audio features via HuBERT | ✅ | Commercial use is the operator's to grant, subject only to **your own training-data license**. This is **not** the research-only Rudrabha Wav2Lip. |
| **HuBERT** `hubert-large-ls960-ft` | Audio feature extractor (wav2lipLS) | **Apache-2.0** (facebook) | HuggingFace | ✅ | — |
| **turn-detector** (Smart Turn v3.2) | Audio semantic end-of-turn | **BSD-2-Clause** (Pipecat / Daily — model + code) | **Bundled in-repo**: `workers/turn/models/smart-turn-v3.2.onnx` | ✅ | Whisper-tiny encoder + linear endpoint head, ONNX/CPU. Open weights + open data + open training code, so the 32 MB model is redistributed directly — no download, no framework lock-in. Judges the just-finished utterance's waveform, in parallel with ASR. Replaced the earlier LiveKit reference model (framework-restricted LiveKit Model License), which is **not** used. The slot stays pluggable — any worker speaking `proto/turn/v1` works. |
| **LivePortrait** (optional, idle baking) | Idle-loop animation | **MIT** (code + self-trained weights) | upstream repo | ✅* | ⚠️ Its default face-crop uses **InsightFace** (`buffalo_l`), which is **non-commercial**. Bypass it entirely with `flag_do_crop=False` or a MediaPipe (Apache-2.0) crop. Without InsightFace the path is clean. |
| Qwen3-TTS / SenseVoice ASR / ollama LLM | Default TTS / ASR / LLM (pluggable) | per upstream | their own repos | per upstream | All are **pluggable** — connect any OpenAI-compatible endpoint instead. Check the license of whichever model you actually deploy. |

`✅*` = commercially usable on the documented non-InsightFace path.

## Removed for license cleanliness

- **Rudrabha Wav2Lip** (classic 256, `wav2lip.pth`): weights trained on **LRS2**
  (BBC-licensed) → genuinely non-commercial. The entire engine (vendored code +
  checkpoint + compose profile) was **removed** from this repo. If you need a
  classic-Wav2Lip path, train your own or use a commercially-licensed provider.

## What you must verify for your deployment

1. **Your wav2lipLS training data** (if you use the wav2lipls profile) — the
   weights are self-trained, so the commercial grant flows from your dataset.
2. **LTX-Video VAE terms** (only if you ship the FlashHead *lite* variant).

Everything else in the default stack is permissively licensed.
