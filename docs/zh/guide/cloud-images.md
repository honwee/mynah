---
title: 云镜像（AutoDL / UCloud）
---

# 云镜像（AutoDL / UCloud）

给不想装 Docker 和驱动的人：租一台 GPU 实例，选 Mynah 镜像，跑一条命令。

::: warning 状态
配方和引导脚本已在仓库里（`deploy/cloud/`），脚本按两家平台的容器布局写好。**镜像本身还没有发布。** 发布前请在任意 PyTorch / CUDA 12 基础镜像上按下面的手动步骤做。镜像上线后这里会放链接。
:::

## 为什么这两家要另一套配方

AutoDL 和 UCloud 给的是带 GPU 的容器，不是虚拟机，里面没有 Docker 守护进程。所以镜像里 Mynah 是**原生**跑的：预编译的 `cored` 二进制加 conda 环境里的 Python worker。端口、TLS、控制台和 Docker 部署完全一样。

## 手动引导（镜像替你做的事）

在一台有 conda 和 GPU 的新实例上：

```bash
git clone https://github.com/honwee/mynah.git
bash mynah/deploy/cloud/bootstrap.sh            # 12GB 以上显卡可加 --engine flashhead
~/mynah/start.sh
```

`bootstrap.sh` 装 ffmpeg，下载 `cored` 发布二进制，为 EdgeTTS、SenseVoice ASR 和 wav2lipLS 引擎各建环境（这条路没有大文件下载），并写出 `~/mynah/start.sh` / `stop.sh`，按顺序拉起服务并等每个就绪。

然后在平台的自定义服务面板映射两个端口：

| 端口 | 用途 |
|---|---|
| 8443 | 访客页：`https://<映射地址>:8443/` |
| 9443 | 控制台 |

证书是自签的，浏览器警告点过去。初始管理员密码在 `~/mynah/logs-cored.log`（搜 `initial admin`）。

## 你得到什么

- EdgeTTS 语音（免费，实例需能访问外网）
- wav2lipLS 引擎加内置默认形象（约 2GB 显存）
- SenseVoice 语音识别
- 不带数据库的控制台：配置内嵌；把 `PL_DB_DSN` 指向一个 Postgres 后，知识库和频道发布即可用

升级路径和别处一样：12GB 以上的卡在「本地服务」切 MuseTalk 或 FlashHead，在「配置中心」切 TTS 档。

## 把云实例当笔记本的显卡用

也可以只让实例当**数字人引擎**，`cored` 和控制台跑在自己机器上。映射 worker 的 gRPC 端口（wav2lipLS 为 9420），本地设 `AVATAR_ADDR=<映射地址>:<端口>`。见[快速开始路线 C](./quickstart#路线-c-无显卡)。

## 发布镜像（维护者）

AutoDL：从 PyTorch 2.x / CUDA 12.x 基础镜像建实例 → 跑 `bootstrap.sh` → 验证 `start.sh` → 保存镜像 → 用 `deploy/cloud/autodl.md` 的文案发布到 codewithgpu。UCloud compshare 同样流程 → 制作镜像。镜像保持精简：除非做「GPU 版」，不要塞 Qwen3-TTS 和 FlashHead 权重。
