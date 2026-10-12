/* Mynah 管理控制台 — 对话调试（playground）
   与访客回合同一条 LLM+RAG 链路（无语音/数字人）；历史由前端持有、
   每轮全量提交，后端无状态，不产生会话、不影响在线访客。 */

function ChunkDrawer({ chunks }) {
  const [open, setOpen] = React.useState(false);
  if (!chunks || !chunks.length) return null;
  return (
    <div className="pg-chunks">
      <a href="#" onClick={(e) => { e.preventDefault(); setOpen(!open); }}>
        <Icon name="book" size={11} style={{ verticalAlign: "-1px", marginRight: 4 }} />
        {t("本轮注入了 {0} 条知识片段", chunks.length)} {open ? "▾" : "▸"}
      </a>
      {open ? chunks.map((c, i) => (
        <div className="card hit-card" key={i} style={{ marginTop: 8 }}>
          <div className="hit-head">
            {c.rerank_score != null ? <RerankBadge score={c.rerank_score} /> : null}
            <ScoreBadge score={c.score} />
            <span className="hit-src"><Icon name="file" size={11} style={{ verticalAlign: "-1px", marginRight: 4 }} />{c.filename} · {t("第 {0} 块", c.seq)}</span>
          </div>
          <div className="hit-content">{c.content}</div>
        </div>
      )) : null}
    </div>
  );
}

function fmtClock(ts) {
  if (!ts) return "";
  var d = new Date(ts);
  var p = (n) => (n < 10 ? "0" + n : "" + n);
  return p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds());
}

