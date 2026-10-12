/* Mynah — 动作骨架（driving motion skeleton）前端模块【EE 功能】

   这是一个「按功能模块交付」的样板：整块功能装在一个文件里，靠 index.html 里这一条
   <script> 标签的有无决定交付/开源（搬仓=搬此文件+加标签）。它不改宿主页源码，而是
   经 window.PL_FEATURES 注册表把自己挂进「形象与声音」页：
     - tabs:  新增「动作」子 Tab（骨架库管理）
     - slots: 训练弹窗的动作选择器 + 形象 tab 的「为形象指定动作」面板
   宿主（pages-voices.jsx, OSS）只读注册表渲染；此文件缺席则这些入口无声消失。

   依赖的全局（ui.jsx / i18n.js / api.js 提供）：React, t, PL, Icon, Btn, Modal,
   Field, ConfirmDialog, useToast, EmptyState, Loading。必须在 main.jsx 之前加载。 */
(function () {
  var PLF = (window.PL_FEATURES = window.PL_FEATURES || { tabs: [], slots: {} });
  PLF.tabs = PLF.tabs || [];
  PLF.slots = PLF.slots || {};

  /* 内置动作预设：客观指标筛出的待机优等骨架（头部幅度/嘴动/眨眼/闭环）。ref 形如 builtin:<id>，
     pkl 已随 LivePortrait 发布；与上传的用户骨架（asset:<id>）共同构成可选动作。 */
  var BUILTIN_MOTIONS = [
    { id: "d14", name: "安静倾听", sub: "头部微动 · 静默待机最佳", ref: "builtin:d14" },
    { id: "d9", name: "自然活泼", sub: "头部转动 · 默认", ref: "builtin:d9" },
    { id: "d13", name: "轻微点头", sub: "极小幅度 · 循环最稳", ref: "builtin:d13" },
  ];

  function motionStatusMeta(s) {
    switch (s) {
      case "queued": return { label: t("排队中"), cls: "muted" };
      case "extracting": return { label: t("抽取中"), cls: "info" };
      case "done": return { label: t("已完成"), cls: "ok" };
      case "failed": return { label: t("失败"), cls: "danger" };
      case "canceled": return { label: t("已取消"), cls: "muted" };
      default: return { label: s, cls: "muted" };
    }
  }

  /* ref → 可读名（用于选择器回显） */
  function refLabel(ref, assets) {
    if (!ref) return t("默认");
    var b = BUILTIN_MOTIONS.find(function (m) { return m.ref === ref; });
    if (b) return t("内置 · ") + t(b.name);
    if (ref.indexOf("asset:") === 0) {
      var id = ref.slice(6);
      var a = (assets || []).find(function (x) { return String(x.id) === id; });
      return a ? a.name : t("已删除的动作");
    }
    return ref;
  }

  /* 选择器选项：内置预设 + 用户已完成骨架 */
  function motionOptions(assets) {
    var opts = BUILTIN_MOTIONS.map(function (m) { return { value: m.ref, label: t("内置 · ") + t(m.name) }; });
    (assets || []).filter(function (a) { return a.status === "done"; }).forEach(function (a) {
      opts.push({ value: "asset:" + a.id, label: a.name });
    });
    return opts;
  }

  /* done 骨架的指标徽章 */
  function StatChips(props) {
    var s = props.stats || {};
    var chips = [];
    if (s.head_deg != null) chips.push([t("头部"), s.head_deg + "°"]);
    if (s.lip_max != null) chips.push([t("嘴动"), Number(s.lip_max).toFixed(2)]);
    if (s.blink != null) chips.push([t("眨眼"), s.blink + t(" 次")]);
    if (s.n_frames != null) chips.push([t("帧"), String(s.n_frames)]);
    if (!chips.length) return null;
    return React.createElement("div", { className: "motion-stats" },
      chips.map(function (c, i) {
        return React.createElement("span", { className: "motion-stat", key: i },
          React.createElement("span", { className: "k" }, c[0]),
          React.createElement("span", { className: "v" }, c[1]));
      }));
  }

  /* 内置预设卡（只读：动作是共享库，指派在选择器里做） */
  function MotionCard(props) {
    var m = props.m;
    return (
      <div className="voice-card motion-card">
        <div className="voice-card-head">
          <div className="motion-card-thumb"><Icon name="film" size={22} /></div>
          <div className="grow" style={{ minWidth: 0 }}>
            <div className="v-name" title={t(m.name)}>{t(m.name)}</div>
            <div className="row" style={{ gap: 6, marginTop: 5, flexWrap: "wrap" }}>
              <span className="badge info">{t("内置")}</span>
              <span className="small muted">{t(m.sub)}</span>
            </div>
          </div>
        </div>
        <button className="btn btn-secondary v-play" disabled><Icon name="check" size={14} />{t("可指派给形象")}</button>
      </div>
    );
  }

  /* 上传动作弹窗：driving 视频 → 抽取骨架。镜像形象训练弹窗。 */
  function MotionUploadModal(props) {
    var s = React.useState(""), name = s[0], setName = s[1];
    var f = React.useState(null), file = f[0], setFile = f[1];
    var c = React.useState(false), consent = c[0], setConsent = c[1];
    var b = React.useState(false), busy = b[0], setBusy = b[1];
    var e = React.useState(null), error = e[0], setError = e[1];
    var toast = useToast();

    async function submit() {
      if (!name.trim()) { setError(t("请填写动作名称")); return; }
      if (!file) { setError(t("请选择一段动作视频")); return; }
      if (!consent) { setError(t("请确认你已获得该视频的使用授权")); return; }
      setBusy(true); setError(null);
      try {
        await PL.uploadMotion({
          video: file, name: name.trim(),
          consent: t("已确认：本人已获得该 driving 视频用于动作骨架抽取与合成的合法授权"),
        });
        toast.ok(t("已提交动作「{0}」，正在后台抽取", name.trim()));
        props.onSaved();
      } catch (err) {
        setError(PL.errText(err) + (err.msgRaw ? ": " + err.msgRaw : ""));
      }
      setBusy(false);
    }

    return (
      <Modal title={t("上传动作骨架")} width={540} onClose={busy ? null : props.onClose} footer={
        <React.Fragment>
          <Btn kind="ghost" onClick={props.onClose} disabled={busy}>{t("取消")}</Btn>
          <Btn kind="primary" onClick={submit} loading={busy}>{t("开始抽取")}</Btn>
        </React.Fragment>
      }>
        <div className="banner info" style={{ marginBottom: 14 }}>
          <Icon name="info" size={15} />
          {t("上传一段含自然头部与表情动作的真人视频（建议 5-30 秒、单人正脸）。系统抽取其动作轨迹生成可复用的「动作骨架」，可迁移到任意数字人形象——骨架只含运动轨迹，不保留任何人脸样貌。")}
        </div>
        <Field label={t("动作名称")} required error={error} help={t("便于辨认，如：温和点头-倾听")}>
          <input className="input" value={name} autoFocus maxLength={40}
            onChange={(ev) => { setName(ev.target.value); setError(null); }} placeholder={t("温和点头-倾听")} />
        </Field>
        <Field label={t("动作视频")} required help={t("支持 mp4 / mov / webm，单个文件 ≤ 200MB")}>
          <input className="input" type="file" accept="video/*"
            onChange={(ev) => { setFile((ev.target.files || [])[0] || null); setError(null); }} />
          {file ? (
            <div className="row" style={{ gap: 6, marginTop: 8 }}>
              <span className="badge muted" style={{ display: "inline-flex", alignItems: "center", gap: 6, maxWidth: 320 }}>
                <Icon name="film" size={12} />
                <span style={{ maxWidth: 240, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{file.name}</span>
              </span>
            </div>
          ) : null}
        </Field>
        <label className="row" style={{ gap: 8, alignItems: "flex-start", cursor: "pointer", marginTop: 6 }}>
          <input type="checkbox" checked={consent} onChange={(ev) => { setConsent(ev.target.checked); setError(null); }} style={{ marginTop: 3 }} />
          <span className="small muted">{t("我确认已获得该视频的合法授权，用于动作骨架抽取与合成，并承担相应法律责任。")}</span>
        </label>
      </Modal>
    );
  }

  /* 动作 Tab 主体：内置预设网格 + 我的动作（上传骨架）+ 上传/删除。open/setOpen 由宿主页头按钮控制。 */
  function MotionSection(props) {
    var a = React.useState(null), assets = a[0], setAssets = a[1]; // null=加载中
    var d = React.useState(null), delTarget = d[0], setDelTarget = d[1];
    var db = React.useState(false), delBusy = db[0], setDelBusy = db[1];
    var toast = useToast();

    var load = React.useCallback(async function (silent) {
      try { var r = await PL.listMotions(); setAssets((r && r.assets) || []); }
      catch (err) { if (!silent) setAssets([]); }
    }, []);
    React.useEffect(function () { load(); }, [load]);

    var live = (assets || []).some(function (x) { return x.status === "queued" || x.status === "extracting"; });
    React.useEffect(function () {
      if (!live) return;
      var id = setInterval(function () { load(true); }, 2000);
      return function () { clearInterval(id); };
    }, [live, load]);

    async function doDelete() {
      var j = delTarget; setDelBusy(true);
      try {
        await PL.deleteMotion(j.id);
        toast.ok(j.status === "queued" || j.status === "extracting" ? t("已取消动作「{0}」", j.name) : t("已删除动作「{0}」", j.name));
        setDelTarget(null); load(true);
      } catch (err) { toast.error(PL.errText(err), err.msgRaw); }
      setDelBusy(false);
    }

    return (
      <div className={props.embedded ? "" : "card card-pad avatar-section"}>
        <div className="card-title" style={{ marginTop: 0, marginBottom: 10 }}>
          {t("内置动作")}<span className="muted small" style={{ fontWeight: 400 }}>{BUILTIN_MOTIONS.length}</span>
        </div>
        <div className="voice-grid">
          {BUILTIN_MOTIONS.map(function (m) { return <MotionCard key={m.id} m={m} />; })}
        </div>

        <div className="card-title" style={{ marginTop: 18, marginBottom: 10 }}>
          {t("我的动作")}<span className="muted small" style={{ fontWeight: 400 }}>{(assets || []).length}</span>
        </div>
        {assets === null ? <Loading rows={2} /> :
          assets.length === 0 ? (
            <EmptyState icon="film" title={t("还没有自定义动作")}
              desc={t("点右上角「上传动作」，上传一段 driving 视频即可抽取专属动作骨架")}
              action={<Btn kind="primary" icon="plus" onClick={() => props.setOpen(true)}>{t("上传动作")}</Btn>} />
          ) : (
            <div className="train-jobs" style={{ display: "flex", flexDirection: "column", gap: 10 }}>
              {assets.map(function (j) {
                var m = motionStatusMeta(j.status);
                var running = j.status === "running" || j.status === "queued" || j.status === "extracting";
                return (
                  <div key={j.id} className="train-job-row" style={{ display: "flex", alignItems: "center", gap: 12, padding: "10px 12px", border: "1px solid var(--border)", borderRadius: 10 }}>
                    <Icon name="film" size={18} />
                    <div className="grow" style={{ minWidth: 0 }}>
                      <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
                        <span style={{ fontWeight: 600, maxWidth: 220, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={j.name}>{j.name}</span>
                        <span className={"badge " + m.cls}>{j.status === "extracting" ? <span className="spinner" style={{ marginRight: 4 }} /> : null}{m.label}</span>
                      </div>
                      {j.status === "extracting" ? (
                        <div className="train-bar" style={{ marginTop: 7, height: 6, borderRadius: 4, background: "var(--track, #eee)", overflow: "hidden" }}>
                          <div style={{ width: (j.progress || 0) + "%", height: "100%", background: "var(--accent)", transition: "width .4s" }} />
                        </div>
                      ) : null}
                      {j.status === "done" ? <StatChips stats={j.stats} /> : null}
                      {j.status === "failed" && j.error ? <div className="small" style={{ color: "var(--danger)", marginTop: 4 }}>{j.error}</div> : null}
                    </div>
                    {j.status === "extracting" ? <span className="small muted" style={{ flex: "none", fontVariantNumeric: "tabular-nums" }}>{(j.progress || 0) + "%"}</span> : null}
                    <Btn kind="ghost" size="sm" style={{ flex: "none" }} onClick={() => setDelTarget(j)}>{running ? t("取消") : t("删除")}</Btn>
                  </div>
                );
              })}
            </div>
          )}

        {props.open ? <MotionUploadModal onClose={() => props.setOpen(false)} onSaved={() => { props.setOpen(false); load(true); }} /> : null}
        {delTarget ? (
          <ConfirmDialog
            title={(delTarget.status === "queued" || delTarget.status === "extracting") ? t("取消动作「{0}」", delTarget.name) : t("删除动作「{0}」", delTarget.name)}
            body={(delTarget.status === "queued" || delTarget.status === "extracting") ? t("将中止该抽取任务，不可恢复。") : t("将删除该动作骨架及其上传的视频，不可恢复。")}
            confirmText={(delTarget.status === "queued" || delTarget.status === "extracting") ? t("取消抽取") : t("永久删除")} danger busy={delBusy}
            onConfirm={doDelete} onClose={() => setDelTarget(null)}
          />
        ) : null}
      </div>
    );
  }

  /* slot: 训练弹窗里的动作骨架选择器（{value,onChange}）。自取一次用户骨架列表。 */
  function TrainMotionField(props) {
    var a = React.useState([]), assets = a[0], setAssets = a[1];
    React.useEffect(function () {
      PL.listMotions().then(function (r) { setAssets((r && r.assets) || []); }).catch(function () {});
    }, []);
    var opts = motionOptions(assets);
    return (
      <Field label={t("动作骨架")} help={t("决定该形象的待机动作风格；可先在「动作」页上传自定义骨架")}>
        <select className="input" value={props.value} onChange={(e) => props.onChange(e.target.value)}>
          {opts.map(function (o) { return <option key={o.value} value={o.value}>{o.label}</option>; })}
        </select>
      </Field>
    );
  }

  /* slot: 形象 tab 里「当前形象的待机神态」单一控件（只管当前形象，不再是 per-avatar 表）。
     选动作 = 即时存偏好（config.avatar.motions[avatarId]，便宜）；点「重新生成待机画面」才真烘焙
     该形象的 idle 并热换上线——这是唯一会打到 worker 的入口。缓存命中时 bakeAvatar 首响应即 done、
     体感即时。props: {avatarId, onChanged, onApplying}（宿主回调，用于刷新 hero / 显示「应用中」）。 */
  function CurrentMotionControl(props) {
    var m = React.useState(null), motions = m[0], setMotions = m[1];
    var a = React.useState([]), assets = a[0], setAssets = a[1];
    var sv = React.useState(false), saving = sv[0], setSaving = sv[1];
    var bk = React.useState(null), baking = bk[0], setBaking = bk[1]; // {progress,status}
    var toast = useToast();
    var pollRef = React.useRef(null);

    React.useEffect(function () {
      PL.getConfigGroup("avatar").then(function (c) { setMotions((c && c.motions) || {}); }).catch(function () { setMotions({}); });
      PL.listMotions().then(function (r) { setAssets((r && r.assets) || []); }).catch(function () {});
      return function () { if (pollRef.current) clearInterval(pollRef.current); };
    }, []);

    var avatarId = props.avatarId || "default";
    var cur = (motions && motions[avatarId]) || "builtin:d14";

    async function setMotion(ref) {
      var next = Object.assign({}, motions); next[avatarId] = ref;
      setSaving(true);
      try { await PL.putConfig("avatar", { motions: next }); setMotions(next); }
      catch (err) { toast.error(PL.errText(err), err.msgRaw); }
      setSaving(false);
    }

    function finishOk() {
      setBaking(null);
      if (props.onApplying) props.onApplying(null);
      if (props.onChanged) props.onChanged();
      toast.ok(t("已应用到 live，待机神态已更新"));
    }

    /* 烘焙当前形象的 idle 并热换上线，轮询进度直到终态。 */
    async function regenerate() {
      setBaking({ progress: 0, status: "baking" });
      if (props.onApplying) props.onApplying(t("重新生成待机画面"));
      try {
        var job = await PL.bakeAvatar(avatarId, cur, true);
        var jid = job && job.id;
        if (!jid) throw new Error("no job id");
        if (job.status === "done") { finishOk(); return; } // 缓存命中 → 即时
        setBaking({ progress: job.progress || 0, status: job.status || "baking" });
        if (pollRef.current) clearInterval(pollRef.current);
        pollRef.current = setInterval(async function () {
          try {
            var s = await PL.getAvatarBake(jid);
            if (!s) return;
            setBaking({ progress: s.progress || 0, status: s.status });
            if (s.status === "done") { clearInterval(pollRef.current); pollRef.current = null; finishOk(); }
            else if (s.status === "failed" || s.status === "canceled") {
              clearInterval(pollRef.current); pollRef.current = null;
              setBaking(null); if (props.onApplying) props.onApplying(null);
              toast.error(s.error || t("烘焙失败"));
            }
          } catch (e) { /* 瞬时错误，下个 tick 重试 */ }
        }, 3000);
      } catch (err) {
        setBaking(null); if (props.onApplying) props.onApplying(null);
        toast.error(PL.errText(err), err.msgRaw);
      }
    }

    if (motions === null) return null;
    var opts = motionOptions(assets);
    return (
      <div className="motion-assign-row" style={{ marginTop: 4 }}>
        <select className="input" style={{ maxWidth: 260 }} value={cur} disabled={saving || !!baking}
          onChange={(e) => setMotion(e.target.value)}>
          {opts.map(function (o) { return <option key={o.value} value={o.value}>{o.label}</option>; })}
        </select>
        {saving ? <span className="spinner" /> : null}
        {baking ? (
          <span className="small muted" style={{ display: "inline-flex", alignItems: "center", gap: 6, flex: "none", fontVariantNumeric: "tabular-nums" }}>
            <span className="spinner" />{t("生成中")} {(baking.progress || 0) + "%"}
          </span>
        ) : (
          <Btn kind="secondary" size="sm" style={{ flex: "none" }} disabled={saving} onClick={regenerate}>
            <Icon name="refresh" size={13} />{t("重新生成待机画面")}
          </Btn>
        )}
      </div>
    );
  }

  /* slot: 动作库弹窗（上传/抽取骨架的管理界面，从形象 tab 的「管理动作库」链接打开）。 */
  function MotionLibraryModal(props) {
    var u = React.useState(false), upOpen = u[0], setUpOpen = u[1];
    return (
      <Modal title={t("动作库")} width={680} onClose={props.onClose} footer={
        <React.Fragment>
          <Btn kind="ghost" onClick={props.onClose}>{t("关闭")}</Btn>
          <Btn kind="primary" icon="plus" onClick={() => setUpOpen(true)}>{t("上传动作")}</Btn>
        </React.Fragment>
      }>
        <MotionSection open={upOpen} setOpen={setUpOpen} embedded />
      </Modal>
    );
  }

  /* ref → 给 hero 用的简短待机神态名（同步，无需异步加载 assets）。 */
  function motionLabel(ref) {
    if (!ref) return t("默认");
    var b = BUILTIN_MOTIONS.find(function (m) { return m.ref === ref; });
    if (b) return t(b.name);
    if (ref.indexOf("asset:") === 0) return t("自定义动作");
    return ref;
  }

  // ── 注册到宿主页 ──────────────────────────────────────────────────────────
  // 不再注册顶层「动作」tab：动作降级为形象 tab 里「当前形象的待机神态」单控件 +「动作库」弹窗。
  PLF.slots.trainMotionField = function (p) { return <TrainMotionField value={p.value} onChange={p.onChange} />; };
  PLF.slots.currentMotionControl = function (p) { return <CurrentMotionControl avatarId={p.avatarId} onChanged={p.onChanged} onApplying={p.onApplying} />; };
  PLF.slots.motionLibraryModal = function (p) { return <MotionLibraryModal onClose={p.onClose} />; };
  PLF.slots.motionLabel = motionLabel;
})();
