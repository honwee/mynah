/* Mynah SDK — Apache-2.0 — https://github.com/honwee/mynah */
var MynahSDK = (() => {
  var __defProp = Object.defineProperty;
  var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
  var __getOwnPropNames = Object.getOwnPropertyNames;
  var __hasOwnProp = Object.prototype.hasOwnProperty;
  var __export = (target, all) => {
    for (var name in all)
      __defProp(target, name, { get: all[name], enumerable: true });
  };
  var __copyProps = (to, from, except, desc) => {
    if (from && typeof from === "object" || typeof from === "function") {
      for (let key of __getOwnPropNames(from))
        if (!__hasOwnProp.call(to, key) && key !== except)
          __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
    }
    return to;
  };
  var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: true }), mod);

  // src/index.js
  var index_exports = {};
  __export(index_exports, {
    CHROMA_DEFAULTS: () => CHROMA_DEFAULTS,
    Mynah: () => Mynah,
    MynahAbility: () => MynahAbility,
    MynahAvatar: () => MynahAvatar,
    chromaFromCfg: () => chromaFromCfg,
    default: () => Mynah,
    makeChromaKey: () => makeChromaKey
  });

  // src/chroma.js
  var CHROMA_DEFAULTS = { key: [0, 177, 64], similarity: 0.1, smoothness: 0.08, spill: 1 };
  function chromaFromCfg(cfg) {
    const ck = cfg && cfg.chroma || {};
    const m = /^#([0-9a-f]{6})$/i.exec(ck.key_color || "");
    return {
      key: m ? [parseInt(m[1].slice(0, 2), 16), parseInt(m[1].slice(2, 4), 16), parseInt(m[1].slice(4, 6), 16)] : CHROMA_DEFAULTS.key,
      similarity: ck.similarity != null ? ck.similarity : CHROMA_DEFAULTS.similarity,
      smoothness: ck.smoothness != null ? ck.smoothness : CHROMA_DEFAULTS.smoothness,
      spill: ck.spill != null ? ck.spill : CHROMA_DEFAULTS.spill
    };
  }
  function makeChromaKey(canvas, video, bgURL, ck) {
    const gl = canvas.getContext("webgl", { premultipliedAlpha: false, alpha: true });
    if (!gl) return null;
    const vsrc = "attribute vec2 p;varying vec2 uv;void main(){uv=vec2((p.x+1.)/2.,1.-(p.y+1.)/2.);gl_Position=vec4(p,0.,1.);}";
    const fsrc = `
    precision mediump float; varying vec2 uv; uniform sampler2D tex, bgTex;
    uniform vec3 keyRGB; uniform float similarity, smoothness, spill, hasBg;
    uniform vec2 bgScale, bgOffset;
    vec2 rgb2uv(vec3 c){ return vec2(c.r*-.169+c.g*-.331+c.b*.5+.5, c.r*.5+c.g*-.419+c.b*-.081+.5); }
    void main(){
      vec4 c = texture2D(tex, uv);
      float d = distance(rgb2uv(c.rgb), rgb2uv(keyRGB));
      float alpha = smoothstep(similarity, similarity + smoothness, d);
      float greenness = c.g - max(c.r, c.b);
      alpha = mix(1., alpha, smoothstep(0., .08, greenness)); // 非绿像素保持不透明
      vec3 fg = vec3(c.r, mix(c.g, min(c.g, max(c.r, c.b)), spill), c.b);
      vec3 bg = texture2D(bgTex, uv * bgScale + bgOffset).rgb;
      gl_FragColor = mix(vec4(fg, 1.) * alpha, vec4(mix(bg, fg, alpha), 1.), hasBg);
    }`;
    const sh = (type, src) => {
      const s = gl.createShader(type);
      gl.shaderSource(s, src);
      gl.compileShader(s);
      return s;
    };
    const prog = gl.createProgram();
    gl.attachShader(prog, sh(gl.VERTEX_SHADER, vsrc));
    gl.attachShader(prog, sh(gl.FRAGMENT_SHADER, fsrc));
    gl.linkProgram(prog);
    gl.useProgram(prog);
    const buf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, buf);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
    const loc = gl.getAttribLocation(prog, "p");
    gl.enableVertexAttribArray(loc);
    gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
    function makeTex(unit) {
      const t = gl.createTexture();
      gl.activeTexture(gl.TEXTURE0 + unit);
      gl.bindTexture(gl.TEXTURE_2D, t);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
      return t;
    }
    makeTex(0);
    makeTex(1);
    gl.activeTexture(gl.TEXTURE1);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, 1, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, new Uint8Array([0, 0, 0, 255]));
    gl.uniform1i(gl.getUniformLocation(prog, "tex"), 0);
    gl.uniform1i(gl.getUniformLocation(prog, "bgTex"), 1);
    gl.uniform3f(gl.getUniformLocation(prog, "keyRGB"), ck.key[0] / 255, ck.key[1] / 255, ck.key[2] / 255);
    gl.uniform1f(gl.getUniformLocation(prog, "similarity"), ck.similarity);
    gl.uniform1f(gl.getUniformLocation(prog, "smoothness"), ck.smoothness);
    gl.uniform1f(gl.getUniformLocation(prog, "spill"), ck.spill);
    const uHasBg = gl.getUniformLocation(prog, "hasBg");
    const uBgScale = gl.getUniformLocation(prog, "bgScale");
    const uBgOffset = gl.getUniformLocation(prog, "bgOffset");
    gl.uniform1f(uHasBg, 0);
    gl.uniform2f(uBgScale, 1, 1);
    gl.uniform2f(uBgOffset, 0, 0);
    let bgW = 0, bgH = 0;
    if (bgURL) {
      const img = new Image();
      img.crossOrigin = "anonymous";
      img.onload = () => {
        try {
          gl.activeTexture(gl.TEXTURE1);
          gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, img);
          bgW = img.naturalWidth;
          bgH = img.naturalHeight;
          gl.uniform1f(uHasBg, 1);
        } catch (e) {
        }
      };
      img.src = bgURL;
    }
    function fitBg() {
      if (!bgW || !canvas.width) return;
      const s = Math.max(canvas.width / bgW, canvas.height / bgH);
      const sx = canvas.width / (bgW * s), sy = canvas.height / (bgH * s);
      gl.uniform2f(uBgScale, sx, sy);
      gl.uniform2f(uBgOffset, (1 - sx) / 2, (1 - sy) / 2);
    }
    let raf = 0;
    function draw() {
      if (video.readyState >= 2 && video.videoWidth) {
        if (canvas.width !== video.videoWidth) {
          canvas.width = video.videoWidth;
          canvas.height = video.videoHeight;
        }
        gl.viewport(0, 0, canvas.width, canvas.height);
        fitBg();
        gl.activeTexture(gl.TEXTURE0);
        gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, video);
        gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
      }
      raf = requestAnimationFrame(draw);
    }
    raf = requestAnimationFrame(draw);
    return { stop() {
      cancelAnimationFrame(raf);
    } };
  }

  // src/mynah.js
  var OFFER_ERRORS = {
    401: ["需要访问令牌", "此频道受令牌保护，请使用包含密钥的完整分享链接。"],
    403: ["访问受限", "当前来源或网络不在该频道的允许范围内。"],
    404: ["频道不存在", "该频道不存在或已下线。"],
    429: ["当前访问人数较多", "频道已达并发上限，请稍后再试。"],
    503: ["数字人正忙", "当前所有数字人都在通话中，请稍后再试。"]
  };
  var Mynah = class {
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
      this.abilities = /* @__PURE__ */ new Map();
      this.handlers = /* @__PURE__ */ new Map();
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
        try {
          cb(payload);
        } catch (e) {
          console.error("[mynah]", event, e);
        }
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
        } catch (e) {
        }
      }
      const pc = new RTCPeerConnection({ iceServers: [] });
      this._pc = pc;
      const dc = pc.createDataChannel("chat", { ordered: true });
      this._dc = dc;
      dc.onopen = () => this._declare();
      dc.onmessage = (ev) => this._onMessage(ev.data);
      pc.addTransceiver("video", { direction: "recvonly" });
      let mic = false;
      if (this.wantMic && navigator.mediaDevices?.getUserMedia) {
        try {
          const ms = await navigator.mediaDevices.getUserMedia({
            audio: { echoCancellation: true, noiseSuppression: true }
          });
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
      if (this.slug) {
        body.slug = this.slug;
        body.token = this.token;
      }
      const res = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body)
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
      if (this._keyer) {
        this._keyer.stop();
        this._keyer = null;
      }
      if (this._micStream) {
        this._micStream.getTracks().forEach((t) => t.stop());
        this._micStream = null;
      }
      try {
        this._pc?.close();
      } catch (e) {
      }
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
      this._keyer = makeChromaKey(
        this._canvas,
        this._video,
        this.bg || this.cfg?.bg_image || "",
        chromaFromCfg(this.cfg)
      );
      if (!this._keyer) {
        this._canvas.remove();
        this._video.style.cssText = "width:100%;height:100%;display:block;object-fit:contain";
      }
    }
    // ---- 说话 / 打断 ---------------------------------------------------------
    /** 用文字问一句（走 LLM，会触发能力调用）。 */
    ask(text) {
      return this._human(text, "chat");
    }
    /** 让数字人逐字念出这句话（不过 LLM）。 */
    say(text) {
      return this._human(text, "echo");
    }
    interrupt() {
      if (!this.sessionId) return Promise.resolve();
      return fetch(`${this.endpoint}/interrupt_talk`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sessionid: this.sessionId })
      });
    }
    /** 开关麦克风采集（轨道级静音，不重协商）。 */
    mic(on) {
      if (!this._micStream) return false;
      this._micStream.getAudioTracks().forEach((t) => {
        t.enabled = !!on;
      });
      this.micOn = !!on;
      return this.micOn;
    }
    _human(text, type) {
      if (!this.sessionId || !text) return Promise.resolve();
      return fetch(`${this.endpoint}/human`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text, type, interrupt: true, sessionid: this.sessionId })
      });
    }
    // ---- DataChannel ---------------------------------------------------------
    _dcOpen() {
      return this._dc && this._dc.readyState === "open";
    }
    _declare() {
      if (!this._dcOpen()) return;
      const abilities = [...this.abilities.values()].map(
        ({ name, description, parameters, blocking, filler }) => ({ name, description, parameters, blocking, filler })
      );
      this._dc.send(JSON.stringify({ type: "abilities", abilities }));
    }
    async _onMessage(raw) {
      if (raw === "pong") return;
      let m;
      try {
        m = JSON.parse(raw);
      } catch (e) {
        return;
      }
      if (m.type === "tool_call") {
        await this._runAbility(m);
        return;
      }
      if (m.type === "event") {
        this._emit("state", m);
        return;
      }
      if (m.status === "start") {
        this._emit("subtitle", { text: "", start: true });
        return;
      }
      if (m.status === "ing") {
        this._emit("subtitle", { text: m.text });
        return;
      }
      if (m.status === "user") {
        this._emit("user", { text: m.text });
        return;
      }
    }
    async _runAbility(m) {
      const a = this.abilities.get(m.name);
      this._emit("tool", { name: m.name, arguments: m.arguments, blocking: !!m.blocking });
      if (!a) return;
      try {
        const result = await a.run(m.arguments || {});
        if (m.blocking) this._reply({ type: "tool_result", id: m.id, result: result ?? { ok: true } });
      } catch (e) {
        this._emit("error", { kind: "ability", name: m.name, cause: e });
        if (m.blocking) this._reply({ type: "tool_result", id: m.id, error: String(e && e.message || e) });
      }
    }
    _reply(msg) {
      if (this._dcOpen()) this._dc.send(JSON.stringify(msg));
    }
  };
  function waitIce(pc) {
    if (pc.iceGatheringState === "complete") return Promise.resolve();
    return new Promise((resolve) => {
      const done = setTimeout(resolve, 2e3);
      pc.onicegatheringstatechange = () => {
        if (pc.iceGatheringState === "complete") {
          clearTimeout(done);
          resolve();
        }
      };
    });
  }

  // src/element.js
  var MynahAvatar = class extends HTMLElement {
    static get observedAttributes() {
      return ["endpoint", "slug", "token"];
    }
    connectedCallback() {
      if (this.pl) return;
      if (!this.style.display) this.style.display = "block";
      this.pl = new Mynah({
        endpoint: this.getAttribute("endpoint") || "",
        slug: this.getAttribute("slug") || "",
        token: this.getAttribute("token") || "",
        mic: !this.hasAttribute("no-mic"),
        chroma: !this.hasAttribute("no-chroma"),
        bg: this.getAttribute("bg") || ""
      });
      for (const ev of ["subtitle", "user", "state", "tool", "connected", "disconnected", "error"]) {
        this.pl.on(ev, (detail) => this.dispatchEvent(new CustomEvent(ev, { detail })));
      }
      const stage = document.createElement("div");
      stage.style.cssText = "width:100%;height:100%;position:relative";
      this.appendChild(stage);
      this.pl.mount(stage);
      this._adoptAbilities();
      this._mo = new MutationObserver(() => this._adoptAbilities());
      this._mo.observe(this, { childList: true });
      if (!this.hasAttribute("manual")) {
        this.pl.connect().catch(() => {
        });
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
        try {
          parameters = JSON.parse(node.getAttribute("parameters") || "null");
        } catch (e) {
          console.error("[mynah] ability %s 的 parameters 不是合法 JSON", name);
        }
        this.pl.ability({
          name,
          description: node.getAttribute("description") || "",
          parameters: parameters || void 0,
          blocking: node.hasAttribute("blocking"),
          filler: node.getAttribute("filler") || "",
          run: (args) => new Promise((resolve) => {
            let answered = false;
            const respond = (v) => {
              if (!answered) {
                answered = true;
                resolve(v);
              }
            };
            node.dispatchEvent(new CustomEvent("ability", {
              bubbles: true,
              detail: { name, arguments: args, respond }
            }));
            if (!node.hasAttribute("blocking")) respond({ ok: true });
          })
        });
      }
    }
    // 便于宿主脚本直接用：document.querySelector("mynah-avatar").ask("...")
    ability(a) {
      return this.pl.ability(a);
    }
    connect() {
      return this.pl.connect();
    }
    ask(text) {
      return this.pl.ask(text);
    }
    say(text) {
      return this.pl.say(text);
    }
    interrupt() {
      return this.pl.interrupt();
    }
    mic(on) {
      return this.pl.mic(on);
    }
  };
  var MynahAbility = class extends HTMLElement {
    connectedCallback() {
      this.style.display = "none";
    }
  };
  if (!customElements.get("mynah-avatar")) {
    customElements.define("mynah-avatar", MynahAvatar);
  }
  if (!customElements.get("mynah-ability")) {
    customElements.define("mynah-ability", MynahAbility);
  }
  if (!customElements.get("personalive-avatar")) {
    customElements.define("personalive-avatar", class extends MynahAvatar {
    });
  }
  if (!customElements.get("personalive-ability")) {
    customElements.define("personalive-ability", class extends MynahAbility {
    });
  }
  return __toCommonJS(index_exports);
})();
window.Mynah = MynahSDK.Mynah;
window.Mynah.makeChromaKey = MynahSDK.makeChromaKey;
window.Mynah.chromaFromCfg = MynahSDK.chromaFromCfg;
//# sourceMappingURL=mynah.js.map
