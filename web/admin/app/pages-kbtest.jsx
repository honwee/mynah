/* Mynah 管理控制台 — 检索测试（调参 playground，跨所有启用的库） */

function ScoreBadge({ score }) {
  const cls = score >= 0.6 ? "hi" : score >= 0.5 ? "mid" : "lo";
  return <span className={"score-badge " + cls}>{score.toFixed(2)}</span>;
}

/* 精排分徽标：交叉编码器分数分布与向量分不同（好答案通常 >0.3，区分度更大） */
function RerankBadge({ score }) {
  const cls = score >= 0.3 ? "hi" : score >= 0.1 ? "mid" : "lo";
  return (
    <span className={"score-badge " + cls} title={t("精排分（交叉编码器相关性，0~1）")}>
      <Icon name="zap" size={10} style={{ verticalAlign: "-1px", marginRight: 3 }} />{score.toFixed(2)}
    </span>
  );
}

function SearchTestView({ navigate }) {
  const [ragCfg, setRagCfg] = React.useState(null);
  const [canRerank, setCanRerank] = React.useState(false); // features.rerank：企业版且已部署精排
  const [rerank, setRerank] = React.useState(true);        // 本次检索是否启用精排（缺省开）
  const [query, setQuery] = React.useState("");
  const [topK, setTopK] = React.useState(5);
  const [threshold, setThreshold] = React.useState(0.5);
  const [result, setResult] = React.useState(null);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const [saveBusy, setSaveBusy] = React.useState(false);
  const toast = useToast();

  /* 缺省值取当前线上配置；能力开关决定是否展示精排 UI */
  React.useEffect(() => {
    (async () => {
      try {
        const r = await PL.getConfigGroup("rag");
        setRagCfg(r);
        setTopK(r.top_k); setThreshold(r.threshold);
      } catch (e) { /* 表单仍可用默认值 */ }
      try {
        const f = await PL.features();
        setCanRerank(!!(f && f.features && f.features.rerank));
      } catch (e) { /* 拉不到按无精排处理 */ }
    })();
  }, []);

  async function run(e, rerankOverride) {
    if (e) e.preventDefault();
    if (!query.trim()) return;
    const useRerank = rerankOverride != null ? rerankOverride : rerank;
    setBusy(true); setError(null);
    try {
      const params = { query: query.trim(), top_k: topK, threshold: threshold };
      if (canRerank) params.rerank = useRerank; // 社区版后端忽略该字段，不传以保持请求最简
      const r = await PL.searchTest(params);
      setResult(r);
    } catch (err) {
      setError(err); setResult(null);
    }
    setBusy(false);
  }

  async function saveAsConfig() {
    setSaveBusy(true);
    try {
      await PL.putConfig("rag", { top_k: topK, threshold: threshold });
      toast.ok(t("已保存为线上配置，下一轮对话即生效"));
      setRagCfg((r) => r ? { ...r, top_k: topK, threshold: threshold } : r);
    } catch (e) { toast.error(PL.errText(e), e.msgRaw); }
    setSaveBusy(false);
  }

  const dirtyVsOnline = ragCfg && (topK !== ragCfg.top_k || threshold !== ragCfg.threshold);

  return (
    <div className="search-layout" data-screen-label="检索测试">
      <div>
        <form className="card card-pad" onSubmit={run}>
          <Field label={t("测试问题")} required>
            <textarea className="textarea" rows={3} value={query} placeholder={t("模拟访客提问，如：企业版多少钱？")}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); run(); } }}></textarea>
          </Field>
          <Field label="top_k" help={t("返回的最大片段数")}>
            <input className="input" type="number" min={1} max={20} value={topK} style={{ width: 120 }}
              onChange={(e) => setTopK(Math.max(1, Math.min(20, Number(e.target.value) || 1)))} />
          </Field>
          <Field label={t("相似度阈值")} help={t("低于该分数的片段不返回")}>
            <Slider value={threshold} onChange={setThreshold} min={0} max={1} step={0.05} format={(v) => v.toFixed(2)} />
          </Field>
          {canRerank ? (
            <div className="field">
              <div className="row" style={{ gap: 12 }}>
                <Switch checked={rerank} onChange={setRerank} />
                <label className="field-label" style={{ margin: 0 }}>
                  {t("精排（rerank）")}<span className="badge info" style={{ marginLeft: 8, fontSize: 10.5 }}>{t("企业版")}</span>
                </label>
              </div>
              <div className="field-help">{t("交叉编码器对向量候选二次排序，命中更准；关闭可对比向量原序")}</div>
            </div>
          ) : null}
          <Btn kind="primary" size="lg" type="submit" loading={busy} icon="search">{t("检 索")}</Btn>
          {dirtyVsOnline ? (
            <div className="mt8">
              <Btn kind="ghost" size="sm" onClick={saveAsConfig} loading={saveBusy} type="button">{t("将当前参数保存为线上配置")}</Btn>
            </div>
          ) : null}
        </form>

        <div className="card card-pad mt16">
          <div className="config-note">
            <h5>{t("说明")}</h5>
            <p>{t("本测试不影响线上配置；线上对话使用「配置中心 → 知识库」中保存的参数。检索跨所有启用的库进行。")}</p>
            <p className="mt8">{t("中文问答的真实命中分数通常在 0.55 ~ 0.78 之间，阈值设 0.7 以上会漏掉正确答案。")}</p>
            {canRerank ? (
              <p className="mt8">{t("精排开启时结果按精排分排序；线上对话同样默认精排，失败自动回落向量序。")}</p>
            ) : null}
          </div>
        </div>
      </div>

      <div>
        {error ? (
          <div className="card"><ErrorState error={error} onRetry={run} /></div>
        ) : !result ? (
          <div className="card">
            <EmptyState icon="search" title={t("输入问题开始测试")} desc={t("检索结果将按相似度分数从高到低展示")} />
          </div>
        ) : result.chunks.length === 0 ? (
          <div className="card">
            <EmptyState icon="search" title={t("无命中")}
              desc={t("可尝试降低相似度阈值，或确认文档已就绪、所在库已启用")}
              action={<Btn kind="secondary" size="sm" onClick={() => { setThreshold(Math.max(0, Math.round((threshold - 0.15) * 100) / 100)); }}>{t("阈值降到 {0} 重试", Math.max(0, threshold - 0.15).toFixed(2))}</Btn>} />
          </div>
        ) : (
          <div>
            <div className="row mb8" style={{ fontSize: 12.5, color: "var(--text-3)", padding: "0 2px" }}>
              <span>{t("命中")} <b className="num" style={{ color: "var(--text)" }}>{result.chunks.length}</b> {t("条")}</span>
              <span>·</span>
              <span>{t("耗时")} <b className="num" style={{ color: "var(--text)" }}>{result.latency_ms}</b> ms</span>
              {result.reranked ? (
                <React.Fragment>
                  <span>·</span>
                  <span className="badge info" style={{ fontSize: 11 }}>
                    <Icon name="zap" size={10} />{t("已精排")}
                  </span>
                </React.Fragment>
              ) : null}
            </div>
            {result.chunks.map((c, i) => (
              <div className="card hit-card" key={i}>
                <div className="hit-head">
                  {result.reranked && c.rerank_score != null ? <RerankBadge score={c.rerank_score} /> : null}
                  <ScoreBadge score={c.score} />
                  <span className="hit-src"><Icon name="file" size={11} style={{ verticalAlign: "-1px", marginRight: 4 }} />{c.filename} · {t("第 {0} 块", c.seq)}</span>
                </div>
                <div className="hit-content">{c.content}</div>
              </div>
            ))}
            {result.reranked ? (
              <div className="muted small" style={{ padding: "2px 2px 0" }}>
                <Icon name="zap" size={10} style={{ verticalAlign: "-1px" }} /> = {t("精排分（排序依据）")} · {t("右侧为向量相似度分")}
                <a style={{ marginLeft: 10 }} href="#" onClick={(e) => { e.preventDefault(); setRerank(false); run(null, false); }}>{t("对比向量原序")} →</a>
              </div>
            ) : null}
          </div>
        )}
      </div>
    </div>
  );
}

Object.assign(window, { SearchTestView });
