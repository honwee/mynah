---
title: Docker Compose 部署
---

# Docker Compose 部署

`deploy/compose/` 下有两份 compose：

| 文件 | 用途 | 容器 |
|---|---|---|
| `docker-compose.yml` | **开发栈**。cored + 一个数字人引擎 + 轮次检测。TTS、LLM、ASR、Postgres 留在宿主机。换端口换显卡后可与另一套部署并存。 | `cored`、`flashhead`（默认）或 `avatar`（wav2lipLS，profile）、`turn`、`tts-edge` |
| `docker-compose.full.yml` | **生产栈**。全部容器化，引擎池，烘焙管线。要带 `--env-file .env.prod`。 | `cored`、`musetalk`、`musetalk2`、`asr`、`tts-edge`、`avatartrain`、`postgres`（profile `db`）、`tts`（profile `qwen-tts`）、`redis`（profile） |

## 环境变量

复制示例再填。凡是宿主机上的文件路径都是必填，没有默认值：

```bash
cp .env.example .env            # 开发
cp .env.prod.example .env.prod  # 生产
```

真正会碰的几个：

| 变量 | 含义 |
|---|---|
| `PL_MODELS_DIR` | 模型权重和烘焙形象的目录。以同一路径挂进 worker。 |
| `AVATAR_GPU`、`FLASHHEAD_GPU`、`CORED_GPUS` | 各服务的 CDI 显卡序号。`CORED_GPUS=all` 让引擎池看到所有卡。 |
| `CORED_HTTP_PORT`、`CORED_TLS_PORT`、`ADMIN_TLS_PORT` | 访客 HTTP、访客 HTTPS、管理 HTTPS。生产默认 8020 / 8443 / 9443。 |
| `AVATAR_ADDR`、`ASR_ADDR`、`TURN_ADDR` | cored 要连的 gRPC worker。指到远程主机就能拆分部署。 |
| `TTS_URL`、`VOICE` | OpenAI 兼容 TTS 地址和默认音色。EdgeTTS 的音色形如 `zh-CN-XiaoxiaoNeural`。 |
| `LLM_URL`、`LLM_MODEL`、`LLM_KEY` | 对话模型。ollama 留空 key。 |
| `CORED_TLS_CERT`、`CORED_TLS_KEY` | `tls/` 下的证书文件（只读挂载）。 |
| `PL_DB_DSN` | 数据库在 compose 外时的 Postgres DSN。 |

这些值之后都能在控制台「配置中心」改，热生效；数据库层覆盖启动参数，启动参数覆盖内置默认。

## 生产启动

```bash
cd deploy/compose
docker compose --env-file .env.prod -f docker-compose.full.yml --profile db up -d postgres   # 或用你自己的 Postgres（需要 pgvector）
docker compose --env-file .env.prod -f docker-compose.full.yml up -d cored musetalk asr tts-edge
docker compose --env-file .env.prod -f docker-compose.full.yml ps
```

服务要点名。重的可选项（`qwen-tts`、`avatartrain`）放在 profile 里，不叫不起。

健康检查看控制台仪表盘，或：

```bash
curl -sk https://127.0.0.1:9443/api/v1/health -H "Authorization: Bearer $TOKEN"
```

## TLS

WebRTC 采集麦克风要求安全来源。首次运行自带自签证书。有域名时把 `fullchain.crt` 和 `.key` 放进 `tls/`，设置 `CORED_TLS_CERT` / `CORED_TLS_KEY`。用 acme.sh 走 DNS 验证整套就是：

```bash
acme.sh --issue --dns dns_cf -d your.domain --keylength ec-256
acme.sh --install-cert -d your.domain --ecc \
  --fullchain-file /path/to/mynah/tls/your.domain.fullchain.crt \
  --key-file       /path/to/mynah/tls/your.domain.key \
  --reloadcmd "docker restart mynah-cored"
```

证书在启动时读取；reload 命令重启 cored，大约 10 秒，进行中的会话会断。

## 显卡与引擎池

一个数字人 worker 同时只服务一路会话，容量就是"显存里放得下几个 worker"。控制台「本地服务」页按卡显示这笔账，并允许你声明式地组合引擎池（`POST /api/v1/avatar/pool`）。实测稳态数字见[硬件](./hardware)。

给 cored 看多张卡时设 `CORED_GPUS=all`（或列表），每个 worker 容器各自设 `*_GPU`。

## 升级

```bash
git pull
docker compose --env-file .env.prod -f docker-compose.full.yml up -d --build --force-recreate cored
```

只 `up -d` 不会换镜像，`--force-recreate` 才会。数据库迁移在 cored 启动时自动执行。

## 备份

有状态的东西只有两处：Postgres（配置、频道、知识库）和 `PL_MODELS_DIR`（烘焙形象）。`pg_dump` 加 `tar` 即可。

## 端口与防火墙

| 端口 | 服务 | 是否公网开放 |
|---|---|---|
| 8443 | 访客页与 WebRTC 信令 | 开，访客就连这里 |
| 9443 | 控制台与管理 API | 只对管理员；放 VPN 或 IP 白名单后面 |
| 8020 | 访客 HTTP（无 TLS） | 不开，本机用 |
| 94xx | gRPC worker | 不开 |

WebRTC 媒体本身是 UDP；默认用公共 STUN 和主机候选。严格 NAT 后面需要 TURN，通过 `--stun` 参数或 配置中心 → system 设置。
