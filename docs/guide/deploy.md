---
title: Deploy with Docker Compose
---

# Deploy with Docker Compose

Two compose files live in `deploy/compose/`:

| File | Purpose | Containers |
|---|---|---|
| `docker-compose.yml` | **Dev stack**. cored + one avatar engine + turn detector. TTS, LLM, ASR, Postgres stay host-native. Safe to run next to another deployment on different ports and a different GPU. | `cored`, `flashhead` (default) or `avatar` (wav2lipLS, profile), `turn`, `tts-edge` |
| `docker-compose.full.yml` | **Production stack**. Everything containerized, engine pool, bake pipeline. Pass `--env-file .env.prod`. | `cored`, `musetalk`, `musetalk2`, `asr`, `tts-edge`, `avatartrain`, `postgres` (profile `db`), `tts` (profile `qwen-tts`), `redis` (profile) |

## Environment

Copy the example and fill it in. Anything that is a filesystem path on your host is required, not defaulted:

```bash
cp .env.example .env            # dev
cp .env.prod.example .env.prod  # production
```

The knobs you will actually touch:

| Variable | Meaning |
|---|---|
| `PL_MODELS_DIR` | Where model checkpoints and baked avatars live. Bind-mounted into workers at the same path. |
| `AVATAR_GPU`, `FLASHHEAD_GPU`, `CORED_GPUS` | CDI GPU index per service. `CORED_GPUS=all` lets the pool see every card. |
| `CORED_HTTP_PORT`, `CORED_TLS_PORT`, `ADMIN_TLS_PORT` | Visitor HTTP, visitor HTTPS, admin HTTPS. Production defaults 8020 / 8443 / 9443. |
| `AVATAR_ADDR`, `ASR_ADDR`, `TURN_ADDR` | gRPC workers cored dials. Point at remote hosts to split the deployment. |
| `TTS_URL`, `VOICE` | OpenAI-compatible TTS base and default voice id. EdgeTTS ids look like `zh-CN-XiaoxiaoNeural`. |
| `LLM_URL`, `LLM_MODEL`, `LLM_KEY` | Conversation model. Blank key for ollama. |
| `CORED_TLS_CERT`, `CORED_TLS_KEY` | Certificate files under `tls/` (bind-mounted read-only). |
| `PL_DB_DSN` | Postgres DSN when the database runs outside compose. |

Every value can also be changed later in the console's 配置中心 and applies without a restart; the database layer wins over flags, flags win over built-in defaults.

## Production bring-up

```bash
cd deploy/compose
docker compose --env-file .env.prod -f docker-compose.full.yml --profile db up -d postgres   # or use your own Postgres (needs pgvector)
docker compose --env-file .env.prod -f docker-compose.full.yml up -d cored musetalk asr tts-edge
docker compose --env-file .env.prod -f docker-compose.full.yml ps
```

Name the services explicitly: profiles keep the heavyweight options (`qwen-tts`, `avatartrain`) off until you ask.

Check health from the console dashboard or:

```bash
curl -sk https://127.0.0.1:9443/api/v1/health -H "Authorization: Bearer $TOKEN"
```

## TLS

WebRTC microphone capture requires a secure origin. The stack ships a self-signed certificate for first run. For a real domain, place `fullchain.crt` and `.key` under `tls/` and set `CORED_TLS_CERT` / `CORED_TLS_KEY`. With acme.sh and DNS validation the whole thing is:

```bash
acme.sh --issue --dns dns_cf -d your.domain --keylength ec-256
acme.sh --install-cert -d your.domain --ecc \
  --fullchain-file /path/to/mynah/tls/your.domain.fullchain.crt \
  --key-file       /path/to/mynah/tls/your.domain.key \
  --reloadcmd "docker restart mynah-cored"
```

Certificates are read at startup; the reload command restarts cored (about 10 seconds, live sessions drop).

## GPUs and the engine pool

One avatar worker serves one live session. Capacity is therefore "how many workers fit in VRAM", and the console's 本地服务 page shows that budget per GPU and lets you compose the pool declaratively (`POST /api/v1/avatar/pool`). Measured steady-state figures are in [Hardware](./hardware).

If you expose more than one GPU to cored, set `CORED_GPUS=all` (or a list) and give each worker container its own `*_GPU`.

## Upgrading

```bash
git pull
docker compose --env-file .env.prod -f docker-compose.full.yml up -d --build --force-recreate cored
```

`up -d` alone does not swap images; `--force-recreate` does. Database migrations run automatically when cored starts.

## Backup

Everything stateful is in Postgres (config, channels, knowledge base) and under `PL_MODELS_DIR` (baked avatars). `pg_dump` the database and `tar` that directory.

## Ports and firewalls

| Port | Service | Expose publicly? |
|---|---|---|
| 8443 | visitor pages and WebRTC signaling | yes, this is what visitors hit |
| 9443 | admin console and API | only to admins; put it behind a VPN or IP allow-list |
| 8020 | visitor HTTP (no TLS) | no, local only |
| 94xx | gRPC workers | no |

WebRTC media itself is UDP; the default configuration uses a public STUN server and host candidates. Behind strict NAT you will want a TURN server; set it via the `--stun` flag or 配置中心 → system.
