# Mynah · UCloud 优云智算镜像说明（社区镜像发布页文案）

**Mynah 开源数字人（Apache-2.0）：一张肖像、一条命令，得到一个能实时对话、会做表情的半身数字人，自带管理控制台。**
镜像已装好 FlashHead 引擎（Lite 权重）、SenseVoice 语音识别、Smart Turn 语义轮次、EdgeTTS、PostgreSQL+pgvector、对话模型 qwen2.5:7b 和 Luna / Nova 两套内置半身形象，开机后一条命令启动。

> **定位：单机体验版。** 这个镜像把 Mynah 的全部组件装在一张卡、一台实例上，面向试用、开发和演示。Mynah 本身是面向生产的企业级架构：Go 实时核心 + Python 推理 worker 池 + 管理控制台三平面，容器化部署、多 worker 并发配额、频道级发布管理、知识库与本能层都在同一套代码里。正式上线请按文档站「部署」篇做多实例部署，镜像里的配置和数据可以直接迁过去。

## 用法

```bash
~/mynah/mynahctl start
```

**方式一：SSH 隧道（最省事）。** 在自己电脑上：

```bash
ssh -N -L 8443:127.0.0.1:8443 -L 9443:127.0.0.1:9443 -L 8444:127.0.0.1:8444 root@<实例地址>
```

浏览器打开 `https://127.0.0.1:8443/`（自签证书，点"继续访问"），允许麦克风，开始对话；控制台 `https://127.0.0.1:9443/`。

**方式二：公网直连。** 在控制台开放 8443、9443、8444 三个 TCP 端口，把 `~/mynah/env` 里的 `ICE_NAT_IP` 改成实例的公网 IP，`mynahctl restart`，然后访问 `https://<公网IP>:8443/`。

管理员初始密码打印在 `mynahctl start` 的输出末尾（`~/mynah/mynahctl password` 可再看），登录后请立即修改。

## 显卡

24 GB 显卡（4090 / 3090 / A10）跑默认配置：FlashHead ≈ 6.5 GB + 7B 对话模型 ≈ 5.5 GB + 语音识别 ≈ 1.4 GB。16 GB 可用；12 GB 请在 `~/mynah/env` 里设 `LLM_MODE=none`，控制台里填云端模型地址。

## 常用命令

```bash
~/mynah/mynahctl status | logs <服务> | restart | password
```

- 文档：https://honwee.github.io/mynah/zh/ · 源码：https://github.com/honwee/mynah
- 想换形象：控制台「形象与声音」里选 Luna / Nova，或上传一张自己的肖像按文档做半身预设。
- 想要音色克隆：切 Qwen3-TTS，见文档站「TTS 三档」。
