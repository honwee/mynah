/* Mynah 管理控制台 — 形象与声音 · 音色管理（EE）
   形象区（默认形象 + 企业版训练占位）+ 试听设置栏 + 音色卡片网格（内置/自定义）。
   上传克隆（可双参考）+ 试听（风格/语速/换一版）+ 删除。
   音色实体存 TTS 侧；上传后自动出现在配置中心音色下拉。*/

function isCustomVoice(v) { return v.id !== v.name; } // handleVoices 给自定义加了「（自定义）」后缀

const MAX_VOICE_CLIPS = 5;

/* 头像取色：按音色名确定性映射到一组克制的柔色，给每个音色一点身份感（非随机、刷新稳定） */
const VOICE_COLORS = [
  ["#eceefb", "#4a57c8"], ["#e7f5ed", "#1d8a52"], ["#fbeee4", "#bd611d"],
  ["#fbe9f0", "#c0356d"], ["#e4f2f5", "#1a8193"], ["#efeafa", "#7549c9"],
];
function voiceColor(name) {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return VOICE_COLORS[h % VOICE_COLORS.length];
}

/* 上传音色克隆 */
function VoiceUploadModal({ onClose, onSaved }) {
  const [name, setName] = React.useState("");
  const [files, setFiles] = React.useState([]); // 一段或多段参考音频，按顺序拼接
  const [consent, setConsent] = React.useState(false);
  const [refText, setRefText] = React.useState("");
  const [desc, setDesc] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [transcribing, setTranscribing] = React.useState(false);
  const [error, setError] = React.useState(null);
  const toast = useToast();

  // 按顺序识别所有片段，拼成与拼接音频对齐的参考文本
  async function runTranscribe(arr) {
    if (!arr || !arr.length) { setRefText(""); return; }
    setTranscribing(true);
    try {
      const parts = [];
      for (const f of arr) {
        const r = await PL.voiceTranscribe(f);
        if (r && r.text) parts.push(r.text.trim());
      }
      if (parts.filter(Boolean).length) { setRefText(parts.filter(Boolean).join(" ")); toast.ok(t("已自动识别参考文本，请校对")); }
      else toast.warn(t("未识别到语音，请手动填写参考文本"));
    } catch (e) {
      toast.warn(t("自动识别失败，请手动填写参考文本"));
    }
    setTranscribing(false);
  }

  function onPickFiles(picked) {
    if (!picked || !picked.length) return;
    const merged = files.slice();
    for (const f of picked) {
      if (!merged.some((x) => x.name === f.name && x.size === f.size)) merged.push(f);
    }
    if (merged.length > MAX_VOICE_CLIPS) { setError(t("最多 {0} 段参考音频", MAX_VOICE_CLIPS)); }
    const capped = merged.slice(0, MAX_VOICE_CLIPS);
    setFiles(capped); if (merged.length <= MAX_VOICE_CLIPS) setError(null);
    runTranscribe(capped);
  }
  function removeFile(i) {
    const next = files.filter((_, j) => j !== i);
    setFiles(next); setError(null); runTranscribe(next);
  }

  async function submit() {
    if (!/^[a-z0-9][a-z0-9_-]{1,39}$/.test(name)) { setError(t("音色标识需为 2-40 位小写字母、数字、下划线或连字符")); return; }
    if (!files.length) { setError(t("请选择参考音频文件")); return; }
    if (!consent) { setError(t("请确认你已获得该声音的克隆授权")); return; }
    setBusy(true); setError(null);
    try {
      await PL.voiceUpload({
        files: files, name: name,
        consent: t("已确认：本人已获得该声音用于克隆的合法授权"),
        ref_text: refText.trim(), speaker_description: desc.trim(),
      });
      toast.ok(t("音色「{0}」已克隆", name));
      onSaved();
    } catch (e) {
      setError(PL.errText(e) + (e.msgRaw ? ": " + e.msgRaw : ""));
    }
    setBusy(false);
  }

  return (
    <Modal title={t("克隆新音色")} width={540} onClose={busy ? null : onClose} footer={
      <React.Fragment>
        <Btn kind="ghost" onClick={onClose} disabled={busy}>{t("取消")}</Btn>
        <Btn kind="primary" onClick={submit} loading={busy}>{t("上传克隆")}</Btn>
      </React.Fragment>
    }>
      <div className="banner info" style={{ marginBottom: 14 }}>
        <Icon name="info" size={15} />
        {t("上传一段清晰的参考人声（建议 10-30 秒、单人、无背景噪音），即可零样本克隆该音色，用于数字人播报。")}
      </div>
      <Field label={t("音色标识")} required error={error} help={t("唯一英文标识，如 cs-female-01")}>
        <input className="input mono" value={name} autoFocus
          onChange={(e) => { setName(e.target.value.toLowerCase()); setError(null); }} placeholder="cs-female-01" />
      </Field>
      <Field label={t("参考音频")} required help={t("可一次选多段，系统会按顺序拼成更长参考、提升相似度；支持 wav/mp3/m4a 等（手机录音可直接传），单段≤20MB，最多 {0} 段", MAX_VOICE_CLIPS)}>
        <input className="input" type="file" accept="audio/*" multiple
          onChange={(e) => { onPickFiles(Array.from(e.target.files || [])); e.target.value = ""; }} />
        {files.length ? (
          <div className="row" style={{ gap: 6, marginTop: 8, flexWrap: "wrap" }}>
            {files.map((f, i) => (
              <span key={i} className="badge muted" style={{ display: "inline-flex", alignItems: "center", gap: 6, maxWidth: 220 }}>
                <Icon name="mic" size={12} />
                <span className="ellipsis" style={{ maxWidth: 150, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{f.name}</span>
                <a onClick={() => removeFile(i)} title={t("移除")} style={{ cursor: "pointer", fontWeight: 700 }}>×</a>
              </span>
            ))}
          </div>
        ) : null}
      </Field>
      <Field label={t("参考文本（强烈建议）")} help={t("录音的逐字稿，决定克隆相似度；选音频后会自动识别，请校对修正")}>
        <textarea className="input" rows={2} value={refText} onChange={(e) => setRefText(e.target.value)}
          placeholder={transcribing ? t("识别中…") : t("录音里说的原话（自动识别后可修改）")} disabled={transcribing} />
        <div className="row" style={{ gap: 8, marginTop: 6, alignItems: "center" }}>
          <Btn kind="ghost" size="sm" icon="refresh" loading={transcribing}
            disabled={!files.length || transcribing} onClick={() => runTranscribe(files)}>{t("重新识别")}</Btn>
          {!refText.trim() && !transcribing ? <span className="small" style={{ color: "var(--warn, #b8860b)" }}>{t("留空会明显降低克隆相似度")}</span> : null}
        </div>
      </Field>
      <Field label={t("音色描述")} help={t("选填，如：成熟稳重的男声")}>
        <input className="input" value={desc} onChange={(e) => setDesc(e.target.value)} placeholder={t("（选填）温柔的女声")} />
      </Field>
      <label className="row" style={{ gap: 8, alignItems: "flex-start", cursor: "pointer", marginTop: 6 }}>
        <input type="checkbox" checked={consent} onChange={(e) => { setConsent(e.target.checked); setError(null); }} style={{ marginTop: 3 }} />
        <span className="small muted">{t("我确认已获得该声音所有者的合法授权，用于克隆与合成，并承担相应法律责任。")}</span>
      </label>
    </Modal>
  );
}

/* 上传真人视频，训练专属数字人形象（EE）。训练在后台进行，本页轮询进度。 */
function AvatarTrainModal({ onClose, onSaved }) {
  const [name, setName] = React.useState("");
  const [file, setFile] = React.useState(null);
  const [consent, setConsent] = React.useState(false);
  const [motionRef, setMotionRef] = React.useState("builtin:d9"); // 动作骨架（EE 模块经 slot 注入选择器；默认活泼档）
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const toast = useToast();

  async function submit() {
    if (!name.trim()) { setError(t("请填写形象名称")); return; }
    if (!file) { setError(t("请选择一段真人视频")); return; }
    if (!consent) { setError(t("请确认你已获得该人物的形象训练授权")); return; }
    setBusy(true); setError(null);
    try {
      await PL.trainAvatar({
        video: file, name: name.trim(), motion_ref: motionRef,
        consent: t("已确认：本人已获得该人物形象用于训练与合成的合法授权"),
      });
      toast.ok(t("已提交形象训练「{0}」，正在后台进行", name.trim()));
      onSaved();
    } catch (e) {
      setError(PL.errText(e) + (e.msgRaw ? ": " + e.msgRaw : ""));
    }
    setBusy(false);
  }

  return (
    <Modal title={t("训练专属形象")} width={540} onClose={busy ? null : onClose} footer={
      <React.Fragment>
        <Btn kind="ghost" onClick={onClose} disabled={busy}>{t("取消")}</Btn>
        <Btn kind="primary" onClick={submit} loading={busy}>{t("开始训练")}</Btn>
      </React.Fragment>
    }>
      <div className="banner info" style={{ marginBottom: 14 }}>
        <Icon name="info" size={15} />
        {t("上传一段清晰的真人正脸视频（建议 10-60 秒、单人、光线均匀、正对镜头），训练拥有该样貌的专属数字人形象。训练在后台进行，可在本页查看进度。")}
      </div>
      <Field label={t("形象名称")} required error={error} help={t("便于辨认，如：品牌代言人-小李")}>
        <input className="input" value={name} autoFocus maxLength={40}
          onChange={(e) => { setName(e.target.value); setError(null); }} placeholder={t("品牌代言人-小李")} />
      </Field>
      <Field label={t("真人视频")} required help={t("支持 mp4 / mov / webm，单个文件 ≤ 200MB")}>
        <input className="input" type="file" accept="video/*"
          onChange={(e) => { setFile((e.target.files || [])[0] || null); setError(null); }} />
        {file ? (
          <div className="row" style={{ gap: 6, marginTop: 8 }}>
            <span className="badge muted" style={{ display: "inline-flex", alignItems: "center", gap: 6, maxWidth: 320 }}>
              <Icon name="user" size={12} />
              <span style={{ maxWidth: 240, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{file.name}</span>
            </span>
          </div>
        ) : null}
      </Field>
      {/* 动作骨架选择器：由 EE 动作模块经 PL_FEATURES.slots 注入；模块未交付时此处为空（功能无声降级）。 */}
      {window.PL_FEATURES && window.PL_FEATURES.slots && window.PL_FEATURES.slots.trainMotionField
        ? window.PL_FEATURES.slots.trainMotionField({ value: motionRef, onChange: setMotionRef })
        : null}
      <label className="row" style={{ gap: 8, alignItems: "flex-start", cursor: "pointer", marginTop: 6 }}>
        <input type="checkbox" checked={consent} onChange={(e) => { setConsent(e.target.checked); setError(null); }} style={{ marginTop: 3 }} />
        <span className="small muted">{t("我确认已获得该人物的合法授权，用于数字人形象训练与合成，并承担相应法律责任。")}</span>
      </label>
    </Modal>
  );
}

/* 形象烘焙：上传一段说话视频 → 抽帧 → MuseTalk 素材目录 → 出现在上方形象列表。

   在此之前这是一条 ssh 手工流程（scp 视频、跑 ffmpeg、进对的 conda 环境跑
   烘焙脚本、再等 cored 重扫），每一步都能做错，而且控制台里完全看不见。 */
function AvatarBakeModal({ onClose, onSaved }) {
  const [name, setName] = React.useState("");
  const [file, setFile] = React.useState(null);
  const [seconds, setSeconds] = React.useState(15);
  const [bboxShift, setBboxShift] = React.useState(0);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const toast = useToast();

  async function submit() {
    const n = name.trim().toLowerCase();
    // 与后端 mtbake.ValidateName 同一条规则：它会成为目录名，先在前端拦一道，
    // 免得视频传完了才被拒。
    if (!/^[a-z][a-z0-9_-]{1,31}$/.test(n)) {
      setError(t("形象名只能用小写字母、数字、下划线和连字符，以字母开头，长度 2-32"));
      return;
    }
    if (!file) { setError(t("请选择一段说话视频")); return; }
    setBusy(true); setError(null);
    try {
      const r = await PL.bakeAvatarMT({ video: file, name: n, seconds: seconds, bbox_shift: bboxShift });
      if (r && r.warning) toast.warn(t(r.warning));
      else toast.ok(t("已提交烘焙「{0}」，可在下方查看进度", n));
      onSaved();
    } catch (e) {
      setError(PL.errText(e) + (e.msgRaw ? ": " + e.msgRaw : ""));
    }
    setBusy(false);
  }

  return (
    <Modal title={t("烘焙新形象")} width={560} onClose={busy ? null : onClose} footer={
      <React.Fragment>
        <Btn kind="ghost" onClick={onClose} disabled={busy}>{t("取消")}</Btn>
        <Btn kind="primary" onClick={submit} loading={busy}>{t("开始烘焙")}</Btn>
      </React.Fragment>
    }>
      <div className="banner info" style={{ marginBottom: 14 }}>
        <Icon name="info" size={15} />
        {t("上传一段该人物「正在说话」的视频：单人、正脸、光线均匀、镜头不动。烘焙会把它拆成素材帧，之后引擎用音频驱动嘴部。素材来源必须与待机画面同源，否则切换时会跳。")}
      </div>
      <Field label={t("形象名")} required error={error} help={t("会成为素材目录名，如 leiya_mt。只能小写字母、数字、下划线、连字符")}>
        <input className="input" value={name} autoFocus maxLength={32}
          onChange={(e) => { setName(e.target.value); setError(null); }} placeholder="leiya_mt" />
      </Field>
      <Field label={t("说话视频")} required help={t("支持 mp4 / mov / webm / mkv，单个文件 ≤ 512MB")}>
        <input className="input" type="file" accept="video/*"
          onChange={(e) => { setFile((e.target.files || [])[0] || null); setError(null); }} />
        {file ? (
          <div className="row" style={{ gap: 6, marginTop: 8 }}>
            <span className="badge muted" style={{ display: "inline-flex", alignItems: "center", gap: 6, maxWidth: 320 }}>
              <Icon name="user" size={12} />
              <span style={{ maxWidth: 240, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{file.name}</span>
            </span>
          </div>
        ) : null}
      </Field>
      <Field label={t("烘焙时长（秒）")}
        help={t("只烘焙视频的前 N 秒。这是内存上限不是耐心上限：烘焙会把每一帧解码后全留在内存里，再镜像一份，20 秒已接近安全边界。形象是循环播放的，更长也不会更生动。")}>
        <input className="input" type="number" min={3} max={20} value={seconds}
          onChange={(e) => setSeconds(Math.max(3, Math.min(20, Number(e.target.value) || 15)))} />
      </Field>
      <Field label={t("嘴部幅度 bbox_shift")}
        help={t("MuseTalk 的口型幅度旋钮。默认 0；嘴张不开就往正数调（+5 ~ +15），糊成一团往负数调。改这个要重新烘焙。")}>
        <input className="input" type="number" min={-30} max={30} value={bboxShift}
          onChange={(e) => setBboxShift(Math.max(-30, Math.min(30, Number(e.target.value) || 0)))} />
      </Field>
    </Modal>
  );
}

/* 烘焙任务状态 → 文案与样式 */
function bakeStatusMeta(s) {
  switch (s) {
    case "queued": return { label: t("排队中"), cls: "muted" };
    case "running": return { label: t("烘焙中"), cls: "info" };
    case "done": return { label: t("已完成"), cls: "ok" };
    case "failed": return { label: t("失败"), cls: "danger" };
    case "canceled": return { label: t("已取消"), cls: "muted" };
    default: return { label: s, cls: "muted" };
  }
}

/* 烘焙任务列表。只在后端启用了烘焙（有 docker socket + 配齐素材路径）时出现：
   接口 404/501 就整块不渲染，而不是摆一个点了报错的按钮。 */
function AvatarBakePanel({ onBaked }) {
  const [jobs, setJobs] = React.useState(null); // null=未知/不可用
  const [open, setOpen] = React.useState(false);
  const toast = useToast();

  // 只接受数组。请求成功但拿回别的形状（例如打错端点打到了 /avatars/bake）时，
  // 下面的 .some 会直接抛错炸掉整个页面——这块面板不值得让形象页打不开。
  const asJobs = (v) => (Array.isArray(v) ? v : null);

  const load = React.useCallback(async () => {
    try { setJobs(asJobs(await PL.listAvatarBakesMT())); } catch (e) { setJobs(null); }
  }, []);

  React.useEffect(() => { load(); }, [load]);

  // 有任务在跑时才轮询——烘焙是分钟级的，空闲时每 3 秒打一次接口没有意义。
  const active = (jobs || []).some((j) => j.status === "queued" || j.status === "running");
  React.useEffect(() => {
    if (!active) return;
    const id = setInterval(async () => {
      let next = null;
      try { next = asJobs(await PL.listAvatarBakesMT()); } catch (e) { return; }
      if (!next) return;
      setJobs((prev) => {
        // 有任务刚跑完 → 形象目录多了一个，通知外层重新拉取
        const was = (prev || []).filter((j) => j.status === "running" || j.status === "queued").length;
        const now = next.filter((j) => j.status === "running" || j.status === "queued").length;
        if (now < was && onBaked) onBaked();
        return next;
      });
    }, 3000);
    return () => clearInterval(id);
  }, [active, onBaked]);

  async function remove(j) {
    try {
      await PL.deleteAvatarBakeMT(j.id);
      toast.ok(j.status === "queued" || j.status === "running" ? t("已取消烘焙「{0}」", j.name) : t("已删除记录"));
      load();
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
  }

  if (jobs === null) return null; // 后端未启用烘焙

  return (
    <React.Fragment>
      <div className="row" style={{ marginTop: 18, marginBottom: 4, alignItems: "center", gap: 10 }}>
        <div className="card-title" style={{ margin: 0 }}>
          {t("烘焙新形象")}
          {jobs.length ? <span className="muted small" style={{ fontWeight: 400 }}>{jobs.length}</span> : null}
        </div>
        <div className="grow" />
        <Btn kind="primary" onClick={() => setOpen(true)}><Icon name="plus" size={13} />{t("上传说话视频")}</Btn>
      </div>
      <div className="muted small" style={{ marginBottom: 10 }}>
        {t("烘焙与直播共用同一张显卡，进行中会占用显存——建议避开高峰。完成后形象直接出现在上方列表，无需重启服务。")}
      </div>
      {jobs.length ? (
        <div className="avatar-tiles">
          {jobs.map((j) => {
            const m = bakeStatusMeta(j.status);
            return (
              <div key={j.id} className="avatar-tile">
                <div className="avatar-thumb"><Icon name="user" size={26} /></div>
                <div className="grow" style={{ minWidth: 0 }}>
                  <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
                    <span style={{ fontWeight: 600 }} title={j.name}>{j.name}</span>
                    <span className={"badge " + m.cls}>
                      {j.status === "running" ? <span className="spinner" style={{ marginRight: 4 }} /> : null}{m.label}
                    </span>
                    {j.status === "done" && j.frames ? (
                      <span className="muted small">{t("{0} 帧", String(j.frames))}</span>
                    ) : null}
                  </div>
                  {/* 与「我训练的形象」用同一条进度条样式（theme.css 里没有
                      通用的 .progress，别新造一个不存在的类名）。 */}
                  {j.status === "running" || j.status === "queued" ? (
                    <div className="train-bar" style={{ marginTop: 7, height: 6, borderRadius: 4, background: "var(--track, #eee)", overflow: "hidden" }}>
                      <div style={{ width: (j.progress || 0) + "%", height: "100%", background: "var(--accent)", transition: "width .4s" }} />
                    </div>
                  ) : null}
                  {/* 本段素材实际可用的 bbox_shift 区间——由这段视频自己的关键点
                      算出来，每段都不一样，所以只能等烘焙跑到关键点阶段才知道。
                      它是重新烘焙调嘴部幅度时唯一的依据，之前只存在容器日志里。 */}
                  {j.bbox_range ? (
                    <div className="small muted" style={{ marginTop: 4 }}>
                      {t("嘴部幅度可调区间 {0}（本次 {1}）", j.bbox_range, String(j.bbox_shift || 0))}
                      {" · "}
                      {t("嘴张不开就调大，糊了就调小，改完要重新烘焙")}
                    </div>
                  ) : null}
                  {j.error ? <div className="small" style={{ color: "var(--danger)", marginTop: 4 }}>{j.error}</div> : null}
                </div>
                {j.status === "running" ? (
                  <span className="small muted" style={{ flex: "none", fontVariantNumeric: "tabular-nums" }}>{(j.progress || 0) + "%"}</span>
                ) : null}
                <Btn kind="ghost" size="sm" style={{ flex: "none" }} onClick={() => remove(j)}>
                  {j.status === "queued" || j.status === "running" ? t("取消") : t("删除")}
                </Btn>
              </div>
            );
          })}
        </div>
      ) : null}
      {open ? <AvatarBakeModal onClose={() => setOpen(false)}
        onSaved={() => { setOpen(false); load(); }} /> : null}
    </React.Fragment>
  );
}

/* 训练任务状态 → 文案与样式 */
function trainStatusMeta(s) {
  switch (s) {
    case "queued": return { label: t("排队中"), cls: "muted" };
    case "running": return { label: t("训练中"), cls: "info" };
    case "done": return { label: t("已完成"), cls: "ok" };
    case "failed": return { label: t("失败"), cls: "danger" };
    case "canceled": return { label: t("已取消"), cls: "muted" };
    default: return { label: s, cls: "muted" };
  }
}

/* 内置数字人形象目录：产品预设，随构建发布；缩略图磁盘直服 /app/assets/avatars/。
   形象 id 写入 avatar.current 指针；引擎在下个会话据此切换说话脸（cond_image，见
   internal/avatarcatalog）。打包了正面源图的内置形象（如 F = mynah-f.jpg）即时可驱动；
   其余缩略图是产品预设，配齐源图前为偏好占位。 */
/* 缩略图查找表（仅供页头「当前形象」显示用），**不是**可选形象的来源——
   那来自后端 /avatars。
   只保留 avatarcatalog 里真实存在的条目。曾经这里还有 linxia/chenmo/
   xiaorou/zhouheng 四项，但 worker 侧只有 mynah-default.jpg 与
   mynah-f.jpg 两个肖像文件，那四个 id 在后端目录里根本不存在：选中后
   解析为空串、worker 保持原样，也就是点了静默无效。控制台却有它们的缩略图，
   于是看起来像能用的——已删除。历史配置里若仍存着这些 id，页头会显示原始 id，
   这比显示一个像模像样却不存在的名字更诚实。 */
const AVATAR_THUMBS = [
  { id: "f", name: "F", img: "/app/assets/avatars/f.webp" },
];

/* 缩略图查找，模块级：形象网格和页头「当前形象」都要用，放在任一组件内部
   另一个就取不到（上一版正是这样崩的：函数定义在 CurrentAvatarHero 里，却在
   AvatarSection 中调用）。查不到返回 null，卡片回退成图标占位。 */
function avatarThumb(id) {
  const b = AVATAR_THUMBS.find((a) => a.id === id);
  return b ? b.img : null;
}

/* 单张形象卡（默认 + 内置）：缩略图 + 名称 + 气质 + 类型徽标。镜像 VoiceCard 选中交互：
   命中「当前形象」指针时亮绿光晕 + 显示「当前启用」，否则可点「设为当前形象」。 */
/* 单张形象卡。kind: default | builtin | baked | unusable。
   baked 多一枚引擎徽标和「预热」按钮——形象是否已驻留决定访客要不要等 15 秒；
   unusable 灰显不可点，因为当前引擎池渲染不了它。
   hasIdle（后端 /avatars 的 has_idle）标这个形象有没有自己的待机素材：没有不等于坏了，
   人还是对的，只是那段待机改由引擎实时渲染，频道开着就一直占着 worker。不摆出来的话，
   绑一个新形象＝把零成本待机悄悄换成常驻 GPU。 */
function AvatarCard({ id, name, sub, img, kind, engine, hasIdle, active, busy, baking, locked,
                     onSelect, onPreload, preloading }) {
  const unusable = kind === "unusable";
  const kindLabel = kind === "default" ? t("默认") : kind === "baked" ? t("烘焙") : t("内置");
  const kindCls = kind === "default" ? "muted" : kind === "baked" ? "ok" : "info";
  return (
    <div className={"voice-card avatar-card" + (active ? " is-current" : "")}
      style={unusable ? { opacity: 0.5 } : null}
      onClick={() => { if (!unusable && !active && !busy && !baking && !locked) onSelect(id, name); }}>
      <div className="voice-card-head">
        <div className="avatar-card-thumb">
          {img ? <img src={img} alt={name} loading="lazy" /> : <Icon name="user" size={24} />}
        </div>
        <div className="grow" style={{ minWidth: 0 }}>
          <div className="v-name" title={name}>{name}</div>
          <div className="row" style={{ gap: 6, rowGap: 4, marginTop: 5, flexWrap: "wrap", minWidth: 0 }}>
            <span className={"badge " + kindCls} style={{ flex: "none" }}>{kindLabel}</span>
            {engine ? <span className="badge" style={{ flex: "none" }}>{engine}</span> : null}
            {hasIdle
              ? <span className="badge ok" style={{ flex: "none" }}
                  title={t("这个形象有自己的待机素材，没人说话时由 cored 直接回放，不占 GPU。")}>{t("有待机")}</span>
              : <span className="badge warn" style={{ flex: "none" }}
                  title={t("待机将由引擎实时生成（占 GPU），可在下方烘焙待机以省电。")}>{t("待机实时生成")}</span>}
            {sub ? <span className="small muted" title={sub} style={{ flexBasis: "100%", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 }}>{sub}</span> : null}
          </div>
        </div>
      </div>
      {unusable
        ? <button className="btn btn-secondary v-play" disabled>{t("需 {0} 引擎", engine || "?")}</button>
        : baking
        ? <button className="btn btn-secondary v-play" disabled><span className="spinner" />{t("应用中")} {(baking.progress || 0) + "%"}</button>
        : active
        ? <button className="btn btn-secondary v-play" disabled><Icon name="check" size={14} />{t("当前")}</button>
        : <button className="btn btn-secondary v-play" disabled={busy || locked} onClick={(e) => { e.stopPropagation(); onSelect(id, name); }}>{busy ? <span className="spinner" /> : null}{t("设为当前")}</button>}
      {onPreload && !unusable ? (
        <button className="btn btn-ghost v-play" style={{ marginTop: 6 }}
          disabled={preloading || locked}
          onClick={(e) => { e.stopPropagation(); onPreload(); }}
          title={t("让引擎提前加载该形象（约 15 秒），之后访客切换零等待")}>
          {preloading ? <span className="spinner" /> : null}{preloading ? t("预热中") : t("预热")}
        </button>
      ) : null}
    </div>
  );
}

/* 数字人形象区：内置形象网格（默认 + 内置预设）+ 我训练的形象 + 训练任务进度。
   内置网格选形象（写 avatar.current → cond_image）在 OSS 即可用；训练/烘焙是 EE，OSS 下
   selectAvatar 仅写指针（下个会话生效）、训练段（done.length=0）自然隐藏。
   「当前形象」由配置中心单一指针 avatar.current 决定（default / 内置 id / trained:<jobId>），全局只一张亮「当前启用」。
   「训练新形象」按钮已上提到页头（随 tab 切换），开关由父级经 trainOpen/setTrainOpen 控制。 */
function AvatarSection({ trainOpen, setTrainOpen, onChanged, onApplying, applying, ee }) {
  const [jobs, setJobs] = React.useState(null); // null=加载中, []=无
  const [current, setCurrent] = React.useState("default"); // avatar.current 指针
  const [delTarget, setDelTarget] = React.useState(null);
  const [delBusy, setDelBusy] = React.useState(false);
  const [busyId, setBusyId] = React.useState(null); // 正在设为当前的卡 id（default/内置 id/trained:<id>）
  const [motions, setMotions] = React.useState({}); // avatar id -> 动作骨架 ref（决定烘焙用哪个骨架）
  const [baking, setBaking] = React.useState(null); // {id, jobId, progress} 烘焙+应用进行中
  const [motionLibOpen, setMotionLibOpen] = React.useState(false); // 动作库弹窗
  const bakePoll = React.useRef(null);
  // Baked avatars come from the SERVER (scanned on disk), so they cannot live
  // in a frontend constant. Each carries the engine
  // that can render it — a MuseTalk bake is unusable by FlashHead, so the badge
  // is not decoration.
  const [catalog, setCatalog] = React.useState([]);
  // 池可以同时跑多个引擎，所以这里存的是引擎列表而不是一个名字：绑定 musetalk
  // 的形象只要池里有一个 musetalk worker 就可用，不必整池都是它。
  const [poolEngines, setPoolEngines] = React.useState([]);
  const [preloading, setPreloading] = React.useState("");
  const toast = useToast();

  React.useEffect(() => {
    let alive = true;
    PL.avatars().then((l) => { if (alive) setCatalog(l || []); }).catch(() => {});
    // Which engine the pool actually runs decides what is selectable, so the
    // page asks rather than assuming. Failure just leaves everything shown.
    PL.avatarCapacity()
      .then((c) => { if (alive) setPoolEngines((c && c.pool_engines) || []); })
      .catch(() => {});
    return () => { alive = false; };
  }, []);

  // An avatar with no engine requirement (built-in portrait) is always offered;
  // a baked one only when the pool runs the engine it was baked for. Showing
  // the rest as selectable is what made the old page lie.
  const served = (a) => !a.engine || !poolEngines.length || poolEngines.indexOf(a.engine) >= 0;
  const usable = catalog.filter(served);
  const unusable = catalog.filter((a) => !served(a));

  // 「默认形象」那张卡是写死的（目录里的 default 条目没有缩略图/文案），所以它的
  // has_idle 得回目录里查一次，不能像其它卡那样顺着 map 拿。
  const hasIdle = (id) => {
    const hit = catalog.filter((a) => a.id === id)[0];
    return !!(hit && hit.has_idle);
  };

  // Warm an avatar without binding a channel to it. Useful because the load is
  // ~15s and a worker withholds Ready until it finishes: doing it here means no
  // visitor ever pays for it.
  async function preload(a) {
    setPreloading(a.id);
    try {
      const r = await PL.avatarPreload(a.id);
      if (r && r.warning) toast.warn(t(r.warning));
      else toast.ok(t("已在 {0}/{1} 个引擎上预热", String(r.ready), String(r.total)));
    } catch (e) {
      toast.error(PL.errText(e, {}), e.msgRaw);
    }
    setPreloading("");
  }

  const load = React.useCallback(async (silent) => {
    if (!ee) { setJobs([]); return; } // OSS: training is EE; show built-ins only, skip the EE list call
    try {
      const r = await PL.listTrainingJobs();
      setJobs((r && r.jobs) || []);
    } catch (e) {
      if (!silent) setJobs([]); // 拉不到（如非 EE 后端）按空处理，仍显示默认/内置形象
    }
  }, [ee]);
  React.useEffect(() => { load(); }, [load]);

  // 当前形象指针 + 动作偏好（配置中心 avatar 组，对标 tts.voice）
  React.useEffect(() => {
    PL.getConfigGroup("avatar").then((c) => {
      if (c && c.current) setCurrent(c.current);
      setMotions((c && c.motions) || {});
    }).catch(() => {});
    return () => { if (bakePoll.current) clearInterval(bakePoll.current); };
  }, []);

  // 有排队/训练中任务时轮询进度
  const live = (jobs || []).some((j) => j.status === "queued" || j.status === "running");
  React.useEffect(() => {
    if (!live) return;
    const id = setInterval(() => load(true), 2000);
    return () => clearInterval(id);
  }, [live, load]);

  const done = (jobs || []).filter((j) => j.status === "done");
  const pending = (jobs || []).filter((j) => j.status !== "done");

  // 选用内置/默认形象：写指针 + 真烘焙该形象 idle（用其指定动作骨架）→ applyIdle 热换 + 换说话脸。
  async function selectAvatar(id, name) {
    if (id === current || baking || applying) return;
    setBusyId(id);
    try { await PL.putConfig("avatar", { current: id }); setCurrent(id); }
    catch (e) { toast.error(PL.errText(e), e.msgRaw); setBusyId(null); return; }
    setBusyId(null);
    if (!ee) { // OSS: no idle-bake pipeline; the pointer drives cond_image on the next session
      if (onChanged) onChanged();
      toast.ok(t("已选用形象「{0}」，将在下个会话生效", name));
      return;
    }
    bakeAndApply(id, name);
  }
  // 烘焙形象 idle 并热换上线（idle + 说话脸一起切），轮询进度直到终态。
  // 缓存命中时 bakeAvatar 首响应即 done → 体感即时（内置已预制）。
  async function bakeAndApply(id, name, refOverride) {
    const ref = refOverride || motions[id] || "builtin:d14";
    setBaking({ id: id, jobId: null, progress: 0 });
    if (onApplying) onApplying(name);
    const finish = (okFlag, err) => {
      setBaking(null); if (onApplying) onApplying(null);
      if (okFlag) { if (onChanged) onChanged(); toast.ok(t("已启用形象「{0}」", name)); }
      else toast.error(err || t("烘焙失败"));
    };
    try {
      const job = await PL.bakeAvatar(id, ref, true);
      const jid = job && job.id;
      if (!jid) throw new Error("no job id");
      if (job.status === "done") { finish(true); return; } // 缓存命中 → 即时
      setBaking({ id: id, jobId: jid, progress: job.progress || 0 });
      if (bakePoll.current) clearInterval(bakePoll.current);
      bakePoll.current = setInterval(async () => {
        try {
          const s = await PL.getAvatarBake(jid);
          if (!s) return;
          setBaking({ id: id, jobId: jid, progress: s.progress || 0 });
          if (s.status === "done") {
            clearInterval(bakePoll.current); bakePoll.current = null; finish(true);
          } else if (s.status === "failed" || s.status === "canceled") {
            clearInterval(bakePoll.current); bakePoll.current = null; finish(false, s.error);
          }
        } catch (e) { /* 瞬时错误，下个 tick 重试 */ }
      }, 3000);
    } catch (e) {
      setBaking(null); if (onApplying) onApplying(null);
      toast.error(PL.errText(e), e.msgRaw);
    }
  }
  // 选用训练形象：保 is_active（DB 痕迹）+ 写配置中心指针（前端编排，UI 以 avatar.current 为唯一来源），
  // 再烘焙该形象的 idle（源图=训练产物 portrait，后端 resolver 解析 trained:<id>）并热换上线。
  async function selectTrained(j) {
    const cur = "trained:" + j.id;
    if (cur === current || baking || applying) return;
    setBusyId(cur);
    try {
      await PL.setTrainingActive(j.id);
      await PL.putConfig("avatar", { current: cur });
      setCurrent(cur); load(true);
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); setBusyId(null); return; }
    setBusyId(null);
    bakeAndApply(cur, j.name, j.motion_ref);
  }
  async function doDelete() {
    const j = delTarget;
    setDelBusy(true);
    try {
      await PL.deleteTrainingJob(j.id);
      // 删的若是当前形象，回退默认，避免指针悬空（无卡亮「当前启用」）
      if (current === "trained:" + j.id) { try { await PL.putConfig("avatar", { current: "default" }); } catch (_) {} setCurrent("default"); if (onChanged) onChanged(); }
      toast.ok(j.status === "queued" || j.status === "running" ? t("已取消训练「{0}」", j.name) : t("已删除形象「{0}」", j.name));
      setDelTarget(null); load(true);
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setDelBusy(false);
  }

  return (
    <div className="card card-pad avatar-section">
      {/* 形象一律从后端目录取，并按「能不能用」分组，复用与音色一致的卡片样式。
          之前这里是三组并列（选择形象 / 烘焙形象 / 我训练的形象），用户无从
          判断该点哪个；更糟的是「内置形象」曾是前端硬编码的 5 张卡，而后端目录
          里只有 default 和 f —— 另外 4 张点了静默无效，现已删除。 */}
      <div className="card-title" style={{ marginTop: 0, marginBottom: 4 }}>
        {t("选择形象")}<span className="muted small" style={{ fontWeight: 400 }}>{usable.length}</span>
      </div>
      <div className="muted small" style={{ marginBottom: 10 }}>
        {t("当前引擎池跑的是 {0}，只有这些引擎能渲染的形象才可选用。", poolEngines.join(" / ") || t("（未知）"))}
      </div>
      <div className="voice-grid">
        <AvatarCard id="default" name={t("默认形象")} sub={t("Mynah 内置")} img={null} kind="default"
          hasIdle={hasIdle("default")}
          active={current === "default"} busy={busyId === "default"}
          baking={baking && baking.id === "default" ? baking : null}
          locked={!!applying} onSelect={selectAvatar} />
        {usable.filter((a) => a.id !== "default").map((a) => (
          <AvatarCard key={a.id} id={a.id} name={a.name} sub={a.baked ? t("烘焙素材") : t("内置肖像")}
            img={avatarThumb(a.id)} kind={a.baked ? "baked" : "builtin"} engine={a.engine}
            hasIdle={!!a.has_idle}
            active={current === a.id} busy={busyId === a.id}
            baking={baking && baking.id === a.id ? baking : null} locked={!!applying}
            onSelect={selectAvatar}
            onPreload={a.baked ? () => preload(a) : null}
            preloading={preloading === a.id} />
        ))}
      </div>

      {/* 不可用的单独列出，灰显不可点，而不是混在可选项里让人点了没反应。 */}
      {unusable.length ? (
        <React.Fragment>
          <div className="card-title" style={{ marginTop: 18, marginBottom: 4 }}>
            {t("当前引擎不可用")}<span className="muted small" style={{ fontWeight: 400 }}>{unusable.length}</span>
          </div>
          <div className="muted small" style={{ marginBottom: 10 }}>
            {t("这些形象需要其它引擎渲染。要用它们，先在「本地服务 → 引擎池」加上对应引擎。")}
          </div>
          <div className="voice-grid">
            {unusable.map((a) => (
              <AvatarCard key={a.id} id={a.id} name={a.name}
                sub={a.baked ? t("烘焙素材") : t("内置肖像")}
                img={avatarThumb(a.id)} kind="unusable" engine={a.engine} hasIdle={!!a.has_idle} />
            ))}
          </div>
        </React.Fragment>
      ) : null}

      {/* 烘焙新形象：上传视频 → 出现在上方「选择形象」。完成后刷新目录，
          让新形象立刻可选，不必手动刷新页面。 */}
      <AvatarBakePanel onBaked={() => {
        PL.avatars().then((l) => setCatalog(l || [])).catch(() => {});
      }} />

      {/* 我训练的形象（EE 训练完成）：与内置共用同一「当前形象」指针 */}
      {done.length ? (
        <React.Fragment>
          <div className="card-title" style={{ marginTop: 18, marginBottom: 10 }}>
            {t("我训练的形象")}<span className="muted small" style={{ fontWeight: 400 }}>{done.length}</span>
          </div>
          <div className="avatar-tiles">
            {done.map((j) => {
              const isCur = current === "trained:" + j.id;
              return (
                <div key={j.id} className={"avatar-tile" + (isCur ? " is-active" : "")}>
                  <div className="avatar-thumb"><Icon name="user" size={26} /></div>
                  <div className="grow" style={{ minWidth: 0 }}>
                    <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
                      <span style={{ fontWeight: 600, maxWidth: 160, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={j.name}>{j.name}</span>
                      {isCur ? <span className="badge ok"><Icon name="check" size={11} />{t("当前")}</span> : null}
                    </div>
                    <div className="row" style={{ gap: 8, marginTop: 6 }}>
                      {isCur
                        ? <span className="small muted">{t("形象资产已生成（{0}）；接入实时引擎为企业版后续能力", t("占位"))}</span>
                        : <Btn kind="secondary" size="sm" loading={busyId === "trained:" + j.id} onClick={() => selectTrained(j)}>{t("设为当前")}</Btn>}
                      <Btn kind="ghost" size="sm" icon="trash" onClick={() => setDelTarget(j)}>{t("删除")}</Btn>
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        </React.Fragment>
      ) : null}

      {/* 进行中 / 失败的训练任务 */}
      {pending.length ? (
        <div className="train-jobs" style={{ marginTop: 14, display: "flex", flexDirection: "column", gap: 10 }}>
          {pending.map((j) => {
            const m = trainStatusMeta(j.status);
            const running = j.status === "running" || j.status === "queued";
            return (
              <div key={j.id} className="train-job-row" style={{ display: "flex", alignItems: "center", gap: 12, padding: "10px 12px", border: "1px solid var(--border)", borderRadius: 10 }}>
                <Icon name="user" size={18} />
                <div className="grow" style={{ minWidth: 0 }}>
                  <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
                    <span style={{ fontWeight: 600, maxWidth: 220, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={j.name}>{j.name}</span>
                    <span className={"badge " + m.cls}>{j.status === "running" ? <span className="spinner" style={{ marginRight: 4 }} /> : null}{m.label}</span>
                  </div>
                  {j.status === "running" ? (
                    <div className="train-bar" style={{ marginTop: 7, height: 6, borderRadius: 4, background: "var(--track, #eee)", overflow: "hidden" }}>
                      <div style={{ width: (j.progress || 0) + "%", height: "100%", background: "var(--accent)", transition: "width .4s" }} />
                    </div>
                  ) : null}
                  {j.status === "failed" && j.error ? <div className="small" style={{ color: "var(--danger)", marginTop: 4 }}>{j.error}</div> : null}
                </div>
                {j.status === "running" ? <span className="small muted" style={{ flex: "none", fontVariantNumeric: "tabular-nums" }}>{(j.progress || 0) + "%"}</span> : null}
                <Btn kind="ghost" size="sm" style={{ flex: "none" }} onClick={() => setDelTarget(j)}>{running ? t("取消") : t("删除")}</Btn>
              </div>
            );
          })}
        </div>
      ) : null}

      {/* 当前形象的待机神态：由 EE 动作模块经 slot 注入单一控件（只管当前形象）+ 动作库弹窗入口。
          模块未交付时整块不渲染（无声降级）。 */}
      {ee && window.PL_FEATURES && window.PL_FEATURES.slots && window.PL_FEATURES.slots.currentMotionControl ? (
        <div className="card card-pad" style={{ marginTop: 16 }}>
          <div className="card-title" style={{ marginBottom: 4, justifyContent: "space-between" }}>
            <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
              {t("当前形象的待机神态")}
              <span className="badge info" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
                <Icon name="film" size={11} />{t("动作骨架")}
              </span>
            </span>
            {window.PL_FEATURES.slots.motionLibraryModal
              ? <a className="adv-toggle" style={{ marginTop: 0 }} onClick={() => setMotionLibOpen(true)}>{t("管理动作库")}<Icon name="chevronRight" size={13} /></a>
              : null}
          </div>
          <div className="small muted" style={{ marginBottom: 12, lineHeight: 1.7 }}>
            {t("决定当前形象不说话时的头部与表情神态。换神态需重新生成待机画面（约 6 分钟，不打断在线会话）；换脸、换音色即时生效。")}
          </div>
          {window.PL_FEATURES.slots.currentMotionControl({ avatarId: current, onChanged: onChanged, onApplying: onApplying })}
        </div>
      ) : null}

      {motionLibOpen && ee && window.PL_FEATURES && window.PL_FEATURES.slots && window.PL_FEATURES.slots.motionLibraryModal
        ? window.PL_FEATURES.slots.motionLibraryModal({ open: true, onClose: () => setMotionLibOpen(false) })
        : null}

      {trainOpen ? <AvatarTrainModal onClose={() => setTrainOpen(false)} onSaved={() => { setTrainOpen(false); load(true); }} /> : null}
      {delTarget ? (
        <ConfirmDialog
          title={(delTarget.status === "queued" || delTarget.status === "running") ? t("取消训练「{0}」", delTarget.name) : t("删除形象「{0}」", delTarget.name)}
          body={(delTarget.status === "queued" || delTarget.status === "running") ? t("将中止该训练任务，不可恢复。") : t("将删除该形象及其上传的视频，不可恢复。")}
          confirmText={(delTarget.status === "queued" || delTarget.status === "running") ? t("取消训练") : t("永久删除")} danger busy={delBusy}
          onConfirm={doDelete} onClose={() => setDelTarget(null)}
        />
      ) : null}
    </div>
  );
}

/* 单个音色卡片：首字母头像 + 名称 + 类型 + 常驻试听；自定义卡 hover 露删除。
   整卡可点选中（顶部试听栏随之作用于它）；卡底分隔行显示「这个音色自己存的种子」。
   默认音色额外亮实心绿「✓ 默认」标 + 柔和绿光晕；选中卡加蓝色描边。 */
function VoiceCard({ v, playing, onPreview, onSelect, onDelete, isDefault, isSelected, savedSeed }) {
  const cust = isCustomVoice(v);
  const col = voiceColor(v.id);
  const isPlaying = playing === v.id;
  const initial = (v.id[0] || "?").toUpperCase();
  const hasSeed = savedSeed > 0;
  const showFoot = isDefault || hasSeed;
  return (
    <div className={"voice-card" + (isDefault ? " is-default" : "") + (isSelected ? " is-selected" : "")}
      onClick={() => onSelect(v)}>
      {cust ? (
        <span className="tip v-del" data-tip={t("删除")}>
          <button className="btn-icon btn danger" onClick={(e) => { e.stopPropagation(); onDelete(v); }}><Icon name="trash" size={14} /></button>
        </span>
      ) : null}
      <div className="voice-card-head">
        <div className="voice-av" style={{ "--vc-bg": col[0], "--vc-fg": col[1] }}>{initial}</div>
        <div className="grow" style={{ minWidth: 0 }}>
          <div className="v-name" title={v.id}>{v.id}</div>
          <div className="row" style={{ gap: 6, marginTop: 5, flexWrap: "wrap" }}>
            <span className={"badge " + (cust ? "info" : "muted")}>{cust ? t("自定义") : t("内置")}</span>
            {isDefault ? <span className="v-default-tag"><Icon name="check" size={11} />{t("当前")}</span> : null}
          </div>
        </div>
      </div>
      <button className="btn btn-secondary v-play" disabled={isPlaying} onClick={(e) => { e.stopPropagation(); onPreview(v); }}>
        {isPlaying ? <span className="spinner" /> : <Icon name="play" size={14} />}
        {isPlaying ? t("播放中") : t("试听")}
      </button>
      {showFoot ? (
        <div className="v-seed-foot" title={hasSeed ? t("逐句固定音色") : t("每句随机采样")}>
          {hasSeed
            ? <React.Fragment><span className="muted">{t("种子")}</span><b className="v-seed-num">{savedSeed}</b>{isDefault ? <span className="muted">· {t("逐句一致")}</span> : null}</React.Fragment>
            : <span className="muted">{t("随机种子 · 每句浮动")}</span>}
        </div>
      ) : null}
    </div>
  );
}

/* 当前数字人总览卡（hero）：常驻页头下、Tabs 上，一眼看清线上是哪张脸 + 哪把嗓子 + 什么待机神态。
   读的正是各 tab 写的 config（avatar.current / avatar.motions / tts.voice）= 唯一真相源；reloadKey 变即重拉。
   待机神态名经 EE 动作模块的 motionLabel slot 解析，模块缺席用中性回退（宿主完整渲染）。 */
function CurrentAvatarHero({ reloadKey, applying, navigate, ee }) {
  const [cur, setCur] = React.useState("default");
  const [motions, setMotions] = React.useState({});
  const [voice, setVoice] = React.useState("");
  const [trained, setTrained] = React.useState([]);
  const [playing, setPlaying] = React.useState(false);
  const audioRef = React.useRef(null);
  const urlRef = React.useRef(null);
  const toast = useToast();
  const sample = t("你好，我是你的智能助手，很高兴为你服务。");

  React.useEffect(() => {
    PL.getConfigGroup("avatar").then((c) => { if (c) { setCur(c.current || "default"); setMotions(c.motions || {}); } }).catch(() => {});
    PL.getConfigGroup("tts").then((c) => { if (c && c.voice) setVoice(c.voice); }).catch(() => {});
    if (ee) PL.listTrainingJobs().then((r) => setTrained((r && r.jobs) || [])).catch(() => {});
  }, [reloadKey]);
  React.useEffect(() => () => { if (urlRef.current) URL.revokeObjectURL(urlRef.current); }, []);

  function avatarName(id) {
    if (!id || id === "default") return t("默认形象");
    const b = AVATAR_THUMBS.find((a) => a.id === id);
    if (b) return t(b.name);
    if (id.indexOf("trained:") === 0) { const j = trained.find((x) => "trained:" + x.id === id); return j ? j.name : t("我训练的形象"); }
    // Baked ids are "bake:<dir>"; show the readable half rather than the raw id.
    if (id.indexOf("bake:") === 0) return id.slice(5);
    return id;
  }
  const avatarImg = avatarThumb;
  function motionName(ref) {
    const fn = ee && window.PL_FEATURES && window.PL_FEATURES.slots && window.PL_FEATURES.slots.motionLabel;
    if (fn) return fn(ref);
    return ref ? t("待机动作") : t("默认");
  }

  async function tryListen() {
    if (!voice) { toast.warn(t("尚未设置音色")); return; }
    setPlaying(true);
    try {
      const url = await PL.voicePreview(sample, voice, {});
      if (urlRef.current) URL.revokeObjectURL(urlRef.current);
      urlRef.current = url;
      if (!audioRef.current) audioRef.current = new Audio();
      audioRef.current.src = url;
      audioRef.current.onended = () => setPlaying(false);
      await audioRef.current.play();
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); setPlaying(false); }
  }

  const img = avatarImg(cur);
  const motionRef = motions[cur] || "builtin:d14";
  return (
    <div className="hero-card">
      <div className="hero-card-thumb">
        {img ? <img src={img} alt="" loading="lazy" /> : <Icon name="user" size={28} />}
      </div>
      <div className="hero-card-body">
        <div className="hero-card-top">
          <span className="hero-card-name">{t("当前数字人")}</span>
          {applying
            ? <span className="hero-status applying"><span className="spinner" />{t("应用中")}</span>
            : <span className="hero-status live"><Icon name="check" size={12} />{t("已上线")}</span>}
        </div>
        <div className="hero-attrs">
          <div className="hero-attr"><span className="hero-attr-k">{t("形象")}</span><span className="hero-attr-v" title={avatarName(cur)}>{avatarName(cur)}</span></div>
          <div className="hero-attr"><span className="hero-attr-k">{t("音色")}</span><span className="hero-attr-v" title={voice}>{voice || t("默认")}</span></div>
          <div className="hero-attr"><span className="hero-attr-k">{t("待机神态")}</span><span className="hero-attr-v">{motionName(motionRef)}</span></div>
        </div>
      </div>
      <div className="hero-actions">
        {ee ? <Btn kind="secondary" icon="play" loading={playing} disabled={playing || !voice} onClick={tryListen}>{t("试听")}</Btn> : null}
        {navigate ? <Btn kind="primary" icon="message" onClick={() => { try { sessionStorage.setItem("pl_autoconnect", "1"); } catch (e) {} navigate("/playground"); }}>{t("进入对话")}</Btn> : null}
      </div>
    </div>
  );
}

/* 形象与声音主页（当前实做音色；形象为企业版占位） */
function VoicesPage({ navigate, features }) {
  const [voices, setVoices] = React.useState(null);
  const [error, setError] = React.useState(null);
  const [loading, setLoading] = React.useState(true);
  const [modal, setModal] = React.useState(false);
  const [delTarget, setDelTarget] = React.useState(null);
  const [delBusy, setDelBusy] = React.useState(false);
  const [sample, setSample] = React.useState(t("你好，我是你的智能助手，很高兴为你服务。"));
  const [instructions, setInstructions] = React.useState("");
  const [speed, setSpeed] = React.useState(1);
  const savedStyleRef = React.useRef({ instructions: "", speed: 1 }); // 配置中心里已保存的风格，用于脏检测
  const [styleDirty, setStyleDirty] = React.useState(false); // 风格指令/语速改过但未保存
  const [savingStyle, setSavingStyle] = React.useState(false);
  const [playing, setPlaying] = React.useState(null); // voice id currently auditioning
  const [seedInput, setSeedInput] = React.useState(""); // 选中音色的种子（可调，空=随机）
  const [voiceSeeds, setVoiceSeeds] = React.useState({}); // 每个音色各自存的种子 id→seed
  const [defVoice, setDefVoice] = React.useState(null); // 生产默认音色 id
  const [selected, setSelected] = React.useState(null); // 当前选中（试听栏作用对象）id
  const [savingSeed, setSavingSeed] = React.useState(false);
  const [savingDefault, setSavingDefault] = React.useState(false);
  const audioRef = React.useRef(null);
  const urlRef = React.useRef(null);
  const toast = useToast();
  const [tab, setTab] = React.useState("avatar"); // avatar=数字人形象, voice=音色, +feature 模块注册的功能 tab（如 motion）
  // Full-featured build: training/bake/voice-clone light up when the backend
  // gate reports the training feature (the merged build has it all on; a
  // narrower assembly reports what it enables).
  const EE = !!(features && features.features && features.features.training);
  const [avatarOpen, setAvatarOpen] = React.useState(false); // 训练新形象弹窗（开关由页头按钮控制）
  // EE 功能模块经 window.PL_FEATURES.tabs 注册的「形象与声音」子 Tab（如动作骨架）。模块未交付则为空。
  const featTabs = ((window.PL_FEATURES && window.PL_FEATURES.tabs) || []).filter((x) => x.host === "voices");
  const featTab = featTabs.find((x) => x.key === tab);
  const [featOpen, setFeatOpen] = React.useState({}); // 各功能 tab 的页头按钮弹窗开关 key→bool
  const [heroKey, setHeroKey] = React.useState(0); // bump 后 hero 重拉 config（换脸/换神态/换音色完成后）
  const [applying, setApplying] = React.useState(null); // hero「应用中」标：烘焙进行中时的形象/动作名
  const [advOpen, setAdvOpen] = React.useState(false); // 音色「高级」折叠（种子等进阶项）

  const load = React.useCallback(async (silent) => {
    if (!silent) { setLoading(true); setError(null); }
    try { setVoices(await PL.ttsVoices()); }
    catch (e) { if (!silent) setError(e); }
    setLoading(false);
  }, []);
  React.useEffect(() => { load(); }, [load]);

  // 拉配置中心 TTS：每音色种子映射 + 当前默认音色；默认选中默认音色，种子框预填它的种子。
  // 风格指令/语速也从配置回填 —— 它们随「设为当前/保存」写入生产，试听所见即生产所得。
  React.useEffect(() => {
    PL.getConfigGroup("tts").then((c) => {
      if (!c) return;
      const vs = c.voice_seeds || {};
      setVoiceSeeds(vs);
      if (c.voice) {
        setDefVoice(c.voice);
        setSelected(c.voice);
        const s = vs[c.voice] || 0;
        setSeedInput(s > 0 ? String(s) : "");
      }
      if (c.instructions) setInstructions(c.instructions);
      if (c.speed > 0) setSpeed(c.speed);
      savedStyleRef.current = { instructions: c.instructions || "", speed: c.speed > 0 ? c.speed : 1 };
      setStyleDirty(false);
    }).catch(() => {});
  }, []);

  React.useEffect(() => () => { if (urlRef.current) URL.revokeObjectURL(urlRef.current); }, []);

  async function doPreview(v, seed) {
    if (!sample.trim()) { toast.warn(t("请先输入试听文本")); return; }
    setPlaying(v.id);
    try {
      const url = await PL.voicePreview(sample.trim(), v.id, {
        seed: seed, instructions: instructions.trim(), speed: speed,
      });
      if (urlRef.current) URL.revokeObjectURL(urlRef.current);
      urlRef.current = url;
      if (!audioRef.current) audioRef.current = new Audio();
      audioRef.current.src = url;
      audioRef.current.onended = () => setPlaying(null);
      await audioRef.current.play();
    } catch (e) {
      toast.error(PL.errText(e), e.msgRaw);
      setPlaying(null);
    }
  }
  function curSeed() { const n = parseInt(seedInput, 10); return Number.isFinite(n) && n > 0 ? n : 0; } // 0=随机

  // 点卡片：把它设为试听栏作用对象，种子框换成它自己存的种子
  function selectVoice(v) {
    setSelected(v.id);
    const s = voiceSeeds[v.id] || 0;
    setSeedInput(s > 0 ? String(s) : "");
  }
  // 试听：选中的用当前种子框；其它音色先选中、用它自己存的种子
  function preview(v) {
    if (v.id === selected) { doPreview(v, curSeed()); return; }
    const s = voiceSeeds[v.id] || 0;
    setSelected(v.id);
    setSeedInput(s > 0 ? String(s) : "");
    doPreview(v, s);
  }
  function reroll() {
    if (!selected) return;
    const s = 1 + Math.floor(Math.random() * 999998);
    setSeedInput(String(s));
    const v = (voices || []).find((x) => x.id === selected);
    if (v) doPreview(v, s);
  }

  const selName = selected || "";

  // 保存「选中音色」的种子到配置中心（发送完整 voice_seeds map）。若它正是默认音色，同时更新生效 seed。
  async function saveSeed() {
    if (!selected) return;
    setSavingSeed(true);
    try {
      const seed = curSeed();
      const next = Object.assign({}, voiceSeeds, { [selected]: seed });
      const patch = selected === defVoice ? { seed: seed, voice_seeds: next } : { voice_seeds: next };
      await PL.putConfig("tts", patch);
      setVoiceSeeds(next);
      toast.ok(seed > 0
        ? t("已保存音色「{0}」的种子 {1}", selected, seed)
        : t("已把音色「{0}」设为每句随机", selected));
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setSavingSeed(false);
  }

  // 把「选中音色」设为生产默认：voice=它、seed=它当前种子，并存进 voice_seeds；
  // 风格指令/语速一并写入（生产 TTS 同参合成）。热生效；已发布频道需重新发布或改频道音色。
  async function setDefault() {
    if (!selected || selected === defVoice) return;
    setSavingDefault(true);
    try {
      const seed = curSeed();
      const next = Object.assign({}, voiceSeeds, { [selected]: seed });
      await PL.putConfig("tts", { voice: selected, seed: seed, voice_seeds: next,
        instructions: instructions.trim(), speed: speed });
      setDefVoice(selected); setVoiceSeeds(next); setHeroKey((k) => k + 1);
      savedStyleRef.current = { instructions: instructions.trim(), speed: speed };
      setStyleDirty(false);
      toast.ok(t("已设默认音色「{0}」（种子 {1}），已即时生效；已发布频道需重新发布", selected, seed > 0 ? seed : t("随机")));
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setSavingDefault(false);
  }

  // 只保存风格指令/语速到生产（不换音色）。热生效。
  async function saveStyle() {
    setSavingStyle(true);
    try {
      await PL.putConfig("tts", { instructions: instructions.trim(), speed: speed });
      savedStyleRef.current = { instructions: instructions.trim(), speed: speed };
      setStyleDirty(false);
      toast.ok(t("风格与语速已保存，已即时生效；已发布频道需重新发布"));
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setSavingStyle(false);
  }
  function markStyle(nextIns, nextSpeed) {
    const s = savedStyleRef.current;
    setStyleDirty(nextIns.trim() !== s.instructions || Math.abs(nextSpeed - s.speed) > 1e-9);
  }

  async function doDelete() {
    setDelBusy(true);
    try {
      await PL.voiceDelete(delTarget.id);
      toast.ok(t("音色「{0}」已删除", delTarget.id));
      setDelTarget(null);
      load(true);
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setDelBusy(false);
  }

  const custom = (voices || []).filter(isCustomVoice);
  const builtin = (voices || []).filter((v) => !isCustomVoice(v));

  return (
    <div className="fade-in" data-screen-label="形象与声音">
      <div className="page-head">
        <div>
          <h1>{EE ? t("形象与声音") : t("数字人形象")}</h1>
          <div className="desc">{EE ? t("克隆与管理数字人的形象与音色") : t("选择数字人形象（下个会话生效）")}</div>
        </div>
        <div className="actions">
          {EE ? (tab === "avatar"
            ? <Btn kind="primary" icon="plus" onClick={() => setAvatarOpen(true)}>{t("训练新形象")}</Btn>
            : tab === "voice"
            ? <Btn kind="primary" icon="plus" onClick={() => setModal(true)}>{t("克隆新音色")}</Btn>
            : featTab && featTab.actionLabel
            ? <Btn kind="primary" icon={featTab.actionIcon || "plus"} onClick={() => setFeatOpen((o) => Object.assign({}, o, { [featTab.key]: true }))}>{featTab.actionLabel}</Btn>
            : null) : null}
        </div>
      </div>

      <CurrentAvatarHero reloadKey={heroKey} applying={applying} navigate={navigate} ee={EE} />

      <Tabs active={tab} onChange={setTab} items={EE ? [
        { key: "avatar", label: t("形象") },
        { key: "voice", label: t("音色") },
      ].concat(featTabs.map((x) => ({ key: x.key, label: x.label }))) : [
        { key: "avatar", label: t("形象") },
      ]} />

      {tab === "avatar" ? <AvatarSection trainOpen={avatarOpen} setTrainOpen={setAvatarOpen} onChanged={() => setHeroKey((k) => k + 1)} onApplying={setApplying} applying={applying} ee={EE} /> :
        featTab ? featTab.render({ open: !!featOpen[featTab.key], setOpen: (v) => setFeatOpen((o) => Object.assign({}, o, { [featTab.key]: v })) }) :
        loading ? <Loading rows={4} /> :
        error ? <ErrorState error={error} onRetry={() => load()} /> : (
          <React.Fragment>
            {/* 试听设置：作用于「选中的音色」；每个音色各自保存自己的种子。sticky：选下方卡片后工具栏始终可见 */}
            <div className="card card-pad audition-sticky" style={{ marginTop: 4, marginBottom: 14 }}>
              <div className="card-title" style={{ display: "flex", alignItems: "center", gap: 8 }}>
                {t("试听设置")}
                {selName
                  ? <span className="v-tuning-chip"><Icon name="play" size={11} />{t("正在调：{0}", selName)}</span>
                  : <span className="small muted" style={{ fontWeight: 400 }}>{t("点下方卡片选一个音色")}</span>}
              </div>
              <div className="audition-bar">
                <input className="input grow" style={{ minWidth: 200 }} value={sample} maxLength={200}
                  onChange={(e) => setSample(e.target.value)} placeholder={t("试听文本")} />
                <input className="input" style={{ width: 140 }} value={instructions} maxLength={60}
                  onChange={(e) => { setInstructions(e.target.value); markStyle(e.target.value, speed); }} placeholder={t("（选填）更沉稳")} />
                <div className="row" style={{ gap: 10, flex: "none", width: 232 }}>
                  <span className="small muted" style={{ flex: "none" }}>{t("语速")}</span>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <Slider value={speed} min={0.5} max={2} step={0.05} onChange={(v) => { setSpeed(v); markStyle(instructions, v); }} format={(x) => x.toFixed(2) + "×"} />
                  </div>
                </div>
                {styleDirty
                  ? <Btn disabled={savingStyle} loading={savingStyle} onClick={saveStyle}>{t("保存风格")}</Btn>
                  : null}
                <Btn kind="primary" disabled={!selected || selected === defVoice || savingDefault} loading={savingDefault} onClick={setDefault}>{t("设为当前")}</Btn>
              </div>
              <div className="adv-toggle" onClick={() => setAdvOpen(!advOpen)}>
                <Icon name={advOpen ? "chevronDown" : "chevronRight"} size={13} />{t("高级")}
              </div>
              {advOpen ? (
                <div className="adv-panel">
                  <span className="small muted" style={{ flex: "none" }}>{t("声线种子")}</span>
                  <input className="input" type="number" min={0} style={{ width: 92 }} value={seedInput}
                    placeholder={t("随机")} disabled={!selected}
                    onChange={(e) => setSeedInput(e.target.value)} />
                  <Btn kind="ghost" icon="refresh" disabled={!selected || playing !== null} onClick={reroll}>{t("随机一版")}</Btn>
                  <Btn kind="secondary" disabled={!selected || savingSeed} loading={savingSeed} onClick={saveSeed}>{t("保存种子")}</Btn>
                  <span className="small muted" style={{ flexBasis: "100%", marginTop: 2 }}>
                    {t("声线种子固定后每句一致；留空=每句随机。调好「保存种子」即记在该音色名下（每个音色各存各的）。")}
                  </span>
                </div>
              ) : null}
              <div className="small muted" style={{ marginTop: 8 }}>
                {t("点卡片选中并「试听」，满意后「设为当前」，生产数字人即用它；换音色即时生效，已发布频道需重新发布。")}
              </div>
            </div>

            {/* 自定义音色 */}
            <div className="card" style={{ marginBottom: 14 }}>
              <div className="card-pad" style={{ paddingBottom: custom.length ? 0 : 20 }}>
                <div className="card-title" style={{ marginBottom: 0 }}>
                  {t("自定义音色")}<span className="muted small" style={{ fontWeight: 400 }}>{custom.length}</span>
                </div>
              </div>
              {custom.length === 0 ? (
                <EmptyState icon="mic" title={t("还没有自定义音色")}
                  desc={t("点右上角「克隆新音色」，上传一段参考人声即可生成专属音色")}
                  action={<Btn kind="primary" icon="plus" onClick={() => setModal(true)}>{t("克隆新音色")}</Btn>} />
              ) : (
                <div className="voice-grid">
                  {custom.map((v) => <VoiceCard key={v.id} v={v} playing={playing} onPreview={preview} onSelect={selectVoice} onDelete={setDelTarget} isDefault={v.id === defVoice} isSelected={v.id === selected} savedSeed={voiceSeeds[v.id] || 0} />)}
                </div>
              )}
            </div>

            {/* 内置音色 */}
            <div className="card" style={{ marginBottom: 14 }}>
              <div className="card-pad" style={{ paddingBottom: 0 }}>
                <div className="card-title" style={{ marginBottom: 0 }}>
                  {t("内置音色")}<span className="muted small" style={{ fontWeight: 400 }}>{builtin.length}</span>
                </div>
              </div>
              <div className="voice-grid">
                {builtin.map((v) => <VoiceCard key={v.id} v={v} playing={playing} onPreview={preview} onSelect={selectVoice} onDelete={setDelTarget} isDefault={v.id === defVoice} isSelected={v.id === selected} savedSeed={voiceSeeds[v.id] || 0} />)}
              </div>
            </div>

            {/* 三档预留：按说话人微调，企业版功能占位 */}
            <div className="card card-pad" style={{ opacity: 0.9 }}>
              <div className="card-title" style={{ marginBottom: 8 }}>
                {t("音色微调")}
                <span className="badge muted" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
                  <Icon name="lock" size={11} />{t("企业版")}
                </span>
                <span className="badge info">{t("即将上线")}</span>
              </div>
              <div className="small muted" style={{ lineHeight: 1.7 }}>
                {t("零样本克隆能抓住音色大轮廓，但有天花板。「音色微调」用你的若干分钟录音针对性训练一个专属权重，相似度显著高于现学，逼近“真假难辨”。")}
                <div style={{ marginTop: 6 }}>{t("企业版功能，需要更高保真度请联系我们开通。")}</div>
              </div>
            </div>
          </React.Fragment>
        )}

      {modal ? <VoiceUploadModal onClose={() => setModal(false)} onSaved={() => { setModal(false); load(true); }} /> : null}
      {delTarget ? (
        <ConfirmDialog
          title={t("删除音色「{0}」", delTarget.id)}
          body={t("删除后使用该音色的频道将回退到默认音色，不可恢复。")}
          confirmText={t("永久删除")} danger busy={delBusy}
          onConfirm={doDelete} onClose={() => setDelTarget(null)}
        />
      ) : null}
    </div>
  );
}

Object.assign(window, { VoicesPage });
