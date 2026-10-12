/* Mynah 管理控制台 — 主框架：路由 / 导航 / 顶栏（主题+语言切换）/ 401 拦截 / 演示面板 */

const SCENARIO_MAP = {
  "正常运行": "normal",
  "部分组件异常": "degraded",
  "高负载（多会话）": "busy",
  "空数据（新部署）": "empty",
  "管理服务不可达": "down",
};
const TWEAK_DEFAULTS = /*EDITMODE-BEGIN*/{
  "scenario": "正常运行",
  "forceChange": false
}/*EDITMODE-END*/;

const THEMES = [
  { key: "ink", zh: "玄墨", en: "Ink", swatch: "#21211d" },
  { key: "celadon", zh: "青瓷", en: "Celadon", swatch: "#1c7a6c" },
  { key: "indigo", zh: "黛蓝", en: "Indigo", swatch: "#3552d4" },
];

/* ---------- 路由 ---------- */
function useHashRoute() {
  const get = () => {
    const h = location.hash || "#/";
    return h.startsWith("#") ? h.slice(1) : h;
  };
  const [route, setRoute] = React.useState(get);
  React.useEffect(() => {
    const fn = () => setRoute(get());
    window.addEventListener("hashchange", fn);
    return () => window.removeEventListener("hashchange", fn);
  }, []);
  return route || "/";
}
function navigate(path) { location.hash = "#" + path; }

const NAV_ITEMS = [
  { path: "/", icon: "dashboard", label: "仪表盘", match: (r) => r === "/" },
  { path: "/kb", icon: "book", label: "知识库", match: (r) => r.startsWith("/kb") },
  { path: "/playground", icon: "message", label: "对话调试", match: (r) => r.startsWith("/playground") },
  { path: "/publish", icon: "globe", label: "发布管理", match: (r) => r.startsWith("/publish") },
  { path: "/config", icon: "sliders", label: "配置中心", match: (r) => r.startsWith("/config") },
  { path: "/sessions", icon: "monitor", label: "会话监控", match: (r) => r.startsWith("/sessions") },
  { path: "/services", icon: "server", label: "本地服务", match: (r) => r.startsWith("/services") },
];
// Feature-gated pages (full open-source build enables all; a narrower
// assembly reports its actual gate via /features and locked entries hide).
const FEATURE_ITEMS = [
  { icon: "user", label: "形象与声音", feature: "training", path: "/voices", match: (r) => r.startsWith("/voices") },
];

function Sidebar({ route, features, collapsed, onToggle }) {
  const feats = (features && features.features) || {};
  const navItems = NAV_ITEMS;
  const entItems = FEATURE_ITEMS.filter((it) => feats[it.feature]);
  return (
    <aside className={"sidebar" + (collapsed ? " collapsed" : "")}>
      <button className="sb-toggle" title={collapsed ? t("展开侧栏") : t("收起侧栏")}
        aria-label={collapsed ? t("展开侧栏") : t("收起侧栏")} onClick={onToggle}>
        <Icon name={collapsed ? "chevronRight" : "chevronLeft"} size={13} />
      </button>
      <div className="brand">
        <BrandMark size={30} />
        {collapsed ? null : (
          <div className="brand-text">
            <div className="brand-name">Mynah</div>
            <div className="brand-sub">{t("Mynah · 管理控制台")}</div>
          </div>
        )}
      </div>
      <nav>
        {navItems.map((it) => (
          <button key={it.path} className={"nav-item" + (it.match(route) ? " active" : "")} onClick={() => navigate(it.path)}>
            <Icon name={it.icon} size={collapsed ? 18 : 16} />
            <span className="nav-label">{t(it.label)}</span>
          </button>
        ))}
      </nav>
      {entItems.length === 0 ? null : (collapsed ? <div className="nav-group-divider"></div> : <div className="nav-group-label">{t("形象工坊")}</div>)}
      {entItems.map((it) => (
        <button key={it.label} className={"nav-item" + (it.match && it.match(route) ? " active" : "")} onClick={() => navigate(it.path)}>
          <Icon name={it.icon} size={collapsed ? 18 : 16} />
          <span className="nav-label">{t(it.label)}</span>
        </button>
      ))}
      <div className="sidebar-foot">{collapsed ? "v1" : t("API v1")}</div>
    </aside>
  );
}

