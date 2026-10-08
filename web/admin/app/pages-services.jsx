/* Mynah 管理控制台 — 本地服务：容器化组件的状态与启停
 *
 * 后端 /services 走 supervisor 抽象（当前 Docker 实现，将来可换 k8s），
 * cored 自身不在可控列表里——停掉它等于自断手脚，只能 ssh 救回来。
 *
 * 这个页面的重点不是"有几个开关"，而是**说清楚停掉某个服务会坏掉什么**：
 * 最容易踩的坑是云端大脑模式下 ASR 看起来闲着（识别模型确实不跑），
 * 但它仍是全栈唯一的 Opus 解码器，停掉数字人就聋了。
 */

const STATE_META = {
  running:  { cls: "ok",   label: "运行中" },
  starting: { cls: "warn", label: "启动中" },
  stopped:  { cls: "muted",label: "已停止" },
  failed:   { cls: "err",  label: "异常退出" },
  missing:  { cls: "muted",label: "容器不存在" },
  unknown:  { cls: "muted",label: "状态未知" },
};

function StateDot({ state }) {
  const m = STATE_META[state] || STATE_META.unknown;
  return (
    <span className={"svc-state svc-" + m.cls}>
      <span className="svc-dot" />{t(m.label)}
    </span>
  );
}

function fmtUptime(iso) {
  if (!iso) return "";
  const ms = Date.now() - new Date(iso).getTime();
  if (!isFinite(ms) || ms < 0) return "";
  const m = Math.floor(ms / 60000);
  if (m < 60) return m + t(" 分钟");
  const h = Math.floor(m / 60);
  if (h < 24) return h + t(" 小时");
  return Math.floor(h / 24) + t(" 天");
}

const MODE_LABELS = {
  "decode-only": "精简模式 · 仅解码",
};

// 显存按 GB 显示：MiB 的精度在这里没意义，7700 和 7.5GB 说的是同一件事，
// 后者一眼能和 24GB 一张卡对上。
function fmtVram(mb) {
  if (!mb) return "";
  return (mb / 1024).toFixed(1) + "GB";
}

// gpu < 0 有两种含义，不能都写成"不占用 GPU"：ASR 精简模式确实不加载模型，
// 而口型引擎必然吃一张卡——那里的 -1 只说明没读到（inspect 失败/容器已删），
// 说成"不占用"就是在报假消息。
function gpuText(svc) {
  if (svc.gpu >= 0) return "GPU" + svc.gpu;
  if (svc.engine) return t("GPU 未知");
  return t("不占用 GPU");
}

