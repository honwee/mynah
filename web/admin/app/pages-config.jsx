/* Mynah 管理控制台 — 配置中心 */

const CONFIG_GROUPS = [
  { key: "llm", label: "对话模型" },
  { key: "tts", label: "语音合成" },
  { key: "brain", label: "对话大脑" },
  { key: "turn", label: "语音轮次" },
  { key: "rag", label: "知识库" },
  { key: "system", label: "系统" },
];

const CONFIG_FORM_DEFS = {
  llm: {
    note: "对话模型决定数字人的语言能力与人设。全部字段热生效：保存后在线会话的下一轮对话即使用新配置。",
    fields: [], // 按 provider 动态生成，见 llmFields()
  },
  tts: {
    note: "语音合成服务配置，热生效。",
    fields: [
      { k: "base_url", label: "服务地址", type: "text" },
      { k: "voice", label: "音色", type: "voice", help: "音色列表为二期增补接口（PRD §8）；接口不可用时自动退化为文本输入。每个音色的种子在「形象与声音」按音色保存" },
    ],
  },
  turn: {
    note: "语音轮次策略决定「什么时候算你说完了、什么时候可以打断数字人」。保存后新建立的会话生效（正在通话中的会话保持原模式，刷新页面重连即可）。云端模式需要「对话大脑」已切到 Qwen 云端实时，否则自动回退本地模式。",
    fields: [], // 按 mode 动态生成，见 turnFields()
  },
  brain: {
    note: "对话大脑决定「谁来回答」：本地模式走「对话模型」+「语音合成」两级管线；Qwen 云端实时模式为端到端语音对话（语气语调直达云端模型，回复语音由云端合成，本地音色配置不生效）。保存后新建立的会话生效（通话中的会话保持原模式，刷新页面重连即可）。人设 Prompt 两种模式共用「对话模型 → 人设 Prompt」。",
    fields: [], // 按 mode 动态生成，见 brainFields()
  },
  rag: {
    note: "知识库检索参数，热生效。调整 top_k / 阈值前建议先在「知识库 → 检索测试」中验证效果。",
    fields: [
      { k: "enabled", label: "知识库问答总开关", type: "switch", help: "开启后对话将引用知识库内容（与仪表盘快捷开关同源）" },
      { k: "top_k", label: "top_k", type: "number", min: 1, max: 20, help: "每轮注入的最大知识片段数" },
      { k: "threshold", label: "相似度阈值", type: "slider", help: "低于此分数的片段不注入。默认 0.5，调高更严格但易漏" },
      { k: "timeout_ms", label: "检索超时（毫秒）", type: "number", min: 100, help: "超时自动放弃检索、正常回答，不阻塞对话" },
      { k: "chunk_size", label: "分块长度（字）", type: "number", min: 50, help: "改后需对已有文档「重新索引」才生效" },
      { k: "chunk_overlap", label: "分块重叠（字）", type: "number", min: 0 },
    ],
    /* 企业版精排（features.rerank 为 true 时显示，全部热生效） */
    rerank: [
      { k: "rerank_url", label: "精排服务地址", type: "text", placeholder: "http://127.0.0.1:9403", help: "交叉编码器精排服务地址（兼容 jina / cohere / siliconflow / TEI / vLLM 协议）。留空关闭精排，仅按向量相似度排序；修改即时生效，无需重启" },
      { k: "rerank_model", label: "精排模型", type: "text", placeholder: "BAAI/bge-reranker-v2-m3", help: "精排模型名，自建服务保持默认即可" },
      { k: "rerank_api_key", label: "精排 API Key", type: "password", help: "云端精排服务密钥，自建服务留空" },
      { k: "rerank_candidates", label: "精排候选数", type: "number", min: 0, max: 50, help: "送入精排的向量召回条数。0 = 自动（top_k×4，12~20 之间），通常无需修改" },
    ],
    rerankNote: "精排（rerank）为企业版能力：先按向量相似度召回候选，再用交叉编码器重排取 top_k，可显著提升命中质量。精排失败自动降级回向量序，不阻塞对话。",
    advanced: [
      { k: "embed_url", label: "向量服务地址", type: "text" },
      { k: "embed_model", label: "嵌入模型", type: "text" },
      { k: "embed_dim", label: "向量维度", type: "number" },
    ],
    advancedNote: "已有知识库锁定建库时的嵌入模型与维度，改动通常需要重建库。",
  },
  system: {
    note: "以下修改保存后需重启 cored 服务才会生效。",
    fields: [
      { k: "listen", label: "访客服务监听", type: "text" },
      { k: "tls_listen", label: "TLS 监听", type: "text" },
      { k: "admin_listen", label: "管理 API 监听", type: "text" },
      { k: "worker_addr", label: "Worker 地址", type: "text" },
      { k: "asr_addr", label: "ASR 服务地址", type: "text" },
      { k: "video_codec", label: "视频编码", type: "select", options: ["vp8", "h264"] },
      { k: "idle_asset", label: "待机视频路径", type: "text" },
      { k: "idle_silence", label: "待机静默阈值（秒）", type: "text" },
      { k: "web_dir", label: "静态资源目录", type: "text" },
      { k: "stun_urls", label: "STUN 服务器", type: "text" },
    ],
  },
};

