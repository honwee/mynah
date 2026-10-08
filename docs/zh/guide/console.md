---
title: 控制台总览
---

# 控制台总览

控制台是 cored 在管理端口（`https://<host>:9443/`）上提供的单页应用，背后是[管理 API](/api/admin-api)；你能点的，都能写脚本做。

<img src="/screens/dashboard.png" alt="仪表盘" style="border:1px solid #e5e7eb;border-radius:8px">


## 登录

一个管理员账号。初始密码在 cored 日志里打印一次（`initial admin`），首次登录强制修改。令牌是 24 小时有效的 JWT。随时可在账号菜单或 `POST /api/v1/auth/password` 改密。

## 页面

| 页面 | 在这做什么 | 背后接口 |
|---|---|---|
| **仪表盘** | 一眼看各组件健康：数字人引擎、ASR、TTS、LLM、向量模型、Postgres。 | `GET /api/v1/health` |
| **配置中心** | 对话模型（任何 OpenAI 兼容接口，「测试连接」会真跑一轮）、人设提示词、流式回复、TTS 地址与音色、对话大脑（本地链路或 Qwen 云端实时）、语音轮次参数、RAG 参数。改完热生效。 | `GET/PUT /api/v1/config/{group}` |
| **形象与声音** | 选当前形象，上传说话视频烘焙新形象，预热形象避免冷启动，选音色并试听，TTS 档支持时用参考音频克隆音色。 | `/api/v1/avatars*`、`/api/v1/tts/voices` |
| **知识库** | 建库、传文档、看摄取进度、按当前参数做检索测试。 | `/api/v1/kb*`、`/api/v1/documents*` |
| **发布管理** | 把当前配置冻结成频道：带访问控制、并发上限和品牌定制的分享链接。重新发布、轮换令牌、上下线。 | `/api/v1/channels*` |
| **本地服务** | 引擎池：哪些引擎跑在哪张卡，显存预算和还能分配多少，启停重启 worker，按引擎分的并发配额和绑定的频道。 | `/api/v1/services*`、`/api/v1/avatar/*` |
| **对话调试** | 用访客同一条链路试：文字或语音，看命中的知识片段和延迟，文本驱动数字人，触发动作，实时调绿幕抠像参数。 | `/api/v1/playground/*` |
| **会话监控** | 谁在线、连了多久、几轮、是否在说话；可踢下线。 | `/api/v1/sessions*` |

## 看懂控制台的三个概念

**配置分层。** 内置默认 < 启动参数 < 数据库。控制台里保存的最优先，重启也在。API 返回合并后的视图。

**频道是快照。** 发布时，当前的模型、音色、大脑模式和知识库参数被冻结进频道。你可以在控制台继续折腾，访客看到的不变；点「重新发布」才把新设置推出去。品牌字段（名称、logo、背景）是实时元数据，改了立刻生效。

**容量是明账。** 一个 worker 服务一路会话。「本地服务」按实测的每引擎显存算出你的卡能放几个 worker，放不下的组合直接拒绝。频道并发上限从这个池子里分配，你可以向某个部门承诺"同时三位访客"，系统会替你守住。

<img src="/screens/services.png" alt="本地服务：引擎池与显存账" style="border:1px solid #e5e7eb;border-radius:8px">


<img src="/screens/playground.png" alt="对话调试" style="border:1px solid #e5e7eb;border-radius:8px">


## 主题与语言

明暗两套主题，中英文界面，顶栏切换。访客页按频道各自定主题。
