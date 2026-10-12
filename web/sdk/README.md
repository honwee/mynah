# Mynah 浏览器 SDK

把数字人当一个**通用组件**挂进任意宿主页面 —— BI 大屏、后台系统、展厅屏幕。

核心不是「页面里嵌了个会说话的头像」，而是**能力契约**：宿主页面声明自己会做
什么，cored 只把模型发起的调用原样转发回来，**永远不知道宿主是什么**。所以这个
SDK 里没有一行业务代码，接入方也不需要改 cored —— 「数字人能操作大屏」和
「数字人能做成通用组件」本来就是同一件事。

## 快速开始

```html
<div id="avatar" style="width:300px;height:400px"></div>
<script src="https://<cored>/sdk/mynah.min.js"></script>
<script>
  const pl = new Mynah({ endpoint: "https://<cored>", slug: "bi", token: "…" });

  pl.ability({
    name: "goto_page",
    description: "打开大屏上的指定页面",   // 模型只靠这句话决定要不要调，写清楚
    parameters: {
      type: "object",
      properties: { page: { type: "string", enum: ["overview", "sales"] } },
      required: ["page"],
    },
    run: (a) => store.goto(a.page),
  });

  pl.mount("#avatar");
  await pl.connect();
</script>
```

完整可跑的例子见 [`web/user/sdk-example.html`](../user/sdk-example.html)（一块假
BI 大屏 + 数字人，全 mock）。

## 两类能力 —— 这是设计的要点

| | 动作类（默认） | 查询类（`blocking: true`） |
|---|---|---|
| cored 等不等 | **不等**，立刻回 `{ok:true}` 给模型 | 等 `run()` 的返回值，最长 6 秒 |
| 效果 | 「好的，我打开销售页」与**切页同时发生** | 结果进答案，模型能把数字念出来 |
| 垫场话 | 无 | **必须有**，默认「我查一下。」 |

查询类为什么强制垫场：等待期间数字人如果不说话，2 秒静默看起来不是「在思考」，
是「卡住了」。`filler` 走的是和正常语句同一条句子管道，所以 TTS 节奏和字幕时间轴
自动对齐。

`run()` 超时（6s）或抛错，模型会收到 `{"error":...}` 并照常把话说完 —— 宿主慢或
崩，数字人不会跟着僵住。

## API

```js
new Mynah({ endpoint, slug, token, mic = true, chroma = true, bg })
```

- `endpoint` — cored 基址，留空 = 同源。
- `slug` / `token` — 已发布频道；不给 `slug` 则走无频道的 `/offer`。
- `mic` — 是否请求麦克风（拿不到会自动降级为文字输入，并 emit `error`）。
- `chroma` — 绿幕抠像成**透明 canvas**，直接叠在大屏上。
- `bg` — 给了背景图就在绿幕处铺图，而不是透明。

| 方法 | 说明 |
|---|---|
| `.ability({name, description, parameters, blocking, filler, run})` | 注册能力，连接前后都可调 |
| `.removeAbility(name)` | 注销 |
| `.mount(selector \| element)` | 挂画面 |
| `.connect()` / `.disconnect()` | 连接 / 断开 |
| `.ask(text)` | 文字提问（过 LLM，会触发能力调用） |
| `.say(text)` | 逐字念出（不过 LLM） |
| `.interrupt()` | 打断当前发言 |
| `.mic(on)` | 开关采集（轨道级静音，不重协商） |
| `.on(event, cb)` / `.off(event, cb)` | 订阅事件 |

事件：`subtitle` `{text, start?}`、`user` `{text}`（识别到的问话）、
`state`（引擎状态机）、`tool` `{name, arguments, blocking}`、
`connected` `{mic, sessionId}`、`disconnected`、`error` `{kind, title, message}`。

### 声明式写法

```html
<mynah-avatar endpoint="https://<cored>" slug="bi" token="…"
                    style="width:300px;height:400px">
  <mynah-ability name="goto_page" description="打开大屏上的某个页面"
    parameters='{"type":"object","properties":{"page":{"type":"string"}},"required":["page"]}'>
  </mynah-ability>
</mynah-avatar>

<script>
  document.querySelector("mynah-ability")
    .addEventListener("ability", (e) => { store.goto(e.detail.arguments.page); e.detail.respond({ok:true}); });
</script>
```

`blocking` 能力必须调 `e.detail.respond(值或Promise)`；动作类调不调都行。
加 `manual` 属性可阻止自动连接，`no-mic` / `no-chroma` 关掉对应功能。

## 构建

```sh
cd web/sdk && npm install && node build.mjs        # 或 --watch
```

产物落在 **`web/user/sdk/`**，也就是 cored 静态目录里 —— 不用加路由、加 flag，
现有部署和 compose 的 bind-mount 原样生效。产物入库，接入方直接 `<script src>`。

## 跨域接入

大屏部署在别的域名时，SDK 会跨域访问 `/channel/offer`、`/channel/{slug}/config`、
`/human`、`/interrupt_talk`。这几个端点都带 CORS（见 `internal/signaling/cors.go`）。

访问控制**不在 CORS 层**，而是照旧在 handler 里：token（constant-time）→
`domains` 白名单 → CIDR。所以：

- 生产接入建议 `access_mode=token` + `domains` 白名单。
- **`domains` 写主机名，不带端口** —— 匹配用的是 URL 的 Hostname，
  写 `localhost:5555` 永远匹配不上，应写 `localhost`。
- token 直接写在页面里，等于把它交给了所有访客。演示站可以接受；生产应由宿主
  后端换发短时票据（**尚未实现**，见仓库 issue）。
- cored 用请求体里的 token 鉴权、不使用 Cookie，所以 CORS 层反射 Origin 且
  **不开** `Access-Control-Allow-Credentials`。改动这里前请先读 `cors.go` 的注释。

## 已知边界

- **qwen realtime 模式（`brain=qwen`）不支持能力调用** —— 工具调用走的是
  `brain=local` 的 LLM 链路。BI 类场景请用 `brain=local` + 指向云端
  OpenAI 兼容端点。
- 单轮最多 3 轮「模型 ↔ 能力」往返，超出后模型用手上已有的信息作答。
- 抠像逻辑目前与 `web/user/channel.html` 各存一份，等 SDK 稳定后由访客页反过来
  用 SDK。