/* ---- 语音轮次 mode：三选一 + 按模式显示对应旋钮 ---- */
const TURN_MODES = [
  { key: "local", label: "本地判轮次（默认）", desc: "本机 VAD + Smart Turn 模型判断说完没说完；打断判定也在本地，不依赖云端、所有链路可用。云端大脑下只有这一档能用知识库" },
  { key: "smart_turn", label: "云端·语义判停（Qwen）", desc: "Qwen 实时模型结合语义判断（「嗯」「呃」等停顿不会被误判为说完），响应通常更快；仅 Qwen 大脑模式生效。注意：此模式下知识库不生效" },
  { key: "server_vad", label: "云端·声学判停（Qwen）", desc: "Qwen 服务端按静音时长判停，可调阈值；仅 Qwen 大脑模式生效。注意：此模式下知识库不生效" },
];

function turnFields(mode) {
  if (mode === "server_vad") {
    return [
      { k: "vad_silence_ms", label: "尾静音判停（毫秒）", type: "number", min: 0, max: 3000, help: "静音持续多久算说完。0 = 服务端默认（800ms）；可压到 400~500 换更快响应，代价是长停顿容易被切断" },
      { k: "vad_threshold", label: "人声检测阈值", type: "slider", help: "0 = 服务端默认（0.5）。嘈杂环境调高，安静环境轻声说话调低" },
    ];
  }
  if (mode === "smart_turn") {
    return []; // 语义模式由模型自行判断，无可调项
  }
  return [
    { k: "grace_ms", label: "犹豫等待（毫秒）", type: "number", min: 0, max: 3000, help: "模型认为你可能还没说完时，多等这么久再回答（等你把下半句说出来）。默认 700，调低响应更快" },
    { k: "min_interrupt_ms", label: "声学打断（毫秒）", type: "number", min: -1, max: 3000, help: "数字人说话时，你持续开口超过这个时长就立即打断（不等识别出文字）。0 = 沿用启动参数，-1 = 关闭（只按识别文字判断打断）" },
  ];
}

/* ---- 对话大脑 mode：二选一 + qwen 模式显示模型/音色 ---- */
const BRAIN_MODES = [
  { key: "local", label: "本地管线（默认）", desc: "「对话模型」生成文字 → 「语音合成」出声。全链路自控，支持知识库检索与本地音色" },
  { key: "qwen", label: "Qwen 云端实时", desc: "端到端语音对话：你的语音（含语气）直达云端模型，回复语音由云端实时合成。响应更快更自然；需要启动时已配置 DashScope 凭据。知识库需配合「本地判轮次」使用" },
];

function brainFields(mode) {
  if (mode === "qwen") {
    return [
      { k: "model", label: "实时模型", type: "text", placeholder: "qwen-audio-3.0-realtime-flash", help: "DashScope 实时语音模型 id，通常保持默认" },
      { k: "voice", label: "云端音色", type: "qwenvoice", help: "Qwen 系统音色（5 款）或声音复刻 voice_id。本地「语音合成」的音色配置在此模式下不生效" },
    ];
  }
  return [];
}

