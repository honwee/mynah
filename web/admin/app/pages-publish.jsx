/* Mynah 管理控制台 — 发布管理（把当前配置冻结发布成 /channel/:slug 访客频道）*/

/* 从 system 配置推导访客端 base（频道页跑在访客监听端口，与 admin 不同口）*/
function portOf(addr) {
  if (!addr) return "";
  const m = String(addr).match(/:(\d+)$/);
  return m ? m[1] : "";
}
function visitorBase(sys) {
  const httpsPort = portOf(sys && sys.tls_listen);
  const httpPort = portOf(sys && sys.listen);
  let proto, port;
  if (httpsPort) { proto = "https:"; port = httpsPort; }
  else { proto = location.protocol; port = httpPort; }
  return proto + "//" + location.hostname + (port ? ":" + port : "");
}
function shareLink(base, ch) {
  // Prefer the server-computed share_url (knows the real visitor TLS port);
  // fall back to deriving from system config only if absent.
  if (ch.share_url) return ch.share_url;
  let url = base + "/channel/" + ch.slug;
  if (ch.access_mode === "token" && ch.access_token) url += "?k=" + ch.access_token;
  return url;
}
function linesToList(s) {
  return String(s || "").split(/[\n,]/).map((x) => x.trim()).filter(Boolean);
}

/* 并发总额：total = worker 数（旧后端无 /workers 端点 → total 0 = 不启用配额）；
   used = 其他在线频道的并发上限之和（excludeId = 正在编辑的频道自身）。 */
function useConcurrencyQuota(excludeId) {
  const [quota, setQuota] = React.useState(null);
  React.useEffect(() => {
    Promise.all([PL.workers().catch(() => []), PL.channelList().catch(() => [])])
      .then(([wk, chs]) => {
        const used = (chs || []).filter((c) => c.enabled && c.id !== excludeId)
          .reduce((sum, c) => sum + (c.max_concurrent || 0), 0);
        setQuota({ total: (wk || []).length, used: used });
      });
  }, [excludeId]);
  return quota;
}

/* 访问控制字段（发布/编辑共用）。quota = {total, used}：并发总额分配制 ——
   总额 = 数字人引擎池 worker 数，各在线频道的并发上限之和不能超过总额。 */
function AccessFields({ mode, setMode, domains, setDomains, cidrs, setCidrs, maxc, setMaxc, quota }) {
  const hasQuota = quota && quota.total > 0;
  const remain = hasQuota ? Math.max(0, quota.total - quota.used) : 0;
  const over = hasQuota && (parseInt(maxc, 10) || 1) > remain;
  return (
    <React.Fragment>
      <Field label={t("访问控制")} help={t("公开：任何人可访问；令牌：仅带密钥链接可访问，可随时轮换吊销")}>
        <select className="input" value={mode} onChange={(e) => setMode(e.target.value)}>
          <option value="public">{t("公开链接")}</option>
          <option value="token">{t("令牌链接")}</option>
        </select>
      </Field>
      <Field label={t("域名白名单")} help={t("限制嵌入页面的来源域，一行一个（如 example.com）；留空=不限")}>
        <textarea className="input" rows={2} value={domains} onChange={(e) => setDomains(e.target.value)} placeholder={"example.com\napp.example.com"} />
      </Field>
      <Field label={t("IP 白名单")} help={t("限制访客 IP 段，CIDR 一行一个（如 10.0.0.0/8）；留空=不限")}>
        <textarea className="input" rows={2} value={cidrs} onChange={(e) => setCidrs(e.target.value)} placeholder={"10.0.0.0/8\n192.168.1.0/24"} />
      </Field>
      <Field label={t("并发上限")} help={hasQuota
        ? t("从 worker 总额中分配：共 {0} 个 worker，其他在线频道已占用 {1}，本频道最多可设 {2}。下线频道会释放其占用", quota.total, quota.used, remain)
        : t("同时在线会话数上限，超出拒绝接入。真实并行能力 = 数字人引擎池的 worker 数（见仪表盘）")}>
        <div className="row" style={{ gap: 10, alignItems: "center" }}>
          <input className="input" type="number" min={1} max={hasQuota ? Math.max(1, remain) : undefined}
            value={maxc} onChange={(e) => setMaxc(e.target.value)} style={{ width: 120 }} />
          {over ? <span className="badge warn">{t("超出总额，保存将被拒绝")}</span> : null}
        </div>
      </Field>
    </React.Fragment>
  );
}

