---
layout: home
hero:
  name: Mynah
  image:
    src: /mynah-mark.png
    alt: Mynah
  text: Open-source digital humans, ready to deploy.
  tagline: One portrait, one command — a digital human that talks back in realtime, reacts with gestures, and ships as a link. Engine, console and delivery path, all open source.
  actions:
    - theme: brand
      text: Quickstart (15 min)
      link: /guide/quickstart
    - theme: alt
      text: Architecture
      link: /guide/architecture
    - theme: alt
      text: GitHub
      link: https://github.com/honwee/mynah
features:
  - title: Realtime, not rendered video
    details: The cloud brain answers within 0.2 s of your last word and lip-synced video starts under a second; interrupt it and it stops. A semantic end-of-turn model decides when you are actually done. WebRTC straight to the browser.
  - title: One portrait, a half-body presenter
    details: FlashHead renders the whole head and pastes it onto a 720×1280 head-to-waist canvas; idle breathing and gesture clips (shy, laugh, shake, wink) are baked from the same canvas. Luna and Nova ship as presets.
  - title: Instinct, not just answers
    details: An instinct layer picks an expression or gesture while the brain is still composing, and plays it the moment the reply ends. Backed by the Jev cognition model, or a zero-dependency rules provider when offline.
  - title: A console, not a script
    details: Avatars, voices, knowledge bases, channel publishing, access control, concurrency quotas, an engine pool with a VRAM budget, one-click avatar swap per channel.
  - title: Plugs into your stack
    details: Brain options — local LLM, Qwen Realtime, or any Pipecat pipeline; avatar, TTS and ASR behind gRPC and OpenAI-compatible contracts; Python SDK and a DeepSeek Harness plugin included.
  - title: Yours to ship
    details: Apache-2.0, no feature wall, no watermark requirement, consent-gated avatar training. One RTX 4090 runs MuseTalk and FlashHead side by side; every component can also be a remote endpoint.
---