const LLM_PROVIDERS = [
  { key: "openai", label: "OpenAI 兼容", desc: "ollama / vLLM / FastGPT / RAGFlow / OneAPI / 各家云端" },
  { key: "dify", label: "Dify", desc: "Dify 应用（云端或私有化部署）" },
  { key: "coze", label: "Coze（扣子）", desc: "扣子智能体" },
];

/* 各 provider 的表单字段。必填项尽量只留 base_url + api_key；
   其余给默认值或折叠，帮助文案教会用户去哪里复制。 */
function llmFields(provider) {
  if (provider === "dify") {
    return [
      { k: "base_url", label: "服务地址", type: "text", placeholder: "https://api.dify.ai 或私有化部署地址", help: "Dify 平台地址；私有化部署填你们自己的域名" },
      { k: "api_key", label: "API Key", type: "password", help: "在 Dify 应用 → 访问 API 页复制（app- 开头）。Key 已绑定具体应用，人设与知识库都在 Dify 侧配置" },
      { k: "stream", label: "流式回复", type: "switch", help: "开启时边生成边播报，首句响应最快（推荐）；关闭则等完整回答后再合成语音" },
    ];
  }
  if (provider === "coze") {
    return [
      { k: "base_url", label: "服务地址", type: "text", placeholder: "https://api.coze.cn（默认，可留空）", help: "国内版留空即可；海外版填 https://api.coze.com" },
      { k: "api_key", label: "API Key", type: "password", help: "在扣子 → API 管理中创建个人访问令牌" },
      { k: "bot_id", label: "Bot ID", type: "text", placeholder: "73428668*****", help: "打开你的 bot 编排页，浏览器地址栏 bot/ 后面的数字串" },
    ];
  }
  return [
    { k: "base_url", label: "服务地址", type: "text", placeholder: "http://127.0.0.1:11434", help: "OpenAI 兼容服务地址（ollama / vLLM / FastGPT / RAGFlow / 云端均可）" },
    { k: "api_key", label: "API Key", type: "password", help: "云端服务密钥，本地 ollama 留空" },
    { k: "model", label: "模型名", type: "text" },
    { k: "stream", label: "流式回复", type: "switch", help: "开启时边生成边播报，首句响应最快（推荐）；个别不支持流式输出的兼容服务才需要关闭" },
    { k: "system_prompt", label: "人设 Prompt", type: "textarea", rows: 12, help: "数字人人设。修改后立即生效，无需重启" },
  ];
}

/* 测试连接按钮：用表单当前值（含未保存改动）发一轮真实对话 */
function LLMTestButton({ draft }) {
  const [busy, setBusy] = React.useState(false);
  const [result, setResult] = React.useState(null); // {ok, text}
  async function test() {
    setBusy(true); setResult(null);
    try {
      const r = await PL.llmTest({
        provider: draft.provider || "openai", base_url: draft.base_url || "",
        model: draft.model || "", api_key: draft.api_key || "", bot_id: draft.bot_id || "",
        stream: draft.stream !== false,
      });
      setResult({ ok: true, text: t("连接正常（{0} ms）", r.latency_ms) });
    } catch (e) {
      setResult({ ok: false, text: e.msgRaw || PL.errText(e) });
    }
    setBusy(false);
  }
  return (
    <div className="field">
      <div className="row" style={{ gap: 10 }}>
        <Btn kind="secondary" size="sm" type="button" loading={busy} icon="zap" onClick={test}>{t("测试连接")}</Btn>
        {result ? (
          <span className={"small"} style={{ color: result.ok ? "var(--ok)" : "var(--danger)" }}>
            <Icon name={result.ok ? "check" : "alert"} size={12} style={{ verticalAlign: "-2px", marginRight: 4 }} />{result.text}
          </span>
        ) : null}
      </div>
      <div className="field-help">{t("用上面填写的值（无需先保存）发一轮真实对话验证连通性")}</div>
    </div>
  );
}

/* 试听按钮（语音合成组）：用表单当前选择的音色（含未保存改动）直接合成一句。
   种子取该音色在 voice_seeds 里存的值、风格指令/语速取当前配置值 —— 与生产
   Speak 同参，所听即线上效果。深度调音（种子/风格/语速）去「形象与声音」。 */
