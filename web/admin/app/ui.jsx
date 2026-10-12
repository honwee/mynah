/* Mynah 管理控制台 — 共享 UI 组件 */
const { useState, useEffect, useRef, useContext, createContext, useCallback } = React;

/* ---------- 图标 ---------- */
const ICON_PATHS = {
  dashboard: <g><rect x="3" y="3" width="7" height="7" rx="1.5"></rect><rect x="14" y="3" width="7" height="7" rx="1.5"></rect><rect x="3" y="14" width="7" height="7" rx="1.5"></rect><rect x="14" y="14" width="7" height="7" rx="1.5"></rect></g>,
  book: <g><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"></path><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"></path></g>,
  sliders: <g><line x1="4" y1="21" x2="4" y2="14"></line><line x1="4" y1="10" x2="4" y2="3"></line><line x1="12" y1="21" x2="12" y2="12"></line><line x1="12" y1="8" x2="12" y2="3"></line><line x1="20" y1="21" x2="20" y2="16"></line><line x1="20" y1="12" x2="20" y2="3"></line><line x1="1" y1="14" x2="7" y2="14"></line><line x1="9" y1="8" x2="15" y2="8"></line><line x1="17" y1="16" x2="23" y2="16"></line></g>,
  monitor: <g><rect x="2" y="3" width="20" height="14" rx="2"></rect><line x1="8" y1="21" x2="16" y2="21"></line><line x1="12" y1="17" x2="12" y2="21"></line></g>,
  server: <g><rect x="2" y="2" width="20" height="8" rx="2"></rect><rect x="2" y="14" width="20" height="8" rx="2"></rect><line x1="6" y1="6" x2="6.01" y2="6"></line><line x1="6" y1="18" x2="6.01" y2="18"></line></g>,
  search: <g><circle cx="11" cy="11" r="8"></circle><line x1="21" y1="21" x2="16.65" y2="16.65"></line></g>,
  upload: <g><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="17 8 12 3 7 8"></polyline><line x1="12" y1="3" x2="12" y2="15"></line></g>,
  trash: <g><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></g>,
  edit: <g><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path></g>,
  refresh: <g><polyline points="23 4 23 10 17 10"></polyline><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"></path></g>,
  plus: <g><line x1="12" y1="5" x2="12" y2="19"></line><line x1="5" y1="12" x2="19" y2="12"></line></g>,
  x: <g><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></g>,
  check: <g><polyline points="20 6 9 17 4 12"></polyline></g>,
  alert: <g><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></g>,
  key: <g><path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3-3.5 3.5z"></path></g>,
  logout: <g><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"></path><polyline points="16 17 21 12 16 7"></polyline><line x1="21" y1="12" x2="9" y2="12"></line></g>,
  user: <g><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"></path><circle cx="12" cy="7" r="4"></circle></g>,
  chevronDown: <g><polyline points="6 9 12 15 18 9"></polyline></g>,
  chevronRight: <g><polyline points="9 6 15 12 9 18"></polyline></g>,
  chevronLeft: <g><polyline points="15 18 9 12 15 6"></polyline></g>,
  mic: <g><path d="M12 1a3 3 0 0 0-3 3v8a3 3 0 0 0 6 0V4a3 3 0 0 0-3-3z"></path><path d="M19 10v2a7 7 0 0 1-14 0v-2"></path><line x1="12" y1="19" x2="12" y2="23"></line><line x1="8" y1="23" x2="16" y2="23"></line></g>,
  message: <g><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"></path></g>,
  file: <g><path d="M13 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"></path><polyline points="13 2 13 9 20 9"></polyline></g>,
  eye: <g><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path><circle cx="12" cy="12" r="3"></circle></g>,
  eyeOff: <g><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"></path><line x1="1" y1="1" x2="23" y2="23"></line></g>,
  power: <g><path d="M18.36 6.64a9 9 0 1 1-12.73 0"></path><line x1="12" y1="2" x2="12" y2="12"></line></g>,
  info: <g><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="16" x2="12" y2="12"></line><line x1="12" y1="8" x2="12.01" y2="8"></line></g>,
  zap: <g><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"></polygon></g>,
  clock: <g><circle cx="12" cy="12" r="10"></circle><polyline points="12 6 12 12 16 14"></polyline></g>,
  inbox: <g><polyline points="22 12 16 12 14 15 10 15 8 12 2 12"></polyline><path d="M5.45 5.11L2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"></path></g>,
  external: <g><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path><polyline points="15 3 21 3 21 9"></polyline><line x1="10" y1="14" x2="21" y2="3"></line></g>,
  wifiOff: <g><line x1="1" y1="1" x2="23" y2="23"></line><path d="M16.72 11.06A10.94 10.94 0 0 1 19 12.55"></path><path d="M5 12.55a10.94 10.94 0 0 1 5.17-2.39"></path><path d="M10.71 5.05A16 16 0 0 1 22.58 9"></path><path d="M1.42 9a15.91 15.91 0 0 1 4.7-2.88"></path><path d="M8.53 16.11a6 6 0 0 1 6.95 0"></path><line x1="12" y1="20" x2="12.01" y2="20"></line></g>,
  droplet: <g><path d="M12 2.69l5.66 5.66a8 8 0 1 1-11.31 0z"></path></g>,
  globe: <g><circle cx="12" cy="12" r="10"></circle><line x1="2" y1="12" x2="22" y2="12"></line><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"></path></g>,
  layers: <g><polygon points="12 2 2 7 12 12 22 7 12 2"></polygon><polyline points="2 17 12 22 22 17"></polyline><polyline points="2 12 12 17 22 12"></polyline></g>,
  play: <g><polygon points="5 3 19 12 5 21 5 3"></polygon></g>,
  lock: <g><rect x="3" y="11" width="18" height="11" rx="2"></rect><path d="M7 11V7a5 5 0 0 1 10 0v4"></path></g>,
};
function Icon({ name, size = 16, style }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" style={style} aria-hidden="true">
      {ICON_PATHS[name] || null}
    </svg>
  );
}

