---
title: 常见问题
---

# 常见问题

## 跑起来

**页面说麦克风不可用。** 浏览器只在安全来源下给麦克风。用 HTTPS 端口（8443 / 8455），接受自签证书或装正式证书（[部署 → TLS](./deploy#tls)）。本地测试时 `http://127.0.0.1` 也算安全来源。

**"engine busy" / "数字人正忙"。** 一个 worker 服务一路会话。要么等，要么加 worker（本地服务 → 引擎池），要么换更大的卡。对话调试页和访客抢的是同一批 worker。

**管理员密码在哪？** 数据库首次初始化时在 cored 日志里打印一次：`docker compose logs cored | grep "initial admin"`。首次登录必须改。

**`docker compose up` 停下来点名一个变量。** `.env` 里的文件路径故意没有默认值。把它指到宿主机上一个真实目录。

**容器里看不到显卡。** 启用 CDI：`sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml`，增减显卡后重跑。

**EdgeTTS 不出声 / 报错。** 它需要能访问微软的外网。内网隔离环境请用本地 [Qwen3-TTS](./tts)。

**模型下载慢或被墙。** 默认走国内镜像（`HF_ENDPOINT=https://hf-mirror.com`、ModelScope）。海外设 `HF_ENDPOINT=https://huggingface.co`。

## 用起来

**同时能几个人聊？** 显存放得下几个 worker 就几个：wav2lipLS 约每 2GB 一个，MuseTalk 或 FlashHead 约每 7–8GB 一个，再加 ASR。24GB 的卡跑 2–3 路高清会话。控制台替你算账并守住频道上限。

**没有显卡能跑吗？** cored 和控制台可以。数字人引擎需要某处有一张卡；按小时租一台，`AVATAR_ADDR` 指过去（[快速开始路线 C](./quickstart#路线-c-无显卡)）。

**怎么把公司资料喂给它？** 把 `.txt` / `.md` 传进[知识库](./knowledge)并打开 RAG。它按你的文档回答；仍可能答错，用「检索测试」多试，人设里把边界写清楚。

**手机上能用吗？** 手机浏览器直接用，不装 App。首次使用会请求麦克风权限。

**能放进我自己的网站吗？** 能：iframe、SDK 或自定义元素。见[嵌入](./embed)。

**会做动作吗？** 形象可以携带烘焙好的一次性动作（目前有 `wave`）。访客有按钮，SDK 和 Agent 有接口。

**能打断它吗？** 能。直接开口，轮次检测会发现并让它停下。代码里调 `interrupt()`。

## 许可与商业

**真的可以免费商用？** 代码 Apache-2.0。默认引擎按可商用许可挑选（FlashHead Apache-2.0、MuseTalk MIT、自训 wav2lipLS；InsightFace 换成了 MediaPipe）。不可商用的组件已移除。见 LICENSING.md。

**有水印或使用上报吗？** 没有。

**能训练真人形象吗？** 要本人授权。控制台会要求确认，政策见[合规](/compliance)。

**项目靠什么赚钱？** 部署交付、形象制作、知识库接入和支持服务。代码保持开源；维护者明天消失，东西还在你手里。

**和 LiveTalking 什么关系？** LiveTalking 是很好的引擎框架，Mynah 作者曾与其作者合作 OEM 项目。Mynah 开源的是通常在引擎之上的那一层：控制台、频道、知识库、并发、引擎池。不同层，不冲突。