function TTSPreviewButton({ draft }) {
  const [sample, setSample] = React.useState(t("你好，我是你的智能助手，很高兴为你服务。"));
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState(null);
  const audioRef = React.useRef(null);
  const urlRef = React.useRef(null);
  React.useEffect(() => () => { if (urlRef.current) URL.revokeObjectURL(urlRef.current); }, []);
  async function play() {
    if (!sample.trim() || !draft.voice) return;
    setBusy(true); setErr(null);
    try {
      const seeds = draft.voice_seeds || {};
      const url = await PL.voicePreview(sample.trim(), draft.voice, {
        seed: seeds[draft.voice] || 0,
        instructions: (draft.instructions || "").trim(),
        speed: draft.speed > 0 ? draft.speed : 1,
      });
      if (urlRef.current) URL.revokeObjectURL(urlRef.current);
      urlRef.current = url;
      if (!audioRef.current) audioRef.current = new Audio();
      audioRef.current.src = url;
      audioRef.current.onended = () => setBusy(false);
      await audioRef.current.play();
    } catch (e) {
      setErr(e.msgRaw || PL.errText(e, { 404: t("当前构建不含试听接口") }));
      setBusy(false);
    }
  }
  return (
    <div className="field">
      <div className="row" style={{ gap: 10 }}>
        <input className="input grow" style={{ minWidth: 200, maxWidth: 420 }} value={sample} maxLength={200}
          onChange={(e) => setSample(e.target.value)} placeholder={t("试听文本")} />
        <Btn kind="secondary" size="sm" type="button" loading={busy} disabled={busy || !draft.voice} icon="play" onClick={play}>{t("试 听")}</Btn>
      </div>
      {err ? (
        <div className="small" style={{ color: "var(--danger)", marginTop: 6 }}>
          <Icon name="alert" size={12} style={{ verticalAlign: "-2px", marginRight: 4 }} />{err}
        </div>
      ) : null}
      <div className="field-help">{t("用上面选的音色（无需先保存）+ 已保存的种子/风格/语速合成，所听即线上效果；深度调音去「形象与声音 → 音色」")}</div>
    </div>
  );
}

/* 音色下拉（二期增补接口 GET /tts/voices；404/失败时退化为文本框） */
function VoiceSelect({ value, onChange }) {
  const [voices, setVoices] = React.useState(null);   // null=加载中, []=不可用
  React.useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const v = await PL.ttsVoices();
        if (alive) setVoices(Array.isArray(v) && v.length ? v : []);
      } catch (e) { if (alive) setVoices([]); }
    })();
    return () => { alive = false; };
  }, []);

  if (voices === null) {
    return <div className="row" style={{ height: 36 }}><span className="spinner"></span><span className="muted small">{t("正在获取音色列表…")}</span></div>;
  }
  if (!voices.length) {
    return (
      <div>
        <input className="input" value={value == null ? "" : value} onChange={(e) => onChange(e.target.value)} />
        <div className="field-help">{t("音色列表接口暂不可用，请按 TTS 服务文档手动填写音色标识")}</div>
      </div>
    );
  }
  const known = voices.some((v) => v.id === value);
  return (
    <select className="select" style={{ maxWidth: 320 }} value={known ? value : "__custom"} onChange={(e) => { if (e.target.value !== "__custom") onChange(e.target.value); }}>
      {!known ? <option value="__custom">{value ? t("自定义：{0}", value) : t("（未选择）")}</option> : null}
      {voices.map((v) => <option key={v.id} value={v.id}>{v.name}（{v.id}）</option>)}
    </select>
  );
}

