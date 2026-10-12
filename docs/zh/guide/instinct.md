# 本能层：大脑还没想好，身体先动

Mynah 可以在「大脑」还在组织回答时就做出身体反应：一个快速决策模型看着实时转录，从这个形象烘焙好的动作片段里挑一个——点头、挥手、摇头、大笑、害羞、眨眼——或者什么都不做。说话的模型不负责动作，动作的模型不负责说话。这套分工来自 [open-annie](https://github.com/rehan-remade/open-annie)，决策跑在 [Jev](https://typesafe.ai)（TypeSafe System One）上，本地规则作为底线和兜底。

## 它怎么工作

1. **片段。** 每个动作都是一小段预先烘焙的视频，与形象的待机循环在同一帧域里（见[形象](./avatars)）。用烘焙接口加 `kind: "action"` 和一个 LivePortrait 驱动模板就能烘：

   ```bash
   curl -sk -X POST https://<host>:9443/api/v1/avatars/bake \
     -H "authorization: Bearer $TOKEN" -H "content-type: application/json" \
     -d '{"avatar_id":"luna","kind":"action","action":"shake","motion_ref":"builtin:shake_face","apply":true}'
   ```

   适合做动作的内置模板：`shake_face`、`laugh`、`shy`、`wink`。片段热安装，并持久化在 `config.avatar.actions`。

2. **提问。** 访客说话时，引擎大约每 450 毫秒的新文本问一个封闭题：*这些片段里哪一个此刻最合适，还是都不做？* 数字人说话时再多问一题：*这段话是否明确承诺了一个动作？*（「我挥挥手」→ 是）。每个选项都带一句说明，模型知道片段长什么样。

3. **门控。** 只有置信度够高才播（默认 0.6，文本还在流式进来时 0.8），两次动作至少隔 4 秒，并且只在数字人没有在说话时播（说话帧优先，片段等下一次提问）。

4. **兜底。** 没有 key、超时、429、连续三次失败 → 规则引擎用关键词回答同样的问题。永远不会卡住对话。

## 决策来源

| 来源 | 延迟 | 费用 | 需要 |
|---|---|---|---|
| `rules` | 0 ms | 免费 | 无 |
| `jev` | 约 100–500 ms | 每百万输入 token 0.042 美元 | TypeSafe API Key；`api.typesafe.ai` 返回 451 的地区需要一个出海代理 |

在 **控制台 → 配置中心 → 本能层**（配置组 `instinct`）里设置：开关、来源、Key、可选的 `proxy_url`、门槛。新建会话生效。

## 怎么看到它在工作

访客页的 DataChannel 会收到每一次决策：`{"type":"instinct", clip, reason, provider, latency_ms, answers}`；cored 日志以 `[instinct <会话>]` 打印。dsh 插件的 `mynah_action` 工具可以手动触发同一批片段。
