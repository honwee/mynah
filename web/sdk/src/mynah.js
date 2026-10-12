// Mynah browser SDK — 把数字人当一个通用组件挂进任意宿主页面。
//
// 核心是「能力契约」：宿主页面声明自己会做什么，cored 只把模型发起的调用原样
// 转发回来，永远不知道宿主是 BI 大屏、后台系统还是游戏。所以这个 SDK 里没有一行
// 业务代码，接入方也不需要改 cored。
//
//   const pl = new Mynah({ endpoint: "https://host:8443", slug: "bi", token });
//   pl.ability({ name: "goto_page", description: "打开大屏上的某个页面",
//                parameters: {...}, run: (a) => store.goto(a.page) });
//   pl.mount("#avatar");
//   await pl.connect();
//
// 两类能力，行为不同：
//   - 动作类（默认）：cored 不等回执，「好的，我打开销售页」与切页同时发生。
//   - 查询类（blocking:true）：结果要进答案，cored 会先说 filler 垫场再等 run()
//     的返回值。run() 超过 6 秒没返回，模型会拿到 timeout 并照常把话说完。

import { chromaFromCfg, makeChromaKey } from "./chroma.js";

const OFFER_ERRORS = {
  401: ["需要访问令牌", "此频道受令牌保护，请使用包含密钥的完整分享链接。"],
  403: ["访问受限", "当前来源或网络不在该频道的允许范围内。"],
  404: ["频道不存在", "该频道不存在或已下线。"],
  429: ["当前访问人数较多", "频道已达并发上限，请稍后再试。"],
  503: ["数字人正忙", "当前所有数字人都在通话中，请稍后再试。"],
};

export class Mynah {
  /**
   * @param {object} opts
   * @param {string} [opts.endpoint] cored 基址，默认同源
   * @param {string} [opts.slug]     频道 slug；不给则走无频道的 /offer
   * @param {string} [opts.token]    频道访问令牌（access_mode=token 时必须）
   * @param {boolean}[opts.mic]      是否请求麦克风，默认 true
   * @param {boolean}[opts.chroma]   是否抠像成透明画布，默认 true
   * @param {string} [opts.bg]       背景图 URL；给了就在绿幕处铺背景而非透明
   */
  constructor(opts = {}) {
    this.endpoint = (opts.endpoint || "").replace(/\/$/, "");
    this.slug = opts.slug || "";
    this.token = opts.token || "";
    this.wantMic = opts.mic !== false;
    this.wantChroma = opts.chroma !== false;
    this.bg = opts.bg || "";

    this.abilities = new Map();
    this.handlers = new Map();
    this.cfg = null;
    this.sessionId = null;
    this.micOn = false;

    this._pc = null;
    this._dc = null;
    this._mount = null;
    this._video = null;
    this._canvas = null;
    this._keyer = null;
    this._micStream = null;
  }

  // ---- 能力 ----------------------------------------------------------------

  /**
   * 注册一个能力。可在 connect() 前后任意时刻调用；连上后会立即重新声明。
   * @param {object} a
   * @param {string} a.name        模型看到的函数名
   * @param {string} a.description 什么时候该调它 —— 模型只靠这句话判断，写清楚
   * @param {object} [a.parameters] JSON Schema；不给则视为无参
   * @param {boolean}[a.blocking]   true = 结果要进答案，cored 会等 run() 返回
   * @param {string} [a.filler]     blocking 时的垫场话，默认「我查一下。」
   * @param {Function} a.run        (args) => any | Promise<any>
   */
  ability(a) {
    if (!a || !a.name || typeof a.run !== "function") {
      throw new Error("ability() 需要 {name, run}");
    }
    this.abilities.set(a.name, a);
    if (this._dcOpen()) this._declare();
    return this;
  }

  removeAbility(name) {
    this.abilities.delete(name);
    if (this._dcOpen()) this._declare();
    return this;
  }

  // ---- 事件 ----------------------------------------------------------------

  /** on("subtitle"|"user"|"state"|"tool"|"connected"|"disconnected"|"error", cb) */
  on(event, cb) {
    if (!this.handlers.has(event)) this.handlers.set(event, []);
    this.handlers.get(event).push(cb);
    return this;
  }

