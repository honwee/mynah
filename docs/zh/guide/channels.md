---
title: 发布频道
---

# 发布频道

频道就是一个可以发出去的数字人页面：`https://<host>:8443/channel/<slug>`。发布会把你在控制台调好的配置冻结进这个页面，你可以继续调，不会吓到访客。

<img src="/screens/publish.png" alt="发布管理页" style="border:1px solid #e5e7eb;border-radius:8px">


<img src="/screens/visitor-start.jpg" alt="访客点「开始对话」前看到的页面" style="border:1px solid #e5e7eb;border-radius:8px">


## 一分钟发布

1. 把控制台调成你要的样子：形象、音色、模型、人设、知识库、大脑模式。
2. 发布管理 → **新建频道**。起一个 slug（就是 URL）和显示名。
3. 选访问方式：
   - **公开链接**：拿到 URL 就能进。
   - **令牌链接**：URL 带 `?k=<token>`，随时可轮换。
   - 可选按**域名**（用于嵌入）或 **CIDR**（办公网、触摸屏）限制。
4. 设**并发上限**。这个数从引擎池里分配，所有启用频道的总和超过池子容量时控制台会拒绝。
5. 发布。复制分享链接。

## 什么被冻结，什么是实时的

| 发布时冻结（快照） | 实时生效 |
|---|---|
| 模型、人设、流式 | 品牌名、logo、背景图、主题色 |
| TTS 音色（Qwen 大脑模式下是云端音色） | 描述、推荐问题 |
| 大脑模式、轮次参数 | 启用 / 停用、访问令牌 |
| RAG 参数 | 知识库*内容* |

控制台改了想推到频道？**重新发布**会把版本号加一。正在聊的访客用旧快照聊完。

## 品牌与背景

- **品牌名 / logo** 替换顶栏的 Mynah 标。logo 留空则显示内置标。
- **背景图**会打开浏览器端绿幕抠像：形象的绿色背景被抠掉，你的图垫在后面。抠像参数（`key_color`、`similarity`、`smoothness`、`spill`）来自对话调试页的「绿幕调试」卡，按形象保存。不配背景图就不抠像，访客看到原始画面。
- 仓库自带一张中性的预设背景 `/assets/bg/studio.jpg`，想马上用就填它。

## 容量与礼貌

频道并发到顶，或没有空闲 worker 时，访客看到的是"数字人正忙"的礼貌提示和重试按钮，不是转圈。访客端点还有按 IP 的限流（`--visitor-rate-limit`）。

## 给接入方

- 频道页是全屏应用，可以放进 `<iframe>`；用域名限制保证只有你的站能嵌。
- 原生接入用 [SDK](./embed)，传同样的 slug 和 token；它和页面一样走 `/channel/offer`。
- 可以给访客看的频道配置公开在 `GET /channel/<slug>/config`（品牌、动作、抠像参数）。密钥、提示词、key 永远不会从这里出去。

## API

`POST /api/v1/channels` 发布，`PATCH /api/v1/channels/{id}` 改实时字段，`POST …/republish`，`POST …/rotate-token`，`DELETE`。详见[管理 API](/api/admin-api)。