/* 展示/品牌字段（发布/编辑共用；是频道元数据，非冻结快照，可热改）*/
function DisplayFields({ desc, setDesc, suggestions, setSuggestions, brand, setBrand, logo, setLogo, bg, setBg, theme, setTheme, avatar, setAvatar }) {
  return (
    <React.Fragment>
      <Field label={t("频道描述")} help={t("访客欢迎页展示的一句话简介；留空=不展示")}>
        <textarea className="input" rows={2} value={desc} onChange={(e) => setDesc(e.target.value)} placeholder={t("如：7×24 在线的智能客服助手")} />
      </Field>
      <Field label={t("推荐提问")} help={t("访客页「您可以问我」快捷按钮，一行一个，最多 8 条")}>
        <textarea className="input" rows={3} value={suggestions} onChange={(e) => setSuggestions(e.target.value)} placeholder={"你们的价格如何？\n如何联系人工？\n支持哪些功能？"} />
      </Field>
      <div className="row" style={{ gap: 12 }}>
        <Field label={t("品牌名")} help={t("顶栏品牌，留空=Mynah")} style={{ flex: 1 }}>
          <input className="input" value={brand} onChange={(e) => setBrand(e.target.value)} placeholder={t("如：某某科技")} />
        </Field>
        <Field label={t("主题色")} help={t("按钮与高亮色，留空=默认蓝")} style={{ width: 140 }}>
          <input className="input" value={theme} onChange={(e) => setTheme(e.target.value)} placeholder="#4361ee" />
        </Field>
      </div>
      <Field label={t("品牌 Logo URL")} help={t("顶栏 logo 图片（建议方形或横条，高度约 28px 展示）；留空=内置声波图标。给三方定制页面时配合品牌名一起换")}>
        <input className="input" value={logo} onChange={(e) => setLogo(e.target.value)} placeholder="https://…/logo.png" />
      </Field>
      <Field label={t("舞台背景图 URL")} help={t("配置后访客页实时抠除数字人绿幕、以此图为背景（WebGL）；留空=原样显示绿幕画面")}>
        <input className="input" value={bg} onChange={(e) => setBg(e.target.value)} placeholder="https://…/background.jpg" />
      </Field>
      <Field label={t("头像图片 URL")} help={t("访客欢迎页展示的头像图片；留空=用首字母图标")}>
        <input className="input" value={avatar} onChange={(e) => setAvatar(e.target.value)} placeholder="https://…/avatar.png" />
      </Field>
    </React.Fragment>
  );
}

/* 绑定的形象没有自己的待机素材时提醒一句。没有素材不等于坏了——那段待机改由引擎
   实时渲染，人是对的，但频道开着就一直占着 worker。不说的话这是一次静默的性能回退：
   操作者只会看到「绑成功了」。has_idle 来自 GET /avatars（见 control/avatars_handlers.go）。 */
function warnNoIdle(list, avatarId, toast) {
  const hit = (list || []).filter((a) => a.id === avatarId)[0];
  if (hit && !hit.has_idle) {
    toast.warn(t("形象「{0}」没有自己的待机素材，待机将由引擎实时生成（占 GPU）；可在「形象与声音」页烘焙待机以省电。", hit.name || avatarId));
  }
}

/* 数字人字段：音色（写入冻结快照）+ 形象（企业版占位）。发布/编辑共用。
   brainMode="qwen" 时音色是 Qwen 云端音色 id（本地音色列表不适用，改为文本输入）。 */