function ServiceCard({ svc, busy, onAction }) {
  const running = svc.state === "running" || svc.state === "starting";
  const cold = svc.cold_start_seconds || 0;
  const coldMin = cold >= 90 ? Math.round(cold / 60) : 0;
  // note 里以 ⚠ 开头 = 配置冲突，用告警色而非提示色。
  const noteIsWarning = !!svc.note && svc.note.indexOf("⚠") === 0;

  function confirmAndRun(action) {
    let msg = "";
    if (action === "stop") {
      msg = t("确定停止「") + t(svc.label) + t("」？") + "\n\n" + t(svc.note || svc.description);
      if (coldMin) msg += "\n\n" + t("注意：重新启动需要约 ") + coldMin + t(" 分钟（模型加载）。");
    } else if (action === "restart") {
      msg = t("确定重启「") + t(svc.label) + t("」？");
      if (cold) msg += "\n\n" + t("期间不可用约 ") + (coldMin ? coldMin + t(" 分钟") : cold + t(" 秒")) + "。";
    }
    if (msg && !window.confirm(msg)) return;
    onAction(svc.name, action);
  }

  return (
    <div className="card card-pad svc-card">
      <div className="row" style={{ justifyContent: "space-between", alignItems: "flex-start", gap: 12 }}>
        <div style={{ minWidth: 0 }}>
          <div className="row" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
            <h4 style={{ margin: 0 }}>{t(svc.label)}</h4>
            <StateDot state={svc.state} />
            {/* 引擎名不做徽标：它已经在标题里了，而标题才是停止/重启确认框里
                出现的那个字符串——那里认错引擎的代价最高。 */}
            {svc.mode ? (
              <span className="badge ok">{t(MODE_LABELS[svc.mode] || svc.mode)}</span>
            ) : null}
            {svc.needed === false ? (
              <span className="badge">{t("当前模式下未使用")}</span>
            ) : null}
          </div>
          <div className="muted small" style={{ marginTop: 6 }}>
            {svc.port ? ":" + svc.port : ""}
            {"  ·  " + gpuText(svc)}
            {/* 没有读数 ≠ 0：模型还在加载的 worker 不能显示成"没占显存"，
                否则看起来像是空闲的。缺读数就什么都不说。 */}
            {svc.vram_mb ? "  ·  " + t("显存 ") + fmtVram(svc.vram_mb) : ""}
            {running && svc.started_at ? "  ·  " + t("已运行 ") + fmtUptime(svc.started_at) : ""}
          </div>
        </div>
        <div className="row" style={{ gap: 8, flexShrink: 0 }}>
          {running ? (
            <React.Fragment>
              <button className="btn" disabled={busy || !svc.controllable}
                onClick={() => confirmAndRun("restart")}>{t("重启")}</button>
              <button className="btn btn-danger" disabled={busy || !svc.controllable}
                onClick={() => confirmAndRun("stop")}>{t("停止")}</button>
            </React.Fragment>
          ) : (
            <button className="btn btn-primary" disabled={busy || !svc.controllable || svc.state === "missing"}
              onClick={() => confirmAndRun("start")}>{t("启动")}</button>
          )}
        </div>
      </div>

      <div className="muted small" style={{ marginTop: 10, lineHeight: 1.6 }}>
        {t(svc.description)}
      </div>

      {svc.note ? (
        <div className={"banner " + (noteIsWarning ? "warn" : svc.needed === false ? "info" : "warn")}
             style={{ marginTop: 10, padding: "6px 10px" }}>
          <Icon name={svc.needed === false && !noteIsWarning ? "info" : "alert"} size={13} />{t(svc.note)}
        </div>
      ) : null}

      {svc.detail && svc.state !== "running" ? (
        <div className="muted small" style={{ marginTop: 8, fontFamily: "var(--mono)" }}>{svc.detail}</div>
      ) : null}
    </div>
  );
}

/* ---- 引擎池组合 ----
 * 目的不是"给个数字"，而是把「为什么上限是这个」摊开：显存预算的算式直接
 * 显示出来，用户不用再人肉算。
 *
 * 池是「组合」而不是「一个引擎 + 一个数字」：musetalk 服务已烘焙的 720×1280
 * 形象、flashhead 服务单张肖像，两者可以同时在线。派发按 worker 的引擎过滤
 * （频道的引擎由它绑定的形象派生），所以混合池是正常状态，不是故障。 */
