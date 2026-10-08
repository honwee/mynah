/* ============================================================
   Mynah 管理控制台 — API 客户端
   - 真实 fetch 调用 /api/v1（dev proxy / 同源托管均适用）
   - 启动时探测一次：不可达或响应非约定格式 → 自动切 Mock
   - 401 全局拦截（/auth/* 除外）→ 派发 pl:unauthorized
   ============================================================ */
window.PL = (function () {
  "use strict";

  var BASE = window.PL_API_BASE || "/api/v1";
  var mode = null;       // 'real' | 'mock'
  var probing = null;

  var tokenStore = {
    get: function () { return localStorage.getItem("pl_token"); },
    set: function (t) { t ? localStorage.setItem("pl_token", t) : localStorage.removeItem("pl_token"); }
  };

  function authHeaders() {
    var t = tokenStore.get();
    return t ? { Authorization: "Bearer " + t } : {};
  }

  function probe() {
    if (mode) return Promise.resolve(mode);
    if (probing) return probing;
    probing = (async function () {
      try {
        var ctrl = new AbortController();
        var tm = setTimeout(function () { ctrl.abort(); }, 1500);
        var res = await fetch(BASE + "/health", { headers: authHeaders(), signal: ctrl.signal });
        clearTimeout(tm);
        var j = await res.json();
        mode = (j && typeof j.code === "number") ? "real" : "mock";
      } catch (e) {
        mode = "mock";
      }
      try { document.dispatchEvent(new CustomEvent("pl:mode", { detail: mode })); } catch (e) {}
      return mode;
    })();
    return probing;
  }

  function fail(status, msg, network) {
    var e = new Error(msg || "request failed");
    e.status = status; e.msgRaw = msg || ""; e.network = !!network;
    return e;
  }

  async function request(method, path, opts) {
    opts = opts || {};
    await probe();
    var status, payload;

    if (mode === "mock") {
      var r;
      try {
        r = await window.PLMock.handle(method, path, opts.body, opts.file, tokenStore.get() || "");
      } catch (e2) {
        throw fail(0, "network error", true);
      }
      status = r.status; payload = r.body;
    } else {
      var headers = authHeaders();
      var fetchBody;
      if (opts.file) {
        var fd = new FormData();
        fd.append("file", opts.file);
        fetchBody = fd;
      } else if (opts.body !== undefined) {
        headers["Content-Type"] = "application/json";
        fetchBody = JSON.stringify(opts.body);
      }
      var res;
      try {
        res = await fetch(BASE + path, { method: method, headers: headers, body: fetchBody });
        payload = await res.json();
      } catch (e3) {
        throw fail(0, "network error", true);
      }
      status = res.status;
    }

    if (payload && payload.code === 0) return payload.data;
    var msg = (payload && payload.msg) || ("HTTP " + status);
    if (status === 401 && path.indexOf("/auth/") !== 0) {
      try { document.dispatchEvent(new CustomEvent("pl:unauthorized")); } catch (e4) {}
    }
    throw fail(status, msg);
  }

  /* 按场景把英文 msg 转成中文文案；msgRaw 放详情。文案经 window.t 做多语言 */
  function tt(s) { return (typeof window.t === "function") ? window.t(s) : s; }
  function errText(e, map) {
    if (e && e.network) return tt("无法连接管理服务，请确认 cored 正在运行");
    if (e && map && map[e.status]) return tt(map[e.status]);
    var def = { 400: "参数有误", 401: "认证失败", 404: "目标不存在", 409: "操作冲突", 413: "文件超过大小限制", 500: "服务端错误", 502: "依赖服务不可达" };
    return tt((e && def[e.status]) || "请求失败");
  }

  return {
    mode: function () { return mode; },
    probe: probe,
    token: tokenStore,
    errText: errText,
    // auth
    login: function (username, password) { return request("POST", "/auth/login", { body: { username: username, password: password } }); },
    me: function () { return request("GET", "/auth/me"); },
    changePassword: function (oldPw, newPw) { return request("POST", "/auth/password", { body: { old_password: oldPw, new_password: newPw } }); },
    // health
    health: function () { return request("GET", "/health"); },
    // features（版本与能力开关：edition + features.rerank/...）
    features: function () { return request("GET", "/features"); },
    // config
    getConfig: function () { return request("GET", "/config"); },
    getConfigGroup: function (g) { return request("GET", "/config/" + g); },
    putConfig: function (g, patch) { return request("PUT", "/config/" + g, { body: patch }); },
    llmTest: function (params) { return request("POST", "/config/llm/test", { body: params }); },
    // kb
    kbList: function () { return request("GET", "/kb"); },
    kbCreate: function (name, description) { return request("POST", "/kb", { body: { name: name, description: description } }); },
    kbPatch: function (id, patch) { return request("PATCH", "/kb/" + id, { body: patch }); },
    kbDelete: function (id) { return request("DELETE", "/kb/" + id); },
    // documents
    docList: function (kbId) { return request("GET", "/kb/" + kbId + "/documents"); },
    docUpload: function (kbId, file) { return request("POST", "/kb/" + kbId + "/documents", { file: file }); },
    docGet: function (id) { return request("GET", "/documents/" + id); },
    docDelete: function (id) { return request("DELETE", "/documents/" + id); },
    docReindex: function (id) { return request("POST", "/documents/" + id + "/reindex"); },
    docChunks: function (id) { return request("GET", "/documents/" + id + "/chunks"); },
    // tts（二期增补：音色列表）
    ttsVoices: function () { return request("GET", "/tts/voices"); },
    /* Qwen 云端音色试听：后台开一次性 realtime 会话读样例句 → audio/wav object URL（调用方 revoke）。
       每次试听都是一笔云端调用，别做成自动播放 */
    qwenVoicePreview: async function (voice, model, text) {
      await probe();
      if (mode !== "real") throw fail(0, "voice preview requires the live backend", true);
      var res;
      try {
        var headers = authHeaders();
        headers["Content-Type"] = "application/json";
        res = await fetch(BASE + "/config/brain/preview", { method: "POST", headers: headers, body: JSON.stringify({ voice: voice || "", model: model || "", text: text || "" }) });
      } catch (e) { throw fail(0, "network error", true); }
      if (res.status === 401) { try { document.dispatchEvent(new CustomEvent("pl:unauthorized")); } catch (e1) {} throw fail(401, "unauthorized"); }
      if (!res.ok) {
        var msg = "HTTP " + res.status;
        try { var j = await res.json(); if (j && j.msg) msg = j.msg; } catch (e2) {}
        throw fail(res.status, msg);
      }
      var blob = await res.blob();
      return URL.createObjectURL(blob);
    },
    // 音色克隆（EE：上传/删除/试听；上传与试听走自定义 fetch，避开通用 JSON 通道）
    voiceUpload: async function (fields) {
      await probe();
      if (mode !== "real") throw fail(0, "voice clone requires the live backend", true);
      var fd = new FormData();
      (fields.files || []).forEach(function (f) { fd.append("audio_sample", f); });
      fd.append("name", fields.name || "");
      fd.append("consent", fields.consent || "");
      if (fields.ref_text) fd.append("ref_text", fields.ref_text);
      if (fields.speaker_description) fd.append("speaker_description", fields.speaker_description);
      var res, payload;
      try {
        res = await fetch(BASE + "/tts/voices", { method: "POST", headers: authHeaders(), body: fd });
        payload = await res.json();
      } catch (e) { throw fail(0, "network error", true); }
      if (payload && payload.code === 0) return payload.data;
      if (res.status === 401) { try { document.dispatchEvent(new CustomEvent("pl:unauthorized")); } catch (e1) {} }
      throw fail(res.status, (payload && payload.msg) || ("HTTP " + res.status));
    },
    voiceDelete: function (name) { return request("DELETE", "/tts/voices/" + encodeURIComponent(name)); },
    /* 自动识别参考音频文字（ASR），用于预填克隆参考文本 */
    voiceTranscribe: async function (f) {
      await probe();
      if (mode !== "real") throw fail(0, "transcription requires the live backend", true);
      var fd = new FormData();
      fd.append("audio_sample", f);
      var res, payload;
      try {
        res = await fetch(BASE + "/tts/voices/transcribe", { method: "POST", headers: authHeaders(), body: fd });
        payload = await res.json();
      } catch (e) { throw fail(0, "network error", true); }
      if (payload && payload.code === 0) return payload.data;
      if (res.status === 401) { try { document.dispatchEvent(new CustomEvent("pl:unauthorized")); } catch (e1) {} }
      throw fail(res.status, (payload && payload.msg) || ("HTTP " + res.status));
    },
    /* 试听：返回可直接喂给 <audio> 的 object URL（调用方负责 revoke）
       opts 可选：{seed, instructions, speed} —— 换一版/风格指令/语速 */
    voicePreview: async function (text, voice, opts) {
      await probe();
      if (mode !== "real") throw fail(0, "voice preview requires the live backend", true);
      opts = opts || {};
      var res;
      try {
        var headers = authHeaders();
        headers["Content-Type"] = "application/json";
        var payload = { text: text, voice: voice };
        if (opts.seed !== undefined && opts.seed !== null) payload.seed = opts.seed;
        if (opts.instructions) payload.instructions = opts.instructions;
        if (opts.speed !== undefined && opts.speed !== null) payload.speed = opts.speed;
        res = await fetch(BASE + "/tts/voices/preview", { method: "POST", headers: headers, body: JSON.stringify(payload) });
      } catch (e) { throw fail(0, "network error", true); }
      if (res.status === 401) { try { document.dispatchEvent(new CustomEvent("pl:unauthorized")); } catch (e1) {} throw fail(401, "unauthorized"); }
      if (!res.ok) {
        var msg = "HTTP " + res.status;
        try { var j = await res.json(); if (j && j.msg) msg = j.msg; } catch (e2) {}
        throw fail(res.status, msg);
      }
      var blob = await res.blob();
      return URL.createObjectURL(blob);
    },
    // 形象训练（EE：上传真人视频→排队→轮询进度→设为当前形象）。上传走自定义 fetch（multipart），其余走通用 JSON 通道。
    trainAvatar: async function (fields) {
      await probe();
      if (mode !== "real") throw fail(0, "avatar training requires the live backend", true);
      var fd = new FormData();
      fd.append("video", fields.video);
      fd.append("name", fields.name || "");
      fd.append("consent", fields.consent || "");
      if (fields.motion_ref) fd.append("motion_ref", fields.motion_ref);
      var res, payload;
      try {
        res = await fetch(BASE + "/avatars/training", { method: "POST", headers: authHeaders(), body: fd });
        payload = await res.json();
      } catch (e) { throw fail(0, "network error", true); }
      if (payload && payload.code === 0) return payload.data;
      if (res.status === 401) { try { document.dispatchEvent(new CustomEvent("pl:unauthorized")); } catch (e1) {} }
      throw fail(res.status, (payload && payload.msg) || ("HTTP " + res.status));
    },
    // 形象目录（内置肖像 + 扫描到的烘焙形象，带 engine/baked 标记）
    avatars: function () { return request("GET", "/avatars"); },
    // 形象烘焙（上传说话视频 → 抽帧 → MuseTalk 素材目录 → 出现在形象目录）。
    // 与 trainAvatar 一样走 multipart；后端只烘焙前 seconds 秒（内存上限）。
    //
    // 名字带 MT 后缀不是啰嗦：本对象下方还有一组 bakeAvatar/listAvatarBakes/
    // getAvatarBake，那是 idle 循环烘焙（internal/avatarbake）。同名键在一个对象字面量里
    // 不报错、后者静默胜出，两组撞名的后果是这一组全部失效且没有任何提示。
    bakeAvatarMT: async function (fields) {
      await probe();
      if (mode !== "real") throw fail(0, "avatar baking requires the live backend", true);
      var fd = new FormData();
      fd.append("video", fields.video);
      fd.append("name", fields.name || "");
      if (fields.seconds != null) fd.append("seconds", String(fields.seconds));
      if (fields.bbox_shift != null) fd.append("bbox_shift", String(fields.bbox_shift));
      if (fields.mirror === false) fd.append("mirror", "false");
      var res, payload;
      try {
        res = await fetch(BASE + "/avatars/bake-mt", { method: "POST", headers: authHeaders(), body: fd });
        payload = await res.json();
      } catch (e) { throw fail(0, "network error", true); }
      if (payload && payload.code === 0) return payload.data;
      if (res.status === 401) { try { document.dispatchEvent(new CustomEvent("pl:unauthorized")); } catch (e1) {} }
      throw fail(res.status, (payload && payload.msg) || ("HTTP " + res.status));
    },
    listAvatarBakesMT: function () { return request("GET", "/avatars/bake-mt"); },
    getAvatarBakeMT: function (id) { return request("GET", "/avatars/bake-mt/" + id); },
    deleteAvatarBakeMT: function (id) { return request("DELETE", "/avatars/bake-mt/" + id); },
    // 绑定形象时预热：让 worker 提前加载，避免第一个访客等 15 秒黑屏
    avatarPreload: function (avatar) {
      return request("POST", "/avatars/preload", { body: { avatar: avatar } });
    },
    listTrainingJobs: function () { return request("GET", "/avatars/training"); },
    getTrainingJob: function (id) { return request("GET", "/avatars/training/" + id); },
    deleteTrainingJob: function (id) { return request("DELETE", "/avatars/training/" + id); },
    setTrainingActive: function (id) { return request("POST", "/avatars/training/" + id + "/active"); },
    // 动作骨架（EE：上传 driving 视频→抽取 LivePortrait 运动模板 pkl→指定给数字人）。上传走 multipart，同 trainAvatar。
    uploadMotion: async function (fields) {
      await probe();
      if (mode !== "real") throw fail(0, "motion extraction requires the live backend", true);
      var fd = new FormData();
      fd.append("video", fields.video);
      fd.append("name", fields.name || "");
      fd.append("consent", fields.consent || "");
      var res, payload;
      try {
        res = await fetch(BASE + "/motions", { method: "POST", headers: authHeaders(), body: fd });
        payload = await res.json();
      } catch (e) { throw fail(0, "network error", true); }
      if (payload && payload.code === 0) return payload.data;
      if (res.status === 401) { try { document.dispatchEvent(new CustomEvent("pl:unauthorized")); } catch (e1) {} }
      throw fail(res.status, (payload && payload.msg) || ("HTTP " + res.status));
    },
    listMotions: function () { return request("GET", "/motions"); },
    getMotion: function (id) { return request("GET", "/motions/" + id); },
    deleteMotion: function (id) { return request("DELETE", "/motions/" + id); },
    // 形象 idle 烘焙（Phase B 真烘焙：EE。烘 avatar+动作骨架→idle_<avatar>.h264f→热换 live idle）。
    bakeAvatar: function (avatarId, motionRef, apply) {
      return request("POST", "/avatars/bake", { body: { avatar_id: avatarId, motion_ref: motionRef, apply: apply !== false } });
    },
    listAvatarBakes: function () { return request("GET", "/avatars/bake"); },
    getAvatarBake: function (id) { return request("GET", "/avatars/bake/" + id); },
    cancelAvatarBake: function (id) { return request("DELETE", "/avatars/bake/" + id); },
    // search
    searchTest: function (params) { return request("POST", "/kb/search-test", { body: params }); },
    // playground（对话调试：无状态，历史由前端持有）
    playgroundChat: function (history) { return request("POST", "/playground/chat", { body: { history: history } }); },
    /* 流式版：onDelta 逐段收到回复文本，resolve 为完整 DebugChatResult。
       mock 模式（或后端无此端点的旧版本）自动退化为一次性返回 + 模拟打字。 */
    playgroundChatStream: async function (history, onDelta) {
      await probe();
      if (mode === "real") {
        var res;
        try {
          var headers = authHeaders();
          headers["Content-Type"] = "application/json";
          res = await fetch(BASE + "/playground/chat/stream", {
            method: "POST", headers: headers, body: JSON.stringify({ history: history })
          });
        } catch (e0) {
          throw fail(0, "network error", true);
        }
        if (res.status === 401) {
          try { document.dispatchEvent(new CustomEvent("pl:unauthorized")); } catch (e1) {}
          throw fail(401, "unauthorized");
        }
        var ctype = (res.headers.get("Content-Type") || "");
        if (res.ok && ctype.indexOf("text/event-stream") !== -1) {
          var reader = res.body.getReader();
          var dec = new TextDecoder();
          var buf = "", event = "", result = null, errMsg = null;
          function feedLine(line) {
            if (line.indexOf("event:") === 0) { event = line.slice(6).trim(); return; }
            if (line.indexOf("data:") !== 0) return;
            var data = line.slice(5).trim();
            if (!data) return;
            var j; try { j = JSON.parse(data); } catch (e2) { return; }
            if (event === "delta" && j.content) onDelta(j.content);
            else if (event === "result") result = j;
            else if (event === "error") errMsg = j.message || "stream error";
          }
          for (;;) {
            var r2 = await reader.read();
            if (r2.done) break;
            buf += dec.decode(r2.value, { stream: true });
            var nl;
            while ((nl = buf.indexOf("\n")) !== -1) {
              feedLine(buf.slice(0, nl).replace(/\r$/, ""));
              buf = buf.slice(nl + 1);
            }
          }
          if (errMsg) throw fail(502, errMsg);
          if (!result) throw fail(0, "stream ended without result", true);
          return result;
        }
        /* 非 SSE 响应（旧后端 404 等）：落回一次性端点 */
      }
      var full = await request("POST", "/playground/chat", { body: { history: history } });
      /* 模拟打字：把整段回复按 ~16 字符切片逐段回调 */
      var text = full.reply || "";
      for (var i = 0; i < text.length; i += 16) {
        onDelta(text.slice(i, i + 16));
        await new Promise(function (r3) { setTimeout(r3, 24); });
      }
      return full;
    },
    // sessions
    sessions: function () { return request("GET", "/sessions"); },

    // services（本地服务：TTS / ASR / 数字人引擎的容器生命周期）
    services: function () { return request("GET", "/services"); },
    serviceAction: function (name, action) {
      return request("POST", "/services/" + encodeURIComponent(name) + "/" + action);
    },
    // 引擎池容量：显存预算 + 各引擎能开几路（只读）
    avatarCapacity: function () { return request("GET", "/avatar/capacity"); },
    // 组合池：声明"我要的池长这样"（引擎 → 路数），后端算差集后创建/退役。
    // 缺省的引擎按 0 处理，所以这是全量而不是增量。
    avatarPoolCompose: function (workers) {
      return request("POST", "/avatar/pool", { workers: workers });
    },
    // 扩缩容：受显存容量（扩）与并发配额（缩）双向约束。
    // 混合池下一个数字说不清要改哪个引擎，后端会拒；用 avatarPoolCompose。
    avatarPoolResize: function (size) {
      return request("POST", "/avatar/pool/size", { size: size });
    },
    // 只跑一个引擎：目标引擎全量、其余归零，受素材就绪/显存容量/并发配额三重约束
    avatarEngineSwitch: function (engine, size) {
      return request("POST", "/avatar/engine", { engine: engine, size: size });
    },
    kick: function (id) { return request("DELETE", "/sessions/" + id); },
    // workers（数字人引擎池：每个 worker 单会话，session 非空 = 被占用）
    workers: function () { return request("GET", "/workers"); },
    // avatar playground（数字人调试：经管理面驱动一路真实访客管线会话）
    avatarOffer: function (sdp) { return request("POST", "/playground/avatar/offer", { body: { sdp: sdp } }); },
    avatarHuman: function (params) { return request("POST", "/playground/avatar/human", { body: params }); },
    avatarInterrupt: function (sid) { return request("POST", "/playground/avatar/interrupt", { body: { sessionid: sid } }); },
    avatarActions: function () { return request("GET", "/playground/avatar/actions"); },
    avatarAction: function (sid, action) { return request("POST", "/playground/avatar/action", { body: { sessionid: sid, action: action } }); },
    // channels（发布管理：把当前配置冻结发布成 /channel/:slug 访客频道）
    channelList: function () { return request("GET", "/channels"); },
    channelGet: function (id) { return request("GET", "/channels/" + id); },
    channelPublish: function (body) { return request("POST", "/channels", { body: body }); },
    channelPatch: function (id, patch) { return request("PATCH", "/channels/" + id, { body: patch }); },
    channelRepublish: function (id, body) { return request("POST", "/channels/" + id + "/republish", { body: body || {} }); },
    channelRotateToken: function (id) { return request("POST", "/channels/" + id + "/rotate-token", { body: {} }); },
    channelDelete: function (id) { return request("DELETE", "/channels/" + id); }
  };
})();