  off(event, cb) {
    const list = this.handlers.get(event) || [];
    const i = list.indexOf(cb);
    if (i >= 0) list.splice(i, 1);
    return this;
  }

  _emit(event, payload) {
    for (const cb of this.handlers.get(event) || []) {
      // 一个订阅者抛错不该弄挂连接。
      try { cb(payload); } catch (e) { console.error("[mynah]", event, e); }
    }
  }

  // ---- 挂载 ----------------------------------------------------------------

  /** 把数字人画面挂进容器。抠像开启时画的是透明 canvas，可直接叠在大屏上。 */
  mount(target) {
    const el = typeof target === "string" ? document.querySelector(target) : target;
    if (!el) throw new Error("mount(): 找不到容器 " + target);
    this._mount = el;

    const video = document.createElement("video");
    video.autoplay = true;
    video.playsInline = true;
    video.muted = false;
    this._video = video;

    if (this.wantChroma) {
      // video 只当纹理源，不进 DOM 可见区（否则绿幕会露出来）。
      video.style.cssText = "position:absolute;width:1px;height:1px;opacity:0;pointer-events:none";
      const canvas = document.createElement("canvas");
      canvas.style.cssText = "width:100%;height:100%;display:block";
      this._canvas = canvas;
      el.appendChild(video);
      el.appendChild(canvas);
    } else {
      video.style.cssText = "width:100%;height:100%;display:block;object-fit:contain";
      el.appendChild(video);
    }
    return this;
  }

  // ---- 连接 ----------------------------------------------------------------

