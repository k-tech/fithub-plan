# FitHub Git 服务与可复用实现调研

访问日期：2026-09-24。

> 当前产品决策（2026-10-05）：FitHub不实现分支保护；仓库write权限允许直接推送默认分支或合并PR，review与CI不设为门槛。本文“FitHub建议”段落记录的是调研时的候选方案，不代表当前产品范围；以[产品规格](../01-product-and-permissions.md)为准。本文限定于 Git 仓库传输、push 接入、PR 比较/合并及相关备份边界；以 Git 官方手册和 Gitea/Forgejo 官方文档、源码为证据。Gitea 源码链接指向 `main`，源码会变动；真正实施前要按选定 commit 再核对。结论中的事实和 FitHub 设计建议分开标注。不是法律意见。

## 给方案设计的结论

针对当前已确认的 10–100 人、小团队自托管、少量服务器：FitHub 首期可采用**模块化单体 + 本地文件系统 bare Git 仓库 + 系统 Git 可执行程序**。同一服务负责 HTTP/SSH 鉴权映射、仓库元数据和受控 hook；独立 runner 运行 CI；备份覆盖数据库、bare repositories、LFS 对象、配置和密钥。先不引入分布式 Git 存储服务或 Kubernetes，也不以改造 Gitea 为产品底座。

**推论 / 建议：** Git 协议与仓库对象格式交给 Git 官方实现，FitHub 重点自研账户/组织授权、refs 写入策略、PR 状态及数据库一致性。首期先保证 HTTPS clone/fetch/push 与 SSH clone/fetch/push 都进入同一套授权和 ref 策略；Git LFS 作为可选的后续交付范围单独验收。

## 核心事实与实现约束

### 1. Git Smart HTTP、SSH 与 bare repository