function AvatarVoiceFields({ voices, voice, setVoice, brainMode, avatarId, setAvatarId, avatarList }) {
  return (
    <React.Fragment>
      {brainMode === "qwen" ? (
        <Field label={t("云端音色")} help={t("当前对话大脑为 Qwen 云端实时：访客听到的是 Qwen 系统音色或声音复刻音色（本地音色在此模式下不生效）。留空沿用「对话大脑」里的音色")}>
          <QwenVoiceSelect value={voice} onChange={setVoice} emptyLabel={t("（沿用「对话大脑」当前音色）")} />
        </Field>
      ) : (
        <Field label={t("音色")} help={t("该频道数字人使用的音色；在「形象与声音」克隆的自定义音色会出现在这里")}>
          <select className="input" value={voice} onChange={(e) => setVoice(e.target.value)}>
            {voices.length === 0
              ? <option value={voice}>{voice || t("加载中…")}</option>
              : voices.map((v) => (
                <option key={v.id} value={v.id}>{v.id}{v.id !== v.name ? t("（自定义）") : ""}</option>
              ))}
          </select>
        </Field>
      )}
      <Field label={t("数字人形象")}
        help={t("发布后这张脸就固定了，不随「配置中心」的全局形象切换而变。烘焙形象只能由对应引擎渲染，绑定后该频道只会派发到跑着该引擎的 worker。")}>
        <select className="select" style={{ maxWidth: 360 }} value={avatarId || ""}
          onChange={(e) => setAvatarId(e.target.value)}>
          <option value="">{t("跟随全局设置")}</option>
          {(avatarList || []).map((a) => (
            <option key={a.id} value={a.id}>
              {a.name + (a.engine ? "（" + a.engine + "）" : "")}
            </option>
          ))}
        </select>
      </Field>
    </React.Fragment>
  );
}

