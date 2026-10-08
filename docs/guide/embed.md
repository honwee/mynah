---
title: Embed the visitor widget / SDK
---

# Embed the visitor widget / SDK

Three levels, from zero code to full control.

## Level 0: a link

Publish a [channel](./channels) and send the URL. Works on phones; no app.

## Level 1: an iframe

```html
<iframe src="https://your-mynah:8443/channel/sales?k=TOKEN"
        allow="microphone; autoplay" style="width:100%;height:100vh;border:0"></iframe>
```

Restrict the channel to your domain in 发布管理 so nobody else can embed it. The `allow` attribute is required or the browser blocks the microphone inside the frame.

## Level 2: the SDK

The browser SDK is served from your own cored at `/sdk/mynah.js` (global `MynahSDK`, minified `/sdk/mynah.min.js`; the old `/sdk/personalive.js` name still works) and also lives in `web/sdk/` as an ES module. A runnable example ships at `/sdk-example.html`. It renders the avatar into any element, keys out the green screen onto a transparent canvas, and exposes events and **abilities** so the digital human can act inside your page.

```html
<div id="stage" style="width:480px;height:720px"></div>
<script src="https://your-mynah:8443/sdk/mynah.js"></script>
<script>
  const human = new MynahSDK.Mynah({
    endpoint: "https://your-mynah:8443",
    slug: "sales",        // published channel
    token: "TOKEN",       // only for token-mode channels
    mic: true,            // ask for the microphone
    chroma: true,         // transparent canvas instead of green
    bg: "",               // or a background image URL
  });
  human.mount(document.getElementById("stage"));
  human.on("subtitle", ({ text }) => console.log("avatar:", text));
  human.on("user",     ({ text }) => console.log("visitor:", text));
  human.on("state",    (s) => console.log(s.state));   // idle | listening | thinking | speaking
  await human.connect();
  human.say("您好，欢迎光临。");          // speak verbatim
  human.ask("今天有什么优惠？");          // through the LLM + knowledge base
</script>
```

### API

| Method | Does |
|---|---|
| `mount(el)` | render into `el` (transparent canvas when `chroma` is on) |
| `connect()` / `disconnect()` | open / close the WebRTC session |
| `say(text)` | speak verbatim |
| `ask(text)` | answer through the configured brain; may trigger abilities |
| `interrupt()` | stop speaking now |
| `mic(on)` | mute / unmute the microphone track without renegotiating |
| `on(event, cb)` / `off(event, cb)` | events: `connected`, `disconnected`, `state`, `subtitle`, `user`, `tool`, `error` |
| `ability(def)` / `removeAbility(name)` | register a function the model can call |

### Abilities: let the avatar drive your page

```js
human.ability({
  name: "goto_page",
  description: "Open a page on the big screen when the visitor asks to see a product line.",
  parameters: { type: "object", properties: { page: { type: "string" } }, required: ["page"] },
  blocking: false,
  run: ({ page }) => router.push(page),
});
```

The model sees the description, decides when to call it, cored relays the call to your page over the data channel, your `run` executes, and if `blocking` is true its return value is fed back into the answer (the avatar says a short filler while it waits). This is how a digital guide switches slides or looks up an order without any server-side change.

### Custom element

The same thing declaratively:

```html
<script type="module" src="/sdk/mynah.js"></script>
<mynah-avatar endpoint="https://your-mynah:8443" slug="sales" mic>
  <mynah-ability name="goto_page" description="Open a page on the big screen"
                       parameters='{"type":"object","properties":{"page":{"type":"string"}}}'></mynah-ability>
</mynah-avatar>
```

## Level 3: raw HTTP

For non-browser clients the visitor surface is five routes: `POST /offer` (or `/channel/offer` with `slug` and `token`), `/human`, `/interrupt_talk`, `/is_speaking`, `/action`. The [README](https://github.com/honwee/mynah#integration--api) lists the bodies. CORS is enabled on all of them.

## Agents

An LLM agent is just another client. The [DeepSeek Harness plugin](./agents) wraps these routes as tools so an agent can make the avatar speak, wave and report.
