---
title: FAQ
---

# FAQ

## Getting it running

**The page says the microphone is unavailable.** Browsers only expose the microphone on a secure origin. Use the HTTPS port (8443 / 8455), accept the self-signed certificate or install a real one ([Deploy → TLS](./deploy#tls)). `http://127.0.0.1` also counts as secure for local testing.

**"engine busy" / "数字人正忙".** One avatar worker serves one session. Either wait, add workers (本地服务 → engine pool), or raise capacity with a bigger GPU. The playground competes with visitors for the same workers.

**Where is the admin password?** Printed once in the cored log when the database is first initialized: `docker compose logs cored | grep "initial admin"`. You must change it on first login.

**`docker compose up` aborts naming a variable.** Filesystem paths in `.env` have no defaults on purpose. Set the named variable to a real directory on your host.

**GPU not visible in the container.** Enable CDI: `sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml`, re-run after adding or removing GPUs.

**EdgeTTS says nothing / errors.** It needs outbound internet to Microsoft. Air-gapped? Use [Qwen3-TTS](./tts) locally.

**Model downloads are slow or blocked.** Defaults use China-friendly mirrors (`HF_ENDPOINT=https://hf-mirror.com`, ModelScope). Outside China set `HF_ENDPOINT=https://huggingface.co`.

## Using it

**How many people can talk to it at once?** As many workers as fit in VRAM: about one per 2 GB with wav2lipLS, one per 7–8 GB with MuseTalk or FlashHead, plus ASR. A 24 GB card runs 2–3 concurrent quality sessions. The console does the arithmetic and enforces channel caps.

**Can it run with no GPU?** cored and the console can. The avatar worker needs a GPU somewhere; rent one by the hour and point `AVATAR_ADDR` at it ([Quickstart, path C](./quickstart#path-c-no-gpu)).

**How do I give it my company's knowledge?** Upload `.txt` / `.md` to a [knowledge base](./knowledge) and enable RAG. It answers from your documents; it can still be wrong, so test with 检索测试 and keep the system prompt honest about limits.

**Does it work on phones?** Yes, in the mobile browser, no app. Microphone permission is asked on first use.

**Can I put it in my own website?** Yes: iframe, SDK, or custom element. See [Embed](./embed).

**Can it make gestures?** Avatars can carry baked one-shot clips (`wave` today). Visitors get a button, the SDK and agents get an API.

**Can I interrupt it?** Yes. Start talking; the turn detector notices and the avatar stops. From code, `interrupt()`.

## Licensing and business

**Is it really free for commercial use?** Apache-2.0 for the code. Default engines are chosen for commercially usable licenses (FlashHead Apache-2.0, MuseTalk MIT, self-trained wav2lipLS; InsightFace replaced by MediaPipe). Non-commercial components were removed. See LICENSING.md.

**Is there a watermark or a usage phone-home?** No.

**Can I train an avatar of a real person?** With their consent. The console asks for it; the policy is in [Compliance](/compliance).

**How does the project make money?** Deployment, avatar production, knowledge base integration and support contracts. The code stays open; if the maintainers vanished tomorrow you would still have everything.

**How does this relate to LiveTalking?** LiveTalking is an excellent engine framework; Mynah's author worked with its author on OEM projects. Mynah open-sources the layer that usually sits above the engine: console, channels, knowledge base, concurrency, engine pool. Different layer, no conflict.