function ConfigField({ def, value, onChange }) {
  if (def.type === "switch") {
    return (
      <div className="field">
        <div className="row" style={{ gap: 12 }}>
          <Switch checked={!!value} onChange={onChange} />
          <label className="field-label" style={{ margin: 0 }}>{t(def.label)}</label>
        </div>
        {def.help ? <div className="field-help">{t(def.help)}</div> : null}
      </div>
    );
  }
  if (def.type === "voice") {
    return (
      <Field label={t(def.label)} help={t(def.help)}>
        <VoiceSelect value={value} onChange={onChange} />
      </Field>
    );
  }
  if (def.type === "qwenvoice") {
    return (
      <Field label={t(def.label)} help={t(def.help)}>
        <QwenVoiceSelect value={value == null ? "" : value} onChange={onChange} />
      </Field>
    );
  }
  if (def.type === "textarea") {
    return (
      <Field label={t(def.label)} help={def.help ? t(def.help) : null}>
        <textarea className="textarea" rows={def.rows || 6} value={value == null ? "" : value} onChange={(e) => onChange(e.target.value)}></textarea>
      </Field>
    );
  }
  if (def.type === "password") {
    return (
      <Field label={t(def.label)} help={def.help ? t(def.help) : null}>
        <PasswordInput value={value == null ? "" : value} onChange={onChange} />
      </Field>
    );
  }
  if (def.type === "number") {
    return (
      <Field label={t(def.label)} help={def.help ? t(def.help) : null}>
        <input className="input" type="number" min={def.min} max={def.max} value={value == null ? "" : value}
          onChange={(e) => onChange(e.target.value === "" ? "" : Number(e.target.value))} style={{ width: 180 }} />
      </Field>
    );
  }
  if (def.type === "slider") {
    return (
      <Field label={t(def.label)} help={def.help ? t(def.help) : null}>
        <div style={{ maxWidth: 320 }}>
          <Slider value={Number(value) || 0} onChange={onChange} min={0} max={1} step={0.05} format={(v) => v.toFixed(2)} />
        </div>
      </Field>
    );
  }
  if (def.type === "select") {
    return (
      <Field label={t(def.label)} help={def.help ? t(def.help) : null}>
        <select className="select" style={{ width: 180 }} value={value == null ? "" : value} onChange={(e) => onChange(e.target.value)}>
          {def.options.map((o) => <option key={o} value={o}>{o}</option>)}
        </select>
      </Field>
    );
  }
  return (
    <Field label={t(def.label)} help={def.help ? t(def.help) : null}>
      <input className="input" value={value == null ? "" : value} placeholder={def.placeholder || ""} onChange={(e) => onChange(e.target.value)} />
    </Field>
  );
}

