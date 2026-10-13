# FitHub 依赖与复用登记

这个文件回答一个问题：我们直接用了哪些外部实现，用的哪个版本，许可是什么。
按 `decisions/README.md` 的 ADR-016，成熟底层优先直接依赖而不是自研；登记是这条路线的
一部分，不是额外手续——不登记就等于没人负责跟进上游安全修复。

版本与 `web/node_modules` 里的实际安装值一致，由 `web/scripts/check-deps.mjs` 校验：
新增依赖忘记登记、登记了却没用、或版本对不上，这个检查都会失败。

## 前端直接依赖

| 组件 | 版本 | 许可 | 用途 |
|---|---|---|---|
| react | 19.3.0 | MIT | UI 运行时 |
| react-dom | 19.3.0 | MIT | DOM 渲染 |
| react-router-dom | 7.18.4 | MIT | 前端路由 |
| @tanstack/react-query | 5.103.2 | MIT | 服务端状态与请求缓存 |
| lucide-react | 0.468.0 | ISC | 图标 |
| @radix-ui/react-dialog | 1.1.23 | MIT | 弹窗：焦点陷阱、ESC、滚动锁定 |
| @radix-ui/react-dropdown-menu | 2.1.24 | MIT | 下拉菜单：方向键、type-ahead、返回焦点 |
| @radix-ui/react-select | 2.3.7 | MIT | 可选搜索的下拉选择器 |
| @radix-ui/react-tooltip | 1.2.16 | MIT | 提示气泡 |
| @radix-ui/react-checkbox | 1.3.11 | MIT | 复选框的无障碍语义与可见焦点 |
| @radix-ui/react-radio-group | 1.4.7 | MIT | 单选组的无障碍语义与方向键导航 |
| @radix-ui/react-collapsible | 1.1.20 | MIT | 可折叠面板的 aria-expanded/aria-controls 关联 |
| markdown-it | 15.0.2 | MIT | 解析用户写的 Markdown（Issue 正文、评论、PR 说明） |
| dompurify | 3.4.16 | MPL-2.0 或 Apache-2.0 | 对渲染结果做白名单过滤；用户内容安全的第二道防线 |

## 构建期依赖

| 组件 | 版本 | 许可 | 用途 |
|---|---|---|---|
| vite | 7.3.6 | MIT | 开发服务器与打包 |
| typescript | 5.9.3 | Apache-2.0 | 类型检查 |
| tailwindcss | 4.3.3 | MIT | 样式工具链 |
| @tailwindcss/vite | 4.3.3 | MIT | Tailwind 的 Vite 插件 |
| @vitejs/plugin-react | 5.2.0 | MIT | React 的 Vite 插件 |
| @types/node | 26.6.2 | MIT | Node 类型定义 |
| @types/react | 19.3.0 | MIT | React 类型定义 |
| @types/react-dom | 19.3.0 | MIT | ReactDOM 类型定义 |
| @types/markdown-it | 14.2.0 | MIT | markdown-it 类型定义 |
| jsdom | 30.1.1 | MIT | 测试环境的 DOM，让过滤层能在测试里被真正执行 |
| @types/jsdom | 30.0.0 | MIT | jsdom 类型定义 |

## 后端与运行时

- Go 依赖见 `go.mod`。**尚未逐条登记**，在第一次部署评审前补齐。
- Git 协议、对象格式与传输复用**系统安装的原生 Git 程序**，不随本项目分发，因此不在
  此登记，但部署时必须记录所支持的最低 Git 版本。
- 容器镜像：`deploy/compose.dev.yml` 固定了 PostgreSQL 的 digest。生产镜像同样必须按
  digest 固定，禁止漂移 `latest`（见 `02-architecture-and-stack.md`）。

## 摘取或改写的代码

当前**没有**从 Gitea、Forgejo、GitHub 或其它项目复制代码，也没有改写过的上游片段。
如需摘取，按下列字段补一条记录，并核对具体文件的许可（Forgejo 主服务自 v9 起为
GPLv3+，不能按 MIT 处理）：

```yaml
- component: ""            # 组件名
  upstream_url: ""         # 官方来源
  revision: ""             # 确切 commit 或版本，不用分支名
  files: []                # 上游文件 → 本地目标文件
  use: ""                  # dependency | adapted | copied
  license: ""              # 核实后的 SPDX 标识
  copyright_notice: ""     # 需保留的声明
  modifications: ""        # 具体改动与原因
  reviewed_by: ""          # 负责人
  security_update_owner: "" # 安全更新负责人
```

## 许可注意事项

`dompurify` 不是 MIT，而是 **MPL-2.0 或 Apache-2.0 双许可**。以库的形式依赖、不做修改，
两种许可都可满足；若将来要改动其源码再分发，需按 MPL-2.0 的义务处理。

## 已知待办

- **安全跟进负责人尚未指派。** 在正式部署前必须指派到人，否则上游安全修复无人跟踪。
- Go 依赖与后端使用的库尚未逐条登记；在第一次部署评审前补齐。