/* ---------- 品牌标 ---------- */
function BrandMark({ size = 30 }) {
  // Mynah 主标（assets/mynah-mark.png，自带天蓝圆角底板，透明外边），深浅背景都可读。
  return (
    <img className="brand-mark" src="assets/mynah-mark.png" alt="Mynah" style={{ width: size, height: size, display: "block" }} />
  );
}

/* ---------- 基础控件 ---------- */
function Btn({ kind = "secondary", size, icon, children, loading, ...rest }) {
  return (
    <button className={"btn btn-" + kind + (size ? " btn-" + size : "")} disabled={loading || rest.disabled} {...rest}>
      {loading ? <span className="spinner" style={{ borderTopColor: "currentColor", opacity: 0.7 }}></span> : (icon ? <Icon name={icon} size={14} /> : null)}
      {children}
    </button>
  );
}
function IconBtn({ icon, tip, danger, ...rest }) {
  const btn = <button className={"btn-icon btn" + (danger ? " danger" : "")} {...rest}><Icon name={icon} size={15} /></button>;
  return tip ? <span className="tip" data-tip={tip}>{btn}</span> : btn;
}
function Switch({ checked, onChange, disabled, accent, busy }) {
  return (
    <label className={"switch" + (accent ? " accent" : "")} style={busy ? { opacity: 0.6, pointerEvents: "none" } : null}>
      <input type="checkbox" checked={!!checked} disabled={disabled} onChange={(e) => onChange && onChange(e.target.checked)} />
      <span className="track"></span>
    </label>
  );
}
function Field({ label, required, help, error, children, style }) {
  return (
    <div className="field" style={style}>
      {label ? <label className="field-label">{label}{required ? <span className="req">*</span> : null}</label> : null}
      {children}
      {error ? <div className="field-err">{error}</div> : (help ? <div className="field-help">{help}</div> : null)}
    </div>
  );
}
function PasswordInput({ value, onChange, placeholder, autoFocus }) {
  const [show, setShow] = useState(false);
  return (
    <div className="input-wrap">
      <input className="input" type={show ? "text" : "password"} value={value} placeholder={placeholder} autoFocus={autoFocus}
        onChange={(e) => onChange(e.target.value)} style={{ paddingRight: 38 }} autoComplete="off" />
      <span className="input-suffix">
        <button type="button" className="btn-icon btn" tabIndex={-1} onClick={() => setShow(!show)} style={{ width: 26, height: 26 }}>
          <Icon name={show ? "eyeOff" : "eye"} size={14} />
        </button>
      </span>
    </div>
  );
}
function Slider({ value, onChange, min = 0, max = 1, step = 0.05, format }) {
  const pct = ((value - min) / (max - min)) * 100;
  return (
    <div className="slider-row">
      <input type="range" className="slider" min={min} max={max} step={step} value={value}
        style={{ "--fill": pct + "%" }}
        onChange={(e) => onChange(parseFloat(e.target.value))} />
      <span className="slider-val">{format ? format(value) : value}</span>
    </div>
  );
}
function Tabs({ items, active, onChange }) {
  return (
    <div className="tabs" role="tablist">
      {items.map((it) => (
        <button key={it.key} role="tab" className={"tab" + (active === it.key ? " active" : "")} onClick={() => onChange(it.key)}>
          {it.label}
        </button>
      ))}
    </div>
  );
}