- **事实：** `git-http-backend` 是 Git 自带的 HTTP(S) 服务端 CGI，支持 Smart HTTP clone/fetch 和 push；默认允许 `upload-pack`（读取），而 `receive-pack`（写入）默认仅对已认证用户开放。Smart HTTP 要把 `info/refs` 的服务发现请求和后续 `git-upload-pack` / `git-receive-pack` 请求纳入相同的认证边界。Git 官方文档还指出 URL 到磁盘路径由 `GIT_PROJECT_ROOT + PATH_INFO` 映射，HTTP 服务配置需正确传递协议协商环境变量。[Git `git-http-backend`](https://git-scm.com/docs/git-http-backend)
- **事实：** Git pack 协议在 SSH、git、HTTP(S) 等传输上共享 packfile 传输语义；fetch 侧服务端程序为 `upload-pack`，push 侧为 `receive-pack`。HTTP 的无状态 POST 交互可由 `upload-pack` / `receive-pack` 的 `--stateless-rpc` 处理。[Git pack protocol](https://git-scm.com/docs/gitprotocol-pack), [Git `git-upload-pack`](https://git-scm.com/docs/git-upload-pack), [Git HTTP protocol](https://git-scm.com/docs/gitprotocol-http)
- **事实：** Git over SSH 的核心是远端运行相应的 Git 服务命令；Gitea 的 SSH 包装器 `cmd/serv.go` 验证允许的命令并以进程参数启动 Git 子命令，附带仓库和操作者上下文。Gitea 说明它不提供一般 shell 访问。[Gitea `cmd/serv.go`](https://github.com/go-gitea/gitea/blob/main/cmd/serv.go)
- **事实：** bare repository 的 `$GIT_DIR` 本身就是仓库管理目录，没有工作树；Git 文档用 `git clone --bare` 明确描述其结构和无 checkout 属性。[Git `git-clone`](https://git-scm.com/docs/git-clone)
- **FitHub 建议（推论）：** 新仓库在受控存储根目录内初始化 bare repo；DB 用稳定 repository ID 关联其物理位置，不直接用用户输入的 owner/repo 字符串拼接磁盘路径。协议 URL 的组织名和仓库名先解析成 DB 实体，再由服务端得出 canonical storage path。

### 2. push hooks、权限与安全边界

- **事实：** `pre-receive` 每次 receive 操作运行一次，通过 stdin 收到所有候选 ref 的 `old-oid new-oid refname` 行；非零退出时所有 ref 更新都会被拒绝。`update` 每个 ref 单独运行，可拒绝单个 ref。**这不等于 ref 更新事务原子：**普通非 `--atomic` push 即使通过 pre-receive，也可能在后续逐 ref 更新时因锁/并发等错误出现部分成功。`post-receive` 只收到实际成功更新的 refs，且退出码不能撤销更新；适合按成功 refs 做异步通知/队列触发，不应用作准入门禁。Git 将 incoming objects 放在 quarantine 区，pre-receive 成功后才迁入正式对象库，并禁止 pre-receive 把 ref 更新到 quarantine 对象。[Git `git-receive-pack`](https://git-scm.com/docs/git-receive-pack), [Git hooks](https://git-scm.com/docs/githooks)
- **事实：** `receive.denyNonFastForwards` 能拒绝非快进更新。客户端可通过 `git push --atomic` 请求将同一次 push 的 ref 更新整体成功或整体失败；服务端必须支持/声明 atomic 能力。普通不带 `--atomic` 的 push 不具备该事务保证；atomic 只覆盖 Git refs，不覆盖 FitHub 数据库和 webhook 队列。[Git `git-receive-pack`](https://git-scm.com/docs/git-receive-pack), [Git `git-push`](https://git-scm.com/docs/git-push)
- **事实：** Gitea 的 hook 接入可作目录导航：`routers/private/hook_pre_receive.go` 解析每个 ref、区分 branch/tag 等，然后根据 repository permission、受保护分支/标签规则做授权；`routers/private/hook_post_receive.go` 处理成功 push 后的同步；`cmd/serv.go` 是 SSH 命令入口。它们展现协议 hook 到应用权限、模型、事件处理的连接位置。[Gitea `hook_pre_receive.go`](https://github.com/go-gitea/gitea/blob/main/routers/private/hook_pre_receive.go), [Gitea `hook_post_receive.go`](https://github.com/go-gitea/gitea/blob/main/routers/private/hook_post_receive.go), [Gitea `cmd/serv.go`](https://github.com/go-gitea/gitea/blob/main/cmd/serv.go)
- **事实：** Gitea 最近公开的安全公告说明 multi-ref push 的 pre-receive 权限校验是容易出错的安全边界；另一公告说明可被本机其他账号写入的 Git hook 文件/目录可能以 Gitea 服务账号执行，形成主机代码执行路径。[Gitea pre-receive 权限公告](https://github.com/go-gitea/gitea/security/advisories/GHSA-649p-mmhf-85c7), [Gitea hook 权限公告](https://github.com/go-gitea/gitea/security/advisories/GHSA-pq8x-xpgp-rvmh)
- **FitHub 建议（推论）：** 权限判断按一次 push 中的每一个 ref 独立完整计算；先解析/校验全部候选更新，任何一个不允许时由 pre-receive 拒绝整次 push。若产品要承诺在后续 ref 锁/并发更新失败时也绝无部分成功，客户端必须使用 `git push --atomic` 且服务端支持该能力；不带 `--atomic` 时不能作此承诺，post-receive 及 FitHub 事件队列必须以实际成功更新的 refs 为准。验收与产品文案应区分“策略拒绝整次 push”和“ref 事务原子”。覆盖 ref 创建、删除、同名不同大小写、标签更新、强推和 protected branch 例外。HTTPS 与 SSH 共享一个授权服务，不让两条入口各自实现规则。权限更新和 hook 执行之间要有明确的策略快照/版本语义，避免管理员刚撤权而旧 hook 仍按缓存放行。
- **FitHub 建议（推论）：** 传给 Git 的所有值都作为独立 argv 项和受控环境变量，不通过 shell 拼接；只接受 `upload-pack`、`receive-pack` 等白名单服务名。路径从服务端 ID 映射，拒绝 `..`、绝对路径、分隔符变种和符号链接逃逸，并在打开/执行前校验 realpath 仍在仓库根目录内。仓库、配置、hook 目录和 hook 文件应由专用服务账号拥有且其他本机账号不能写；hook 只安装 FitHub 自己的入口，用户自定义 hook 默认关闭，避免将任意主机命令执行权限误作普通 Git 权限。
- **FitHub 建议（推论）：** 收到 pre-receive 决策后，数据库/索引更新放 post-receive 的幂等任务队列，以 ref 的 old/new OID 做去重和补偿；后台同步失败不能谎称 Git push 被回滚。定期重扫 refs/DB 元数据可修复崩溃导致的队列缺口。

### 3. PR merge-base、diff、冲突检测和合并原子性

- **事实：** Git `merge-base` 计算两个或多个提交的最佳共同祖先；存在多个最佳共同祖先的 criss-cross 历史也有专门行为，调用方不能默认只有一个。[Git `git-merge-base`](https://git-scm.com/docs/git-merge-base)
- **事实：** GitHub 风格的 PR 三点比较 `base...head` 是从 merge-base 到 head 的变更；Gitea 源码也把比较结果的 `CompareBase` 存成 PR 的 `MergeBase`，而下载 PR diff 的服务再使用 PR merge-base 与 head commit。[GitHub Git cheat sheet](https://training.github.com/downloads/github-git-cheat-sheet.pdf), [Gitea PR API `pull.go`](https://github.com/go-gitea/gitea/blob/main/routers/api/v1/repo/pull.go), [Gitea `services/pull/review.go`](https://github.com/go-gitea/gitea/blob/main/services/pull/review.go)
- **事实：** Gitea `services/pull/merge.go` 支持 merge commit、rebase、rebase-merge、squash、fast-forward-only 多种方式；代码用临时 Git 工作区/仓库准备 merge、识别 `MERGE_HEAD` 冲突，并在执行时带 expected head commit ID 和 PR 锁。源码也显式检查所选策略是否是该 repo 配置允许的策略。[Gitea `services/pull/merge.go`](https://github.com/go-gitea/gitea/blob/main/services/pull/merge.go)
- **事实：** `git update-ref` 可用 expected-old OID 条件更新 ref：仅当 ref 当前仍等于所期望的 old value 时才更新。这是避免并发 push 覆盖新 head 的基础原语。[Git `git-update-ref`](https://git-scm.com/docs/git-update-ref)
- **FitHub 建议（推论）：** PR 页面用 `merge-base(base, head)` 算三点 diff；把“最新 head/base OID”保存为计算输入，PR 合并前重新读取并确认 base/head 仍匹配，再执行 merge。冲突、无共同祖先、多 merge-base、已删除源分支、force-push 后旧 review/comment 行号失效都要有显式状态。合并结果先在临时工作区计算并跑策略检查，再通过带 expected-old 的 ref 更新原子推进目标分支；DB 以条件更新记录 merged commit。若 ref 成功而 DB 更新失败，需有可重复 reconcile 逻辑；数据库事务无法与 Git ref transaction 自动形成一个跨系统原子事务。
- **FitHub 建议（推论）：** 小团队一期只实现 merge commit + squash（或产品决定只选其一）可以控制范围；merge-base diff、冲突状态和并发保护仍需首期正确。Rebase、merge queue、复杂 required checks 可后续加。

### 4. LFS、submodule、浅克隆和 force-push

- **事实：** Git LFS 将大文件放在独立对象服务；客户端通过 LFS Batch API 查询对象动作，然后向对象端上传/下载内容。它不是 Git pack 的一部分，Git repo 备份成功不代表 LFS 数据已备份。[Git LFS Batch API](https://github.com/git-lfs/git-lfs/blob/main/docs/api/batch.md), [Gitea Git LFS 设置](https://docs.gitea.com/administration/git-lfs-setup/)
- **事实：** `git clone --depth` 创建 shallow clone，默认还意味着单分支；`--recurse-submodules` 才会初始化检出的子模块，submodule 可以再浅克隆。子模块在 superproject 中以 commit 固定，不只是一个路径链接。[Git `git-clone`](https://git-scm.com/docs/git-clone), [Git `git-submodule`](https://git-scm.com/docs/git-submodule), [Git submodules](https://git-scm.com/docs/gitsubmodules)
- **事实：** force-push 改写远端 ref 历史；Git 文档明确说明对 `--force` 应谨慎，因为它会丢弃其他人基于原远端历史的提交。接收端可用拒绝 non-fast-forward 的设置/策略对关键分支保护。[Git `git-push`](https://git-scm.com/docs/git-push), [Forgejo PR 与 force-push 指引](https://forgejo.org/docs/v15.0/user/collaboration/pull-requests-and-git-flow/)
- **FitHub 建议（推论）：** LFS 若首期不实现，页面、API 和 clone 说明要清晰拒绝/提示；若实现，必须独立校验上传用户是否有目标 repo 写权限，并将 LFS 内容纳入配额、校验哈希和备份恢复。submodule 的 URL 是用户控制内容，网页预览/服务端自动拉取不能盲目访问任意 URL（SSRF 风险）；客户端 clone 可由 Git 正常处理。支持 shallow clone 时要覆盖 PR diff 所需历史不完整和 deepen fetch；不在浅仓库上假设完整 ancestry。强推只在授权范围内接受，保护默认分支，并把 force update 的 old/new OID、操作者写入审计日志。

### 5. 备份与恢复

- **事实：** Gitea 官方备份文档明确要求备份时停机，理由是数据库、文件和 Git 仓库同时变化，迁移途中可能出现 DB 与 repository 不一致。官方 dump 包含配置、data（含 LFS 等）、repos 副本和 SQL dump；文档说当前没有一键 restore 命令，恢复需手工将数据和数据库还原至对应位置。[Gitea Backup and Restore](https://docs.gitea.com/administration/backup-and-restore/), [Gitea CLI dump 选项](https://docs.gitea.com/administration/command-line/)
- **FitHub 建议（推论）：** 首期最容易审计的做法是备份窗口停止写入（或进入维护模式），同一时间点做数据库 dump + repositories/LFS/config 快照，复制到异地目标，并以清单保存 Git ref 采样、对象/文件计数、版本和校验和。恢复演练应先恢复数据库，再恢复相同 snapshot 的 bare repos/LFS/config，验证仓库 refs 能被 `git fsck` 检查，且 PR、权限、SSH key、LFS 记录可用。只同步磁盘目录而不保证数据库时点一致会留下幽灵仓库或断链记录。

## Gitea 源码导航（只借鉴模块思想，不把它当 FitHub 产品基线）

| 范围 | Gitea 官方源码位置 | 可研究的内容 |
|---|---|---|
| Git HTTP 和仓库存取抽象 | [`modules/git/`](https://github.com/go-gitea/gitea/tree/main/modules/git)、[`modules/git/gitcmd/`](https://github.com/go-gitea/gitea/tree/main/modules/git/gitcmd) | Git 命令封装、repo/ref/commit/diff 操作和协议能力检测；学习 argv/上下文封装边界。 |
| SSH 服务入口 | [`cmd/serv.go`](https://github.com/go-gitea/gitea/blob/main/cmd/serv.go) | SSH key 对应用户、命令白名单、仓库授权后再执行 Git。 |
| push hook 路由 | [`routers/private/hook_pre_receive.go`](https://github.com/go-gitea/gitea/blob/main/routers/private/hook_pre_receive.go)、[`routers/private/hook_post_receive.go`](https://github.com/go-gitea/gitea/blob/main/routers/private/hook_post_receive.go) | pre-receive 准入、post-receive 事件/队列、副作用分层。 |
| repo 创建和存储 | [`services/repository/`](https://github.com/go-gitea/gitea/tree/main/services/repository)、[`modules/repository/`](https://github.com/go-gitea/gitea/tree/main/modules/repository) | 初始化 bare repo、默认分支、仓库服务级工作流。 |
| PR diff / comments | [`routers/api/v1/repo/pull.go`](https://github.com/go-gitea/gitea/blob/main/routers/api/v1/repo/pull.go)、[`services/pull/review.go`](https://github.com/go-gitea/gitea/blob/main/services/pull/review.go) | compare base、diff 下载、评论位置锚点和更新。 |
| PR merge | [`services/pull/merge.go`](https://github.com/go-gitea/gitea/blob/main/services/pull/merge.go) | merge 策略、临时仓库、冲突识别、expected head 和 PR 锁。 |
| 配置/存储路径 | [`modules/setting/repository.go`](https://github.com/go-gitea/gitea/blob/main/modules/setting/repository.go) | repository root 配置与路径组织。 |

Forgejo 源码跟 Gitea 有共同历史和结构，但 Forgejo 新版本是 GPLv3+，不作为首期复制实现来源。Forgejo 官方目录导航主要供了解兼容演化；对 FitHub 来说优先看 Git 官方原语、借鉴架构概念和自己实现。Forgejo 官方配置还特别提示 custom Git hooks 可执行任意系统操作，并列出 hook/SSH key 重同步管理工作，这支持把 hook 文件安全和修复能力作为系统设计项。[Forgejo 配置说明](https://forgejo.org/docs/v16.0/admin/config-cheat-sheet/)

## 借鉴、复制、依赖建议与许可边界

这是技术和合规风险识别，不替公司作法律结论；上线前应由公司法务/开源合规负责人审阅具体文件、构建产物和传播方式。

- **优先“借鉴”**：参考 Git/Forgejo/Gitea 的模块职责、边界、用户场景、测试案例和安全公告；阅读实现后自行写 FitHub 代码。保留自己的设计记录，避免把整段表达、注释、模板或测试原样照搬。
- **“复制”Gitea 自有实现**：Gitea 官方仓库标为 MIT，MIT 通常允许使用/改写/再发布，但复制时需保留适用的版权与许可声明，并核对被复制文件本身及仓库内 `LICENSE`、`NOTICE`、生成/嵌入资源的声明。Gitea 源文件常见 `SPDX-License-Identifier: MIT`；这不自动证明仓库每项依赖或资源也都是 MIT。[Gitea LICENSE](https://github.com/go-gitea/gitea/blob/main/LICENSE), [Gitea 仓库许可说明](https://github.com/go-gitea/gitea)
- **Forgejo 版本边界**：Forgejo FAQ 明确：v9.0 起 GPL v3+；v8.0 及以前 MIT。不要用“Forgejo 一直是 MIT”或“Forgejo 是 AGPL”作为许可判断；按具体 tag/commit 检查 LICENSE、文件头和依赖。GPLv3+ 复制到 FitHub 可能带来与分发/组合方式有关的义务，不能在架构调研里代公司下结论。[Forgejo FAQ: license](https://forgejo.org/faq/), [Forgejo 许可证变更说明](https://forgejo.org/2024-08-gpl/)
- **单独核对依赖**：Gitea 的 MIT 许可证不把其 `go.mod` 依赖、前端依赖、Git 二进制、LFS server 客户端、字体/图标/本地化内容自动变成 MIT。逐项保存名称、精确版本、许可证、来源和修改记录；FitHub 用系统 Git 独立进程时仍要做 Git 的 GPLv2 分发合规判断，特别是打包 Git 二进制时。[Forgejo 官方许可证说明也提到 Git 为 GPLv2](https://forgejo.org/2024-08-gpl/)
- **不建议“直接改造 Gitea”**：这与当前明确的自研 FitHub 目标不匹配，也会把 Gitea 上游结构、版本升级和第三方许可审查变成长期耦合成本。即便 MIT 允许大量再利用，技术上仍不等于最适合作为自研平台基线。
- **可依赖**：Git 官方命令/库做对象、pack、ref 和 merge 语义；FitHub 自研身份、授权、PR 和 UI；选用任何额外库前单独审核其许可证、维护状态和安全更新路径。若将来选择 go-git 等 Git 实现库，应按具体版本/模块再做许可证和协议兼容验证，而不是因 Gitea 使用它就直接带入。

## 8 个容易遗漏的验收项

1. **授权一致性：** 同一用户/仓库/branch 经 HTTPS 与 SSH push，获准和拒绝结果完全一致；anonymous 读取与认证写入边界覆盖 HTTP discovery 和 pack 请求。
2. **multi-ref push：** 验证 pre-receive 策略拒绝时所有 refs 均不更新；另行验证不带 `--atomic` 的 push 在逐 ref 更新失败时可能部分成功、post-receive 只报告实际成功 refs。若 FitHub 选择承诺 ref 事务全成或全败，需验证客户端 `--atomic` 与服务端 atomic 能力的协商及失败路径，并明确不覆盖 DB/队列副作用。
3. **并发与重试：** 两个并发 push、PR merge 与普通 push 竞争同一分支、陈旧 expected head、post-receive 重复投递/进程崩溃后重放；不得丢事件或把旧 head 覆盖回来。
4. **强推和删除：** 强推、删除默认分支、删除含 PR 的分支、重写 PR head；审计日志中保留 old/new OID 和操作者，保护策略能拦截越权。
5. **恶意路径与命令参数：** owner/repo 名带编码斜杠、`..`、Unicode 混淆、大小写别名、符号链接逃逸和恶意 SSH command 字段；无 shell 注入，无法访问其他用户/仓库目录。
6. **裸仓库与故障：** 空仓库/无 HEAD、超大 pack、损坏 pack、磁盘满、对象 quarantine 清理；客户端看到有用错误，服务不会留下部分数据库元数据或损坏 refs。
7. **Git 兼容角落：** 浅克隆/ deepen、partial clone 是否支持、submodule 相对 URL、没有 merge-base 或多 merge-base、CRLF/二进制 diff、大文件/LFS 超配额。把“不支持”也验收为清楚提示。
8. **备份恢复：** 同一时间点覆盖 DB + bare repos + LFS + 配置/密钥，异地副本可读；至少演练一次整机恢复，并确认恢复后 clone/fetch/push、PR diff/merge、SSH 登录都可用。

## 证据索引

以下为本文主要事实来源，访问日期均为 2026-09-24；每条给出可复核的精确 URL。文中 Git 与 Gitea/Forgejo 的设计建议属于推论，不应误读成上游项目要求。

| 编号 | 来源 | 支持的事实 |
|---|---|---|
| E1 | [Git `git-http-backend`](https://git-scm.com/docs/git-http-backend) | Smart HTTP 服务、upload-pack/receive-pack 默认启用/认证边界、路径映射和环境变量。 |
| E2 | [Git pack protocol](https://git-scm.com/docs/gitprotocol-pack), [Git `git-upload-pack`](https://git-scm.com/docs/git-upload-pack) | pack 传输角色、SSH/HTTP 传输、stateless RPC。 |
| E3 | [Git `git-clone`](https://git-scm.com/docs/git-clone) | bare repository、shallow clone、partial clone 和 submodule clone 选项。 |
| E4 | [Git `git-receive-pack`](https://git-scm.com/docs/git-receive-pack), [Git hooks](https://git-scm.com/docs/githooks) | pre-receive/update/post-receive 调用时序、stdin 格式、退出码、quarantine、push options。 |
| E5 | [Git `git-push`](https://git-scm.com/docs/git-push), [Git `git-update-ref`](https://git-scm.com/docs/git-update-ref) | force / atomic push 能力和 expected-old 条件 ref 更新。 |
| E6 | [Git `git-merge-base`](https://git-scm.com/docs/git-merge-base) | 最佳共同祖先语义。 |
| E7 | [Gitea `cmd/serv.go`](https://github.com/go-gitea/gitea/blob/main/cmd/serv.go), [pre-receive](https://github.com/go-gitea/gitea/blob/main/routers/private/hook_pre_receive.go), [post-receive](https://github.com/go-gitea/gitea/blob/main/routers/private/hook_post_receive.go) | SSH 命令白名单/执行入口、push hook 到授权及事件处理。 |
| E8 | [Gitea `modules/git`](https://github.com/go-gitea/gitea/tree/main/modules/git), [Git command wrapper](https://github.com/go-gitea/gitea/tree/main/modules/git/gitcmd), [repository services](https://github.com/go-gitea/gitea/tree/main/services/repository) | Git 访问封装和仓库服务模块导航。 |
| E9 | [Gitea PR API](https://github.com/go-gitea/gitea/blob/main/routers/api/v1/repo/pull.go), [PR reviews](https://github.com/go-gitea/gitea/blob/main/services/pull/review.go), [PR merge](https://github.com/go-gitea/gitea/blob/main/services/pull/merge.go) | CompareBase/MergeBase、diff review、PR merge 策略和冲突处理。 |
| E10 | [Gitea Backup and Restore](https://docs.gitea.com/administration/backup-and-restore/), [Gitea CLI](https://docs.gitea.com/administration/command-line/) | 一致性停机要求、备份内容、LFS 数据选项及恢复现状。 |
| E11 | [Git LFS Batch API](https://github.com/git-lfs/git-lfs/blob/main/docs/api/batch.md), [Gitea LFS setup](https://docs.gitea.com/administration/git-lfs-setup/) | LFS 对象通过独立 API / 存储端管理。 |
| E12 | [Git `git-submodule`](https://git-scm.com/docs/git-submodule), [Git submodules](https://git-scm.com/docs/gitsubmodules) | submodule clone/update、递归及 URL/提交绑定模型。 |
| E13 | [Gitea LICENSE](https://github.com/go-gitea/gitea/blob/main/LICENSE), [Gitea repo license statement](https://github.com/go-gitea/gitea), [Gitea file headers](https://github.com/go-gitea/gitea/blob/main/services/pull/merge.go) | Gitea 项目 MIT 许可及源文件 SPDX 声明。 |
| E14 | [Forgejo FAQ](https://forgejo.org/faq/), [Forgejo 2024 license change](https://forgejo.org/2024-08-gpl/) | Forgejo v9+ GPLv3+、v8 及更早 MIT、Git GPLv2；许可变更背景。 |
| E15 | [Gitea pre-receive advisory](https://github.com/go-gitea/gitea/security/advisories/GHSA-649p-mmhf-85c7), [Gitea hook permissions advisory](https://github.com/go-gitea/gitea/security/advisories/GHSA-pq8x-xpgp-rvmh), [Forgejo config](https://forgejo.org/docs/v16.0/admin/config-cheat-sheet/) | push 权限漏洞类别、hook 文件本地执行风险、hook/SSH key 重同步配置。 |

## 需主代理拍板的问题

1. 首期是否把 Git LFS 明确列为支持功能；建议若业务没有硬性需求就分期，避免首版同时承担对象配额、下载授权和大对象灾备。
2. 首期 PR 是否只支持 merge commit 和 squash；以及 protected branch 默认是否禁止 force-push 和删除。
3. SSH 实现具体采用 OpenSSH `authorized_keys` 强制命令还是内嵌 SSH 服务；首期可优先复用 OpenSSH，少写一个网络协议服务器，但需明确 SSH key 变更同步和撤权时效。
4. 备份是否接受定时维护窗口停写；小团队可先用停写快照获得清晰一致性，再依据 RPO/RTO 要求演进在线快照。
5. 公司是否允许复制 Gitea MIT 源码片段及其要求的 attribution 流程；由法务/开源合规确认具体文件与依赖清单后再决定复制范围。