  async connect() {
    if (this._pc) return;
    if (this.slug) {
      try {
        const r = await fetch(`${this.endpoint}/channel/${encodeURIComponent(this.slug)}/config`);
        if (r.ok) this.cfg = await r.json();
      } catch (e) { /* 配置只影响抠像参数，拿不到就用默认值 */ }
    }

    const pc = new RTCPeerConnection({ iceServers: [] });
    this._pc = pc;
    const dc = pc.createDataChannel("chat", { ordered: true });
    this._dc = dc;
    dc.onopen = () => this._declare();
    dc.onmessage = (ev) => this._onMessage(ev.data);

    pc.addTransceiver("video", { direction: "recvonly" });
    // 麦克风必须在第一次 offer 里带上：offer 是一次一会话的，之后重协商拿到的
    // 是另一个数字人，不是麦。
    let mic = false;
    if (this.wantMic && navigator.mediaDevices?.getUserMedia) {
      try {
        const ms = await navigator.mediaDevices.getUserMedia({
          audio: { echoCancellation: true, noiseSuppression: true } });
        this._micStream = ms;
        ms.getTracks().forEach((t) => pc.addTrack(t, ms));
        mic = true;
      } catch (e) {
        this._emit("error", { kind: "mic", message: "麦克风不可用，已降级为文字输入", cause: e });
      }
    }
    if (!mic) pc.addTransceiver("audio", { direction: "recvonly" });
    this.micOn = mic;

    pc.ontrack = (ev) => {
      const stream = ev.streams && ev.streams[0];
      if (!stream || !this._video) return;
      if (this._video.srcObject !== stream) this._video.srcObject = stream;
      this._startKeyer();
    };
    pc.onconnectionstatechange = () => {
      const st = pc.connectionState;
      if (st === "connected") this._emit("connected", { mic: this.micOn, sessionId: this.sessionId });
      else if (st === "failed" || st === "closed" || st === "disconnected") this.disconnect();
    };

    await pc.setLocalDescription(await pc.createOffer());
    await waitIce(pc);

    const url = this.slug ? `${this.endpoint}/channel/offer` : `${this.endpoint}/offer`;
    const body = { sdp: pc.localDescription.sdp, type: "offer" };
    if (this.slug) { body.slug = this.slug; body.token = this.token; }

    const res = await fetch(url, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
    });
    if (!res.ok) {
      const [title, message] = OFFER_ERRORS[res.status] || ["无法建立连接", "请稍后重试。"];
      const err = new Error(message);
      err.status = res.status;
      err.title = title;
      this._emit("error", { kind: "offer", status: res.status, title, message });
      this.disconnect();
      throw err;
    }
    const ans = await res.json();
    this.sessionId = ans.sessionid;
    await pc.setRemoteDescription({ type: "answer", sdp: ans.sdp });
    return this;
  }

  disconnect() {
    if (this._keyer) { this._keyer.stop(); this._keyer = null; }
    if (this._micStream) { this._micStream.getTracks().forEach((t) => t.stop()); this._micStream = null; }
    try { this._pc?.close(); } catch (e) {}
    const was = !!this._pc;
    this._pc = null;
    this._dc = null;
    this.sessionId = null;
    this.micOn = false;
    if (this._video) this._video.srcObject = null;
    if (was) this._emit("disconnected", {});
    return this;
  }

  _startKeyer() {
    if (!this.wantChroma || this._keyer || !this._canvas) return;
    this._keyer = makeChromaKey(this._canvas, this._video, this.bg || this.cfg?.bg_image || "",
      chromaFromCfg(this.cfg));
    if (!this._keyer) {
      // 没有 WebGL：退回直接显示 video（带绿幕，但至少有画面）。
      this._canvas.remove();
      this._video.style.cssText = "width:100%;height:100%;display:block;object-fit:contain";
    }
  }

  // ---- 说话 / 打断 ---------------------------------------------------------

  /** 用文字问一句（走 LLM，会触发能力调用）。 */
  ask(text) { return this._human(text, "chat"); }

  /** 让数字人逐字念出这句话（不过 LLM）。 */
  say(text) { return this._human(text, "echo"); }

  interrupt() {
    if (!this.sessionId) return Promise.resolve();
    return fetch(`${this.endpoint}/interrupt_talk`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionid: this.sessionId }),
    });
  }

  /** 开关麦克风采集（轨道级静音，不重协商）。 */
  mic(on) {
    if (!this._micStream) return false;
    this._micStream.getAudioTracks().forEach((t) => { t.enabled = !!on; });
    this.micOn = !!on;
    return this.micOn;
  }

  _human(text, type) {
    if (!this.sessionId || !text) return Promise.resolve();
    return fetch(`${this.endpoint}/human`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text, type, interrupt: true, sessionid: this.sessionId }),
    });
  }

  // ---- DataChannel ---------------------------------------------------------

  _dcOpen() { return this._dc && this._dc.readyState === "open"; }

  _declare() {
    if (!this._dcOpen()) return;
    const abilities = [...this.abilities.values()].map(
      ({ name, description, parameters, blocking, filler }) =>
        ({ name, description, parameters, blocking, filler }));
    this._dc.send(JSON.stringify({ type: "abilities", abilities }));
  }

  async _onMessage(raw) {
    if (raw === "pong") return;
    let m;
    try { m = JSON.parse(raw); } catch (e) { return; }

    if (m.type === "tool_call") {
      await this._runAbility(m);
      return;
    }
    if (m.type === "event") { this._emit("state", m); return; }
    // 字幕协议：start=新一轮开始，ing=一句话，user=识别到的用户问话
    if (m.status === "start") { this._emit("subtitle", { text: "", start: true }); return; }
    if (m.status === "ing") { this._emit("subtitle", { text: m.text }); return; }
    if (m.status === "user") { this._emit("user", { text: m.text }); return; }
  }

  async _runAbility(m) {
    const a = this.abilities.get(m.name);
    this._emit("tool", { name: m.name, arguments: m.arguments, blocking: !!m.blocking });
    if (!a) return; // cored 已经替我们回了 "no such ability"
    try {
      const result = await a.run(m.arguments || {});
      // 动作类不需要回执 —— cored 早就把 ok 给模型了，这里再回也无人接收。
      if (m.blocking) this._reply({ type: "tool_result", id: m.id, result: result ?? { ok: true } });
    } catch (e) {
      this._emit("error", { kind: "ability", name: m.name, cause: e });
      if (m.blocking) this._reply({ type: "tool_result", id: m.id, error: String(e && e.message || e) });
    }
  }

  _reply(msg) {
    if (this._dcOpen()) this._dc.send(JSON.stringify(msg));
  }
}

function waitIce(pc) {
  if (pc.iceGatheringState === "complete") return Promise.resolve();
  return new Promise((resolve) => {
    const done = setTimeout(resolve, 2000); // 非 trickle，但别被慢主机拖死
    pc.onicegatheringstatechange = () => {
      if (pc.iceGatheringState === "complete") { clearTimeout(done); resolve(); }
    };
  });
}

export default Mynah;
