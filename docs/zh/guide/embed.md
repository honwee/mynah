---
title: 嵌入访客组件 / SDK
---

# 嵌入访客组件 / SDK

三个层次，从零代码到完全可控。

## 第 0 层：一个链接

[发布频道](./channels)，把 URL 发出去。手机能用，不装 App。

## 第 1 层：iframe

```html
<iframe src="https://your-mynah:8443/channel/sales?k=TOKEN"
        allow="microphone; autoplay" style="width:100%;height:100vh;border:0"></iframe>
```

在发布管理里把频道限制到你的域名，别人就嵌不了。`allow` 属性必须有，否则浏览器会在 iframe 里禁掉麦克风。

## 第 2 层：SDK

浏览器 SDK 由你自己的 cored 提供，地址 `/sdk/mynah.js`（全局 `MynahSDK`，压缩版 `/sdk/mynah.min.js`；旧名 `/sdk/personalive.js` 仍可用），源码在 `web/sdk/`，也可当 ES 模块用。仓库自带一个可运行示例 `/sdk-example.html`。它把数字人渲染进任意元素，把绿幕抠成透明画布，并暴露事件和**能力**，让数字人能在你的页面里做事。

```html
<div id="stage" style="width:480px;height:720px"></div>
<script src="https://your-mynah:8443/sdk/mynah.js"></script>
<script>
  const human = new MynahSDK.Mynah({
    endpoint: "https://your-mynah:8443",
    slug: "sales",        // 已发布的频道
    token: "TOKEN",       // 令牌频道才需要
    mic: true,            // 请求麦克风
    chroma: true,         // 透明画布而不是绿幕
    bg: "",               // 或一张背景图 URL
  });
  human.mount(document.getElementById("stage"));
  human.on("subtitle", ({ text }) => console.log("数字人:", text));
  human.on("user",     ({ text }) => console.log("访客:", text));
  human.on("state",    (s) => console.log(s.state));   // idle | listening | thinking | speaking
  await human.connect();
  human.say("您好，欢迎光临。");          // 原话播报
  human.ask("今天有什么优惠？");          // 走 LLM + 知识库
</script>
```

### 接口

| 方法 | 作用 |
|---|---|
| `mount(el)` | 渲染进 `el`（开抠像时是透明 canvas） |
| `connect()` / `disconnect()` | 建立 / 断开 WebRTC 会话 |
| `say(text)` | 原话播报 |
| `ask(text)` | 交给配置的大脑回答；可能触发能力 |
| `interrupt()` | 立刻停止说话 |
| `mic(on)` | 静音 / 取消静音麦克风轨道，不重协商 |
| `on(event, cb)` / `off(event, cb)` | 事件：`connected`、`disconnected`、`state`、`subtitle`、`user`、`tool`、`error` |
| `ability(def)` / `removeAbility(name)` | 注册一个模型可以调用的函数 |

### 能力：让数字人操作你的页面

```js
human.ability({
  name: "goto_page",
  description: "访客想看某条产品线时，打开大屏上对应的页面。",
  parameters: { type: "object", properties: { page: { type: "string" } }, required: ["page"] },
  blocking: false,
  run: ({ page }) => router.push(page),
});
```

模型看到 description 自己决定什么时候调，cored 通过数据通道把调用转给你的页面，你的 `run` 执行；`blocking` 为 true 时返回值会回灌进回答（等待时数字人说一句垫场话）。数字讲解员翻页、查订单，不用改任何服务端代码。

### 自定义元素

同样的事，声明式写法：

```html
<script type="module" src="/sdk/mynah.js"></script>
<mynah-avatar endpoint="https://your-mynah:8443" slug="sales" mic>
  <mynah-ability name="goto_page" description="打开大屏上的某个页面"
                       parameters='{"type":"object","properties":{"page":{"type":"string"}}}'></mynah-ability>
</mynah-avatar>
```

## 第 3 层：裸 HTTP

非浏览器客户端只需五个路由：`POST /offer`（或带 `slug` 和 `token` 的 `/channel/offer`）、`/human`、`/interrupt_talk`、`/is_speaking`、`/action`。请求体见 [README](https://github.com/honwee/mynah#integration--api)。全部开了 CORS。

## Agent

LLM Agent 也只是另一种客户端。[DeepSeek Harness 插件](./agents)把这几个路由包成工具，Agent 可以让数字人说话、挥手、汇报。
