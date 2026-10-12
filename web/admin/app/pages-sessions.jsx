/* Mynah 管理控制台 — 会话监控 */

function SessionsPage() {
  const [sessions, setSessions] = React.useState(null);
  const [error, setError] = React.useState(null);
  const [loading, setLoading] = React.useState(true);
  const [refreshing, setRefreshing] = React.useState(false);
  const [kickTarget, setKickTarget] = React.useState(null);
  const [kickBusy, setKickBusy] = React.useState(false);
  const toast = useToast();

  const load = React.useCallback(async (silent) => {
    if (!silent) { setLoading(true); setError(null); } else { setRefreshing(true); }
    try {
      const s = await PL.sessions();
      setSessions(s); setError(null);
    } catch (e) {
      if (!silent) setError(e);
    }
    setLoading(false); setRefreshing(false);
  }, []);

  React.useEffect(() => { load(); }, [load]);
  React.useEffect(() => {
    const t = setInterval(() => load(true), 5000);
    return () => clearInterval(t);
  }, [load]);

  async function doKick() {
    setKickBusy(true);
    try {
      await PL.kick(kickTarget.id);
      toast.ok(t("已断开会话 {0}", kickTarget.id));
    } catch (e) {
      if (e.status === 404) toast.ok(t("该会话已结束"));
      else toast.error(PL.errText(e), e.msgRaw);
    }
    setKickBusy(false);
    setKickTarget(null);
    load(true);
  }

  return (
    <div className="fade-in" data-screen-label="会话监控">
      <div className="page-head">
        <div>
          <h1>{t("会话监控")}</h1>
          <div className="desc">{t("当前接入的访客连接 · 每 5 秒自动刷新")}</div>
        </div>
        <div className="actions">
          <Btn kind="secondary" icon="refresh" onClick={() => load(true)} loading={refreshing}>{t("刷新")}</Btn>
        </div>
      </div>

      <div className="card">
        {loading ? <Loading rows={3} /> :
          error ? <ErrorState error={error} onRetry={() => load()} /> :
          sessions.length === 0 ? (
            <EmptyState icon="monitor" title={t("当前没有访客在线")} desc={t("访客打开数字人页面并建立连接后，会话将出现在这里")} />
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>{t("会话 ID")}</th><th>{t("接入时间")}</th><th>{t("对话轮数")}</th><th>{t("状态")}</th><th>{t("类型")}</th><th style={{ textAlign: "right" }}>{t("操作")}</th>
                </tr>
              </thead>
              <tbody>
                {sessions.map((s) => (
                  <tr key={s.id}>
                    <td className="mono">{s.id}</td>
                    <td className="muted"><RelTime value={s.created_at} /></td>
                    <td className="num">{s.turns}</td>
                    <td>
                      {s.speaking
                        ? <span className="badge ok"><span className="dot ok pulse"></span>{t("说话中")}</span>
                        : <span className="badge muted">{t("待机")}</span>}
                    </td>
                    <td>
                      <span className="badge info" style={s.voice ? null : { background: "var(--surface-2)", color: "var(--text-2)" }}>
                        <Icon name={s.voice ? "mic" : "message"} size={11} />{s.voice ? t("语音") : t("文字")}
                      </span>
                    </td>
                    <td>
                      <div className="row-actions">
                        <Btn kind="danger-ghost" size="sm" icon="power" onClick={() => setKickTarget(s)}>{t("踢下线")}</Btn>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
      </div>

      {kickTarget ? (
        <ConfirmDialog
          title={t("踢下线会话 {0}", kickTarget.id)}
          body={t("将立即断开该访客的连接，访客页面会显示连接已断开。确定继续吗？")}
          confirmText={t("踢下线")}
          danger
          busy={kickBusy}
          onConfirm={doKick}
          onClose={() => setKickTarget(null)}
        />
      ) : null}
    </div>
  );
}

Object.assign(window, { SessionsPage });