/* ---------- 弹窗 ---------- */
function Modal({ title, onClose, children, footer, width }) {
  useEffect(() => {
    const fn = (e) => { if (e.key === "Escape") onClose && onClose(); };
    document.addEventListener("keydown", fn);
    return () => document.removeEventListener("keydown", fn);
  }, [onClose]);
  return (
    <div className="modal-overlay" onMouseDown={(e) => { if (e.target === e.currentTarget && onClose) onClose(); }}>
      <div className="modal" style={width ? { width } : null} role="dialog" aria-label={title}>
        <div className="modal-head">
          <h3>{title}</h3>
          {onClose ? <button className="btn-icon btn" onClick={onClose}><Icon name="x" size={15} /></button> : null}
        </div>
        <div className="modal-body">{children}</div>
        {footer ? <div className="modal-foot">{footer}</div> : null}
      </div>
    </div>
  );
}

/* 二次确认框：danger + requireText（输入名称确认） */
function ConfirmDialog({ title, body, confirmText, danger, requireText, busy, onConfirm, onClose }) {
  const [typed, setTyped] = useState("");
  const blocked = requireText && typed !== requireText;
  return (
    <Modal title={title} onClose={busy ? null : onClose} footer={
      <React.Fragment>
        <Btn kind="ghost" onClick={onClose} disabled={busy}>{t("取消")}</Btn>
        <Btn kind={danger ? "danger" : "primary"} onClick={onConfirm} disabled={blocked} loading={busy}>{confirmText || t("确认")}</Btn>
      </React.Fragment>
    }>
      <div style={{ fontSize: 13.5, color: "var(--text-2)", lineHeight: 1.7 }}>{body}</div>
      {requireText ? (
        <div className="mt16">
          <Field label={<span>{t("请输入")} <b style={{ color: "var(--text)" }}>{requireText}</b> {t("以确认")}</span>}>
            <input className="input" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={requireText} />
          </Field>
        </div>
      ) : null}
    </Modal>
  );
}