function GroupForm({ group, original, allConfig, onSaved }) {
  const def = CONFIG_FORM_DEFS[group];
  const [draft, setDraft] = React.useState(() => ({ ...original }));
  const [busy, setBusy] = React.useState(false);
  const [canRerank, setCanRerank] = React.useState(false); // features.rerank：企业版且本构建带精排能力
  const toast = useToast();

  React.useEffect(() => { setDraft({ ...original }); }, [original]);

  /* 能力开关：仅 rag 组需要（决定是否展示企业版精排字段） */
  React.useEffect(() => {
    if (!def.rerank) return;
    let alive = true;
    PL.features().then((f) => {
      if (alive) setCanRerank(!!(f && f.features && f.features.rerank));
    }).catch(() => { /* 拉不到按无精排处理 */ });
    return () => { alive = false; };
  }, [def.rerank]);

  const dirtyKeys = Object.keys(draft).filter((k) => draft[k] !== original[k]);
  const dirty = dirtyKeys.length > 0;

  function set(k, v) { setDraft((d) => ({ ...d, [k]: v })); }

  async function save() {
    const patch = {};
    dirtyKeys.forEach((k) => { patch[k] = draft[k]; });
    setBusy(true);
    try {
      const res = await PL.putConfig(group, patch);
      if (res.applied === "restart_required") {
        toast.warn(t("已保存，重启 cored 后生效"));
      } else if (group === "turn" || group === "brain") {
        toast.ok(t("已保存：新建立的会话生效（通话中的会话重连后切换）"));
      } else {
        toast.ok(t("已生效：在线会话下一轮对话即使用新配置"));
      }
      onSaved(group, res.config || { ...original, ...patch }, res.applied);
    } catch (e) {
      toast.error(PL.errText(e, { 400: "配置参数有误" }), e.msgRaw);
    }
    setBusy(false);
  }

  const isLLM = group === "llm";
  const provider = isLLM ? (draft.provider || "openai") : null;
  const isTurn = group === "turn";
  const turnMode = isTurn ? (draft.mode || "local") : null;
  const isBrain = group === "brain";
  const brainMode = isBrain ? (draft.mode || "local") : null;
  const fields = isLLM ? llmFields(provider) : isTurn ? turnFields(turnMode) : isBrain ? brainFields(brainMode) : def.fields;
  const platformMode = isLLM && provider !== "openai";

  /* 知识库在「云端大脑 + 云端判停」组合下用不上：服务端在收到问题的同时就
     开始生成，cored 没有插入检索结果的时机（create_response=false 服务端不支持，
     实测忽略）。draft 优先于 allConfig，这样切换下拉时提示实时跟随。 */
  const cfgAll = allConfig || {};
  const effBrainMode = isBrain ? brainMode : ((cfgAll.brain || {}).mode || "local");
  const effTurnMode = isTurn ? turnMode : ((cfgAll.turn || {}).mode || "local");
  const ragEnabled = group === "rag" ? !!draft.enabled : !!(cfgAll.rag || {}).enabled;
  const ragBlocked = effBrainMode === "qwen" &&
    (effTurnMode === "smart_turn" || effTurnMode === "server_vad") &&
    ragEnabled && (group === "rag" || isTurn || isBrain);

  return (
    <div className="config-layout">
      <div className="card card-pad">
        {group === "system" ? (
          <div className="banner warn" style={{ marginBottom: 18, padding: "8px 12px" }}>
            <Icon name="alert" size={14} />{t("以下修改保存后需重启服务生效")}
          </div>
        ) : null}
        {isLLM ? (
          <Field label={t("接入方式")} help={(LLM_PROVIDERS.find((p) => p.key === provider) || {}).desc}>
            <select className="select" style={{ maxWidth: 320 }} value={provider} onChange={(e) => set("provider", e.target.value)}>
              {LLM_PROVIDERS.map((p) => <option key={p.key} value={p.key}>{p.label}</option>)}
            </select>
          </Field>
        ) : null}
        {isTurn ? (
          <Field label={t("轮次模式")} help={t((TURN_MODES.find((m) => m.key === turnMode) || {}).desc || "")}>
            <select className="select" style={{ maxWidth: 360 }} value={turnMode} onChange={(e) => set("mode", e.target.value)}>
              {TURN_MODES.map((m) => <option key={m.key} value={m.key}>{t(m.label)}</option>)}
            </select>
          </Field>
        ) : null}
        {isBrain ? (
          <Field label={t("大脑模式")} help={t((BRAIN_MODES.find((m) => m.key === brainMode) || {}).desc || "")}>
            <select className="select" style={{ maxWidth: 360 }} value={brainMode} onChange={(e) => set("mode", e.target.value)}>
              {BRAIN_MODES.map((m) => <option key={m.key} value={m.key}>{t(m.label)}</option>)}
            </select>
          </Field>
        ) : null}
        {isBrain && brainMode === "qwen" ? (
          <div className="banner info" style={{ marginBottom: 18, padding: "8px 12px" }}>
            <Icon name="info" size={14} />{t("云端模式需要启动时已配置 DashScope 凭据（--qwen-rt-url + QWEN_RT_KEY），未配置时会话自动回退本地管线。配合「语音轮次」的云端判停模式体验最佳")}
          </div>
        ) : null}
        {isTurn && turnMode === "smart_turn" ? (
          <div className="banner info" style={{ marginBottom: 18, padding: "8px 12px" }}>
            <Icon name="info" size={14} />{t("语义判停无需调参：模型自行判断你是否说完，「嗯」「呃」等犹豫停顿不会被切断，说话时开口即打断")}
          </div>
        ) : null}
        {ragBlocked ? (
          <div className="banner warn" style={{ marginBottom: 18, padding: "8px 12px" }}>
            <Icon name="alert" size={14} />{t("知识库当前不生效：「云端大脑 + 云端判停」组合下，云端在收到问题的同时就开始生成，没有插入检索结果的时机。把「语音轮次」切到「本地判轮次」即可让知识库重新生效（大脑仍走云端）")}
          </div>
        ) : null}
        {platformMode ? (
          <div className="banner info" style={{ marginBottom: 18, padding: "8px 12px" }}>
            <Icon name="info" size={14} />{t("人设与知识库由平台侧管理：本地人设 Prompt 不生效，建议关闭「配置中心 → 知识库」总开关，避免双重检索")}
          </div>
        ) : null}
        {fields.map((f) => (
          <ConfigField key={provider ? provider + ":" + f.k : f.k} def={f} value={draft[f.k]} onChange={(v) => set(f.k, v)} />
        ))}
        {isLLM ? <LLMTestButton draft={draft} /> : null}
        {group === "tts" ? <TTSPreviewButton draft={draft} /> : null}
        {def.rerank && canRerank ? (
          <div className="rerank-section" style={{ marginTop: 6, paddingTop: 14, borderTop: "1px solid var(--border)" }}>
            <div className="row" style={{ gap: 8, marginBottom: 4 }}>
              <h5 style={{ margin: 0 }}>{t("检索精排（rerank）")}</h5>
              <span className="badge info">{t("企业版")}</span>
            </div>
            <div className="muted small mb16">{t(def.rerankNote)}</div>
            {def.rerank.map((f) => (
              <ConfigField key={f.k} def={f} value={draft[f.k]} onChange={(v) => set(f.k, v)} />
            ))}
          </div>
        ) : null}
        {def.advanced ? (
          <details className="adv">
            <summary><span className="chev"><Icon name="chevronRight" size={13} /></span>{t("高级 · 向量模型设置")}</summary>
            <div className="adv-body">
              <div className="muted small mb16">{t(def.advancedNote)}</div>
              {def.advanced.map((f) => (
                <ConfigField key={f.k} def={f} value={draft[f.k]} onChange={(v) => set(f.k, v)} />
              ))}
            </div>
          </details>
        ) : null}
        <div className="form-foot">
          <Btn kind="primary" disabled={!dirty} loading={busy} onClick={save}>{t("保存")}</Btn>
          <Btn kind="ghost" disabled={!dirty || busy} onClick={() => setDraft({ ...original })}>{t("重置")}</Btn>
          <span className="dirty-hint">{dirty ? t("已修改 {0} 项，仅提交改动字段", dirtyKeys.length) : t("无改动")}</span>
        </div>
      </div>

      <div>
        <div className="card card-pad">
          <div className="config-note">
            <h5>{t("说明")}</h5>
            <p>{t(def.note)}</p>
            <h5 style={{ marginTop: 14 }}>{t("配置来源")}</h5>
            <p>{t("实际生效值 = 默认值 < 启动参数 < 此处保存的值。仅提交改动字段，不会覆盖命令行参数对其他字段的调整。")}</p>
          </div>
        </div>
      </div>
    </div>
  );
}