function PlaygroundChatPane({ navigate }) {
  const [messages, setMessages] = React.useState([]); // {role, content, ts, chunks?, latency_ms?, failed?}
  const [input, setInput] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [persona, setPersona] = React.useState(null); // 当前线上人设（只读预览）
  const [provider, setProvider] = React.useState(null); // llm 接入方式：openai/dify/coze
  const [ragOn, setRagOn] = React.useState(null);
  const msgsRef = React.useRef(null);
  const stickRef = React.useRef(true); // 用户是否贴在底部（贴底才自动滚）
  const toast = useToast();

  React.useEffect(() => {
    (async () => {
      try {
        const l = await PL.getConfigGroup("llm");
        setPersona(l.system_prompt || "");
        setProvider(l.provider || "openai");
      } catch (e) {}
      try { const r = await PL.getConfigGroup("rag"); setRagOn(!!r.enabled); } catch (e) {}
    })();
  }, []);

  /* 新消息自动滚到底；用户手动上滚后不强行拉回 */
  function onMsgsScroll() {
    const el = msgsRef.current;
    if (el) stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
  }
  React.useEffect(() => {
    const el = msgsRef.current;
    if (el && stickRef.current) el.scrollTop = el.scrollHeight;
  }, [messages, busy]);

  async function send(textOverride, baseOverride) {
    const text = (textOverride != null ? textOverride : input).trim();
    if (!text || busy) return;
    setInput("");
    const base = baseOverride != null ? baseOverride : messages;
    const next = base.filter((m) => !m.failed).concat([{ role: "user", content: text, ts: Date.now() }]);
    setMessages(next);
    setBusy(true);
    stickRef.current = true; // 自己发消息必定回到底部
    try {
      // 只带最近 10 轮（20 条），与访客会话的历史窗口一致
      const history = next.map((m) => ({ role: m.role, content: m.content })).slice(-20);
      // 流式：先挂一条 streaming 占位气泡，delta 逐段追加，result 落定元数据
      let acc = "";
      setMessages(next.concat([{ role: "assistant", content: "", streaming: true }]));
      const r = await PL.playgroundChatStream(history, (delta) => {
        acc += delta;
        const txt = acc;
        setMessages(next.concat([{ role: "assistant", content: txt, streaming: true }]));
      });
      setMessages(next.concat([{ role: "assistant", content: r.reply, chunks: r.chunks || [], latency_ms: r.latency_ms, ts: Date.now() }]));
    } catch (e) {
      setMessages(next.concat([{ role: "assistant", failed: true, content: PL.errText(e, { 502: "对话模型不可达，检查配置中心 → 对话模型" }), retryText: text }]));
    }
    setBusy(false);
  }

  const platformName = provider === "dify" ? "Dify" : provider === "coze" ? "Coze" : null;

  return (
    <div className="search-layout" data-screen-label="对话调试">
      <div>
        <div className="card card-pad">
          <div className="config-note">
            <h5>{t("说明")}</h5>
            <p>{t("这里走与访客对话完全相同的模型与知识库链路（不含语音和数字人画面），用来在改完人设或检索参数后立即验证效果。")}</p>
            <p className="mt8">{t("对话不会产生访客会话、不写入任何记录；刷新页面即清空。")}</p>
          </div>
        </div>
        <div className="card card-pad mt16">
          <div className="config-note">
            <h5>{t("当前生效配置")}</h5>
            <p>{t("知识库问答")}：{ragOn == null ? "…" : ragOn ? t("已开启") : t("已关闭")}</p>
            {platformName ? (
              /* dify/coze 托管模式：本地人设不生效，不展示预览 */
              <p className="mt8">{t("人设与知识库由 {0} 平台管理，本地人设与知识库配置不生效", platformName)}</p>
            ) : persona ? (
              <p className="mt8" style={{ whiteSpace: "pre-wrap", maxHeight: 120, overflow: "hidden", textOverflow: "ellipsis" }}>
                {t("人设 Prompt")}：{persona.length > 160 ? persona.slice(0, 160) + "…" : persona}
              </p>
            ) : persona === "" ? <p className="mt8">{t("人设 Prompt")}：{t("（默认人设）")}</p> : null}
            <p className="mt8"><a href="#" onClick={(e) => { e.preventDefault(); navigate("/config"); }}>{t("去配置中心修改")} →</a></p>
          </div>
        </div>
      </div>

      <div>
        <div className="card pg-chat">
          <div className="pg-msgs" ref={msgsRef} onScroll={onMsgsScroll}>
            {messages.length === 0 ? (
              <EmptyState icon="message" title={t("发一条消息试试")} desc={t("回复将引用当前线上的人设与知识库配置")} />
            ) : messages.map((m, i) => (
              m.role === "user" ? (
                <div className="pg-row user" key={i}><div className="pg-bubble user">{m.content}</div></div>
              ) : m.failed ? (
                <div className="pg-row" key={i}>
                  <div className="pg-bubble failed">
                    <Icon name="alert" size={12} style={{ verticalAlign: "-2px", marginRight: 5 }} />{m.content}
                    <a href="#" style={{ marginLeft: 8 }} onClick={(e) => { e.preventDefault(); send(m.retryText, messages.slice(0, i - 1)); }}>{t("重试")}</a>
                  </div>
                </div>
              ) : m.streaming ? (
                <div className="pg-row" key={i}>
                  <div className="pg-bubble">
                    {m.content ? <span>{m.content}<span className="pg-cursor"></span></span> : <span className="spinner"></span>}
                  </div>
                </div>
              ) : (
                <div className="pg-row" key={i}>
                  <div className="pg-bubble">
                    {m.content}
                    <div className="pg-meta">
                      {m.ts ? <span>{fmtClock(m.ts)} · </span> : null}
                      {t("耗时")} <span className="num">{m.latency_ms}</span> ms
                      {m.chunks && m.chunks.length ? null : ragOn ? <span style={{ marginLeft: 8 }}>· {t("未命中知识库")}</span> : null}
                    </div>
                    <ChunkDrawer chunks={m.chunks} />
                  </div>
                </div>
              )
            ))}
          </div>
          <form className="pg-input" onSubmit={(e) => { e.preventDefault(); send(); }}>
            <textarea className="textarea" rows={2} value={input} placeholder={t("模拟访客提问，Enter 发送，Shift+Enter 换行")}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); } }}></textarea>
            <div className="pg-input-actions">
              {messages.length ? <Btn kind="ghost" size="sm" type="button" onClick={() => setMessages([])}>{t("清空对话")}</Btn> : null}
              <Btn kind="primary" type="submit" loading={busy} icon="message">{t("发 送")}</Btn>
            </div>
          </form>
        </div>
      </div>
    </div>
  );
}

/* ---------- 绿幕抠像预览（与访客频道页同一套 WebGL shader） ----------
   操练场用：video 作纹理逐帧上屏，旋钮实时改 uniform；set() 热更参数、
   setBg() 换预览背景图。频道页拿保存的 avatar.chroma 参数做同样的合成。 */
