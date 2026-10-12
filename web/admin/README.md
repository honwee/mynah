# Mynah 管理控制台

cored 控制面的 Web 前端（React 18 UMD + Babel standalone，**无构建步骤**，目录即产物）。对接 `docs/api/admin-api.md` 描述的 admin API v1。

## 部署

生产推荐由 cored 管理端口同源托管（免 CORS）：

```bash
cored ... --db "$PL_DB_DSN" \
  --admin-listen 127.0.0.1:9080 \
  --admin-tls-listen :9443 \
  --admin-web web/admin               # 指向本目录即可
```

浏览器访问 `https://<host>:9443/`。所有依赖（react / react-dom / babel）已本地化在 `vendor/`，内网无外网环境可直接用。

## 开发

无需 npm/构建，任意静态服务器起本目录即可：

```bash
python3 -m http.server 8088   # 然后访问 http://localhost:8088/
```

- 探测不到 `/api/v1` 后端时自动启用**内置演示数据**（mock.js），右上角显示「演示数据」徽标；右下角 Tweaks 面板可切换演示场景（异常/高负载/空数据/企业版等）。
- 对接真实后端：用 dev proxy 把 `/api/` 转发到 cored 管理端口，探测成功自动切换真实接口。
- 语法检查：`npx -y esbuild "--loader:.jsx=jsx" app/<file>.jsx`（仅 parse，不产出）。

## 结构

| 文件 | 职责 |
|---|---|
| `index.html` | 入口，按依赖顺序加载脚本 |
| `app/i18n.js` | 中/英多语言（源码内联中文为 key） |
| `app/mock.js` | 内置演示后端（仅真实 API 不可达时启用） |
| `app/api.js` | API 客户端（token 管理 / 401 拦截 / SSE 流式） |
| `app/main.jsx` | 框架：路由 / 侧边栏（可收起）/ 顶栏 / 主题与语言 |
| `app/ui.jsx` | 通用组件（按钮/表单/弹窗/Toast/图标） |
| `app/pages-*.jsx` | 各页面：仪表盘 / 知识库 / 对话调试 / 配置中心 / 会话监控 |
| `app/theme.css` | 全部样式（三套主题：玄墨 / 青瓷 / 黛蓝） |
| `vendor/` | 本地化的 react / react-dom / babel（私有化部署不依赖 CDN） |