/* 发布当前配置为新频道 */
function PublishModal({ onClose, onSaved }) {
  const [name, setName] = React.useState("");
  const [slug, setSlug] = React.useState("");
  const [greeting, setGreeting] = React.useState("");
  // 形象绑定：频道发布后这张脸就固定了，不随全局形象切换而变
  const [avatarId, setAvatarId] = React.useState("");
  const [avatarList, setAvatarList] = React.useState([]);
  React.useEffect(() => {
    let alive = true;
    PL.avatars().then((l) => { if (alive) setAvatarList(l || []); }).catch(() => {});
    return () => { alive = false; };
  }, []);
  const [voices, setVoices] = React.useState([]);
  const [voice, setVoice] = React.useState(""); // 默认沿用配置中心当前音色
  const [brainMode, setBrainMode] = React.useState(""); // 配置中心当前大脑模式（发布即冻结）
  const [mode, setMode] = React.useState("public");
  const [domains, setDomains] = React.useState("");
  const [cidrs, setCidrs] = React.useState("");
  const [maxc, setMaxc] = React.useState("1");
  const [desc, setDesc] = React.useState("");
  const [suggestions, setSuggestions] = React.useState("");
  const [brand, setBrand] = React.useState("");
  const [theme, setTheme] = React.useState("");
  const [logo, setLogo] = React.useState("");
  const [bg, setBg] = React.useState("");
  const [avatar, setAvatar] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const quota = useConcurrencyQuota(0);
  const toast = useToast();

  // 拉音色列表 + 配置中心当前音色作默认（发布即冻结这一刻的音色）；
  // 大脑模式为 qwen 时默认音色取「对话大脑」的云端音色
  React.useEffect(() => {
    PL.ttsVoices().then(setVoices).catch(() => {});
    PL.getConfigGroup("brain").then((b) => {
      if (b && b.mode === "qwen") {
        setBrainMode("qwen");
        setVoice(b.voice || "");
      } else {
        PL.getConfigGroup("tts").then((c) => { if (c && c.voice) setVoice(c.voice); }).catch(() => {});
      }
    }).catch(() => {
      PL.getConfigGroup("tts").then((c) => { if (c && c.voice) setVoice(c.voice); }).catch(() => {});
    });
  }, []);

  async function submit() {
    if (!name.trim()) { setError(t("请输入频道名称")); return; }
    if (!/^[a-z0-9][a-z0-9-]{1,62}$/.test(slug)) { setError(t("链接标识需为 2-63 位小写字母、数字或连字符")); return; }
    setBusy(true); setError(null);
    try {
      const saved = await PL.channelPublish({
        name: name.trim(), slug: slug, greeting: greeting.trim(),
        access_mode: mode, domains: linesToList(domains), cidrs: linesToList(cidrs),
        max_concurrent: Math.max(1, parseInt(maxc, 10) || 1),
        voice: voice,
        description: desc.trim(), suggested_questions: linesToList(suggestions),
        brand_name: brand.trim(), brand_logo: logo.trim(), bg_image: bg.trim(), theme_color: theme.trim(), avatar_preview: avatar.trim(),
        avatar: avatarId,
      });
      // 预热绑定的形象：加载一个烘焙约 15 秒，而 worker 要加载完才报 Ready，
      // 不预热的话第一个访客会对着黑屏干等。这里由管理员承担这段等待。
      if (avatarId) {
        try {
          const pre = await PL.avatarPreload(avatarId);
          if (pre && pre.warning) toast.warn(t(pre.warning));
        } catch (e) {
          toast.warn(t("形象预热失败，首个访客可能等待较久：") + PL.errText(e));
        }
        warnNoIdle(avatarList, avatarId, toast);
      }
      toast.ok(t("频道「{0}」已发布", saved.name));
      onSaved(saved);
    } catch (e) {
      if (e.status === 409) setError(t("已存在相同链接标识的频道"));
      else setError(PL.errText(e) + (e.msgRaw ? ": " + e.msgRaw : ""));
    }
    setBusy(false);
  }

  return (
    <Modal title={t("发布当前配置为频道")} width={560} onClose={busy ? null : onClose} footer={
      <React.Fragment>
        <Btn kind="ghost" onClick={onClose} disabled={busy}>{t("取消")}</Btn>
        <Btn kind="primary" onClick={submit} loading={busy}>{t("发布")}</Btn>
      </React.Fragment>
    }>
      <div className="banner info" style={{ marginBottom: 14 }}>
        <Icon name="info" size={15} />
        {t("将冻结当前配置中心的人设、音色与知识库参数为独立快照；之后修改配置不影响本频道，需「重新发布」才生效")}
      </div>
      <Field label={t("名称")} required error={error}>
        <input className="input" value={name} autoFocus onChange={(e) => { setName(e.target.value); setError(null); }} placeholder={t("如：官网客服")} />
      </Field>
      <Field label={t("链接标识 slug")} required help={t("访客页地址 /channel/{0}", slug || "your-slug")}>
        <input className="input mono" value={slug} onChange={(e) => { setSlug(e.target.value.toLowerCase()); setError(null); }} placeholder="website-cs" />
      </Field>
      <Field label={t("开场白")} help={t("访客接入后数字人自动说一句，留空=不主动开口")}>
        <textarea className="input" rows={2} value={greeting} onChange={(e) => setGreeting(e.target.value)} placeholder={t("你好，我是你的智能助手，有什么可以帮你？")} />
      </Field>
      <div className="divider-label">{t("数字人")}</div>
      <AvatarVoiceFields voices={voices} voice={voice} setVoice={setVoice} brainMode={brainMode}
        avatarId={avatarId} setAvatarId={setAvatarId} avatarList={avatarList} />
      <AccessFields mode={mode} setMode={setMode} domains={domains} setDomains={setDomains} cidrs={cidrs} setCidrs={setCidrs} maxc={maxc} setMaxc={setMaxc} quota={quota} />
      <div className="divider-label">{t("访客页展示")}</div>
      <DisplayFields desc={desc} setDesc={setDesc} suggestions={suggestions} setSuggestions={setSuggestions} brand={brand} setBrand={setBrand} logo={logo} setLogo={setLogo} bg={bg} setBg={setBg} theme={theme} setTheme={setTheme} avatar={avatar} setAvatar={setAvatar} />
    </Modal>
  );
}

