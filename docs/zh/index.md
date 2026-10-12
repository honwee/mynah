---
layout: home
hero:
  name: Mynah
  image:
    src: /mynah-mark.png
    alt: Mynah
  text: 开源数字人，开箱即用
  tagline: 一张肖像、一条命令，得到一个能实时对话、会做表情、能发布成链接的数字人。引擎、控制台、交付链路全部开源。
  actions:
    - theme: brand
      text: 15 分钟跑通
      link: /zh/guide/quickstart
    - theme: alt
      text: 架构
      link: /zh/guide/architecture
    - theme: alt
      text: GitHub
      link: https://github.com/honwee/mynah
features:
  - title: 真实时，不是生成视频
    details: 你说完，云端大脑 0.2 秒内接话，唇形视频 1 秒内开口；你插话它立刻停。语义轮次模型判断你是否真的说完，WebRTC 直达浏览器。
  - title: 一张肖像，一个半身形象
    details: FlashHead 扩散引擎渲染整头，贴回 720×1280 的头到腰画布；待机呼吸和害羞、笑、摇头、眨眼等动作在同一画布上烘焙。内置 Luna、Nova 两套预设。
  - title: 有本能，不只会答
    details: 本能层在大脑还在组织语言时就选好表情和动作，说完即做。接 Jev 认知模型，或零依赖的关键词规则，断网自动降级。
  - title: 是控制台，不是脚本
    details: 形象、声音、知识库、频道发布、访问控制、并发配额、引擎池显存账、一键给频道换形象，日常运营不碰命令行。
  - title: 接进你的技术栈
    details: 大脑可选本地 LLM、Qwen Realtime，或任意 Pipecat 流水线；形象、TTS、ASR 走 gRPC 与 OpenAI 兼容契约；自带 Python SDK 和 DeepSeek Harness 插件。
  - title: 拿去就能交付
    details: Apache-2.0，无功能墙，无水印要求，形象训练带授权确认。一张 4090 可同时跑 MuseTalk 与 FlashHead 两路引擎；每个组件也都能指向远端。
---
