# Mynah · AutoDL 镜像说明（CodeWithGPU 发布页文案）

**Mynah 开源数字人（Apache-2.0）：一张肖像、一条命令，得到一个能实时对话、会做表情的半身数字人，自带管理控制台。**
镜像已装好 FlashHead 引擎（Lite 权重）、SenseVoice 语音识别、Smart Turn 语义轮次、EdgeTTS、PostgreSQL+pgvector 和 Luna / Nova 两套内置半身形象，开机后一条命令启动。

> **定位：单机体验版。** 这个镜像把 Mynah 的全部组件装在一张卡、一台实例上，面向试用、开发和演示。Mynah 本身是面向生产的企业级架构：Go 实时核心 + Python 推理 worker 池 + 管理控制台三平面，容器化部署、多 worker 并发配额、频道级发布管理、知识库与本能层都在同一套代码里。正式上线请按文档站「部署」篇做多实例部署，镜像里的配置和数据可以直接迁过去。

## 用法

```bash
~/mynah/mynahctl start      # 首次启动会把对话模型 qwen2.5:7b（约 4.7 GB）拉到数据盘，之后不再下载
```

AutoDL 实例没有公网 UDP，浏览器要通过 SSH 隧道访问。在**自己电脑**上用实例的 SSH 登录命令加三个端口转发（把 `-p 端口 root@地址` 换成实例详情里给的）：

```bash
ssh -N -L 8443:127.0.0.1:8443 -L 9443:127.0.0.1:9443 -L 8444:127.0.0.1:8444 -p <端口> root@<地址>
```

然后浏览器打开 `https://127.0.0.1:8443/`（自签证书，点"继续访问"），允许麦克风，开始对话。
控制台在 `https://127.0.0.1:9443/`，首次登录的管理员密码会打印在 `mynahctl start` 的输出末尾（也可以随时 `~/mynah/mynahctl password` 查看），登录后请立即修改。
8444 是 WebRTC 的 ICE-TCP 端口，三个端口缺一不可。

## 显卡

| 显存 | 怎么用 |
|---|---|
| 24 GB（4090 / 3090 / A10） | 默认配置：FlashHead ≈ 6.5 GB + 7B 对话模型 ≈ 5.5 GB + 语音识别 ≈ 1.4 GB |
| 16 GB | 可以跑默认配置，余量不多 |
| 12 GB | 在 `~/mynah/env` 里把 `LLM_MODE=none`，控制台「配置中心」填任意 OpenAI 兼容的云端模型地址 |

## 常用命令

```bash
~/mynah/mynahctl status       # 各服务端口 + 显存
~/mynah/mynahctl logs avatar  # 看某个服务日志：pg tts asr turn avatar llm cored
~/mynah/mynahctl restart
~/mynah/env                   # 端口、GPU、模型、ICE 设置都在这里
```

- 文档：https://honwee.github.io/mynah/zh/ · 源码：https://github.com/honwee/mynah
- 想换形象：控制台「形象与声音」里选 Luna / Nova，或上传一张自己的肖像按文档做半身预设。
- 想要音色克隆：切 Qwen3-TTS，见文档站「TTS 三档」。