/* 编辑频道设置（不含配置快照；配置变更走「重新发布」）*/
function ChannelSettingsModal({ ch, onClose, onSaved }) {
  const [name, setName] = React.useState(ch.name);
  // Avatar binding: seeded from the channel's frozen value so reopening the
  // dialog shows what is actually bound, not a blank.
  const [avatarId, setAvatarId] = React.useState(ch.avatar || "");
  const [avatarList, setAvatarList] = React.useState([]);
  React.useEffect(() => {
    let alive = true;
    PL.avatars().then((l) => { if (alive) setAvatarList(l || []); }).catch(() => {});
    return () => { alive = false; };
  }, []);
  const [mode, setMode] = React.useState(ch.access_mode);
  const [domains, setDomains] = React.useState((ch.domains || []).join("\n"));
  const [cidrs, setCidrs] = React.useState((ch.cidrs || []).join("\n"));
  const [maxc, setMaxc] = React.useState(String(ch.max_concurrent));
  const [desc, setDesc] = React.useState(ch.description || "");
  const [suggestions, setSuggestions] = React.useState((ch.suggested_questions || []).join("\n"));
  const [brand, setBrand] = React.useState(ch.brand_name || "");
  const [theme, setTheme] = React.useState(ch.theme_color || "");
  const [logo, setLogo] = React.useState(ch.brand_logo || "");
  const [bg, setBg] = React.useState(ch.bg_image || "");
  const [avatar, setAvatar] = React.useState(ch.avatar_preview || "");
  const [voices, setVoices] = React.useState([]);
  const [voice, setVoice] = React.useState(ch.voice || ""); // 频道当前快照音色
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const quota = useConcurrencyQuota(ch.id);
  const toast = useToast();

  React.useEffect(() => { PL.ttsVoices().then(setVoices).catch(() => {}); }, []);

  async function submit() {
    if (!name.trim()) { setError(t("请输入频道名称")); return; }
    setBusy(true); setError(null);
    try {
      await PL.channelPatch(ch.id, {
        name: name.trim(), access_mode: mode,
        domains: linesToList(domains), cidrs: linesToList(cidrs),
        max_concurrent: Math.max(1, parseInt(maxc, 10) || 1),
        voice: voice,
        description: desc.trim(), suggested_questions: linesToList(suggestions),
        brand_name: brand.trim(), brand_logo: logo.trim(), bg_image: bg.trim(), theme_color: theme.trim(), avatar_preview: avatar.trim(),
        avatar: avatarId,
      });
      // Warm the newly bound avatar so the next visitor doesn't pay the ~15s
      // load. Non-blocking for the save itself: the binding is already stored.
      if (avatarId) {
        try {
          const pre = await PL.avatarPreload(avatarId);
          if (pre && pre.warning) toast.warn(t(pre.warning));
        } catch (e2) {
          // Carry the reason. A bare "预热失败" cost a whole debugging session
          // once: the route was simply unmounted and the 404 never surfaced.
          toast.warn(t("形象预热失败，首个访客可能等待较久：") + PL.errText(e2));
        }
        warnNoIdle(avatarList, avatarId, toast);
      }
      toast.ok(t("已保存"));
      onSaved();
    } catch (e) { setError(PL.errText(e) + (e.msgRaw ? ": " + e.msgRaw : "")); }
    setBusy(false);
  }

  return (
    <Modal title={t("频道设置 — {0}", ch.name)} width={560} onClose={busy ? null : onClose} footer={
      <React.Fragment>
        <Btn kind="ghost" onClick={onClose} disabled={busy}>{t("取消")}</Btn>
        <Btn kind="primary" onClick={submit} loading={busy}>{t("保存")}</Btn>
      </React.Fragment>
    }>
      <Field label={t("名称")} required error={error}>
        <input className="input" value={name} autoFocus onChange={(e) => { setName(e.target.value); setError(null); }} />
      </Field>
      <div className="divider-label">{t("数字人")}</div>
      <AvatarVoiceFields voices={voices} voice={voice} setVoice={setVoice} brainMode={ch.brain_mode || ""}
        avatarId={avatarId} setAvatarId={setAvatarId} avatarList={avatarList} />
      <AccessFields mode={mode} setMode={setMode} domains={domains} setDomains={setDomains} cidrs={cidrs} setCidrs={setCidrs} maxc={maxc} setMaxc={setMaxc} quota={quota} />
      <div className="divider-label">{t("访客页展示")}</div>
      <DisplayFields desc={desc} setDesc={setDesc} suggestions={suggestions} setSuggestions={setSuggestions} brand={brand} setBrand={setBrand} logo={logo} setLogo={setLogo} bg={bg} setBg={setBg} theme={theme} setTheme={setTheme} avatar={avatar} setAvatar={setAvatar} />
      <div className="muted small">{t("改音色即时生效（版本 +1，新接入访客生效）；如需更新人设/知识库，请在频道行点「重新发布」以冻结最新配置。")}</div>
    </Modal>
  );
}

