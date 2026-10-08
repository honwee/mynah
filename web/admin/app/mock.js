/* ============================================================
   Mynah 管理控制台 — Mock 后端
   仅当真实 admin API（/api/v1）不可达时由 api.js 自动启用。
   接口路径、响应结构与 docs/api/admin-api.md 完全对齐，
   正式联调时删除本文件 + dev proxy 即可。
   ============================================================ */
window.PLMock = (function () {
  "use strict";

  var rnd = function (a, b) { return a + Math.random() * (b - a); };
  var ri = function (a, b) { return Math.round(rnd(a, b)); };
  var iso = function (msAgo) { return new Date(Date.now() - msAgo).toISOString(); };
  var now = function () { return new Date().toISOString(); };
  var tw = function () { return window.__plTweaks || {}; };
  var scenario = function () { return tw().scenario || "normal"; };
  function hash(s) { var h = 7; for (var i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0; return h; }

  /* ---------- 检索测试用知识片段池 ---------- */
  var SNIPPETS = {
    "产品介绍.md": [
      { seq: 0, content: "Mynah是可私有化部署、可嵌入网页的实时数字人组件。访客通过浏览器即可与数字人进行语音或文字对话，端到端响应延迟低于 800ms，支持打断与连续多轮对话。" },
      { seq: 2, content: "知识库问答：管理员上传 txt/markdown 文档后，系统自动分块并向量化。对话时按相似度检索最相关的知识片段注入模型，使数字人能够准确回答产品、价格、政策等专有问题。" },
      { seq: 5, content: "典型应用场景：展厅导览讲解、官网售前接待、政务大厅咨询、企业内部知识助手。组件以 iframe 或 Web Component 方式嵌入，与现有系统零耦合。" }
    ],
    "企业版报价单.md": [
      { seq: 0, content: "企业版按 GPU 节点授权：单节点 6.8 万元/年，包含数字人形象定制 1 个、声音克隆 1 个与一年远程技术支持；3 节点及以上享 8 折，签约 3 年送 1 年。" },
      { seq: 1, content: "开源版永久免费，包含单路会话、知识库问答与管理控制台全部功能。企业版在此基础上提供多节点负载调度、多管理员权限、审计日志与形象/声音定制服务。" },
      { seq: 3, content: "增值服务单独计价：额外形象定制 1.2 万元/个，声音克隆 0.6 万元/个，私有化部署实施服务 1.5 万元/次（含环境调优与培训）。" }
    ],
    "部署运维手册.md": [
      { seq: 4, content: "最小部署仅需一台 8GB 显存的 GPU 服务器：运行 cored 与 worker 即可对外服务，不依赖 Postgres。需要知识库问答时，再部署 Postgres（含 pgvector）与 ollama 嵌入服务。" },
      { seq: 9, content: "健康检查：管理控制台仪表盘会并行探活 avatar / asr / tts / llm / ollama / postgres 六个依赖。任一组件 down 时整体状态为 degraded，对应能力自动降级，不影响其余功能。" },
      { seq: 11, content: "版本升级前请备份数据库：pg_dump mynah > backup.sql。升级后执行 cored --migrate 完成表结构迁移，再重启服务。配置三层合并：默认值 < 启动参数 < 控制台保存值。" }
    ],
    "常见问题FAQ.md": [
      { seq: 3, content: "数字人不回答知识库内容时，按顺序排查：① 配置中心的知识库总开关是否开启；② 文档状态是否为「就绪」；③ 文档所在库是否启用；④ 相似度阈值是否过高（中文问答建议 0.5，设 0.7 以上会漏掉正确答案）。" },
      { seq: 6, content: "修改人设 Prompt、更换对话模型、调整检索参数均为热生效，在线会话的下一轮对话即使用新配置，无需重启服务。仅「系统」组配置需要重启 cored。" }
    ]
  };

  /* ---------- 初始数据 ---------- */
  var docSeq = 40, kbSeq = 10, sessSeq = 100250;

  function seedDocs() {
    return {
      1: { id: 1, kb_id: 1, filename: "产品介绍.md", size_bytes: 8412, status: "ready", error: null, chunk_count: 12, created_at: iso(86400e3 * 6), updated_at: iso(86400e3 * 6) },
      2: { id: 2, kb_id: 1, filename: "企业版报价单.md", size_bytes: 3174, status: "ready", error: null, chunk_count: 5, created_at: iso(86400e3 * 4), updated_at: iso(86400e3 * 2) },
      3: { id: 3, kb_id: 1, filename: "部署运维手册.md", size_bytes: 18230, status: "ready", error: null, chunk_count: 23, created_at: iso(86400e3 * 3), updated_at: iso(86400e3 * 3) },
      4: { id: 4, kb_id: 1, filename: "旧版功能列表.txt", size_bytes: 2048, status: "failed", error: "invalid UTF-8 sequence at byte offset 1024 (file may be GBK encoded)", chunk_count: 0, created_at: iso(86400e3 * 1), updated_at: iso(86400e3 * 1) },
      5: { id: 5, kb_id: 2, filename: "常见问题FAQ.md", size_bytes: 6890, status: "ready", error: null, chunk_count: 9, created_at: iso(86400e3 * 2), updated_at: iso(86400e3 * 2) }
    };
  }
  function seedKbs() {
    return [
      { id: 1, name: "产品手册", description: "售前知识：产品功能、版本对比与报价", embed_model: "bge-m3", embed_dim: 1024, enabled: true, created_at: iso(86400e3 * 6), updated_at: iso(86400e3 * 1) },
      { id: 2, name: "常见问题", description: "运营整理的访客高频问题与标准答复", embed_model: "bge-m3", embed_dim: 1024, enabled: false, created_at: iso(86400e3 * 2), updated_at: iso(86400e3 * 2) }
    ];
  }
  function seedSessions(busy) {
    if (!busy) return [{ id: "100247", created_at: iso(8 * 60e3), turns: 5, speaking: false, voice: true }];
    var arr = [];
    var mins = [2, 5, 9, 14, 21, 33];
    for (var i = 0; i < 6; i++) {
      arr.push({ id: String(100241 + i), created_at: iso(mins[i] * 60e3), turns: ri(1, 18), speaking: false, voice: i % 3 !== 2 });
    }
    return arr;
  }

  function freshState() {
    return {
      password: "admin123",
      passwordChanged: false,
      expireNext: false,
      restartPending: false,
      kicked: {},
      config: {
        llm: {
          provider: "openai",
          base_url: "http://127.0.0.1:11434/v1",
          model: "qwen2.5:14b",
          api_key: "",
          bot_id: "",
          stream: true,
          system_prompt: "你是「小灵」，Mynah 数字人的售前顾问。\n\n性格：亲切、专业、不啰嗦。\n\n规则：\n1. 优先依据知识库资料回答产品功能、版本与报价问题；\n2. 资料中没有的信息，明确说明需要人工确认，绝不编造数字；\n3. 每次回答控制在三句话以内，便于语音播报；\n4. 访客表达购买意向时，引导其留下联系方式。"
        },
        tts: { base_url: "http://127.0.0.1:8061", voice: "zh_female_qingxin" },
        rag: { enabled: true, embed_url: "http://127.0.0.1:11434", embed_model: "bge-m3", embed_dim: 1024, chunk_size: 500, chunk_overlap: 50, top_k: 5, threshold: 0.5, timeout_ms: 2000, rerank_url: "http://127.0.0.1:9403", rerank_model: "BAAI/bge-reranker-v2-m3", rerank_api_key: "", rerank_candidates: 0 },
        system: { listen: ":8020", tls_listen: ":8443", admin_listen: "127.0.0.1:9080", worker_addr: "127.0.0.1:8030", asr_addr: "127.0.0.1:8050", video_codec: "vp8", idle_asset: "assets/idle.mp4", idle_silence: "30", web_dir: "./web", stun_urls: "stun:stun.l.google.com:19302" }
      },
      main: { kbs: seedKbs(), docs: seedDocs() },
      empty: { kbs: [], docs: {} },
      normalSessions: seedSessions(false),
      busySessions: seedSessions(true)
    };
  }
  var state = freshState();

  function ds() { return scenario() === "empty" ? state.empty : state.main; }
  function docsOf(kbId) {
    var d = ds().docs, out = [];
    for (var k in d) if (d[k].kb_id === kbId) out.push(d[k]);
    out.sort(function (a, b) { return b.created_at < a.created_at ? -1 : 1; });
    return out;
  }
  function kbView(kb) {
    var dc = docsOf(kb.id).length;
    return Object.assign({}, kb, { doc_count: dc });
  }

  /* ---------- 响应工具 ---------- */
  function ok(data) { return { status: 200, body: { code: 0, data: data } }; }
  function err(status, msg) { return { status: status, body: { code: status, msg: msg } }; }

  /* ---------- 摄取状态机 ---------- */
  function scheduleIngest(doc) {
    setTimeout(function () {
      if (doc.status !== "pending") return;
      doc.status = "processing"; doc.updated_at = now();
      setTimeout(function () {
        if (doc.status !== "processing") return;
        var degraded = scenario() === "degraded";
        var nameFail = /fail|失败|坏/.test(doc.filename);
        if (degraded || nameFail) {
          doc.status = "failed";
          doc.error = degraded
            ? 'embedding request failed: Post "http://127.0.0.1:11434/api/embed": connection refused (after 3 retries)'
            : "invalid UTF-8 sequence in document body (file may be GBK encoded)";
        } else {
          doc.status = "ready"; doc.error = null;
          var cs = Math.max(100, state.config.rag.chunk_size - state.config.rag.chunk_overlap);
          doc.chunk_count = Math.max(1, Math.round(doc.size_bytes / 3 / cs));
        }
        doc.updated_at = now();
      }, ri(1800, 3400));
    }, ri(600, 1200));
  }

  /* ---------- 健康 ---------- */
  function healthData() {
    var sc = scenario();
    function mk(st, base, error) {
      return { status: st, latency_ms: st === "ok" ? Math.round(base * rnd(0.8, 1.3)) : 0, error: error || null };
    }
    var c = {
      avatar: mk("ok", 12),
      asr: sc === "empty" ? mk("disabled", 0) : mk("ok", 26),
      tts: sc === "degraded" ? mk("down", 0, "dial tcp 127.0.0.1:8061: connect: connection refused") : mk("ok", 41),
      llm: mk("ok", 460),
      ollama: sc === "degraded" ? mk("down", 0, 'Post "http://127.0.0.1:11434/api/embed": context deadline exceeded (3s)') : mk("ok", 98),
      postgres: mk("ok", 3)
    };
    var degraded = Object.keys(c).some(function (k) { return c[k].status === "down"; });
    return { status: degraded ? "degraded" : "ok", components: c };
  }

  /* ---------- 会话 ---------- */
  function sessionList() {
    var sc = scenario();
    if (sc === "empty") return [];
    var pool = sc === "busy" ? state.busySessions : state.normalSessions;
    pool.forEach(function (s) {
      if (Math.random() < 0.35) s.speaking = !s.speaking;
      if (Math.random() < 0.3) s.turns += 1;
    });
    return pool.filter(function (s) { return !state.kicked[s.id]; });
  }

  /* ---------- 检索 ---------- */
  function searchTest(body) {
    var q = (body && body.query || "").trim();
    if (!q) return err(400, "query is required");
    var topK = body.top_k != null ? body.top_k : state.config.rag.top_k;
    var threshold = body.threshold != null ? body.threshold : state.config.rag.threshold;
    var pool = [];
    ds().kbs.filter(function (kb) { return kb.enabled; }).forEach(function (kb) {
      docsOf(kb.id).filter(function (d) { return d.status === "ready"; }).forEach(function (d) {
        var snips = SNIPPETS[d.filename] || [
          { seq: 0, content: "【演示片段】此片段来自您上传的「" + d.filename + "」。正式环境中将显示该文档分块后的真实内容。" }
        ];
        snips.forEach(function (s) { pool.push({ doc_id: d.id, filename: d.filename, seq: s.seq, content: s.content }); });
      });
    });
    // 按 query 哈希给出稳定但随查询变化的排序与分数
    pool.sort(function (a, b) { return (hash(q + b.filename + b.seq) % 1000) - (hash(q + a.filename + a.seq) % 1000); });
    var base = [0.79, 0.72, 0.66, 0.61, 0.56, 0.52, 0.49, 0.46];
    var chunks = pool.map(function (c, i) {
      var jitter = ((hash(q + i) % 100) / 100 - 0.5) * 0.05;
      var score = Math.max(0.3, Math.min(0.92, (base[i] != null ? base[i] : 0.42) + jitter));
      return Object.assign({}, c, { score: Math.round(score * 10000) / 10000 });
    }).filter(function (c) { return c.score >= threshold; });

    // 企业版精排：rerank_url 配置了才有精排客户端；rerank 缺省开启，可传 rerank:false 对比向量原序（与后端语义一致）
    var useRerank = !!state.config.rag.rerank_url && (body.rerank !== false);
    var reranked = false;
    if (useRerank && chunks.length > 1) {
      chunks.forEach(function (c, i) {
        // 交叉编码器分数分布与向量分无关：用另一个哈希生成，制造"序变化"演示效果
        var rs = 0.05 + (hash("rr" + q + c.filename + c.seq) % 900) / 1000;
        c.rerank_score = Math.round(rs * 10000) / 10000;
      });
      chunks.sort(function (a, b) { return b.rerank_score - a.rerank_score; });
      reranked = true;
    }
    chunks = chunks.slice(0, topK);
    return ok({ latency_ms: ri(38, 170) + (reranked ? ri(25, 60) : 0), reranked: reranked, chunks: chunks });
  }

  /* ---------- 对话调试 playground（无状态，复用检索逻辑生成注入片段） ---------- */
  function playgroundChat(body) {
    var hist = (body && body.history) || [];
    if (!hist.length || hist[hist.length - 1].role !== "user" || !hist[hist.length - 1].content) {
      return err(400, "history must end with a non-empty user message");
    }
    if (scenario() === "degraded") return err(502, "chat turn failed: llm status 502");
    var q = hist[hist.length - 1].content;
    var chunks = [];
    if (state.config.rag.enabled) {
      var r = searchTest({ query: q, rerank: true });
      if (r.status === 200) chunks = r.body.data.chunks;
    }
    var reply;
    if (chunks.length) {
      reply = "（演示回复）关于「" + q.slice(0, 24) + "」：根据知识库内容，" +
        chunks[0].content.replace(/^【[^】]*】/, "").slice(0, 80) + "…… 您还想了解哪方面？";
    } else {
      reply = "（演示回复）您问的是「" + q.slice(0, 24) + "」。当前未命中知识库，这条回答仅基于人设与模型本身。正式环境中将由配置的对话模型生成真实回复。";
    }
    return ok({ reply: reply, chunks: chunks, latency_ms: ri(600, 1800) });
  }

  /* ---------- 配置 ---------- */
  var CONFIG_FIELDS = {
    llm: ["provider", "base_url", "model", "api_key", "bot_id", "stream", "system_prompt"],
    tts: ["base_url", "voice"],
    rag: ["enabled", "embed_url", "embed_model", "embed_dim", "chunk_size", "chunk_overlap", "top_k", "threshold", "timeout_ms", "rerank_url", "rerank_model", "rerank_api_key", "rerank_candidates"],
    system: ["listen", "tls_listen", "admin_listen", "worker_addr", "asr_addr", "video_codec", "idle_asset", "idle_silence", "web_dir", "stun_urls"]
  };
  function putConfig(group, body) {
    if (!CONFIG_FIELDS[group]) return err(404, "unknown config group: " + group);
    for (var k in body) {
      if (CONFIG_FIELDS[group].indexOf(k) === -1) return err(400, 'unknown field "' + k + '" in group ' + group);
    }
    Object.assign(state.config[group], body);
    var applied = group === "system" ? "restart_required" : "hot";
    if (group === "system") state.restartPending = true;
    return ok({ applied: applied, config: Object.assign({}, state.config[group]) });
  }

  /* ---------- 二期增补：音色列表（PRD §8） ---------- */
  var VOICES = [
    { id: "zh_female_qingxin", name: "晴心 · 女声（普通话）" },
    { id: "zh_female_zhiyu", name: "知语 · 女声（温柔）" },
    { id: "zh_male_chenli", name: "辰离 · 男声（沉稳）" },
    { id: "zh_male_yangguang", name: "阳光 · 男声（活力）" },
    { id: "en_female_amy", name: "Amy · Female (English)" }
  ];

  /* ---------- 二期增补：文档分块预览（PRD §8） ---------- */
  var FILLERS = [
    "本段为演示内容：正式环境中此处将展示该文档按当前分块参数切分后的真实文本片段，供管理员核对分块边界是否合理。",
    "分块质量直接影响检索命中率：若一个完整的问答点被切在两块之间，可适当增大分块重叠后重新索引。",
    "提示：修改配置中心的 chunk_size / chunk_overlap 后，需对已有文档执行「重新索引」才会按新参数重新切分。",
    "每个片段在检索时独立参与相似度计算，命中后会随当轮对话临时注入模型，不进入会话历史。"
  ];
  function buildChunks(doc) {
    var snips = SNIPPETS[doc.filename] || [];
    var total = Math.min(doc.chunk_count, 50);
    var out = [];
    for (var i = 0; i < total; i++) {
      var known = null;
      for (var j = 0; j < snips.length; j++) if (snips[j].seq === i) known = snips[j];
      out.push({ seq: i, content: known ? known.content : "（演示）第 " + (i + 1) + " 块 · " + FILLERS[i % FILLERS.length] });
    }
    return out;
  }

  /* ---------- 路由 ---------- */
  function route(method, path, body, file, token) {
    var m, kb, doc, i;
    var d = ds();

    // ---- auth ----
    if (method === "POST" && path === "/auth/login") {
      if (!body || body.username !== "admin" || body.password !== state.password) return err(401, "invalid username or password");
      var mustChange = !!tw().forceChange && !state.passwordChanged;
      return ok({ token: "mock." + Math.random().toString(36).slice(2), must_change_password: mustChange });
    }
    // 统一鉴权
    if (state.expireNext) { state.expireNext = false; return err(401, "token expired"); }
    if (!token || token.indexOf("mock.") !== 0) return err(401, "missing or invalid token");

    if (method === "GET" && path === "/auth/me") return ok({ id: 1, username: "admin" });
    if (method === "POST" && path === "/auth/password") {
      if (!body || body.old_password !== state.password) return err(401, "old password incorrect");
      if (!body.new_password || body.new_password.length < 8) return err(400, "new password must be at least 8 characters");
      state.password = body.new_password; state.passwordChanged = true;
      return ok({ updated: true });
    }

    // ---- health ----
    if (method === "GET" && path === "/health") return ok(healthData());

    // ---- features ----
    if (method === "GET" && path === "/features") {
      return ok({ features: { rerank: true, training: true, motion: true } });
    }

    // ---- config ----
    if (method === "GET" && path === "/config") return ok(JSON.parse(JSON.stringify(state.config)));
    if ((m = path.match(/^\/config\/(\w+)$/))) {
      if (method === "GET") {
        if (!state.config[m[1]]) return err(404, "unknown config group");
        return ok(Object.assign({}, state.config[m[1]]));
      }
      if (method === "PUT") return putConfig(m[1], body || {});
    }

    // ---- kb ----
    if (path === "/kb" && method === "GET") return ok(d.kbs.map(kbView));
    if (path === "/kb" && method === "POST") {
      var name = (body && body.name || "").trim();
      if (!name) return err(400, "name is required");
      if (d.kbs.some(function (k) { return k.name === name; })) return err(409, "knowledge base name already exists");
      kb = { id: ++kbSeq, name: name, description: (body.description || "").trim(), embed_model: state.config.rag.embed_model, embed_dim: state.config.rag.embed_dim, enabled: true, created_at: now(), updated_at: now() };
      d.kbs.push(kb);
      return ok(kbView(kb));
    }
    if ((m = path.match(/^\/kb\/(\d+)$/))) {
      kb = d.kbs.find(function (k) { return k.id === +m[1]; });
      if (!kb) return err(404, "knowledge base not found");
      if (method === "PATCH") {
        if (body && body.name != null) {
          var nn = String(body.name).trim();
          if (!nn) return err(400, "name is required");
          if (d.kbs.some(function (k) { return k.name === nn && k.id !== kb.id; })) return err(409, "knowledge base name already exists");
          kb.name = nn;
        }
        if (body && body.description != null) kb.description = String(body.description).trim();
        if (body && body.enabled != null) kb.enabled = !!body.enabled;
        kb.updated_at = now();
        return ok(kbView(kb));
      }
      if (method === "DELETE") {
        d.kbs = d.kbs.filter(function (k) { return k.id !== kb.id; });
        if (scenario() === "empty") state.empty.kbs = d.kbs; else state.main.kbs = d.kbs;
        for (var id in d.docs) if (d.docs[id].kb_id === kb.id) delete d.docs[id];
        return ok({ deleted: true });
      }
    }

    // ---- documents ----
    if ((m = path.match(/^\/kb\/(\d+)\/documents$/))) {
      kb = d.kbs.find(function (k) { return k.id === +m[1]; });
      if (!kb) return err(404, "knowledge base not found");
      if (method === "GET") return ok(docsOf(kb.id));
      if (method === "POST") {
        if (!file) return err(400, "multipart field 'file' is required");
        if (file.size > 10 * 1024 * 1024) return err(413, "file exceeds 10MB limit");
        if (file.size === 0) return err(400, "file is empty");
        doc = { id: ++docSeq, kb_id: kb.id, filename: file.name, size_bytes: file.size, status: "pending", error: null, chunk_count: 0, created_at: now(), updated_at: now() };
        d.docs[doc.id] = doc;
        scheduleIngest(doc);
        return ok(Object.assign({}, doc));
      }
    }
    if ((m = path.match(/^\/documents\/(\d+)$/))) {
      doc = d.docs[+m[1]];
      if (!doc) return err(404, "document not found");
      if (method === "GET") return ok(Object.assign({}, doc));
      if (method === "DELETE") { delete d.docs[doc.id]; return ok({ deleted: true }); }
    }
    if ((m = path.match(/^\/documents\/(\d+)\/chunks$/)) && method === "GET") {
      doc = d.docs[+m[1]];
      if (!doc) return err(404, "document not found");
      if (doc.status !== "ready") return err(409, "document is not ready");
      return ok({ doc_id: doc.id, filename: doc.filename, chunk_count: doc.chunk_count, chunks: buildChunks(doc) });
    }
    if ((m = path.match(/^\/documents\/(\d+)\/reindex$/)) && method === "POST") {
      doc = d.docs[+m[1]];
      if (!doc) return err(404, "document not found");
      if (doc.status === "pending" || doc.status === "processing") return err(409, "document is already queued for indexing");
      doc.status = "processing"; doc.error = null; doc.updated_at = now();
      setTimeout(function () {
        if (doc.status !== "processing") return;
        if (scenario() === "degraded") {
          doc.status = "failed";
          doc.error = 'embedding request failed: Post "http://127.0.0.1:11434/api/embed": connection refused (after 3 retries)';
        } else {
          doc.status = "ready"; doc.error = null;
          var cs2 = Math.max(100, state.config.rag.chunk_size - state.config.rag.chunk_overlap);
          doc.chunk_count = Math.max(1, Math.round(doc.size_bytes / 3 / cs2));
        }
        doc.updated_at = now();
      }, ri(1500, 2800));
      return ok(Object.assign({}, doc));
    }

    // ---- search test ----
    if (method === "POST" && path === "/kb/search-test") return searchTest(body || {});
    if (method === "POST" && path === "/playground/chat") return playgroundChat(body || {});
    /* avatar playground：演示模式没有真实 WebRTC 后端，统一 503，
       前端会提示「当前部署未启用数字人调试」 */
    if (method === "POST" && path.indexOf("/playground/avatar/") === 0) {
      return err(503, "avatar playground not available in demo mode");
    }

    // ---- llm 测试连接 ----
    if (method === "POST" && path === "/config/llm/test") {
      var p = (body && body.provider) || "openai";
      if (p === "coze" && !(body && body.bot_id)) return err(400, "coze provider requires api_key and bot_id");
      if ((p === "dify" || p === "coze") && !(body && body.api_key)) return err(400, p + " provider requires api_key");
      if (scenario() === "degraded") return err(502, "服务地址不可达：检查 base_url、网络与防火墙。dial tcp: connection refused");
      return ok({ reply: "连接正常", latency_ms: ri(300, 1500) });
    }

    // ---- tts voices（二期增补） ----
    if (method === "GET" && path === "/tts/voices") return ok(VOICES.map(function (v) { return Object.assign({}, v); }));

    // ---- sessions ----
    if (method === "GET" && path === "/sessions") return ok(sessionList());
    if ((m = path.match(/^\/sessions\/(\w+)$/))) {
      var list = sessionList();
      var s = list.find(function (x) { return x.id === m[1]; });
      if (method === "GET") return s ? ok(s) : err(404, "session not found");
      if (method === "DELETE") {
        if (!s) return err(404, "session not found or already closed");
        state.kicked[s.id] = true;
        return ok({ kicked: true });
      }
    }

    // ---- channels (发布管理) ----
    if (path === "/channels" && method === "GET") return ok((state.channels || []).map(function (c) { return Object.assign({}, c); }));
    if (path === "/channels" && method === "POST") {
      state.channels = state.channels || [];
      var nc = {
        id: (state.chSeq = (state.chSeq || 0) + 1), slug: (body && body.slug) || "demo",
        name: (body && body.name) || "Demo", version: 1, enabled: true,
        access_mode: (body && body.access_mode) || "public",
        access_token: (body && body.access_mode) === "token" ? "demotoken" : "",
        domains: (body && body.domains) || [], cidrs: (body && body.cidrs) || [],
        max_concurrent: (body && body.max_concurrent) || 1, active: 0,
        created_at: new Date().toISOString(), updated_at: new Date().toISOString()
      };
      if ((state.channels || []).some(function (x) { return x.slug === nc.slug; })) return err(409, "duplicate slug");
      state.channels.push(nc);
      return ok(nc);
    }
    if ((m = path.match(/^\/channels\/(\d+)$/))) {
      var ch = (state.channels || []).find(function (x) { return x.id === +m[1]; });
      if (!ch) return err(404, "channel not found");
      if (method === "GET") return ok({ channel: ch, snapshot: { system_prompt: "(demo)", voice: "vivian", rag_enabled: false, greeting: "" } });
      if (method === "PATCH") { Object.assign(ch, body || {}); return ok({ updated: true }); }
      if (method === "DELETE") { state.channels = state.channels.filter(function (x) { return x.id !== ch.id; }); return ok({ deleted: true }); }
    }
    if ((m = path.match(/^\/channels\/(\d+)\/republish$/)) && method === "POST") {
      var rc = (state.channels || []).find(function (x) { return x.id === +m[1]; });
      if (!rc) return err(404, "channel not found");
      rc.version += 1; return ok({ republished: true, version: rc.version });
    }
    if ((m = path.match(/^\/channels\/(\d+)\/rotate-token$/)) && method === "POST") {
      var tc = (state.channels || []).find(function (x) { return x.id === +m[1]; });
      if (!tc) return err(404, "channel not found");
      tc.access_mode = "token"; tc.access_token = "tok" + ri(1000, 9999);
      return ok({ access_mode: "token", access_token: tc.access_token });
    }

    return err(404, "no such endpoint: " + method + " " + path);
  }

  /* ---------- 对外 ---------- */
  return {
    handle: function (method, path, body, file, token) {
      return new Promise(function (resolve, reject) {
        setTimeout(function () {
          if (scenario() === "down") { reject({ network: true, msg: "fetch failed" }); return; }
          try { resolve(route(method, path, body, file, token)); }
          catch (e) { resolve(err(500, "mock internal error: " + (e && e.message))); }
        }, ri(120, 380));
      });
    },
    expireSession: function () { state.expireNext = true; },
    reset: function () { state = freshState(); docSeq = 40; kbSeq = 10; }
  };
})();