function ConfigPage({ tab, navigate, onRestartRequired }) {
  const [config, setConfig] = React.useState(null);
  const [error, setError] = React.useState(null);
  const [loading, setLoading] = React.useState(true);
  const active = CONFIG_GROUPS.some((g) => g.key === tab) ? tab : "llm";

  const load = React.useCallback(async () => {
    setLoading(true); setError(null);
    try {
      const c = await PL.getConfig();
      setConfig(c);
    } catch (e) { setError(e); }
    setLoading(false);
  }, []);
  React.useEffect(() => { load(); }, [load]);

  function handleSaved(group, newGroupConfig, applied) {
    setConfig((c) => ({ ...c, [group]: newGroupConfig }));
    if (applied === "restart_required") onRestartRequired();
  }

  return (
    <div className="fade-in" data-screen-label="配置中心">
      <div className="page-head">
        <div>
          <h1>{t("配置中心")}</h1>
          <div className="desc">{t("对话模型 / 语音合成 / 知识库为热生效；系统组需重启生效")}</div>
        </div>
      </div>
      <Tabs
        items={CONFIG_GROUPS.map((g) => ({ key: g.key, label: t(g.label) }))}
        active={active}
        onChange={(k) => navigate("/config/" + k)}
      />
      {loading ? <Loading rows={5} /> :
        error ? <ErrorState error={error} onRetry={load} /> :
        <GroupForm key={active} group={active} original={config[active]} allConfig={config} onSaved={handleSaved} />}
    </div>
  );
}

Object.assign(window, { ConfigPage });