/* ---------- 顶栏健康状态点（30s 轮询） ---------- */
function HealthPill() {
  const [st, setSt] = React.useState(null);
  React.useEffect(() => {
    let alive = true;
    const tick = async () => {
      try { const h = await PL.health(); if (alive) setSt(h.status); }
      catch (e) { if (alive) setSt("error"); }
    };
    tick();
    const tm = setInterval(tick, 30000);
    return () => { alive = false; clearInterval(tm); };
  }, []);
  const dot = st === "ok" ? "ok" : st === "degraded" ? "warn" : st === "error" ? "danger" : "muted";
  const label = st === "ok" ? t("系统正常") : st === "degraded" ? t("部分异常") : st === "error" ? t("服务不可达") : t("检测中…");
  return (
    <button className="health-pill" onClick={() => navigate("/")}>
      <span className={"dot " + dot}></span>{label}
    </button>
  );
}

/* ---------- 主题切换 ---------- */
function ThemeMenu({ theme, onChange }) {
  const [open, setOpen] = React.useState(false);
  const ref = React.useRef(null);
  React.useEffect(() => {
    if (!open) return;
    const fn = (e) => { if (ref.current && !ref.current.contains(e.target)) setOpen(false); };
    document.addEventListener("mousedown", fn);
    return () => document.removeEventListener("mousedown", fn);
  }, [open]);
  const isEn = PLI18N.lang() === "en";
  const cur = THEMES.find((x) => x.key === theme) || THEMES[0];
  return (
    <div className="menu-wrap" ref={ref}>
      <button className="health-pill" onClick={() => setOpen(!open)} title={t("主题")}>
        <Icon name="droplet" size={13} />{isEn ? cur.en : cur.zh}
      </button>
      {open ? (
        <div className="menu">
          <div className="menu-meta">{t("主题")}</div>
          {THEMES.map((th) => (
            <button key={th.key} className="menu-item" onClick={() => { onChange(th.key); setOpen(false); }}>
              <span className="dot" style={{ background: th.swatch, width: 10, height: 10 }}></span>
              {isEn ? th.en : th.zh}
              {th.key === theme ? <span style={{ marginLeft: "auto", display: "inline-flex" }}><Icon name="check" size={13} /></span> : null}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

/* ---------- 语言切换（显示目标语言） ---------- */
function LangButton({ lang, onChange }) {
  return (
    <button className="health-pill" onClick={() => onChange(lang === "zh" ? "en" : "zh")} title={t("语言")}>
      <Icon name="globe" size={13} />{lang === "zh" ? "EN" : "中文"}
    </button>
  );
}

/* 登录/强制改密页右上角的悬浮控制 */
function FloatControls({ theme, setTheme, lang, setLang }) {
  return (
    <div style={{ position: "fixed", top: 16, right: 16, display: "flex", gap: 8, zIndex: 60 }}>
      <ThemeMenu theme={theme} onChange={setTheme} />
      <LangButton lang={lang} onChange={setLang} />
    </div>
  );
}

/* ---------- 顶栏管理员菜单 ---------- */
function AdminMenu({ onLogout, onChangePassword }) {
  const [open, setOpen] = React.useState(false);
  const ref = React.useRef(null);
  React.useEffect(() => {
    if (!open) return;
    const fn = (e) => { if (ref.current && !ref.current.contains(e.target)) setOpen(false); };
    document.addEventListener("mousedown", fn);
    return () => document.removeEventListener("mousedown", fn);
  }, [open]);
  return (
    <div className="menu-wrap" ref={ref}>
      <button className="health-pill" onClick={() => setOpen(!open)}>
        <Icon name="user" size={13} />admin<Icon name="chevronDown" size={12} />
      </button>
      {open ? (
        <div className="menu">
          <div className="menu-meta">{t("单管理员 · OSS 版")}</div>
          <div className="menu-divider"></div>
          <button className="menu-item" onClick={() => { setOpen(false); onChangePassword(); }}>
            <Icon name="key" size={14} />{t("修改密码")}
          </button>
          <button className="menu-item danger" onClick={() => { setOpen(false); onLogout(); }}>
            <Icon name="logout" size={14} />{t("退出登录")}
          </button>
        </div>
      ) : null}
    </div>
  );
}

function pageTitle(route) {
  if (route.startsWith("/kb")) return t("知识库");
  if (route.startsWith("/playground")) return t("对话调试");
  if (route.startsWith("/publish")) return t("发布管理");
  if (route.startsWith("/voices")) return t("形象与声音");
  if (route.startsWith("/config")) return t("配置中心");
  if (route.startsWith("/sessions")) return t("会话监控");
  return t("仪表盘");
}

/* ---------- 主应用 ---------- */
function App() {
  const [tweaks, setTweak] = useTweaks(TWEAK_DEFAULTS);
  const [lang, setLangState] = React.useState(PLI18N.lang());
  const [theme, setThemeState] = React.useState(() => localStorage.getItem("pl_theme") || "ink");

  /* 让 Mock 后端同步读到演示场景（渲染期同步写入） */
  window.__plTweaks = {
    scenario: SCENARIO_MAP[tweaks.scenario] || "normal",
    forceChange: !!tweaks.forceChange,
  };

  React.useEffect(() => {
    document.documentElement.setAttribute("data-theme", theme);
    localStorage.setItem("pl_theme", theme);
  }, [theme]);

  function setLang(l) {
    PLI18N.setLang(l);
    setLangState(l);
  }

  /* key=lang：切换语言时整树重挂载，所有 t() 文案即时刷新 */
  return (
    <ToastProvider key={lang}>
      <AppInner tweaks={tweaks} setTweak={setTweak}
        lang={lang} setLang={setLang} theme={theme} setTheme={setThemeState} />
    </ToastProvider>
  );
}

function AppInner({ tweaks, setTweak, lang, setLang, theme, setTheme }) {
  const route = useHashRoute();
  const [authed, setAuthed] = React.useState(() => !!PL.token.get());
  const [mustChange, setMustChange] = React.useState(() => localStorage.getItem("pl_must_change") === "1");
  const [restartRequired, setRestartRequired] = React.useState(() => sessionStorage.getItem("pl_restart") === "1");
  const [pwModal, setPwModal] = React.useState(false);
  const [mockMode, setMockMode] = React.useState(PL.mode() === "mock");
  const [features, setFeatures] = React.useState(null);
  const [sbCollapsed, setSbCollapsed] = React.useState(() => localStorage.getItem("pl_sidebar_collapsed") === "1");
  const toast = useToast();

  function toggleSidebar() {
    setSbCollapsed((c) => {
      const next = !c;
      localStorage.setItem("pl_sidebar_collapsed", next ? "1" : "0");
      return next;
    });
  }

  /* 版本与能力开关：登录后拉一次，驱动侧边栏企业版入口与版本标识
     （演示面板切换企业版时重新拉取） */
  React.useEffect(() => {
    if (!authed || mustChange) return;
    let alive = true;
    PL.features().then((f) => { if (alive) setFeatures(f); }).catch(() => { /* 拉不到则保持 OSS 默认展示 */ });
    return () => { alive = false; };
  }, [authed, mustChange]);

  /* 401 全局拦截 */
  React.useEffect(() => {
    const fn = () => {
      PL.token.set(null);
      localStorage.removeItem("pl_must_change");
      setAuthed(false); setMustChange(false);
      toast.error(t("登录已过期，请重新登录"));
    };
    document.addEventListener("pl:unauthorized", fn);
    return () => document.removeEventListener("pl:unauthorized", fn);
  }, [toast]);

  /* Mock 模式标识 */
  React.useEffect(() => {
    const fn = (e) => setMockMode(e.detail === "mock");
    document.addEventListener("pl:mode", fn);
    PL.probe();
    return () => document.removeEventListener("pl:mode", fn);
  }, []);

  function handleLoggedIn(needChange) {
    setAuthed(true);
    if (needChange) {
      localStorage.setItem("pl_must_change", "1");
      setMustChange(true);
    } else {
      localStorage.removeItem("pl_must_change");
      setMustChange(false);
      navigate("/");
    }
  }
  function handleLogout() {
    PL.token.set(null);
    localStorage.removeItem("pl_must_change");
    setAuthed(false); setMustChange(false);
  }
  function handleRestartRequired() {
    sessionStorage.setItem("pl_restart", "1");
    setRestartRequired(true);
  }

  /* ---------- 页面分发 ---------- */
  let body;
  if (!authed) {
    body = (
      <React.Fragment>
        <FloatControls theme={theme} setTheme={setTheme} lang={lang} setLang={setLang} />
        <LoginPage onLoggedIn={handleLoggedIn} />
      </React.Fragment>
    );
  } else if (mustChange) {
    body = (
      <React.Fragment>
        <FloatControls theme={theme} setTheme={setTheme} lang={lang} setLang={setLang} />
        <ForceChangePage onDone={() => { localStorage.removeItem("pl_must_change"); setMustChange(false); navigate("/"); }} />
      </React.Fragment>
    );
  } else {
    let page;
    const kbDetail = route.match(/^\/kb\/(\d+)$/);
    if (route.startsWith("/kb/test")) page = <KbPage tab="test" navigate={navigate} />;
    else if (kbDetail) page = <KbDetailPage key={kbDetail[1]} kbId={+kbDetail[1]} navigate={navigate} />;
    else if (route.startsWith("/kb")) page = <KbPage tab="list" navigate={navigate} />;
    else if (route.startsWith("/playground")) page = <PlaygroundView navigate={navigate} />;
    else if (route.startsWith("/publish")) page = <PublishPage navigate={navigate} />;
    else if (route.startsWith("/voices")) page = <VoicesPage navigate={navigate} features={features} />;
    else if (route.startsWith("/config")) page = <ConfigPage tab={(route.split("/")[2] || "llm")} navigate={navigate} onRestartRequired={handleRestartRequired} />;
    else if (route.startsWith("/sessions")) page = <SessionsPage />;
    else if (route.startsWith("/services")) page = <ServicesPage />;
    else page = <DashboardPage navigate={navigate} />;

    body = (
      <div className={"shell" + (sbCollapsed ? " sb-collapsed" : "")}>
        <Sidebar route={route} features={features} collapsed={sbCollapsed} onToggle={toggleSidebar} />
        <div className="main-col">
          <header className="topbar">
            <div className="topbar-title">{pageTitle(route)}</div>
            <div className="topbar-right">
              {mockMode ? (
                <span className="tip" data-tip={t("未检测到 /api/v1 后端，已启用内置演示数据。\n配置 dev proxy 指向 cored 管理端口后自动切换为真实接口。")}>
                  <span className="badge info">{t("演示数据")}</span>
                </span>
              ) : null}
              <ThemeMenu theme={theme} onChange={setTheme} />
              <LangButton lang={lang} onChange={setLang} />
              <HealthPill />
              <AdminMenu onLogout={handleLogout} onChangePassword={() => setPwModal(true)} />
            </div>
          </header>
          {restartRequired ? (
            <div className="restart-bar">
              <Icon name="alert" size={13} />
              {t("系统配置已修改，重启 cored 后生效")}
              <span style={{ marginLeft: "auto" }}>
                <button className="btn btn-ghost btn-sm" style={{ height: 22, color: "inherit" }} onClick={() => { sessionStorage.removeItem("pl_restart"); setRestartRequired(false); }}>{t("知道了")}</button>
              </span>
            </div>
          ) : null}
          <main className="content">
            <div className="content-inner">{page}</div>
          </main>
        </div>
      </div>
    );
  }

  return (
    <React.Fragment>
      {body}
      {pwModal ? (
        <Modal title={t("修改密码")} onClose={() => setPwModal(false)}>
          <PasswordForm onDone={() => setPwModal(false)} onCancel={() => setPwModal(false)} />
        </Modal>
      ) : null}
      <DemoTweaks tweaks={tweaks} setTweak={setTweak} toast={toast} />
    </React.Fragment>
  );
}

/* ---------- 演示 / Tweaks 面板（评审工具，保持中文） ---------- */
function DemoTweaks({ tweaks, setTweak, toast }) {
  return (
    <TweaksPanel title="Tweaks">
      <TweakSection label="演示场景（验收用）" />
      <TweakSelect label="场景" value={tweaks.scenario}
        options={["正常运行", "部分组件异常", "高负载（多会话）", "空数据（新部署）", "管理服务不可达"]}
        onChange={(v) => { setTweak("scenario", v); toast.ok("场景已切换：" + v); }} />
      <TweakToggle label="首次登录强制改密" value={tweaks.forceChange} onChange={(v) => setTweak("forceChange", v)} />
      <TweakButton label="模拟登录过期（下次请求 401）" onClick={() => { window.PLMock.expireSession(); toast.warn("下一个请求将返回 401"); }} />
      <TweakButton label="重置演示数据" onClick={() => { window.PLMock.reset(); toast.ok("演示数据已重置"); setTimeout(() => location.reload(), 600); }} />
    </TweaksPanel>
  );
}

ReactDOM.createRoot(document.getElementById("root")).render(<App />);
