// 声明式外壳：把 SDK 包成一个自定义元素，接入方一行 HTML 就能挂上数字人。
//
//   <mynah-avatar endpoint="https://host:8443" slug="bi" token="…" mic>
//     <mynah-ability name="goto_page" description="打开大屏上的某个页面"
//       parameters='{"type":"object","properties":{"page":{"type":"string"}}}'>
//     </mynah-ability>
//   </mynah-avatar>
//
// 能力的 run 用 DOM 事件回传，这样纯 HTML 也能声明、业务逻辑仍留在宿主页面：
//   el.addEventListener("ability", (e) => { e.detail.respond(doIt(e.detail.arguments)) });
// blocking 能力必须调 respond()（可传 Promise）；动作类调不调都行。

import { Mynah } from "./mynah.js";

class MynahAvatar extends HTMLElement {
  static get observedAttributes() { return ["endpoint", "slug", "token"]; }

  connectedCallback() {
    if (this.pl) return;
    if (!this.style.display) this.style.display = "block";

    this.pl = new Mynah({
      endpoint: this.getAttribute("endpoint") || "",
      slug: this.getAttribute("slug") || "",
      token: this.getAttribute("token") || "",
      mic: !this.hasAttribute("no-mic"),
      chroma: !this.hasAttribute("no-chroma"),
      bg: this.getAttribute("bg") || "",
    });

    // 转发成 DOM 事件，宿主用 addEventListener 就够，不必碰 SDK 实例。
    for (const ev of ["subtitle", "user", "state", "tool", "connected", "disconnected", "error"]) {
      this.pl.on(ev, (detail) => this.dispatchEvent(new CustomEvent(ev, { detail })));
    }

    const stage = document.createElement("div");
    stage.style.cssText = "width:100%;height:100%;position:relative";
    this.appendChild(stage);
    this.pl.mount(stage);

    this._adoptAbilities();
    // 子元素可能在本元素 upgrade 之后才解析出来。
    this._mo = new MutationObserver(() => this._adoptAbilities());
    this._mo.observe(this, { childList: true });

    if (!this.hasAttribute("manual")) {
      this.pl.connect().catch(() => { /* 已经 emit 过 error 了 */ });
    }
  }

  disconnectedCallback() {
    this._mo?.disconnect();
    this.pl?.disconnect();
    this.pl = null;
  }

  _adoptAbilities() {
    for (const node of this.querySelectorAll("mynah-ability, personalive-ability")) {
      const name = node.getAttribute("name");
      if (!name || this.pl.abilities.has(name)) continue;
      let parameters;
      try { parameters = JSON.parse(node.getAttribute("parameters") || "null"); }
      catch (e) { console.error("[mynah] ability %s 的 parameters 不是合法 JSON", name); }

      this.pl.ability({
        name,
        description: node.getAttribute("description") || "",
        parameters: parameters || undefined,
        blocking: node.hasAttribute("blocking"),
        filler: node.getAttribute("filler") || "",
        run: (args) => new Promise((resolve) => {
          let answered = false;
          const respond = (v) => { if (!answered) { answered = true; resolve(v); } };
          node.dispatchEvent(new CustomEvent("ability", {
            bubbles: true, detail: { name, arguments: args, respond } }));
          // 动作类没人调 respond 也要立刻收尾，否则 SDK 会一直挂着。
          if (!node.hasAttribute("blocking")) respond({ ok: true });
        }),
      });
    }
  }

  // 便于宿主脚本直接用：document.querySelector("mynah-avatar").ask("...")
  ability(a) { return this.pl.ability(a); }
  connect() { return this.pl.connect(); }
  ask(text) { return this.pl.ask(text); }
  say(text) { return this.pl.say(text); }
  interrupt() { return this.pl.interrupt(); }
  mic(on) { return this.pl.mic(on); }
}

// 只是个声明载体，本身不渲染。
class MynahAbility extends HTMLElement {
  connectedCallback() { this.style.display = "none"; }
}

if (!customElements.get("mynah-avatar")) {
  customElements.define("mynah-avatar", MynahAvatar);
}
if (!customElements.get("mynah-ability")) {
  customElements.define("mynah-ability", MynahAbility);
}
// Legacy tag names from the PersonaLive era keep working (same classes, subclassed
// because a class can only be registered under one name).
if (!customElements.get("personalive-avatar")) {
  customElements.define("personalive-avatar", class extends MynahAvatar {});
}
if (!customElements.get("personalive-ability")) {
  customElements.define("personalive-ability", class extends MynahAbility {});
}

export { MynahAvatar, MynahAbility };
