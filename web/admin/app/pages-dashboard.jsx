/* Mynah 管理控制台 — 仪表盘 */

const COMPONENT_META = {
  avatar: { name: "数字人引擎", desc: "生成形象画面与口型动作", down: "访客将看不到数字人画面" },
  asr: { name: "语音识别", desc: "将访客语音转为文字", down: "语音输入不可用，文字对话不受影响" },
  tts: { name: "语音合成", desc: "将回答文本合成为语音", down: "数字人将无法发声" },
  llm: { name: "对话模型", desc: "理解问题并生成回答", down: "数字人将无法回答任何问题" },
  ollama: { name: "向量模型", desc: "知识库文档嵌入与检索", down: "知识库检索与文档摄取不可用" },
  postgres: { name: "数据库", desc: "存储配置、知识库与向量索引", down: "配置与知识库功能整体不可用" },
};
const COMPONENT_ORDER = ["avatar", "asr", "tts", "llm", "ollama", "postgres"];

function HealthCard({ id, comp }) {
  const meta = COMPONENT_META[id];
  const st = comp.status;
  return (
    <div className="card health-card">
      <div className="hc-top">
        <span className={"dot " + (st === "ok" ? "ok" : st === "down" ? "danger" : "muted")}></span>
        <span className="hc-name">{t(meta.name)}</span>
        <span className="hc-en">{id}</span>
        <span className="hc-lat num">
          {st === "ok" ? (comp.latency_ms || 0) + " ms" : st === "down" ? <span style={{ color: "var(--danger)", fontWeight: 600 }}>{t("异常")}</span> : t("未启用")}
        </span>
      </div>
      {st === "down" ? (
        <div className="hc-err tip" data-tip={comp.error || t("无错误详情")} style={{ cursor: "help" }}>{t(meta.down)}</div>
      ) : (
        <div className="hc-desc">{st === "disabled" ? t("当前部署未启用该组件") : t(meta.desc)}</div>
      )}
    </div>
  );
}

function DashboardPage({ navigate }) {
  const [health, setHealth] = React.useState(null);
  const [sessions, setSessions] = React.useState(null);
  const [workers, setWorkers] = React.useState([]);
  const [rag, setRag] = React.useState(null);
  const [error, setError] = React.useState(null);
  const [loading, setLoading] = React.useState(true);
  const [ragBusy, setRagBusy] = React.useState(false);
  const toast = useToast();

  const load = React.useCallback(async (silent) => {
    if (!silent) { setLoading(true); setError(null); }
    try {
      const [h, s, r, wk] = await Promise.all([
        PL.health(), PL.sessions(), PL.getConfigGroup("rag"),
        PL.workers().catch(() => []), // 旧后端无此端点 → 不显示 worker 卡
      ]);
      setHealth(h); setSessions(s); setRag(r); setWorkers(wk || []); setError(null);
    } catch (e) {
      if (!silent) setError(e);
    }
    setLoading(false);
  }, []);

  React.useEffect(() => { load(); }, [load]);
  React.useEffect(() => {
    const t = setInterval(() => load(true), 30000);
    return () => clearInterval(t);
  }, [load]);

  async function toggleRag(v) {
    setRagBusy(true);
    try {
      await PL.putConfig("rag", { enabled: v });
      setRag((r) => ({ ...r, enabled: v }));
      toast.ok(v ? t("知识库问答已开启，下一轮对话即生效") : t("知识库问答已关闭"));
    } catch (e) {
      toast.error(PL.errText(e), e.msgRaw);
    }
    setRagBusy(false);
  }

  if (loading) return <Loading rows={5} />;
  if (error) return <ErrorState error={error} onRetry={() => load()} />;

  const downList = COMPONENT_ORDER.filter((k) => health.components[k] && health.components[k].status === "down");

  return (
    <div className="fade-in" data-screen-label="仪表盘">
      {health.status === "ok" ? (
        <div className="banner ok"><Icon name="check" size={15} />{t("系统正常 · 全部组件运行中")}</div>
      ) : (
        <div className="banner warn">
          <Icon name="alert" size={15} />
          {t("部分组件异常：{0}，对应能力已自动降级", downList.map((k) => t(COMPONENT_META[k].name)).join(t("、")))}
        </div>
      )}

      <div className="health-grid">
        {COMPONENT_ORDER.map((k) => health.components[k] ? <HealthCard key={k} id={k} comp={health.components[k]} /> : null)}
      </div>

      <div className="dash-grid">
        {workers.length > 0 ? (
          <div className="card card-pad">
            <div className="card-title">
              {t("数字人引擎池")}
              <span className="extra muted small">{t("{0} 空闲 / {1} 总数", workers.filter((w) => !w.session).length, workers.length)}</span>
            </div>
            <div className="muted small mb8">{t("每个 worker 同一时刻服务一位访客；全部占用时新访客会看到「数字人正忙」")}</div>
            <table className="table">
              <tbody>
                {workers.map((w) => (
                  <tr key={w.addr}>
                    <td className="mono">{w.addr}</td>
                    <td className="muted small">{w.detail || "-"}</td>
                    <td>
                      {w.session
                        ? <span className="badge ok"><span className="dot ok pulse"></span>{t("会话 {0}", w.session)}</span>
                        : <span className="badge muted">{t("空闲")}</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}

        <div className="card card-pad">
          <div className="card-title">
            {t("在线会话")}
            <span className="extra"><a href="#/sessions" onClick={(e) => { e.preventDefault(); navigate("/sessions"); }}>{t("会话监控")} →</a></span>
          </div>
          <div className="metric-num">{sessions.length}</div>
          <div className="muted small mb8">{t("当前接入的访客连接")}</div>
          {sessions.length > 0 ? (
            <table className="table">
              <tbody>
                {sessions.slice(0, 3).map((s) => (
                  <tr key={s.id} style={{ cursor: "pointer" }} onClick={() => navigate("/sessions")}>
                    <td className="mono">{s.id}</td>
                    <td className="muted"><RelTime value={s.created_at} /></td>
                    <td className="num muted">{t("{0} 轮", s.turns)}</td>
                    <td>
                      {s.speaking
                        ? <span className="badge ok"><span className="dot ok pulse"></span>{t("说话中")}</span>
                        : <span className="badge muted">{t("待机")}</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <div className="muted small" style={{ padding: "14px 0 4px" }}>{t("当前没有访客在线")}</div>
          )}
        </div>

        <div className="card card-pad">
          <div className="card-title">{t("快捷开关")}</div>
          <div className="row" style={{ alignItems: "flex-start", gap: 14, padding: "6px 0" }}>
            <Switch checked={rag.enabled} onChange={toggleRag} busy={ragBusy} />
            <div className="grow">
              <div style={{ fontWeight: 600, fontSize: 13.5 }}>{t("知识库问答")}</div>
              <div className="muted small" style={{ marginTop: 2 }}>{t("开启后对话将引用知识库内容；修改立即生效，无需重启")}</div>
            </div>
            <span className={"badge " + (rag.enabled ? "ok" : "muted")}>{rag.enabled ? t("已开启") : t("已关闭")}</span>
          </div>
          <div style={{ borderTop: "1px solid var(--border)", marginTop: 12, paddingTop: 12 }}>
            <div className="muted small">
              {t("检索参数：top_k {0} · 阈值 {1} · 超时 {2} ms", rag.top_k, rag.threshold, rag.timeout_ms)}
              <a style={{ marginLeft: 8 }} href="#/config/rag" onClick={(e) => { e.preventDefault(); navigate("/config/rag"); }}>{t("调整")} →</a>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

Object.assign(window, { DashboardPage, COMPONENT_META });