/* 发布管理主页 */
function PublishPage({ navigate }) {
  const [chs, setChs] = React.useState(null);
  const [sys, setSys] = React.useState(null);
  const [error, setError] = React.useState(null);
  const [loading, setLoading] = React.useState(true);
  const [modal, setModal] = React.useState(null);       // "publish" | {edit: ch}
  const [delTarget, setDelTarget] = React.useState(null);
  const [delBusy, setDelBusy] = React.useState(false);
  const [repTarget, setRepTarget] = React.useState(null);
  const [repBusy, setRepBusy] = React.useState(false);
  const [toggling, setToggling] = React.useState({});
  const toast = useToast();

  const load = React.useCallback(async (silent) => {
    if (!silent) { setLoading(true); setError(null); }
    try {
      const [c, s] = await Promise.all([PL.channelList(), PL.getConfigGroup("system").catch(() => null)]);
      setChs(c); setSys(s);
    } catch (e) { if (!silent) setError(e); }
    setLoading(false);
  }, []);
  React.useEffect(() => { load(); }, [load]);

  const base = visitorBase(sys);

  async function toggle(ch, v) {
    setToggling((x) => ({ ...x, [ch.id]: true }));
    try {
      await PL.channelPatch(ch.id, { enabled: v });
      setChs((list) => list.map((x) => x.id === ch.id ? { ...x, enabled: v } : x));
      toast.ok(v ? t("「{0}」已上线", ch.name) : t("「{0}」已下线，访客将无法接入", ch.name));
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setToggling((x) => ({ ...x, [ch.id]: false }));
  }

  async function copyLink(ch) {
    const url = shareLink(base, ch);
    try {
      await navigator.clipboard.writeText(url);
      toast.ok(t("分享链接已复制"));
    } catch (e) {
      window.prompt(t("复制下方链接"), url);
    }
  }

  async function rotate(ch) {
    try {
      const r = await PL.channelRotateToken(ch.id);
      setChs((list) => list.map((x) => x.id === ch.id ? { ...x, access_mode: "token", access_token: r.access_token } : x));
      toast.ok(t("令牌已轮换，旧链接立即失效"));
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
  }

  async function doRepublish() {
    setRepBusy(true);
    try {
      const r = await PL.channelRepublish(repTarget.id);
      setChs((list) => list.map((x) => x.id === repTarget.id ? { ...x, version: r.version } : x));
      toast.ok(t("已用当前配置重新发布（v{0}）", r.version));
      setRepTarget(null);
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setRepBusy(false);
  }

  async function doDelete() {
    setDelBusy(true);
    try {
      await PL.channelDelete(delTarget.id);
      toast.ok(t("频道「{0}」已删除", delTarget.name));
      setDelTarget(null);
      load(true);
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setDelBusy(false);
  }

  return (
    <div className="fade-in" data-screen-label="发布管理">
      <div className="page-head">
        <div>
          <h1>{t("发布管理")}</h1>
          <div className="desc">{t("把调好的配置冻结发布成独立访客频道，分享链接即可对外服务")}</div>
        </div>
        <div className="actions">
          <Btn kind="primary" icon="plus" onClick={() => setModal("publish")}>{t("发布当前配置")}</Btn>
        </div>
      </div>

      {loading ? <Loading rows={4} /> :
        error ? <ErrorState error={error} onRetry={() => load()} /> : (
          <div className="card">
            {chs.length === 0 ? (
              <EmptyState icon="globe" title={t("还没有发布频道")}
                desc={t("在配置中心/对话调试里调好人设后，回到这里把它发布成对外的访客频道")}
                action={<Btn kind="primary" icon="plus" onClick={() => setModal("publish")}>{t("发布当前配置")}</Btn>} />
            ) : (
              <table className="table">
                <thead>
                  <tr>
                    <th>{t("名称")}</th><th>{t("链接")}</th><th>{t("访问")}</th>
                    <th>{t("音色")}</th>
                    <th>{t("并发")}</th><th>{t("版本")}</th><th>{t("上线")}</th>
                    <th style={{ textAlign: "right" }}>{t("操作")}</th>
                  </tr>
                </thead>
                <tbody>
                  {chs.map((ch) => (
                    <tr key={ch.id}>
                      <td style={{ fontWeight: 600 }}>{ch.name}</td>
                      <td>
                        <span className="row" style={{ gap: 6 }}>
                          <span className="mono small">/channel/{ch.slug}</span>
                          <IconBtn icon="file" tip={t("复制分享链接")} onClick={() => copyLink(ch)} />
                          <IconBtn icon="external" tip={t("打开频道页")} onClick={() => window.open(shareLink(base, ch), "_blank")} />
                        </span>
                      </td>
                      <td>
                        <span className={"badge " + (ch.access_mode === "token" ? "warn" : "muted")}>
                          {ch.access_mode === "token" ? t("令牌") : t("公开")}
                        </span>
                        {(ch.domains && ch.domains.length) || (ch.cidrs && ch.cidrs.length)
                          ? <span className="badge muted" style={{ marginLeft: 4 }}>{t("白名单")}</span> : null}
                      </td>
                      <td className="mono small">
                        {ch.voice || "—"}
                        {ch.brain_mode === "qwen" ? <span className="badge info" style={{ marginLeft: 4 }}>{t("云端")}</span> : null}
                      </td>
                      <td className="num">{ch.active}/{ch.max_concurrent}</td>
                      <td className="muted num">v{ch.version}</td>
                      <td><Switch checked={ch.enabled} busy={toggling[ch.id]} onChange={(v) => toggle(ch, v)} /></td>
                      <td>
                        <div className="row-actions">
                          <IconBtn icon="refresh" tip={t("重新发布：用当前配置刷新快照")} onClick={() => setRepTarget(ch)} />
                          {ch.access_mode === "token" ? <IconBtn icon="key" tip={t("轮换令牌（吊销旧链接）")} onClick={() => rotate(ch)} /> : null}
                          <IconBtn icon="edit" tip={t("频道设置")} onClick={() => setModal({ edit: ch })} />
                          <IconBtn icon="trash" tip={t("删除")} danger onClick={() => setDelTarget(ch)} />
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        )}

      {modal === "publish" ? <PublishModal onClose={() => setModal(null)} onSaved={() => { setModal(null); load(true); }} /> : null}
      {modal && modal.edit ? <ChannelSettingsModal ch={modal.edit} onClose={() => setModal(null)} onSaved={() => { setModal(null); load(true); }} /> : null}
      {repTarget ? (
        <ConfirmDialog
          title={t("重新发布「{0}」", repTarget.name)}
          body={t("将用当前配置中心的人设、音色与知识库参数覆盖该频道的快照（版本 +1）。在线会话不受影响，新接入的访客生效。")}
          confirmText={t("重新发布")} busy={repBusy}
          onConfirm={doRepublish} onClose={() => setRepTarget(null)}
        />
      ) : null}
      {delTarget ? (
        <ConfirmDialog
          title={t("删除频道「{0}」", delTarget.name)}
          body={t("分享链接将立即失效，不可恢复。")}
          confirmText={t("永久删除")} danger requireText={delTarget.slug} busy={delBusy}
          onConfirm={doDelete} onClose={() => setDelTarget(null)}
        />
      ) : null}
    </div>
  );
}

Object.assign(window, { PublishPage });
