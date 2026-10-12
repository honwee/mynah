/* Mynah 管理控制台 — 登录 / 改密 */

/* 登录页 */
function LoginPage({ onLoggedIn }) {
  const [username, setUsername] = React.useState("admin");
  const [password, setPassword] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);

  async function submit(e) {
    e.preventDefault();
    if (!username.trim() || !password) { setError(t("请输入用户名和密码")); return; }
    setBusy(true); setError(null);
    try {
      const data = await PL.login(username.trim(), password);
      PL.token.set(data.token);
      onLoggedIn(!!data.must_change_password);
    } catch (err) {
      if (err.network) setError(t("无法连接管理服务，请确认 cored 正在运行"));
      else if (err.status === 401) setError(t("用户名或密码错误"));
      else setError(PL.errText(err));
    }
    setBusy(false);
  }

  return (
    <div className="auth-page fade-in" data-screen-label="登录页">
      <div>
        <div className="auth-card">
          <div className="auth-brand">
            <BrandMark size={44} />
            <h1>Mynah</h1>
            <p>{t("实时数字人 · 管理控制台")}</p>
          </div>
          <form onSubmit={submit}>
            <Field label={t("用户名")}>
              <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
            </Field>
            <Field label={t("密码")}>
              <PasswordInput value={password} onChange={setPassword} autoFocus={true} />
            </Field>
            {error ? <div className="banner danger" style={{ marginBottom: 14, padding: "8px 12px" }}><Icon name="alert" size={14} />{error}</div> : null}
            <Btn kind="primary" size="lg" type="submit" loading={busy}>{t("登 录")}</Btn>
          </form>
          <div className="auth-foot">{t("初始密码在服务启动日志中打印（仅一次）")}<br />{t("登录后请尽快修改")}</div>
        </div>
        <div style={{ textAlign: "center", marginTop: 16, fontSize: 11.5, color: "var(--text-3)" }}>{t("Mynah 开源版 · API v1")}</div>
      </div>
    </div>
  );
}

/* 改密表单（强制改密页 + 顶栏修改密码弹窗 复用） */
function PasswordForm({ onDone, onCancel, busyLabel }) {
  const [oldPw, setOldPw] = React.useState("");
  const [newPw, setNewPw] = React.useState("");
  const [confirmPw, setConfirmPw] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [errors, setErrors] = React.useState({});
  const toast = useToast();

  async function submit(e) {
    e.preventDefault();
    const errs = {};
    if (!oldPw) errs.old = t("请输入当前密码");
    if (newPw.length < 8) errs.next = t("新密码至少 8 位");
    if (confirmPw !== newPw) errs.confirm = t("两次输入不一致");
    setErrors(errs);
    if (Object.keys(errs).length) return;
    setBusy(true);
    try {
      await PL.changePassword(oldPw, newPw);
      toast.ok(t("密码已修改，当前登录保持有效"));
      onDone();
    } catch (err) {
      if (err.status === 401) setErrors({ old: t("当前密码不正确") });
      else if (err.status === 400) setErrors({ next: t("新密码不符合要求"), detail: err.msgRaw });
      else if (err.network) setErrors({ form: t("无法连接管理服务，请确认 cored 正在运行") });
      else setErrors({ form: PL.errText(err) });
    }
    setBusy(false);
  }

  return (
    <form onSubmit={submit}>
      <Field label={t("当前密码")} error={errors.old}>
        <PasswordInput value={oldPw} onChange={setOldPw} autoFocus={true} />
      </Field>
      <Field label={t("新密码")} error={errors.next} help={errors.detail ? null : t("至少 8 位")}>
        <PasswordInput value={newPw} onChange={setNewPw} />
        {errors.detail ? <div className="code-detail">{errors.detail}</div> : null}
      </Field>
      <Field label={t("确认新密码")} error={errors.confirm}>
        <PasswordInput value={confirmPw} onChange={setConfirmPw} />
      </Field>
      {errors.form ? <div className="banner danger" style={{ padding: "8px 12px" }}><Icon name="alert" size={14} />{errors.form}</div> : null}
      <div className="row" style={{ justifyContent: "flex-end", gap: 10, paddingBottom: 16 }}>
        {onCancel ? <Btn kind="ghost" type="button" onClick={onCancel}>{t("取消")}</Btn> : null}
        <Btn kind="primary" type="submit" loading={busy}>{busyLabel || t("确认修改")}</Btn>
      </div>
    </form>
  );
}

/* 强制改密（独立全屏，不可跳过） */
function ForceChangePage({ onDone }) {
  return (
    <div className="auth-page fade-in" data-screen-label="强制改密页">
      <div className="auth-card" style={{ width: 400 }}>
        <div className="auth-brand" style={{ marginBottom: 18 }}>
          <BrandMark size={44} />
          <h1>{t("请修改初始密码")}</h1>
          <p>{t("首次登录需设置新密码后才能进入控制台")}</p>
        </div>
        <PasswordForm onDone={onDone} busyLabel={t("修改并进入控制台")} />
      </div>
    </div>
  );
}

Object.assign(window, { LoginPage, PasswordForm, ForceChangePage });
