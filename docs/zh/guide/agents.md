---
title: Agent 接入（DeepSeek Harness 插件）
---

# 给 Agent 一张脸：DeepSeek Harness 插件

`dsh-plugin-mynah` 让 [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) 的 Agent 直接驱动 Mynah 数字人：对着屏幕前的人说话、挥手、打断、看谁在线、查健康、往知识库塞资料。仓库：[github.com/honwee/dsh-plugin-mynah](https://github.com/honwee/dsh-plugin-mynah)。

<img src="https://raw.githubusercontent.com/honwee/dsh-plugin-mynah/main/docs/agent-demo.gif" alt="agent demo" width="900">

## 工具

| 工具 | 作用 | 需管理员登录 |
|---|---|---|
| `mynah_speak` | 让数字人说 `text`；`echo` 原话或 `chat` 走 Mynah 大脑；默认排在当前句后面说，`interrupt=true` 才打断 | 否 |
| `mynah_action` | 做动作，如 `wave` 挥手 | 否 |
| `mynah_interrupt` | 闭嘴 | 否 |
| `mynah_sessions` | 在线会话及 id | 是 |
| `mynah_channels` | 已发布频道、链接、可用动作 | 是 |
| `mynah_status` | 核心 / 引擎 / ASR / TTS / 数据库健康 | 是 |
| `mynah_kb_add` | 往知识库加一篇文本 | 是 |

## 安装

```sh
dsh plugin --profile web add dsh-plugin-mynah
```

然后在 profile 的 `cordis.patch.yml` 里指向你的实例（或用环境变量 `MYNAH_URL`、`MYNAH_ADMIN_URL`、`MYNAH_USER`、`MYNAH_PASSWORD`）：

```yaml
- id: mynah
  name: dsh-plugin-mynah
  config:
    baseUrl: https://your-mynah:8443
    adminUrl: https://your-mynah:9443
    username: admin
    password: "********"
```

打开一个频道页点「开始对话」，然后对 Agent 说："找到在线的 Mynah 会话，挥个手，自我介绍，然后汇报系统健康状态。"

## 实测延迟

公开演示站（Qwen 云端实时大脑）：请求到云端首包音频 0.37–0.52 秒，到第一帧带口型画面 0.73–1.07 秒。Agent 演示里感觉到的等待，大部分是 Agent 在工具调用之间的推理，不是数字人。

## 其它 Agent 框架

插件只是五个 HTTP 路由的薄封装，移植到别的框架是一个下午的事。见[嵌入第 3 层](./embed#第-3-层-裸-http)。