/* ---------- Toast ---------- */
const ToastCtx = createContext(null);
function useToast() { return useContext(ToastCtx); }
function ToastProvider({ children }) {
  const [toasts, setToasts] = useState([]);
  const idRef = useRef(0);
  const push = useCallback((type, text, detail) => {
    const id = ++idRef.current;
    setToasts((t) => [...t, { id, type, text, detail }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), type === "error" ? 6000 : 3500);
  }, []);
  const api = {
    ok: (text) => push("ok", text),
    error: (text, detail) => push("error", text, detail),
    warn: (text) => push("warn", text),
  };
  return (
    <ToastCtx.Provider value={api}>
      {children}
      <div className="toast-stack">
        {toasts.map((t) => (
          <div key={t.id} className={"toast " + t.type}>
            <Icon name={t.type === "ok" ? "check" : t.type === "warn" ? "alert" : "alert"} size={15} />
            <div className="grow">
              <div>{t.text}</div>
              {t.detail ? <div className="toast-detail">{t.detail}</div> : null}
            </div>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

/* ---------- 四态 ---------- */
function Loading({ rows = 3 }) {
  return (
    <div style={{ padding: "8px 0" }}>
      {Array.from({ length: rows }).map((_, i) => <div key={i} className="skeleton-row" style={{ width: (88 - i * 14) + "%" }}></div>)}
    </div>
  );
}
function EmptyState({ icon = "inbox", title, desc, action }) {
  return (
    <div className="state-block">
      <div className="state-icon"><Icon name={icon} size={20} /></div>
      <h4>{title}</h4>
      {desc ? <p>{desc}</p> : null}
      {action || null}
    </div>
  );
}
function ErrorState({ error, onRetry }) {
  const network = error && error.network;
  return (
    <div className="state-block">
      <div className="state-icon" style={{ color: "var(--danger)", background: "var(--danger-bg)" }}><Icon name={network ? "wifiOff" : "alert"} size={20} /></div>
      <h4>{network ? t("无法连接管理服务") : t("加载失败")}</h4>
      <p>{network ? t("请确认 cored 正在运行，且管理端口可达") : (error && error.msgRaw) || t("请稍后重试")}</p>
      {onRetry ? <Btn kind="secondary" icon="refresh" onClick={onRetry}>{t("重试")}</Btn> : null}
    </div>
  );
}

/* ---------- 时间 / 字节 ---------- */
function fmtBytes(n) {
  if (n == null) return "-";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}
function fmtDate(s) {
  if (!s) return "-";
  const d = new Date(s);
  const p = (x) => String(x).padStart(2, "0");
  return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " + p(d.getHours()) + ":" + p(d.getMinutes());
}
function relTime(s) {
  const diff = Date.now() - new Date(s).getTime();
  const m = Math.floor(diff / 60000);
  if (m < 1) return t("刚刚");
  if (m < 60) return t("{0} 分钟前", m);
  const h = Math.floor(m / 60);
  if (h < 24) return t("{0} 小时前", h);
  return t("{0} 天前", Math.floor(h / 24));
}
function RelTime({ value }) {
  const [, tick] = useState(0);
  useEffect(() => { const t = setInterval(() => tick((x) => x + 1), 30000); return () => clearInterval(t); }, []);
  return <span className="tip" data-tip={fmtDate(value)} style={{ cursor: "default" }}>{relTime(value)}</span>;
}

/* ---------- Qwen 云端音色 ----------
   qwen-audio-3.0-realtime-flash 的系统音色全集（阿里云百炼文档）。
   仅 lingxin/lufeng 官方给了风格描述，其余试听后自辨；
   声音复刻 voice_id 走「自定义」手填。 */
const QWEN_VOICES = [
  { id: "longanqian", name: "龙安倩", desc: "默认" },
  { id: "longanlingxin", name: "龙安灵心", desc: "女 · 知心温暖" },
  { id: "longanlingxi", name: "龙安灵犀", desc: "" },
  { id: "longanxiaoxin", name: "龙安晓新", desc: "" },
  { id: "longanlufeng", name: "龙安鲁风", desc: "男 · 明亮开朗" },
];
function QwenVoiceSelect({ value, onChange, emptyLabel, model }) {
  const ids = QWEN_VOICES.map((v) => v.id);
  const [custom, setCustom] = useState(() => !!value && !ids.includes(value));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const audioRef = useRef(null);
  const sel = custom ? "__custom__" : (value || "");
  useEffect(() => () => {
    if (audioRef.current) { audioRef.current.pause(); URL.revokeObjectURL(audioRef.current.src); }
  }, []);
  async function audition() {
    setBusy(true); setErr(null);
    try {
      const url = await PL.qwenVoicePreview(value, model);
      if (audioRef.current) { audioRef.current.pause(); URL.revokeObjectURL(audioRef.current.src); }
      const a = new Audio(url);
      audioRef.current = a;
      a.onended = () => { URL.revokeObjectURL(url); if (audioRef.current === a) audioRef.current = null; };
      await a.play();
    } catch (e) {
      setErr(e && e.message ? e.message : t("试听失败"));
    } finally { setBusy(false); }
  }
  return (
    <div>
      <div className="row" style={{ gap: 8, alignItems: "center" }}>
        <select className="input" style={{ flex: 1 }} value={sel}
          onChange={(e) => {
            const v = e.target.value;
            if (v === "__custom__") { setCustom(true); onChange(""); }
            else { setCustom(false); onChange(v); }
          }}>
          {emptyLabel ? <option value="">{emptyLabel}</option> : null}
          {QWEN_VOICES.map((v) => (
            <option key={v.id} value={v.id}>{v.name}（{v.id}）{v.desc ? " · " + t(v.desc) : ""}</option>
          ))}
          <option value="__custom__">{t("声音复刻音色（手填 voice_id）")}</option>
        </select>
        <Btn size="sm" loading={busy} onClick={audition} disabled={busy}>{t("试听")}</Btn>
      </div>
      {custom ? (
        <input className="input mono" style={{ marginTop: 8 }} value={value}
          placeholder="qwen-audio-3.0-realtime-flash-mYvoice-xxxx"
          onChange={(e) => onChange(e.target.value)} />
      ) : null}
      {err ? <div className="field-err" style={{ marginTop: 6 }}>{err}</div> : null}
    </div>
  );
}

/* ---------- 文档状态徽标 ---------- */
function DocStatusBadge({ doc }) {
  if (doc.status === "pending") return <span className="badge muted"><span className="spinner" style={{ width: 10, height: 10, borderWidth: 1.5 }}></span>{t("排队中")}</span>;
  if (doc.status === "processing") return <span className="badge info"><span className="spinner" style={{ width: 10, height: 10, borderWidth: 1.5, borderTopColor: "var(--info)" }}></span>{t("处理中")}</span>;
  if (doc.status === "ready") return <span className="badge ok"><Icon name="check" size={11} />{t("就绪 · {0} 块", doc.chunk_count)}</span>;
  return (
    <span className="tip" data-tip={doc.error || t("未知错误")}>
      <span className="badge danger"><Icon name="alert" size={11} />{t("失败")}</span>
    </span>
  );
}

Object.assign(window, {
  Icon, BrandMark, Btn, IconBtn, Switch, Field, PasswordInput, Slider, Tabs,
  Modal, ConfirmDialog, ToastProvider, useToast,
  Loading, EmptyState, ErrorState,
  fmtBytes, fmtDate, relTime, RelTime, DocStatusBadge,
  QWEN_VOICES, QwenVoiceSelect,
});
