---
title: Agents (DeepSeek Harness plugin)
---

# Give an agent a face: the DeepSeek Harness plugin

`dsh-plugin-mynah` lets a [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) agent drive a Mynah digital human: make it speak to the person watching, wave, interrupt, list who is connected, check health, feed the knowledge base. Repo: [github.com/honwee/dsh-plugin-mynah](https://github.com/honwee/dsh-plugin-mynah).

<img src="https://raw.githubusercontent.com/honwee/dsh-plugin-mynah/main/docs/agent-demo.gif" alt="agent demo" width="900">

## Tools

| Tool | Does | Needs admin login |
|---|---|---|
| `mynah_speak` | avatar says `text`; `echo` verbatim or `chat` through Mynah's brain; queues behind the current sentence unless `interrupt=true` | no |
| `mynah_action` | gesture such as `wave` | no |
| `mynah_interrupt` | stop talking | no |
| `mynah_sessions` | live sessions and their ids | yes |
| `mynah_channels` | published channels, URLs, available gestures | yes |
| `mynah_status` | health of core / engines / ASR / TTS / DB | yes |
| `mynah_kb_add` | add a text document to a knowledge base | yes |

## Install

```sh
dsh plugin --profile web add dsh-plugin-mynah
```

Then point it at your instance in the profile's `cordis.patch.yml` (or `MYNAH_URL`, `MYNAH_ADMIN_URL`, `MYNAH_USER`, `MYNAH_PASSWORD` in the environment):

```yaml
- id: mynah
  name: dsh-plugin-mynah
  config:
    baseUrl: https://your-mynah:8443
    adminUrl: https://your-mynah:9443
    username: admin
    password: "********"
```

Open a channel page, click 开始对话, and ask the agent: *"find the live Mynah session, wave, introduce yourself, then report system health."*

## Latency, measured

On the public demo (Qwen realtime brain): request to first cloud audio 0.37–0.52 s, to first lip-synced frame 0.73–1.07 s. Most of what you perceive in an agent demo is the agent's own reasoning between tool calls, not the avatar.

## Other agent frameworks

The plugin is a thin wrapper over five HTTP routes; porting it to another framework is an afternoon. See [Embed, level 3](./embed#level-3-raw-http).