function makeChromaPreview(canvas, video) {
  var gl = canvas.getContext("webgl", { premultipliedAlpha: false, alpha: true });
  if (!gl) return null;
  var vsrc = "attribute vec2 p;varying vec2 uv;void main(){uv=vec2((p.x+1.)/2.,1.-(p.y+1.)/2.);gl_Position=vec4(p,0.,1.);}";
  var fsrc = [
    "precision mediump float; varying vec2 uv; uniform sampler2D tex, bgTex;",
    "uniform vec3 keyRGB; uniform float similarity, smoothness, spill, hasBg;",
    "uniform vec2 bgScale, bgOffset;",
    "vec2 rgb2uv(vec3 c){ return vec2(c.r*-.169+c.g*-.331+c.b*.5+.5, c.r*.5+c.g*-.419+c.b*-.081+.5); }",
    "void main(){",
    "  vec4 c = texture2D(tex, uv);",
    "  float d = distance(rgb2uv(c.rgb), rgb2uv(keyRGB));",
    "  float alpha = smoothstep(similarity, similarity + smoothness, d);",
    "  float greenness = c.g - max(c.r, c.b);",
    "  alpha = mix(1., alpha, smoothstep(0., .08, greenness));",
    "  vec3 fg = vec3(c.r, mix(c.g, min(c.g, max(c.r, c.b)), spill), c.b);",
    "  vec3 bg = texture2D(bgTex, uv * bgScale + bgOffset).rgb;",
    "  gl_FragColor = mix(vec4(fg, 1.) * alpha, vec4(mix(bg, fg, alpha), 1.), hasBg);",
    "}"].join("\n");
  function sh(type, src) { var s = gl.createShader(type); gl.shaderSource(s, src); gl.compileShader(s); return s; }
  var prog = gl.createProgram();
  gl.attachShader(prog, sh(gl.VERTEX_SHADER, vsrc));
  gl.attachShader(prog, sh(gl.FRAGMENT_SHADER, fsrc));
  gl.linkProgram(prog); gl.useProgram(prog);
  var buf = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
  var loc = gl.getAttribLocation(prog, "p");
  gl.enableVertexAttribArray(loc); gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
  function makeTex(unit) {
    var t = gl.createTexture();
    gl.activeTexture(gl.TEXTURE0 + unit);
    gl.bindTexture(gl.TEXTURE_2D, t);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    return t;
  }
  makeTex(0); makeTex(1);
  gl.activeTexture(gl.TEXTURE1);
  gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, 1, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, new Uint8Array([0, 0, 0, 255]));
  gl.uniform1i(gl.getUniformLocation(prog, "tex"), 0);
  gl.uniform1i(gl.getUniformLocation(prog, "bgTex"), 1);
  var uHasBg = gl.getUniformLocation(prog, "hasBg");
  var uBgScale = gl.getUniformLocation(prog, "bgScale");
  var uBgOffset = gl.getUniformLocation(prog, "bgOffset");
  gl.uniform1f(uHasBg, 0); gl.uniform2f(uBgScale, 1, 1); gl.uniform2f(uBgOffset, 0, 0);
  var bgW = 0, bgH = 0;
  function set(ck) { // {key_color:"#00b140", similarity, smoothness, spill}
    var m = /^#([0-9a-f]{6})$/i.exec(ck.key_color || "");
    var rgb = m ? [parseInt(m[1].slice(0, 2), 16), parseInt(m[1].slice(2, 4), 16), parseInt(m[1].slice(4, 6), 16)] : [0, 177, 64];
    gl.uniform3f(gl.getUniformLocation(prog, "keyRGB"), rgb[0] / 255, rgb[1] / 255, rgb[2] / 255);
    gl.uniform1f(gl.getUniformLocation(prog, "similarity"), ck.similarity);
    gl.uniform1f(gl.getUniformLocation(prog, "smoothness"), ck.smoothness);
    gl.uniform1f(gl.getUniformLocation(prog, "spill"), ck.spill);
  }
  function setBg(url) {
    if (!url) {
      bgW = 0; bgH = 0;
      gl.uniform1f(uHasBg, 0);
      return;
    }
    var img = new Image();
    img.crossOrigin = "anonymous";
    img.onload = function () {
      try {
        gl.activeTexture(gl.TEXTURE1);
        gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, img);
        bgW = img.naturalWidth; bgH = img.naturalHeight;
        gl.uniform1f(uHasBg, 1);
      } catch (e) {}
    };
    img.src = url;
  }
  function fitBg() {
    if (!bgW || !canvas.width) return;
    var s = Math.max(canvas.width / bgW, canvas.height / bgH);
    var sx = canvas.width / (bgW * s), sy = canvas.height / (bgH * s);
    gl.uniform2f(uBgScale, sx, sy);
    gl.uniform2f(uBgOffset, (1 - sx) / 2, (1 - sy) / 2);
  }
  var raf = 0;
  function draw() {
    if (video.readyState >= 2 && video.videoWidth) {
      if (canvas.width !== video.videoWidth) { canvas.width = video.videoWidth; canvas.height = video.videoHeight; }
      gl.viewport(0, 0, canvas.width, canvas.height);
      fitBg();
      gl.activeTexture(gl.TEXTURE0);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, video);
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    }
    raf = requestAnimationFrame(draw);
  }
  raf = requestAnimationFrame(draw);
  return { set: set, setBg: setBg, stop: function () { cancelAnimationFrame(raf); } };
}

