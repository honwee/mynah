/* Mynah 管理控制台 — 知识库（库列表 + 库详情/文档管理 + 分块预览） */

/* 总开关提示条：rag.enabled=false 时常驻 */
function RagDisabledBanner({ onEnabled }) {
  const [busy, setBusy] = React.useState(false);
  const toast = useToast();
  async function enable() {
    setBusy(true);
    try {
      await PL.putConfig("rag", { enabled: true });
      toast.ok(t("知识库问答已开启，下一轮对话即生效"));
      onEnabled();
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setBusy(false);
  }
  return (
    <div className="banner warn">
      <Icon name="alert" size={15} />
      {t("知识库问答总开关当前关闭，库内容不会被对话引用")}
      <span className="banner-action">
        <Btn kind="secondary" size="sm" onClick={enable} loading={busy}>{t("立即开启")}</Btn>
      </span>
    </div>
  );
}

/* 新建 / 编辑库弹窗 */
function KbFormModal({ kb, onClose, onSaved }) {
  const isEdit = !!kb;
  const [name, setName] = React.useState(kb ? kb.name : "");
  const [desc, setDesc] = React.useState(kb ? kb.description : "");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const toast = useToast();

  async function submit() {
    if (!name.trim()) { setError(t("请输入库名称")); return; }
    setBusy(true); setError(null);
    try {
      const saved = isEdit
        ? await PL.kbPatch(kb.id, { name: name.trim(), description: desc.trim() })
        : await PL.kbCreate(name.trim(), desc.trim());
      toast.ok(isEdit ? t("已保存") : t("知识库「{0}」已创建", saved.name));
      onSaved(saved);
    } catch (e) {
      if (e.status === 409) setError(t("已存在同名知识库"));
      else setError(PL.errText(e) + (e.msgRaw ? ": " + e.msgRaw : ""));
    }
    setBusy(false);
  }

  return (
    <Modal title={isEdit ? t("编辑知识库") : t("新建知识库")} onClose={busy ? null : onClose} footer={
      <React.Fragment>
        <Btn kind="ghost" onClick={onClose} disabled={busy}>{t("取消")}</Btn>
        <Btn kind="primary" onClick={submit} loading={busy}>{isEdit ? t("保存") : t("创建")}</Btn>
      </React.Fragment>
    }>
      <Field label={t("名称")} required error={error}>
        <input className="input" value={name} autoFocus onChange={(e) => { setName(e.target.value); setError(null); }} placeholder={t("如：产品手册")} onKeyDown={(e) => { if (e.key === "Enter") submit(); }} />
      </Field>
      <Field label={t("描述")} help={t("给运营同事看的备注，可留空")}>
        <input className="input" value={desc} onChange={(e) => setDesc(e.target.value)} placeholder={t("这个库用来回答什么问题？")} />
      </Field>
    </Modal>
  );
}

/* ---------- 分块预览（二期增补接口 GET /documents/{id}/chunks，PRD §8） ---------- */
function ChunkPreviewModal({ doc, onClose }) {
  const [data, setData] = React.useState(null);
  const [error, setError] = React.useState(null);
  const [loading, setLoading] = React.useState(true);

  const load = React.useCallback(async () => {
    setLoading(true); setError(null);
    try {
      const r = await PL.docChunks(doc.id);
      setData(r);
    } catch (e) { setError(e); }
    setLoading(false);
  }, [doc.id]);
  React.useEffect(() => { load(); }, [load]);

  return (
    <Modal title={t("知识片段预览 — {0}", doc.filename)} onClose={onClose} width={620} footer={
      <React.Fragment>
        <span className="muted small" style={{ marginRight: "auto" }}>
          {t("依赖二期增补接口")} <span className="mono">GET /documents/{"{id}"}/chunks</span>（PRD §8）
        </span>
        <Btn kind="secondary" onClick={onClose}>{t("关闭")}</Btn>
      </React.Fragment>
    }>
      {loading ? <Loading rows={4} /> :
        error ? (
          error.status === 404 ? (
            <EmptyState icon="layers" title={t("接口未就绪")} desc={t("当前后端尚未提供分块预览接口（v1 之外的增补项），联调时请联系后端开启")} />
          ) : <ErrorState error={error} onRetry={load} />
        ) : (
          <div style={{ paddingBottom: 8 }}>
            <div className="muted small mb8">{t("共 {0} 块", data.chunk_count)}</div>
            {data.chunks.map((c) => (
              <div className="card hit-card" key={c.seq} style={{ marginBottom: 10 }}>
                <div className="hit-head">
                  <span className="badge muted mono" style={{ fontSize: 11 }}>#{c.seq}</span>
                  <span className="hit-src">{t("第 {0} 块", c.seq)}</span>
                </div>
                <div className="hit-content">{c.content}</div>
              </div>
            ))}
          </div>
        )}
    </Modal>
  );
}

/* ---------- 库列表 ---------- */
function KbListView({ navigate }) {
  const [kbs, setKbs] = React.useState(null);
  const [rag, setRag] = React.useState(null);
  const [error, setError] = React.useState(null);
  const [loading, setLoading] = React.useState(true);
  const [modal, setModal] = React.useState(null);
  const [delTarget, setDelTarget] = React.useState(null);
  const [delBusy, setDelBusy] = React.useState(false);
  const [toggling, setToggling] = React.useState({});
  const toast = useToast();

  const load = React.useCallback(async () => {
    setLoading(true); setError(null);
    try {
      const [k, r] = await Promise.all([PL.kbList(), PL.getConfigGroup("rag")]);
      setKbs(k); setRag(r);
    } catch (e) { setError(e); }
    setLoading(false);
  }, []);
  React.useEffect(() => { load(); }, [load]);

  async function toggle(kb, v) {
    setToggling((x) => ({ ...x, [kb.id]: true }));
    try {
      await PL.kbPatch(kb.id, { enabled: v });
      setKbs((list) => list.map((x) => x.id === kb.id ? { ...x, enabled: v } : x));
      toast.ok(v ? t("「{0}」已启用", kb.name) : t("「{0}」已停用，文档保留但不参与检索", kb.name));
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setToggling((x) => ({ ...x, [kb.id]: false }));
  }

  async function doDelete() {
    setDelBusy(true);
    try {
      await PL.kbDelete(delTarget.id);
      toast.ok(t("知识库「{0}」已删除", delTarget.name));
      setDelTarget(null);
      load();
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setDelBusy(false);
  }

  if (loading) return <Loading rows={4} />;
  if (error) return <ErrorState error={error} onRetry={load} />;

  return (
    <div>
      {rag && !rag.enabled ? <RagDisabledBanner onEnabled={() => setRag({ ...rag, enabled: true })} /> : null}

      <div className="card">
        {kbs.length === 0 ? (
          <EmptyState icon="book" title={t("还没有知识库")}
            desc={t("创建第一个知识库并上传文档，数字人就能回答专有问题")}
            action={<Btn kind="primary" icon="plus" onClick={() => setModal("create")}>{t("新建知识库")}</Btn>} />
        ) : (
          <table className="table">
            <thead>
              <tr><th>{t("名称")}</th><th>{t("描述")}</th><th>{t("文档数")}</th><th>{t("启用")}</th><th>{t("创建时间")}</th><th style={{ textAlign: "right" }}>{t("操作")}</th></tr>
            </thead>
            <tbody>
              {kbs.map((kb) => (
                <tr key={kb.id}>
                  <td>
                    <a className="tip" data-tip={t("嵌入模型 {0} · {1} 维", kb.embed_model, kb.embed_dim)}
                      style={{ fontWeight: 600 }} href={"#/kb/" + kb.id}
                      onClick={(e) => { e.preventDefault(); navigate("/kb/" + kb.id); }}>{kb.name}</a>
                  </td>
                  <td className="muted" style={{ maxWidth: 320, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{kb.description || "—"}</td>
                  <td className="num">{kb.doc_count}</td>
                  <td><Switch checked={kb.enabled} busy={toggling[kb.id]} onChange={(v) => toggle(kb, v)} /></td>
                  <td className="muted">{fmtDate(kb.created_at)}</td>
                  <td>
                    <div className="row-actions">
                      <IconBtn icon="edit" tip={t("重命名 / 改描述")} onClick={() => setModal({ kb })} />
                      <IconBtn icon="trash" tip={t("删除")} danger onClick={() => setDelTarget(kb)} />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {modal === "create" ? <KbFormModal onClose={() => setModal(null)} onSaved={() => { setModal(null); load(); }} /> : null}
      {modal && modal.kb ? <KbFormModal kb={modal.kb} onClose={() => setModal(null)} onSaved={() => { setModal(null); load(); }} /> : null}
      {delTarget ? (
        <ConfirmDialog
          title={t("删除知识库「{0}」", delTarget.name)}
          body={<span>{t("将")}<b style={{ color: "var(--danger)" }}>{t("级联删除库内全部 {0} 个文档与向量索引，不可恢复", delTarget.doc_count)}</b>{t("。数字人将无法再回答该库覆盖的问题。")}</span>}
          confirmText={t("永久删除")} danger requireText={delTarget.name} busy={delBusy}
          onConfirm={doDelete} onClose={() => setDelTarget(null)}
        />
      ) : null}
    </div>
  );
}

/* ---------- 知识库主页（tabs：库列表 / 检索测试） ---------- */
function KbPage({ tab, navigate }) {
  const active = tab === "test" ? "test" : "list";
  return (
    <div className="fade-in" data-screen-label="知识库">
      <div className="page-head">
        <div>
          <h1>{t("知识库")}</h1>
          <div className="desc">{t("上传文档让数字人回答专有问题；检索测试可跨所有启用的库验证命中效果")}</div>
        </div>
        {active === "list" ? (
          <div className="actions">
            <NewKbButton navigate={navigate} />
          </div>
        ) : null}
      </div>
      <Tabs
        items={[{ key: "list", label: t("库列表") }, { key: "test", label: t("检索测试") }]}
        active={active}
        onChange={(k) => navigate(k === "test" ? "/kb/test" : "/kb")}
      />
      {active === "list" ? <KbListView navigate={navigate} /> : <SearchTestView navigate={navigate} />}
    </div>
  );
}
function NewKbButton({ navigate }) {
  const [open, setOpen] = React.useState(false);
  return (
    <React.Fragment>
      <Btn kind="primary" icon="plus" onClick={() => setOpen(true)}>{t("新建知识库")}</Btn>
      {open ? <KbFormModal onClose={() => setOpen(false)} onSaved={(kb) => { setOpen(false); navigate("/kb/" + kb.id); }} /> : null}
    </React.Fragment>
  );
}

/* ---------- 库详情：文档管理 ---------- */
const ACCEPT_EXT = [".txt", ".md", ".markdown"];
function validateFile(f) {
  const lower = f.name.toLowerCase();
  if (!ACCEPT_EXT.some((e) => lower.endsWith(e))) return t("仅支持 .txt / .md / .markdown 文本文件");
  if (f.size > 10 * 1024 * 1024) return t("文件超过 10MB 限制");
  if (f.size === 0) return t("文件为空");
  return null;
}

function KbDetailPage({ kbId, navigate }) {
  const [kb, setKb] = React.useState(null);
  const [docs, setDocs] = React.useState(null);
  const [error, setError] = React.useState(null);
  const [loading, setLoading] = React.useState(true);
  const [over, setOver] = React.useState(false);
  const [uploadQueue, setUploadQueue] = React.useState(null);
  const [delTarget, setDelTarget] = React.useState(null);
  const [delBusy, setDelBusy] = React.useState(false);
  const [expanded, setExpanded] = React.useState({});
  const [previewDoc, setPreviewDoc] = React.useState(null);
  const fileRef = React.useRef(null);
  const toast = useToast();

  const load = React.useCallback(async (silent) => {
    if (!silent) { setLoading(true); setError(null); }
    try {
      const [kbs, d] = await Promise.all([PL.kbList(), PL.docList(kbId)]);
      const found = kbs.find((x) => x.id === kbId);
      if (!found) { setError({ status: 404, msgRaw: "knowledge base not found" }); setLoading(false); return; }
      setKb(found); setDocs(d); setError(null);
    } catch (e) {
      if (!silent) setError(e);
    }
    setLoading(false);
  }, [kbId]);
  React.useEffect(() => { load(); }, [load]);

  /* 摄取轮询：存在 pending/processing 时每 2s 刷新 */
  const hasActive = docs && docs.some((d) => d.status === "pending" || d.status === "processing");
  React.useEffect(() => {
    if (!hasActive) return;
    const tm = setInterval(() => load(true), 2000);
    return () => clearInterval(tm);
  }, [hasActive, load]);

  /* 串行上传（多选时逐个） */
  async function uploadFiles(fileList) {
    const files = Array.from(fileList);
    const valid = [];
    for (const f of files) {
      const err = validateFile(f);
      if (err) toast.error(t("「{0}」", f.name) + err);
      else valid.push(f);
    }
    if (!valid.length) return;
    setUploadQueue({ done: 0, total: valid.length, current: valid[0].name });
    for (let i = 0; i < valid.length; i++) {
      setUploadQueue({ done: i, total: valid.length, current: valid[i].name });
      try {
        await PL.docUpload(kbId, valid[i]);
      } catch (e) {
        toast.error(t("「{0}」上传失败：", valid[i].name) + PL.errText(e, { 413: "文件超过大小限制", 400: "文件格式不被接受" }), e.msgRaw);
      }
      await load(true);
    }
    setUploadQueue(null);
    toast.ok(valid.length > 1 ? t("{0} 个文件已上传，正在摄取", valid.length) : t("已上传，正在摄取（千字文档约 2~5 秒）"));
  }

  async function reindex(doc) {
    try {
      await PL.docReindex(doc.id);
      toast.ok(t("「{0}」已加入重建队列", doc.filename));
      load(true);
    } catch (e) {
      if (e.status === 409) toast.warn(t("该文档已在队列中"));
      else toast.error(PL.errText(e), e.msgRaw);
    }
  }

  async function doDeleteDoc() {
    setDelBusy(true);
    try {
      await PL.docDelete(delTarget.id);
      toast.ok(t("文档「{0}」已删除", delTarget.filename));
      setDelTarget(null);
      load(true);
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setDelBusy(false);
  }

  if (loading) return <Loading rows={5} />;
  if (error) return (
    <div className="fade-in">
      {error.status === 404
        ? <EmptyState icon="book" title={t("知识库不存在")} desc={t("该库可能已被删除")} action={<Btn kind="secondary" onClick={() => navigate("/kb")}>{t("返回库列表")}</Btn>} />
        : <ErrorState error={error} onRetry={() => load()} />}
    </div>
  );

  return (
    <div className="fade-in" data-screen-label="知识库详情">
      <div className="page-head" style={{ alignItems: "center" }}>
        <button className="btn btn-ghost btn-sm" onClick={() => navigate("/kb")} style={{ paddingLeft: 6 }}>
          <Icon name="chevronLeft" size={14} />{t("知识库")}
        </button>
        <div>
          <h1 style={{ display: "flex", alignItems: "center", gap: 10 }}>
            {kb.name}
            <span className={"badge " + (kb.enabled ? "ok" : "muted")}>{kb.enabled ? t("已启用") : t("已停用")}</span>
          </h1>
          <div className="desc">
            {kb.description || t("无描述")} · {t("{0} 个文档", kb.doc_count)} · {t("嵌入 {0}（{1} 维）", kb.embed_model, kb.embed_dim)}
          </div>
        </div>
        <div className="actions">
          <Btn kind="secondary" icon="search" onClick={() => navigate("/kb/test")}>{t("检索测试")}</Btn>
          <Btn kind="primary" icon="upload" onClick={() => fileRef.current && fileRef.current.click()} disabled={!!uploadQueue}>{t("上传文档")}</Btn>
        </div>
      </div>

      <input type="file" ref={fileRef} multiple accept=".txt,.md,.markdown" style={{ display: "none" }}
        onChange={(e) => { if (e.target.files.length) uploadFiles(e.target.files); e.target.value = ""; }} />

      <div
        className={"dropzone" + (over ? " over" : "")}
        onClick={() => fileRef.current && fileRef.current.click()}
        onDragOver={(e) => { e.preventDefault(); setOver(true); }}
        onDragLeave={() => setOver(false)}
        onDrop={(e) => { e.preventDefault(); setOver(false); if (e.dataTransfer.files.length) uploadFiles(e.dataTransfer.files); }}
      >
        <Icon name="upload" size={20} />
        <div className="dz-title">{uploadQueue ? t("正在上传 {0}/{1}：{2}", uploadQueue.done + 1, uploadQueue.total, uploadQueue.current) : t("点击或拖拽文件到此处上传")}</div>
        <div>{t("支持 .txt / .md / .markdown（UTF-8 编码），单文件 ≤ 10MB")}</div>
      </div>

      <div className="card">
        {docs.length === 0 ? (
          <EmptyState icon="file" title={t("还没有文档")} desc={t("上传第一个文档，几秒后即可在检索测试中验证效果")}
            action={<Btn kind="primary" icon="upload" onClick={() => fileRef.current && fileRef.current.click()}>{t("上传第一个文档")}</Btn>} />
        ) : (
          <table className="table">
            <thead>
              <tr><th>{t("文件名")}</th><th>{t("大小")}</th><th>{t("状态")}</th><th>{t("上传时间")}</th><th style={{ textAlign: "right" }}>{t("操作")}</th></tr>
            </thead>
            <tbody>
              {docs.map((d) => (
                <React.Fragment key={d.id}>
                  <tr>
                    <td style={{ fontWeight: 550 }}>
                      <span className="row" style={{ gap: 8 }}><Icon name="file" size={14} style={{ color: "var(--text-3)", flex: "none" }} />{d.filename}</span>
                    </td>
                    <td className="muted num">{fmtBytes(d.size_bytes)}</td>
                    <td>
                      <span className="row" style={{ gap: 8 }}>
                        <DocStatusBadge doc={d} />
                        {d.status === "failed" ? (
                          <button className="btn btn-ghost btn-sm" style={{ height: 24, fontSize: 12 }} onClick={() => setExpanded((x) => ({ ...x, [d.id]: !x[d.id] }))}>
                            {expanded[d.id] ? t("收起") : t("详情")}
                          </button>
                        ) : null}
                      </span>
                    </td>
                    <td className="muted">{fmtDate(d.created_at)}</td>
                    <td>
                      <div className="row-actions">
                        {d.status === "failed" ? <Btn kind="secondary" size="sm" icon="refresh" onClick={() => reindex(d)}>{t("重试")}</Btn> : null}
                        {d.status === "ready" ? <IconBtn icon="layers" tip={t("知识片段预览")} onClick={() => setPreviewDoc(d)} /> : null}
                        {d.status === "ready" ? <IconBtn icon="refresh" tip={t("重新索引：按当前分块参数重新处理")} onClick={() => reindex(d)} /> : null}
                        <IconBtn icon="trash" tip={t("删除")} danger onClick={() => setDelTarget(d)} />
                      </div>
                    </td>
                  </tr>
                  {expanded[d.id] && d.status === "failed" ? (
                    <tr>
                      <td colSpan={5} style={{ background: "var(--danger-bg)", borderRadius: 8 }}>
                        <div className="mono" style={{ color: "var(--danger)", fontSize: 12 }}>{d.error}</div>
                      </td>
                    </tr>
                  ) : null}
                </React.Fragment>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {previewDoc ? <ChunkPreviewModal doc={previewDoc} onClose={() => setPreviewDoc(null)} /> : null}
      {delTarget ? (
        <ConfirmDialog
          title={t("删除文档「{0}」", delTarget.filename)}
          body={t("将移除该文档的全部知识片段，数字人将无法再引用其中内容。")}
          confirmText={t("删除")} danger busy={delBusy}
          onConfirm={doDeleteDoc} onClose={() => setDelTarget(null)}
        />
      ) : null}
    </div>
  );
}

Object.assign(window, { KbPage, KbDetailPage });