function CapacityCard() {
  const [data, setData] = React.useState(null);
  const [err, setErr] = React.useState(null);
  const [busy, setBusy] = React.useState(false);
  const [want, setWant] = React.useState(null);   // {engine: n}，null = 跟随实际
  const toast = useToast();

  const load = React.useCallback(() => {
    return PL.avatarCapacity()
      .then((d) => { setData(d); setWant(null); })
      .catch((e) => setErr(e));
  }, []);

  React.useEffect(() => { load(); }, [load]);

  if (err) return null;              // 容量信息拿不到不该挡住服务面板
  if (!data) return null;

  const devices = data.devices || [];
  const engines = data.engines || [];
  const cur = data.composition || {};
  const quota = data.per_engine_quota || [];
  const unbound = data.unbound_channels || [];
  const running = data.pool_running || 0;
  // 编辑中的目标组合。以实际组合为底，这样只动一个引擎不会把另一个抹成 0。
  const target = want || cur;
  const countOf = (name) => Math.max(0, parseInt(target[name] || 0, 10));
  const totalTarget = engines.reduce((n, e) => n + countOf(e.name), 0);
  const dirty = engines.some((e) => countOf(e.name) !== (cur[e.name] || 0));
  // 每引擎的显存上限是「只跑它」时的上限，混合时会更低；真正的判定在后端
  // PlanMix，这里只做一个明显超标的提示。
  const overOne = engines.some(
    (e) => typeof e.max_workers === "number" && countOf(e.name) > e.max_workers);

  function setCount(name, n) {
    setWant(Object.assign({}, target, { [name]: Math.max(0, n) }));
  }

  async function apply(workers, okMsg) {
    setBusy(true);
    try {
      const res = await PL.avatarPoolCompose(workers);
      if (res && res.changed === false) toast.ok(t("组合未变"));
      else toast.ok(okMsg || t("已下发：") + (res && res.describe ? res.describe : ""));
      if (res && res.placement) toast.ok(res.placement);
      await load();
      if (res && res.note) toast.ok(t(res.note));
    } catch (e) {
      // 409 携带的是算式 / 缺哪个素材 / 哪个频道的配额，直接展示原文比套模板有用
      toast.error(PL.errText(e, {}), e.msgRaw);
    }
    setBusy(false);
  }

  async function onlyEngine(e) {
    const size = Math.max(1, countOf(e.name) || running || 1);
    const msg = t("确定让池只跑「") + e.label + t("」？") + "\n\n" +
      t("其他引擎的 worker 会被退役，目标 ") + size + t(" 路。") + "\n" +
      t("退役与新建交错进行，池不会归零；新引擎冷启动约 ") + e.cold_start_sec + t(" 秒。");
    if (!window.confirm(msg)) return;
    await apply({ [e.name]: size }, t("已下发：只跑 ") + e.label);
  }

  return (
    <div className="card card-pad" style={{ marginBottom: 14 }}>
      <div className="row" style={{ gap: 8, alignItems: "center", marginBottom: 4 }}>
        <h4 style={{ margin: 0 }}>{t("引擎池组合")}</h4>
        <span className="muted small">{data.describe || t("空池")}</span>
      </div>
      <div className="muted small" style={{ marginBottom: 12 }}>
        {t("并发上限 = 引擎池 worker 数，而池上限由剩余显存决定。" +
           "两个引擎可以同时在线：频道的引擎由它绑定的形象派生，派发只会落到能渲染该形象的 worker 上。")}
      </div>

      {!data.gpu_available ? (
        <div className="banner warn" style={{ padding: "6px 10px" }}>
          <Icon name="alert" size={13} />
          {t("GPU 信息不可用：") + (data.gpu_reason || t("未检测到 nvidia-smi"))}
        </div>
      ) : (
        <React.Fragment>
          {unbound.length && (data.pool_engines || []).length > 1 ? (
            <div className="banner warn" style={{ marginBottom: 12, padding: "8px 12px" }}>
              <Icon name="alert" size={14} />
              {t("有 ") + unbound.length + t(" 个在线频道没绑形象（") + unbound.join(", ") +
               t("）。这类频道任何引擎都能接，而池里现在有 ") +
               (data.pool_engines || []).join(" / ") +
               t(" 两种引擎，不同引擎驱动的素材通常不是同一个人——" +
                 "访客可能看到不同的形象。给这些频道绑定形象即可消除。")}
            </div>
          ) : null}

          <div style={{ marginBottom: 14 }}>
            <div className="row" style={{ gap: 10, alignItems: "flex-end", flexWrap: "wrap" }}>
              {engines.filter((e) => e.selectable || (cur[e.name] || 0) > 0).map((e) => (
                <div key={e.name}>
                  <div className="muted small" style={{ marginBottom: 4 }}>{e.label}</div>
                  <input className="input" type="number" min={0} max={8}
                    style={{ width: 76 }} value={countOf(e.name)}
                    disabled={busy || !e.selectable}
                    onChange={(ev) => setCount(e.name, parseInt(ev.target.value || "0", 10))} />
                </div>
              ))}
              <button className="btn btn-primary" disabled={busy || !dirty}
                onClick={() => apply(target)}>{t("应用")}</button>
            </div>
            <div className="muted small" style={{ marginTop: 6 }}>
              {t("当前实际 ") + running + t(" 路（可派发 ") + (data.pool_dispatch || 0) +
               t(" 路）· 目标共 ") + totalTarget + t(" 路 · 池上限 8 路")}
            </div>
            {overOne ? (
              <div className="muted small" style={{ marginTop: 4 }}>
                {t("某个引擎的路数已超过它单独占满显存时的上限，应用时会被拒并给出算式。")}
              </div>
            ) : null}
          </div>

          <table className="table" style={{ marginBottom: 14 }}>
            <thead>
              <tr>
                <th>{t("显卡")}</th><th>{t("总量")}</th>
                <th>{t("引擎池占用")}</th><th>{t("其他占用")}</th>
                <th>{t("安全余量")}</th><th>{t("可分配")}</th>
              </tr>
            </thead>
            <tbody>
              {devices.map((d) => {
                const usable = d.total_mb - d.used_by_others_mb - d.reserve_mb;
                return (
                  <tr key={d.index}>
                    <td>GPU{d.index}</td>
                    <td>{gb(d.total_mb)}</td>
                    <td>{gb(d.used_by_pool_mb)}</td>
                    <td>{gb(d.used_by_others_mb)}</td>
                    <td className="muted">{gb(d.reserve_mb)}</td>
                    <td><strong>{gb(usable > 0 ? usable : 0)}</strong></td>
                  </tr>
                );
              })}
            </tbody>
          </table>

          <div className="muted small" style={{ marginBottom: 8 }}>
            {t("「引擎池占用」在计算时算作可回收——缩放池子时这部分显存会还回来，" +
               "不计入占用，否则池子就只能缩不能扩。")}
          </div>

          {quota.length ? (
            <React.Fragment>
              <h5 style={{ margin: "14px 0 8px" }}>{t("按引擎的并发配额")}</h5>
              <table className="table" style={{ marginBottom: 8 }}>
                <thead>
                  <tr>
                    <th>{t("引擎")}</th><th>{t("可派发 worker")}</th>
                    <th>{t("已分配并发")}</th><th>{t("绑定它的频道")}</th>
                  </tr>
                </thead>
                <tbody>
                  {quota.map((q) => (
                    <tr key={q.engine}>
                      <td>{q.label}</td>
                      <td>{q.workers}</td>
                      <td>
                        <strong>{q.allocated}</strong>
                        {q.over ? <span className="badge warn" style={{ marginLeft: 6 }}>{t("超配")}</span> : null}
                      </td>
                      <td className="muted small">{(q.channels || []).join(", ") || "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <div className="muted small" style={{ marginBottom: 8 }}>
                {t("绑定某引擎的频道只能落到该引擎的 worker 上，所以这一列要单独够；" +
                   "此外全部在线频道之和（") + (data.quota_allocated || 0) +
                 t(" 路）还要不超过池总数（") + (data.quota_total || 0) + t(" 路）。") +
                 (unbound.length ? t("其中未绑形象的频道：") + unbound.join(", ") : "")}
              </div>
            </React.Fragment>
          ) : null}

          <h5 style={{ margin: "14px 0 8px" }}>{t("可选引擎")}</h5>
          {engines.map((e) => (
            <div key={e.name} className="engine-row">
              <div className="row" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
                <strong>{e.label}</strong>
                {(cur[e.name] || 0) > 0 ? (
                  <span className="badge ok">{t("在线 ") + cur[e.name] + t(" 路")}</span>
                ) : null}
                {!e.selectable ? <span className="badge">{t("不可用")}</span> : null}
                {e.needs_bake ? <span className="badge">{t("需烘焙素材")}</span> : null}
              </div>
              {e.selectable ? (
                <div className="muted small" style={{ marginTop: 4 }}>
                  {gb(e.vram_steady_mb) + t("/路") +
                   (typeof e.max_workers === "number" ? "  ·  " + t("独占最多 ") + e.max_workers + t(" 路") : "") +
                   "  ·  " + t("冷启动 ") + e.cold_start_sec + "s"}
                </div>
              ) : (
                <div className="muted small" style={{ marginTop: 4 }}>{t(e.unavailable_reason || "")}</div>
              )}
              {e.explain ? (
                <div className="muted small mono-hint">{e.explain}</div>
              ) : null}
              {e.notes ? <div className="muted small" style={{ marginTop: 4 }}>{t(e.notes)}</div> : null}
              {e.selectable && !(Object.keys(cur).length === 1 && (cur[e.name] || 0) > 0) ? (
                <button className="btn" style={{ marginTop: 8 }} disabled={busy}
                  onClick={() => onlyEngine(e)}>{t("只跑此引擎")}</button>
              ) : null}
            </div>
          ))}
          <div className="muted small" style={{ marginTop: 10 }}>
            {t("应用前会依次校验：素材是否就绪、显存是否够（每路不跨卡，按卡装箱）、" +
               "以及新组合是否仍满足每个在线频道的并发配额。三关都给算式，不给「操作失败」。")}
          </div>
        </React.Fragment>
      )}
    </div>
  );
}

function gb(mb) {
  if (!mb || mb <= 0) return "0";
  return (mb / 1024).toFixed(1) + "G";
}

function ServicesPage() {
  const [data, setData] = React.useState(null);
  const [error, setError] = React.useState(null);
  const [loading, setLoading] = React.useState(true);
  const [busy, setBusy] = React.useState("");
  const toast = useToast();

  const load = React.useCallback(async (quiet) => {
    if (!quiet) setLoading(true);
    try {
      setData(await PL.services());
      setError(null);
    } catch (e) { setError(e); }
    if (!quiet) setLoading(false);
  }, []);

  React.useEffect(() => { load(); }, [load]);

  /* 启停是异步的（容器起来到端口就绪还有几十秒到几分钟），所以有动作在飞时
     加快轮询，让状态自己收敛，用户不用手动刷新。 */
  React.useEffect(() => {
    const anyTransient = data && (data.services || []).some(
      (s) => s.state === "starting" || s.state === "unknown");
    const period = busy || anyTransient ? 3000 : 10000;
    const id = setInterval(() => load(true), period);
    return () => clearInterval(id);
  }, [load, busy, data]);

  async function act(name, action) {
    setBusy(name);
    try {
      await PL.serviceAction(name, action);
      toast.ok(t("已下发指令，状态稍后刷新"));
      await load(true);
    } catch (e) {
      toast.error(PL.errText(e, { 400: "该服务不可控制", 502: "Docker 操作失败" }), e.msgRaw);
    }
    setBusy("");
  }

  const services = (data && data.services) || [];
  const brain = data && data.brain_mode;
  const turn = data && data.turn_mode;

  return (
    <div className="fade-in" data-screen-label="本地服务">
      <div className="page-head">
        <div>
          <h1>{t("本地服务")}</h1>
          <div className="desc">
            {t("数字人依赖的本地组件。cored 自身不在此处管理——停掉它控制台会一起失联。")}
          </div>
        </div>
        <button className="btn" onClick={() => load()}>{t("刷新")}</button>
      </div>

      {brain ? (
        <div className="banner info" style={{ marginBottom: 16, padding: "8px 12px" }}>
          <Icon name="info" size={14} />
          {t("当前：对话大脑 = ") + t(brain === "qwen" ? "Qwen 云端实时" : "本地管线") +
           t("，语音轮次 = ") +
           t(turn === "smart_turn" ? "云端·语义判停" : turn === "server_vad" ? "云端·声学判停" : "本地判轮次") +
           t("。下方标注「当前模式下未使用」的服务可以停掉省资源，但请先看清提示。")}
        </div>
      ) : null}

      {loading ? <Loading rows={4} /> :
       error ? <ErrorState error={error} onRetry={() => load()} /> :
       services.length === 0 ? (
         <EmptyState title={t("没有可管理的服务")}
           desc={t("cored 未连上 Docker，或本机组件不是以容器方式运行。")} />
       ) : (
        <React.Fragment>
          <CapacityCard />
          <div className="svc-grid">
            {services.map((s) => (
              <ServiceCard key={s.name} svc={s} busy={busy === s.name} onAction={act} />
            ))}
          </div>
        </React.Fragment>
      )}
    </div>
  );
}

Object.assign(window, { ServicesPage });
