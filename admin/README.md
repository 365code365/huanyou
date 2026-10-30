# 管理后台（Next.js 16 + React 19 + Ant Design 6）

用 Next.js 的静态导出（`output: 'export'`）构建成纯 HTML/JS，复制到 `server/internal/api/adminui/dist`，再用 `go:embed` 打进 `vpn-api` 二进制。生产环境不需要 Node 进程，部署仍然只有一个文件。

访问 `https://<API 域名>/admin/`，密码为 `api.json` 的 `admin.password` 或环境变量 `VPN_ADMIN_PASSWORD`。

```bash
npm install
npm run dev        # http://localhost:3001/admin，/admin/api 代理到 $VPN_API（默认 http://127.0.0.1:18090）
npm run build      # 类型检查 + 静态导出 + 复制到 server；之后在 server/ 下重新 go build
npm run typecheck
```

## 目录

```
src/
  app/
    layout.tsx            根布局：AntdRegistry（SSR 样式）+ Providers
    providers.tsx         ConfigProvider（中文、主题）+ <App>（message / modal 上下文）
    login/page.tsx        登录页（在控制台布局之外）
    (console)/            路由组：所有需要登录的页面
      layout.tsx          登录守卫 + 侧边栏 + 顶栏
      page.tsx            概览
      users/              用户（page.tsx 列表，UserDrawer.tsx 详情/编辑）
      orders/ plans/ nodes/ monitor/ apps/ settings/
  lib/
    api/http.ts           fetch 封装：带 token、错误转中文 ApiError、401 自动退出
    api/types.ts          与 Go 返回 JSON 一一对应的类型（改后端字段时同步改这里）
    api/index.ts          每个接口一个函数；页面只从这里调接口
    nav.tsx               侧边栏菜单（加页面在这里加一行）
    theme.ts              颜色 / 圆角 / antd 主题，改视觉只改这里
    format.ts             字节、金额、日期、国旗等格式化
  hooks/
    useCatalog.ts         套餐/节点/应用这类「目录资源」：列表 + 抽屉表单 + 删除确认 + 行内开关
    usePaged.ts           服务端分页表格（用户、订单）：筛选 + 分页，直接喂给 <Table pagination>
    useAsync.ts           useAsync（加载数据）、useAction（执行操作 + 成功/失败提示）
  components/             StatCard、BarChart、NodeHealth、PlanSelect、PageCard
```

## 约定

- 所有页面是客户端组件（`'use client'`），数据在浏览器里请求 `/admin/api/*`；静态导出不跑服务端代码。
- 列表接口统一返回 `{ items, total }`；错误统一 `{ error, message }`，`message` 直接展示给管理员。
- 套餐 / 节点 / 推荐应用 / 系统设置的校验只写在后端 `config.Catalog.Normalize` 一处，前端表单校验只做格式提示。

## 新增一个页面 / 资源

1. 后端：在 `config.Catalog` 加字段和校验，在 `server/internal/api/admin_catalog.go` 写一个 `xxxResource()`，在 `registerAdmin` 里 `mountCatalogResource` 一行挂上，就有完整 CRUD。
2. 前端：`lib/api/types.ts` 加类型，`lib/api/index.ts` 加 `xxx: catalog<Xxx>('xxx')`，新建 `src/app/(console)/xxx/page.tsx`（照 `apps/page.tsx` 用 `useCatalog`），在 `lib/nav.tsx` 加一行菜单。

## Go 端如何提供页面（server/internal/api/admin_ui.go）

| 请求 | 返回 |
|---|---|
| `/admin`、`/admin/` | `index.html` |
| `/admin/users` | `users.html` |
| `/admin/_next/static/*` | 资源文件，长期缓存（文件名带哈希） |
| `/admin.txt` | 首页的 RSC 数据（客户端跳回首页时请求） |
| 其他未知路径 | `index.html` |
