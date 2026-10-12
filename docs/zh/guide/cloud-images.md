---
title: 云镜像（AutoDL / UCloud）
---

# 云镜像（AutoDL / UCloud 优云智算）

给不想装 Docker 和驱动的人：租一台 24 GB 显存的 GPU 实例，选 Mynah 镜像，跑一条命令，开一条 SSH 隧道，浏览器里和 Luna 对话。

::: tip 定位：单机体验版
镜像把全部组件装在一张卡、一台实例上，面向试用、开发和演示。Mynah 本身是面向生产的企业级架构（见[架构](/zh/guide/architecture)）：容器化部署、多 worker 并发配额、频道级发布管理、知识库与本能层都是同一套代码。正式上线请按[部署](/zh/guide/deploy)篇做多实例部署，镜像里的配置和数据可以直接迁过去。
:::

::: warning 状态
配方、引导脚本和 `mynahctl` 都在仓库 `deploy/cloud/`，已在仿 AutoDL 的 CUDA 容器里跑通整条链。**平台上的镜像正在发布中**，上线后这里放链接；在此之前可以在任意 CUDA 12 / Ubuntu 22.04 基础镜像上按下面的步骤自己做，10 多分钟。
:::

## 镜像里有什么

| 组件 | 说明 |
|---|---|
| FlashHead（Lite 权重） | 默认形象引擎，半身画布，约 6.5 GB 显存 |
| Luna / Nova 内置半身预设 | 含烘焙好的待机呼吸和害羞、笑、摇头、眨眼动作片段 |
| SenseVoice + Smart Turn | 语音识别 + 语义轮次（判断你是否说完） |
| EdgeTTS | 默认音色；想要克隆音色切 Qwen3-TTS |
| qwen2.5:7b（ollama） | 对话模型。AutoDL 镜像在首次启动时拉到数据盘；UCloud 镜像已内置 |
| PostgreSQL 16 + pgvector | 控制台、知识库、频道发布所需 |
| `cored` + 控制台 | 实时核心与管理后台，全部原生运行，无 Docker |

## 用法

```bash
~/mynah/mynahctl start        # 启动全部服务，末尾打印控制台初始密码
```

这些实例浏览器直接够不到（AutoDL 没有公网 UDP），所以在**自己电脑**上开一条隧道，把访客页、控制台和 WebRTC 的 TCP 端口一起转发：

```bash
ssh -N -L 8443:127.0.0.1:8443 -L 9443:127.0.0.1:9443 -L 8444:127.0.0.1:8444 -p <端口> root@<实例地址>
```

浏览器打开 `https://127.0.0.1:8443/`（自签证书，点"继续访问"），允许麦克风，开始对话；控制台 `https://127.0.0.1:9443/`。

::: tip 为什么三个端口
WebRTC 默认走 UDP，SSH 隧道只能转发 TCP。镜像里的 cored 以 `--ice-tcp :8444 --ice-nat-ip 127.0.0.1` 启动，向浏览器提供一个 127.0.0.1:8444 的被动 TCP 候选，媒体就从隧道走。UCloud 实例有公网 IP 时，把 `~/mynah/env` 里的 `ICE_NAT_IP` 改成公网 IP、开放三个 TCP 端口，就能不走隧道直连。
:::

常用命令：`mynahctl status | logs <服务> | restart | password`；端口、GPU、模型都在 `~/mynah/env`。

## 显卡

24 GB 跑默认配置（FlashHead ≈ 6.5 GB + 7B 模型 ≈ 5.5 GB + 语音识别 ≈ 1.4 GB）。16 GB 可用。12 GB 请把 `LLM_MODE=none`，在控制台「配置中心」填一个 OpenAI 兼容的云端模型地址。

## 自己做一个（镜像替你做的事）

在一台 CUDA 12 / Ubuntu 22.04 的新实例上：

```bash
git clone https://github.com/honwee/mynah.git ~/mynah/src      # 国内可用 https://gitcode.com/honwee/mynah
bash ~/mynah/src/deploy/cloud/bootstrap.sh                    # UCloud 等大盘实例加 --llm bundled
~/mynah/mynahctl start
```

`bootstrap.sh` 做的事：装系统包；下载 `cored` 发布二进制与 Luna/Nova 预设包；建一个 conda 环境装下全部 worker；用 conda-forge 装 PostgreSQL+pgvector 并初始化；克隆 FlashHead 并只下载 Lite 权重（约 8.4 GB，走 hf-mirror）；下载 SenseVoice（ModelScope）；装 ollama；生成自签证书和 `~/mynah/env`。所有下载都走国内镜像，可重复执行续传。

## 把实例当成笔记本的 GPU

也可以只用实例跑形象 worker，`cored` 和控制台放自己机器上：隧道转发 9421（FlashHead worker 的 gRPC 端口），本地 `AVATAR_ADDR=127.0.0.1:9421`。见 [快速开始 · 路径 C](./quickstart#路径-c-没有-gpu)。