/* ---------- 数字人调试（WebRTC 实时画面） ----------
   经管理面 /playground/avatar/* 驱动一路真实访客管线会话：
   offer/answer 单次 HTTP 信令（非 trickle），DataChannel 收字幕
   {status: start|ing|end|user, text}。会话与访客互斥（worker 单会话）。 */
function PlaygroundAvatarPane() {
  const [conn, setConn] = React.useState("idle"); // idle|connecting|connected|failed
  const [sid, setSid] = React.useState(null);
  const [speaking, setSpeaking] = React.useState(false);
  const [mode, setMode] = React.useState("chat"); // chat|echo
  const [subOn, setSubOn] = React.useState(() => localStorage.getItem("pl_av_subtitle") !== "0");
  const [input, setInput] = React.useState("");
  const [messages, setMessages] = React.useState([]); // {role, content, ts, partial?}
  const [subtitle, setSubtitle] = React.useState("");
  const videoRef = React.useRef(null);
  const pcRef = React.useRef(null);
  const sidRef = React.useRef(null);
  const msgsRef = React.useRef(null);
  const dcRef = React.useRef(null);
  const toast = useToast();
  const [engine, setEngine] = React.useState("");
  const [mouth, setMouth] = React.useState(null);
  const [savingMouth, setSavingMouth] = React.useState(false);
  const mouthSendRef = React.useRef({ timer: null, latest: null });
  const [actions, setActions] = React.useState([]); // 动作编排：已烘焙的一次性动作片 id 列表
  const [actBusy, setActBusy] = React.useState(null); // 正在触发的动作 id
  const [chroma, setChroma] = React.useState(null); // 绿幕调试旋钮（avatar.chroma）
  const [chromaOn, setChromaOn] = React.useState(false); // 预览开关（仅本页，不入库）
  const [chromaBg, setChromaBg] = React.useState(""); // 预览背景图 URL（仅本页）
  const [savingChroma, setSavingChroma] = React.useState(false);
  const chromaCanvasRef = React.useRef(null);
  const chromaKeyerRef = React.useRef(null);

  React.useEffect(() => {
    let alive = true;
    PL.features().then((f) => { if (alive) setEngine((f && f.engine) || ""); }).catch(() => {});
    PL.avatarActions().then((a) => { if (alive) setActions(a || []); }).catch(() => {});
    PL.getConfigGroup("avatar").then((g) => {
      if (!alive) return;
      var m = (g && g.mouth) || {};
      setMouth({
        gain: m.gain != null ? m.gain : 1.30,
        smooth: m.smooth != null ? m.smooth : 0.34,
        sil_rms: m.sil_rms != null ? m.sil_rms : 0.012,
        voice_rms: m.voice_rms != null ? m.voice_rms : 0.030,
      });
      var ck = (g && g.chroma) || {};
      setChroma({
        key_color: ck.key_color || "#00b140",
        similarity: ck.similarity != null ? ck.similarity : 0.10,
        smoothness: ck.smoothness != null ? ck.smoothness : 0.08,
        spill: ck.spill != null ? ck.spill : 1.0,
      });
    }).catch(() => {});
    return () => { alive = false; };
  }, []);

  function liveSendMouth(m) {
    var sref = mouthSendRef.current;
    sref.latest = m;
    if (sref.timer) return;
    sref.timer = setTimeout(function () {
      sref.timer = null;
      var dc = dcRef.current;
      if (dc && dc.readyState === "open") {
        try { dc.send(JSON.stringify({ type: "mouth", gain: sref.latest.gain, smooth: sref.latest.smooth, sil_rms: sref.latest.sil_rms, voice_rms: sref.latest.voice_rms })); } catch (e) {}
      }
    }, 80);
  }

  function setKnob(k, v) {
    setMouth(function (prev) {
      var next = Object.assign({}, prev);
      next[k] = v;
      liveSendMouth(next);
      return next;
    });
  }

  async function saveMouth() {
    if (!mouth) return;
    setSavingMouth(true);
    try {
      await PL.putConfig("avatar", { mouth: mouth });
      toast.ok(t("已保存为默认，新会话生效"));
    } catch (e) { toast.error(PL.errText(e)); }
    setSavingMouth(false);
  }

  /* 绿幕预览生命周期：连接且开关打开时逐帧抠像上屏；关掉/断开即停 */
  React.useEffect(() => {
    if (conn === "connected" && chromaOn && chroma && chromaCanvasRef.current && videoRef.current) {
      var k = makeChromaPreview(chromaCanvasRef.current, videoRef.current);
      if (k) {
        k.set(chroma);
        if (chromaBg.trim()) k.setBg(chromaBg.trim());
        chromaKeyerRef.current = k;
      }
      return () => { if (chromaKeyerRef.current) { chromaKeyerRef.current.stop(); chromaKeyerRef.current = null; } };
    }
  }, [conn, chromaOn]);

  function setChromaKnob(k, v) {
    setChroma(function (prev) {
      var next = Object.assign({}, prev);
      next[k] = v;
      if (chromaKeyerRef.current) chromaKeyerRef.current.set(next);
      return next;
    });
  }

  function setChromaBgURL(v) {
    setChromaBg(v);
    if (chromaKeyerRef.current) chromaKeyerRef.current.setBg(v.trim());
  }

  async function saveChroma() {
    if (!chroma) return;
    setSavingChroma(true);
    try {
      await PL.putConfig("avatar", { chroma: chroma });
      toast.ok(t("已保存为默认，配了背景图的频道页刷新后生效"));
    } catch (e) { toast.error(PL.errText(e)); }
    setSavingChroma(false);
  }

  React.useEffect(() => () => { teardown(); }, []); // unmount 时断开

  // 从「形象与声音」hero 的「进入对话」跳来时自动连接（connect 为函数声明、已提升，可在此引用）
  React.useEffect(() => {
    let on = false;
    try { on = sessionStorage.getItem("pl_autoconnect") === "1"; if (on) sessionStorage.removeItem("pl_autoconnect"); } catch (e) {}
    if (on) connect();
  }, []);

  React.useEffect(() => {
    const el = msgsRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages]);

  function teardown() {
    if (pcRef.current) { try { pcRef.current.close(); } catch (e) {} pcRef.current = null; }
    dcRef.current = null;
    sidRef.current = null;
    setSid(null); setConn("idle"); setSpeaking(false); setSubtitle("");
  }

  /* DataChannel 字幕协议 reducer：start 挂占位，ing 逐句追加，end 落定 */
  const accRef = React.useRef("");
  function onDCMessage(raw) {
    var j; try { j = JSON.parse(raw); } catch (e) { return; }
    if (j.status === "start") {
      accRef.current = "";
      setSpeaking(true);
      setMessages((ms) => ms.concat([{ role: "assistant", content: "", partial: true }]));
    } else if (j.status === "ing" && j.text) {
      accRef.current += j.text;
      var txt = accRef.current;
      setSubtitle(j.text);
      setMessages((ms) => {
        var last = ms[ms.length - 1];
        if (last && last.partial) return ms.slice(0, -1).concat([{ role: "assistant", content: txt, partial: true }]);
        return ms.concat([{ role: "assistant", content: txt, partial: true }]);
      });
    } else if (j.status === "end") {
      setSpeaking(false);
      setSubtitle("");
      setMessages((ms) => {
        var last = ms[ms.length - 1];
        if (last && last.partial) {
          if (!last.content) return ms.slice(0, -1); // 空回合（如打断）直接收走占位
          return ms.slice(0, -1).concat([{ role: "assistant", content: last.content, ts: Date.now() }]);
        }
        return ms;
      });
    } else if (j.status === "user" && j.text) {
      /* 麦克风识别回显 */
      setMessages((ms) => ms.concat([{ role: "user", content: j.text, ts: Date.now() }]));
    }
  }

  async function connect() {
    if (conn === "connecting" || conn === "connected") return;
    setConn("connecting");
    setMessages([]);
    try {
      var pc = new RTCPeerConnection({ iceServers: [] }); // 同机部署常态；STUN 走线上 system 配置的访客页验证
      pcRef.current = pc;
      var dc = pc.createDataChannel("chat", { ordered: true });
      dcRef.current = dc;
      dc.onmessage = (ev) => onDCMessage(ev.data);
      pc.addTransceiver("video", { direction: "recvonly" });
      pc.addTransceiver("audio", { direction: "recvonly" });
      pc.ontrack = (ev) => {
        if (videoRef.current && ev.streams && ev.streams[0]) videoRef.current.srcObject = ev.streams[0];
      };
      pc.onconnectionstatechange = () => {
        if (!pcRef.current) return;
        var st = pc.connectionState;
        if (st === "connected") setConn("connected");
        else if (st === "failed" || st === "disconnected" || st === "closed") {
          if (sidRef.current) toast.warn(t("数字人连接已断开"));
          teardown();
        }
      };
      var offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      /* 非 trickle：等 ICE gathering 完成再发完整 SDP */
      if (pc.iceGatheringState !== "complete") {
        await new Promise((resolve) => {
          var tm = setTimeout(resolve, 2000);
          pc.onicegatheringstatechange = () => {
            if (pc.iceGatheringState === "complete") { clearTimeout(tm); resolve(); }
          };
        });
      }
      var r = await PL.avatarOffer(pc.localDescription.sdp);
      await pc.setRemoteDescription({ type: "answer", sdp: r.sdp });
      sidRef.current = r.sessionid;
      setSid(r.sessionid);
    } catch (e) {
      teardown();
      setConn("failed");
      toast.error(PL.errText(e, { 502: "数字人引擎不可达，确认 avatar worker 正在运行", 503: "当前部署未启用数字人调试" }));
    }
  }

  async function send() {
    var text = input.trim();
    if (!text || !sidRef.current) return;
    setInput("");
    setMessages((ms) => ms.concat([{ role: "user", content: text, ts: Date.now() }]));
    try {
      /* 在播回合自动先打断（livetalking 公开页的交互习惯） */
      await PL.avatarHuman({ sessionid: sidRef.current, text: text, type: mode, interrupt: true });
    } catch (e) {
      toast.error(PL.errText(e, { 404: "会话已结束，请重新连接" }));
    }
  }

  async function interrupt() {
    if (!sidRef.current) return;
    try { await PL.avatarInterrupt(sidRef.current); } catch (e) {}
  }

  /* 动作片名 → 展示文案（未知 id 原样显示） */
  var ACTION_LABELS = { wave: t("挥手"), nod: t("点头"), open: t("摊手"), point: t("指引") };

  async function playAction(id) {
    if (!sidRef.current || actBusy) return;
    setActBusy(id);
    try {
      await PL.avatarAction(sidRef.current, id);
    } catch (e) {
      toast.error(PL.errText(e, { 404: "会话已结束，请重新连接" }));
    }
    /* 动作片是一次性播放，不回报结束事件；按 3s 粗略解锁按钮 */
    setTimeout(function () { setActBusy(null); }, 3000);
  }

  function toggleSubtitle() {
    setSubOn((v) => {
      localStorage.setItem("pl_av_subtitle", v ? "0" : "1");
      return !v;
    });
  }

  var stateLabel = conn === "connected" ? (speaking ? t("播报中") : t("空闲")) :
    conn === "connecting" ? t("连接中…") : conn === "failed" ? t("连接失败") : t("未连接");
  var stateDot = conn === "connected" ? (speaking ? "warn" : "ok") : conn === "failed" ? "danger" : "muted";

  return (
    <div className="search-layout" data-screen-label="数字人调试">
      <div>
        <div className="card card-pad">
          <div className="config-note">
            <h5>{t("说明")}</h5>
            <p>{t("从控制台直连一路真实数字人会话（与访客完全相同的引擎/TTS/对话链路），验证形象、口型与音色效果。")}</p>
            <p className="mt8"><Icon name="alert" size={12} style={{ verticalAlign: "-2px", marginRight: 4 }} />{t("引擎当前为单会话：调试期间访客无法接入，测完请及时断开。")}</p>
          </div>
        </div>
        <div className="card card-pad mt16">
          <div className="config-note">
            <h5>{t("驱动模式")}</h5>
            <div className="seg mt8">
              <button className={"seg-item" + (mode === "chat" ? " active" : "")} onClick={() => setMode("chat")}>{t("对话")}</button>
              <button className={"seg-item" + (mode === "echo" ? " active" : "")} onClick={() => setMode("echo")}>{t("朗读")}</button>
            </div>
            <p className="mt8">{mode === "chat"
              ? t("文本经 LLM 与知识库生成回答后播报（访客对话链路）")
              : t("跳过 LLM，原文直接合成播报——用于单独验证音色与口型")}</p>
          </div>
        </div>
        {actions.length > 0 ? (
          <div className="card card-pad mt16">
            <div className="config-note">
              <h5>{t("动作调试")}</h5>
              <p className="mt8">{t("触发一次性动作片：待机时立即播放，播完自动回到待机循环；播报中会先让说话优先。")}</p>
            </div>
            <div className="mt8" style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
              {actions.map((a) => (
                <Btn key={a} size="sm" disabled={conn !== "connected" || !!actBusy} loading={actBusy === a}
                  onClick={() => playAction(a)}>{ACTION_LABELS[a] || a}</Btn>
              ))}
            </div>
            {conn !== "connected" ? <p className="small muted mt8">{t("连接数字人后可触发")}</p> : null}
          </div>
        ) : null}
        {chroma ? (
          <div className="card card-pad mt16">
            <div className="config-note">
              <h5>{t("绿幕调试")}</h5>
              <p className="mt8">{t("实时预览绿幕抠像效果并调节参数；保存为默认后，配了「舞台背景图」的频道访客页用同一组参数合成。")}</p>
            </div>
            <div className="mt8" style={{ display: "flex", alignItems: "center", gap: 10 }}>
              <label className="small" style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer" }}>
                <input type="checkbox" checked={chromaOn} onChange={(e) => setChromaOn(e.target.checked)} />
                {t("开启抠像预览")}
              </label>
              <input type="color" value={chroma.key_color} title={t("绿幕颜色")}
                style={{ width: 34, height: 24, padding: 0, border: "none", background: "none", cursor: "pointer" }}
                onChange={(e) => setChromaKnob("key_color", e.target.value)} />
              <span className="mono small" style={{ opacity: 0.6 }}>{chroma.key_color}</span>
            </div>
            {chromaOn ? (
              <div className="mt8">
                <Field label={t("预览背景图 URL")} help={t("仅用于本页预览；频道页用各自「舞台背景图」")}>
                  <input className="input" value={chromaBg} placeholder="https://…/background.jpg"
                    onChange={(e) => setChromaBgURL(e.target.value)} />
                </Field>
              </div>
            ) : null}
            <div className="mt8">
              <Field label={t("相似度")} help={t("低于此色度距离的像素被抠掉；太高会吃掉白衬衫/发丝等中性色")}>
                <Slider value={chroma.similarity} min={0} max={0.4} step={0.01} onChange={(v) => setChromaKnob("similarity", v)} format={(x) => Number(x).toFixed(2)} />
              </Field>
              <Field label={t("平滑度")} help={t("抠像边缘的过渡带宽度，越大边缘越柔")}>
                <Slider value={chroma.smoothness} min={0} max={0.3} step={0.01} onChange={(v) => setChromaKnob("smoothness", v)} format={(x) => Number(x).toFixed(2)} />
              </Field>
              <Field label={t("溢色抑制")} help={t("压掉人物边缘与画面里的绿色反光；1=全压（推荐），0=关")}>
                <Slider value={chroma.spill} min={0} max={1} step={0.05} onChange={(v) => setChromaKnob("spill", v)} format={(x) => Number(x).toFixed(2)} />
              </Field>
            </div>
            <div className="mt8" style={{ display: "flex", gap: 10, alignItems: "center" }}>
              <Btn kind="primary" size="sm" disabled={savingChroma} onClick={saveChroma}>{t("保存为默认")}</Btn>
              <span style={{ fontSize: 11, opacity: 0.55 }}>{conn === "connected" ? (chromaOn ? t("实时生效") : t("开启预览后实时调节")) : t("连接后可预览")}</span>
            </div>
          </div>
        ) : null}
        {engine.indexOf("wav2lipLS") >= 0 && mouth ? (
          <div className="card card-pad mt16">
            <div className="config-note">
              <h5>{t("数字人口型")} <span style={{ fontWeight: 400, opacity: 0.55 }}>{" "}wav2lip</span></h5>
              <p className="mt8">{t("拖动实时调节当前会话；保存为默认后所有新会话（含发布频道）生效。")}</p>
            </div>
            <div className="mt8">
              <Field label={t("口型大小")}><Slider value={mouth.gain} min={1.0} max={1.8} step={0.05} onChange={(v) => setKnob("gain", v)} format={(x) => Number(x).toFixed(2) + "x"} /></Field>
              <Field label={t("收合柔度")}><Slider value={mouth.smooth} min={0.1} max={0.6} step={0.02} onChange={(v) => setKnob("smooth", v)} format={(x) => Number(x).toFixed(2)} /></Field>
              <Field label={t("闭嘴阈值")}><Slider value={mouth.sil_rms} min={0.004} max={0.03} step={0.001} onChange={(v) => setKnob("sil_rms", v)} format={(x) => Number(x).toFixed(3)} /></Field>
              <Field label={t("张嘴阈值")}><Slider value={mouth.voice_rms} min={0.015} max={0.06} step={0.002} onChange={(v) => setKnob("voice_rms", v)} format={(x) => Number(x).toFixed(3)} /></Field>
            </div>
            <div className="mt8" style={{ display: "flex", gap: 10, alignItems: "center" }}>
              <Btn kind="primary" size="sm" disabled={savingMouth} onClick={saveMouth}>{t("保存为默认")}</Btn>
              <span style={{ fontSize: 11, opacity: 0.55 }}>{conn === "connected" ? t("实时生效") : t("连接后实时预览")}</span>
            </div>
          </div>
        ) : null}
      </div>

      <div>
        <div className="card pg-chat">
          <div className="av-stage">
            <video ref={videoRef} autoPlay playsInline className="av-video"
              style={chromaOn && conn === "connected" ? { position: "absolute", visibility: "hidden", pointerEvents: "none" } : null} />
            {chromaOn && conn === "connected" ? (
              /* 抠像预览画布：棋盘格底衬托透明区（无背景图时） */
              <canvas ref={chromaCanvasRef} className="av-video" style={{
                background: "repeating-conic-gradient(#2a2a2e 0% 25%, #1b1b1e 0% 50%) 0 0 / 24px 24px",
              }} />
            ) : null}
            {subOn && subtitle ? <div className="av-subtitle">{subtitle}</div> : null}
            {conn !== "connected" ? (
              <div className="av-overlay">
                {conn === "connecting" ? <span className="spinner"></span> : (
                  <Btn kind="primary" icon="play" onClick={connect}>{t("连接数字人")}</Btn>
                )}
              </div>
            ) : null}
            <div className="av-statusbar">
              <span className={"dot " + stateDot}></span>{stateLabel}
              {sid ? <span className="av-sid">#{sid}</span> : null}
              {conn === "connected" ? (
                <span className="av-actions">
                  <Btn kind="ghost" size="sm" onClick={toggleSubtitle}>{subOn ? t("字幕：开") : t("字幕：关")}</Btn>
                  {speaking ? <Btn kind="ghost" size="sm" onClick={interrupt}>{t("打断")}</Btn> : null}
                  <Btn kind="ghost" size="sm" onClick={teardown}>{t("断开")}</Btn>
                </span>
              ) : null}
            </div>
          </div>
          <div className="pg-msgs av-msgs" ref={msgsRef}>
            {messages.map((m, i) => (
              m.role === "user"
                ? <div className="pg-row user" key={i}><div className="pg-bubble user">{m.content}</div></div>
                : <div className="pg-row" key={i}><div className="pg-bubble">{m.content || <span className="spinner"></span>}{m.partial && m.content ? <span className="pg-cursor"></span> : null}</div></div>
            ))}
          </div>
          <form className="pg-input" onSubmit={(e) => { e.preventDefault(); send(); }}>
            <textarea className="textarea" rows={2} value={input}
              placeholder={conn === "connected" ? t("输入文本驱动数字人，Enter 发送") : t("先连接数字人")}
              disabled={conn !== "connected"}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); } }}></textarea>
            <div className="pg-input-actions">
              <Btn kind="primary" type="submit" disabled={conn !== "connected"} icon="message">{t("发 送")}</Btn>
            </div>
          </form>
        </div>
      </div>
    </div>
  );
}

function PlaygroundView({ navigate }) {
  // 从「形象与声音」hero 的「进入对话」跳来时带 pl_autoconnect → 直接落「数字人」tab，
  // 让 PlaygroundAvatarPane 挂载并自动连接（该 flag 由 avatar pane 的 effect 消费清除，这里只读不删）。
  const [tab, setTab] = React.useState(function () {
    try { return sessionStorage.getItem("pl_autoconnect") === "1" ? "avatar" : "chat"; } catch (e) { return "chat"; }
  });
  return (
    <div>
      <Tabs active={tab} onChange={setTab} items={[
        { key: "chat", label: t("文本对话") },
        { key: "avatar", label: t("数字人") },
      ]} />
      {tab === "chat" ? <PlaygroundChatPane navigate={navigate} /> : <PlaygroundAvatarPane />}
    </div>
  );
}

Object.assign(window, { PlaygroundView });
