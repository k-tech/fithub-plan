# FitHub Actions 与 Runner 调研

调研日期：2026-09-24  

> 当前产品决策（2026-10-05）：CI结果只展示、不阻止推送或PR合并；默认分支push可触发显式配置的SSH部署步骤。本文中的required-check和人工审批方案是调研候选，不代表当前产品范围；以[产品规格](../01-product-and-permissions.md)为准。

范围：10–100 人的小团队自托管；主站为模块化单体，Runner 部署在独立主机或 VM；只调研架构与产品边界，不写业务代码。

## 结论

FitHub 一期建议自研小型 Actions 控制面，定义一个受限、可验证的 YAML 子集，调度到独立、临时、隔离的 Linux job VM/容器中执行 shell 步骤；先提供内建 checkout、日志、状态、短期 job token 和少量产物能力。调度服务作为主站模块，任务在数据库持久化，执行器独立部署。10–100 人规模不需要先上 Kubernetes 或实现完整 GitHub Actions。

Gitea `act_runner` 不会仅凭一个 FitHub URL 就成为 FitHub runner。它需要向 Gitea 注册、用 Gitea 的 gRPC over HTTP 协议取 job/回传结果，并使用 Gitea pipeline runtime API 及 Gitea 特定的 token、Git/REST/Artifact 服务。FitHub 若要直接接纳它，实质上要实现一套 Gitea Actions 后端兼容层并持续跟进协议变化。Forgejo Runner 同样是绑定 Forgejo 控制面的产品。`nektos/act` 是在本机或 CI 中模拟运行 workflow 的 CLI，不是面向自研 server 的 runner 服务或控制面协议。

因此首期执行端建议自研一个窄协议的 `fithub-runner` agent，或在隔离的单次 job VM 中运行 shell 执行器；可以评估复用 act 的解析/执行代码，但不要把它当可插拔的 FitHub runner，也不要接受未经筛选的 workflow/actions。仅在 PoC 证明兼容收益高且维护/许可条件清楚后，再考虑复用 act_runner 的工作流引擎并自行实现其控制面适配。

## 已验证事实

### Gitea Actions 与 `act_runner`

- Gitea Actions 从 Gitea 1.19 起作为内建 CI；官方称其“similar and mostly compatible” GitHub Actions，而非完整等价。Runner 独立于 Gitea 部署；官方明确提醒只把可信 runner 用于可信仓库，也只给可信 runner 执行可信代码。[Gitea Actions overview](https://docs.gitea.com/1.26/usage/actions/overview/)
- `act_runner` 基于 Gitea 对 `nektos/act` 的 hard fork；Runner 注册时要提供 Gitea instance URL 和 registration token，之后 daemon 从 Gitea 拉取 job 并回传运行结果。[Gitea Actions design](https://docs.gitea.com/next/usage/actions/design/) [Runner quick start](https://docs.gitea.com/usage/actions/quickstart/)
- Gitea 官方设计文档说明 Runner 与服务端使用 gRPC over HTTP，通过 `actions-proto-def` / `actions-proto-go` 定义协议；Runner 要先报到并报告 labels。文档还指出 job container 自己也要连接实例。故接入自研 FitHub 至少需实现 Runner 注册/身份、标签能力、取 job/ack、任务状态、取消信号、实时日志/执行结果及容器可访问的 Git/API/pipeline 服务；这是协议兼容工作，不是改配置项就能解决。[Gitea Actions design: protocol and network](https://docs.gitea.com/next/usage/actions/design/)
- Runner 在 Docker 模式通过 Docker API 创建 job 容器。官方 quickstart 推荐 Docker 隔离；支持直接跑在 host，但 host 无隔离。将宿主 `/var/run/docker.sock` 挂入 runner 容器等于授予其对宿主 Docker daemon 的高权限，Runner 文档明确指出这实际上给 runner root 权限。[Runner modes](https://docs.gitea.com/1.26/usage/actions/act-runner/) [Docker runner quick start](https://docs.gitea.com/usage/actions/quickstart/)
- Job 可获得短期 `GITEA_TOKEN`，权限可由 workflow 的 `permissions`、实例默认值和最大权限共同约束。Runner 环境还依赖 `ACTIONS_RUNTIME_URL`、`ACTIONS_RUNTIME_TOKEN`、`ACTIONS_CACHE_URL` 和 `ACTIONS_RESULTS_URL` 等服务地址/凭据。这些说明 Git/API/status/token/artifact/cache 都是控制面的一部分。[Gitea token permissions](https://docs.gitea.com/usage/actions/token-permissions/) [Gitea Actions variables](https://docs.gitea.com/usage/actions/actions-variables/)
- `uses: actions/checkout@v4` 这样的简写默认从 GitHub 下载 action；Gitea 另支持带完整 URL 的 action 来源。这能方便既有 workflow，但也意味着外部 actions 是会在 job 内执行的第三方代码，来源、ref 和完整性都要纳入信任策略。[Gitea Actions FAQ](https://docs.gitea.com/usage/actions/faq/)
- `go-gitea/act_runner` 仓库标为 MIT 许可，Gitea 主仓也是 MIT；依赖/打包仍须保留版权和许可通知，并核验固定版本的传递依赖。[act_runner repository](https://github.com/go-gitea/act_runner) [Gitea repository license](https://github.com/go-gitea/gitea)

### Forgejo Runner

- Forgejo Actions workflow 放在 `.forgejo/workflows`；`github` context 是为兼容 GitHub workflow 提供的别名，Runner 会提供 Forgejo/GitHub 环境变量，但具体值和语法仍由 Forgejo 定义。`pull_request` 来自 fork 时 secrets map 为空。[Forgejo basic concepts](https://forgejo.org/docs/v16.0/user/actions/basic-concepts/) [Forgejo Actions reference](https://forgejo.org/docs/latest/user/actions/reference/)
- Forgejo Runner 注册分仓库、组织/用户、全局级别；全局 Runner 可跑任意仓库 workflow。官方安全指南说明 host 模式的命令与 runner 用户同权限、无隔离；Docker socket、privileged、允许挂载卷都会扩大 job 对宿主及其他 job 的访问能力。官方提供 ephemeral 模式，使实例最多执行一个 job 后移除。[Forgejo deployment security](https://forgejo.org/docs/v15.0/admin/actions/security/)
- Forgejo 15（2026）更新了 Runner 注册流程；旧 registration token/`.runner` 注册方案正在弃用。2026 年 5 月官方报告称可插拔执行后端仍是讨论/原型阶段，说明 Runner 内核和控制面协议还在演进。[Forgejo May 2026 report](https://forgejo.org/2026-05-monthly-report/) [Forgejo 15 release](https://forgejo.org/2026-04-release-v15-0/)
- Forgejo v9 起主服务 GPL-3.0-or-later。当前 Forgejo Runner 仓库的 LICENSE 在本次网页调研中因 Codeberg robots 限制未能直接核实，不能据此断定 Runner 与主服务许可相同；若要嵌入、修改或分发，必须检查拟采用 tag 的 LICENSE、版权头及依赖并作法务核验。[Forgejo license FAQ](https://forgejo.org/faq/) [Runner repository](https://codeberg.org/forgejo/runner)

### `nektos/act` 与兼容边界

- `nektos/act` 是 MIT 许可的 CLI，定位是本机运行 GitHub Actions workflow。它读取 workflow、经 Docker API 拉取/构建环境镜像，再在本地运行容器；其仓库写明 Docker 是依赖，service containers 支持有限，默认 runner 镜像不包含 GitHub hosted runner 的全部工具，Windows/macOS runner 不支持。[nektos/act repository](https://github.com/nektos/act)
- `act` 的 CLI/本地任务执行模型并没有给 FitHub 提供 job 注册、远端调度、可信身份、状态/日志 API 或生命周期管理服务。可作为本地开发体验或 PoC 对照；若嵌入，其内部包稳定性、资源控制、日志桥接和隔离责任仍由 FitHub 承担。
- GitHub workflow 语法本身很大：事件、表达式、context、矩阵、复用 workflow、服务容器、环境审批、OIDC、缓存、产物、各种 action runtime 都有各自的边界。Gitea/Forgejo 的兼容是“多数常用写法可工作”的生态兼容，不构成 GitHub 服务端语义、API 或安全行为完全兼容承诺。[GitHub workflow syntax](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax) [Gitea overview](https://docs.gitea.com/1.26/usage/actions/overview/) [Forgejo reference](https://forgejo.org/docs/latest/user/actions/reference/)

## FitHub 需要自己拥有的控制面契约

建议 FitHub 定义自己的版本化 runner 协议，而非对外宣称 GitHub runner protocol 兼容。最小控制面对象和接口建议如下：

| 面 | 一期需要的契约 | 说明 |
|---|---|---|
| Workflow 触发 | `push`、内部 PR opened/synchronize/reopened、手动 dispatch；记录触发事件 JSON、源/目标 SHA、workflow 文件 blob/SHA | workflow 内容从哪个可信版本解析必须固定并可审计；初期默认 workflow 来自 PR merge revision，fork workflow 要审批策略或禁用自动执行 |
| Job 队列 | 持久化 run/job、FIFO queued 状态、label/资源需求、尝试次数、租约期限；runner 拉取 job 并 ack | 任务至少一次派发；job 状态变更需事务/版本号，runner 失联可过期回队列 |
| Runner 身份 | 管理员签发注册凭据；注册后轮换成 runner-scoped 凭据；Runner 可撤销、限仓库/组织/标签 | 注册凭据只用于注册，不作为 job token；runner 不能用身份越权读取其他仓库 |
| 执行与状态 | `queued → assigned → running → success/failure/cancelled/timed_out`；记录每个 step 的 started/completed/exit code | 状态写入要幂等；run attempt 单独编号，重试不覆盖前次日志/结果 |
| 日志 | 按 run/job/step 追加 chunk，单调序号、时间戳、脱敏后持久化；支持 SSE/WebSocket 展示和断线续传 | 控制 chunk、行长、总体大小及保留期；把 runner 日志视为不可信文本，转义 HTML/终端控制符 |
| Checkout | 给 job 提供 commit SHA、Git clone/fetch URL 和只读短期凭据；checkout 默认不持久化凭据 | 第一阶段可由 runner 内建 git checkout 完成，限定 FitHub repo/ref；第三方 `actions/checkout` 先不支持 |
| Job token | 每个 job 生成短期、仓库级、可撤销、权限收敛的 token；只开放 checkout/status 必需权限，默认无写权限 | PR 外部代码永远不拿 repo/org secrets；不把 runner 注册密钥放入容器；token 到期/取消时撤销 |
| 产物/缓存 | 一期只支持明确上传/下载 artifact；对象存储使用短期签名 URL。缓存可延后；若做，按仓库、ref/信任级别隔离 | 缓存可由低信任代码污染，不允许 privileged workflow 无条件恢复其内容 |
| 停止/重试 | cancel API 向 runner 发取消，先中断进程组再强杀并标记；retry 创建新 attempt | 任务脚本有外部副作用时自动重试不等于安全；部署/发布 steps 默认不自动重试，动作应提供幂等键 |
| PR 检查 | 将 run/job 结果回写到 PR 的 commit status/check；幂等键为 repo+SHA+workflow/job+attempt | PR 合并策略只消费明确配置的 required checks，避免旧 SHA 状态影响新 SHA |
| 部署 | 一期仅允许可信默认分支/受保护分支触发；可人工“批准并开始”一个部署 job，审批前不签发部署 secrets | 环境与待审批 job 是控制面状态；审批人、workflow SHA、deploy target 留审计记录 |

这份契约是 FitHub 建议设计，并非复刻 Gitea/Forgejo 或 GitHub API 的事实要求。Gitea 的 workflow API、artifact API、job token、runtime endpoints 可作为实现参考，但应映射到 FitHub 自己的抽象，而不是逐项照搬。[Gitea Actions variables](https://docs.gitea.com/usage/actions/actions-variables/) [Gitea Actions API](https://docs.gitea.com/api/next/)

## 一期 YAML 承诺建议

对用户承诺“FitHub Workflow v1”，不要承诺“GitHub Actions 全兼容”。首版可支持：

- `name`、`on.push`（默认分支/分支过滤）、内部 `on.pull_request`、受权限保护的 `workflow_dispatch`。
- `jobs`、`runs-on: linux-x64`（FitHub 自有固定标签）、`steps` 中 `run`、step `name`、step `env`、`working-directory`、`timeout-minutes`、`if` 的布尔子集。
- 明确限制为 POSIX `bash -e` shell；工作目录、环境变量注入、job 超时可复现且记录在 workflow 版本文档中。
- FitHub 内建 `checkout`、`upload-artifact` / `download-artifact` 可用普通 step 语法包装；引用 FitHub 自己发布且按不可变 digest 固定的官方 action。
- 明确拒绝或报告不支持：任意外部 `uses:`、Docker actions、`container`/`services`、复用 workflow、matrix、`needs` 表达式、动态 `fromJSON`、OIDC、`pull_request_target`、任意自定义 runner 镜像和 Docker socket/volume 配置。
- 在 workflow 校验/PR UI 中展示准确的支持版本与失败字段，不要静默忽略未知 YAML 键；若将来开放生态 action，应支持 commit SHA 固定、来源 allowlist、依赖清单和审计记录。

把表达式实现控制在简单插值和常见条件（如 `success()` / `failure()` / `cancelled()`）内，最好由解析器建立 AST 并做类型/大小/深度限制，拒绝调用宿主函数或以字符串拼接执行模板。当前不需要兼容完整 GitHub 表达式引擎。先做少数直白概念，让产品可用且安全；生态扩展按实测需求追加。

## 安全边界与运行要求

**Workflow 是用户提供的可执行代码。** 对有写权限的贡献者也不能默认提供全局 runner、宿主文件、云元数据端点、生产内网、其他仓库凭据或长期 token。Runner/Job 需要和主站数据库、对象存储管理凭据分开；job VM 只可访问 Git/API 所需入口。禁用 host 模式和特权容器作为公共配置选项。

**fork PR 是不可信输入。** GitHub 官方将 `pull_request` 与 `pull_request_target` 区分：fork 的普通 PR workflow 不拿 secrets，token 只读；`pull_request_target` 则来自 base 分支却拥有高权限，checkout 并执行 fork 代码可造成 secrets 泄漏。FitHub 一期应只执行受限 PR workflow，不给任何 secret 且 job token 只读；可以允许仓库管理员审批陌生 contributor 的执行。不要实现 `pull_request_target`、PR secrets 或“审批后允许任意 PR workflow 拿生产凭据”。[GitHub fork workflow security](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows) [Securely using pull_request_target](https://docs.github.com/en/actions/reference/security/securely-using-pull_request_target)

**Docker socket 是宿主根权限边界。** 将宿主 Docker socket 暴露给 Runner 或 workflow 等价于让 job 控制宿主 daemon；容器内部 UID、workflow 的 volume 限制都无法抵消这种能力。小规模一期优先每个 job 在专用一次性 VM 运行；若成本上必须在一台服务器跑容器，应在专用隔离 VM 内启 Docker daemon，job 容器不挂载 daemon socket，禁用 privileged、host network/IPC、任意宿主卷和默认内网访问。每个 job 结束销毁 VM/容器及临时工作目录。Gitea/Forgejo 官方文档对 host、Docker socket 和 privileged 的风险已有明确说明。[Gitea runner modes](https://docs.gitea.com/1.26/usage/actions/act-runner/) [Forgejo security guide](https://forgejo.org/docs/v15.0/admin/actions/security/)

**Token 与 secrets。** job token 由 FitHub API 签发、scope 到单个仓库/单个 run、最长不超过单个 job 期限，可即时吊销，最低权限默认 read-only。Fork PR 与未批准来源拿不到 secrets；secret 只在获批的 trusted branch job 中于运行期注入，避免出现在命令行、环境转储或日志。脱敏只是辅助，不能当保密保证。Checkout 凭据要 job 完成即失效，默认不持久化到 workspace。

**取消、重试和副作用。** 控制面必须能够请求取消并设置硬超时；Runner 中断子进程组、等待宽限期后 kill 整个进程树，再清理执行环境。取消请求和状态更新应幂等，runner 的租约/心跳超时可触发重新派发。重试用新 attempt ID 和独立日志/产物路径；只有明确声明幂等的构建/测试可安全重试，部署、发包、迁移等有外部副作用的 job 默认要求人工重试或下游幂等键。

**产物、缓存、并发和日志。** 产物限制数量、单文件/总大小、MIME 作为展示提示而非信任证明，并用签名 URL 短期上传下载。缓存是可执行数据的潜在载体：一期可不做；若做则按 repo+可信 ref 隔离，PR job 不能向 trusted branch cache 写入。小团队先设实例/仓库 capacity 与 FIFO 队列，提供一个可选并发组用于 staging/prod 串行，不必立即实现 GitHub 全部 concurrency 语义。日志采用有界追加、明确保留期、游标读取、输出脱敏和审计；PR 状态写回要绑定触发 SHA，避免旧结果覆盖。

**部署审批。** 可将审批做成 FitHub 自己的一期能力：workflow 声明 `environment: production` 或进入部署队列后，job 暂停；由具部署权限的成员审批；批准后才签发部署专用短期凭据，凭据范围限定环境。记录 approver、workflow SHA、目标和结果。GitHub 环境审批也会在规则通过前阻止 job 启动并阻止环境 secrets 可用，可作为设计参照。[GitHub deployments and environments](https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments)

## 执行器方案比较与建议

| 方案 | FitHub 集成成本 | 优点 | 主要问题 | 建议 |
|---|---:|---|---|---|
| 自研小协议 runner + 受限 shell workflow | 中 | 与 FitHub 安全模型一致；接口小、可测、无平台协议包袱；容易做 job token/status/log | 需要自己维护队列、生命周期、日志/产物桥接、runner agent | 一期推荐。控制面做成主站模块，Runner 是独立小进程；执行环境每 job 可销毁 |
| 直接接入 Gitea `act_runner` | 高（要兼容 Gitea 后端） | 能复用其 workflow 引擎与 actions runtime，MIT 仓库许可较友好 | 需要兼容 gRPC actions proto、token 注册、Gitea job schema、pipeline runtime、日志/取消/缓存/产物、Git/REST API 语义；协议升级负担；上游的 Docker socket/执行模式安全责任仍在 | PoC 有强烈既有 YAML 迁移需求再做；只在自己的隔离 VM 中跑。先确认 pinned release 和依赖许可 |
| Forgejo Runner | 高（要兼容 Forgejo 控制面） | 与 Gitea 类似的 Actions 经验与生态；支持 ephemeral runner 与多种执行方式 | 控制面绑定 Forgejo；注册协议持续演进；主服务 GPL-3.0-or-later；Runner 许可本次未核实；不要默认可无缝嵌入闭源 FitHub | 作为自托管替代部署或参考安全/产品设计；对 FitHub 商用嵌入须完成许可证和协议 PoC |
| `nektos/act` | 中到高 | MIT；可在本地开发验证常见 Actions workflow，行为熟悉 | CLI 而不是 FitHub server runner；Docker 依赖；本身不解决多租户身份、调度、token、日志持久化、回传；环境不等同 hosted VM | 可作为开发工具/兼容性探测对照。若复用库，应固定版本、薄封装、隔离 job、把不支持语法 fail closed |
| 采购/接入独立 CI 服务 | 低到中 | 能更快获得成熟调度、执行隔离、日志产物 | 额外服务/许可/数据路径，平台集成与自托管形态不一定适合 | 若近期不需要产品化 Actions，可一期外接 CI 并由 FitHub 只接 webhook/status；以后再内置 |

### 推荐的最小架构

1. FitHub 模块化单体接收 Git push/PR 事件，读取固定 commit 对应 workflow，校验安全子集，生成 run/job 并写入现有数据库。
2. 独立 Runner 主机/VM 通过 TLS 拉取带 lease 的任务；runner 只持有短期注册/通信身份，不拿 DB、对象存储 root key 或长寿命 Git token。
3. 每个 job 建独立一次性执行环境（建议单次 VM；资源紧时在专用 runner VM 的隔离容器中运行），Runner 负责 checkout、注入短期 token、执行 shell、分段上传日志和结果，然后销毁环境。
4. 主站负责状态机、权限、PR checks、日志查询、artifact 元数据和部署审批；blob 内容存本地/S3 兼容对象存储。所有回调都带不可猜 run/job ID 与 attempt ID，并校验 runner 身份。

不需要 v1 就实现通用 action marketplace、任意 `uses`、缓存协议兼容、OIDC、matrix、Kubernetes、弹性多云 executor 或完整 GitHub REST API。演进路线应由真实 workflow 迁移需求驱动。

## 实际 PoC 与验收矩阵（建议 1–2 周，限两台 VM）

PoC 目标：从一个 FitHub 仓库的 commit/PR 触发 workflow，由独立 Runner VM 在一次性 job VM 执行；FitHub 展示运行状态/实时日志，并能取消、重试、查看产物。只实现 shell 子集与内建 checkout/artifact，不接 Docker socket。用 `hello`、单测构建、长时间任务、恶意 PR 和失败/失联注入覆盖。

| 验收场景 | 方法 | 通过条件 |
|---|---|---|
| 受限语法解析 | 有效 YAML 及包含 `uses: docker://...`、`pull_request_target`、未知字段、深层表达式的文件 | 有效子集可校验；未支持或危险构造在入队前报具体行/字段错误，不被忽略 |
| push/PR 与状态回写 | push 默认分支、内部 PR opened/synchronize/reopen，触发两次不同 SHA | 每个 run 绑定正确 workflow blob 与 SHA；PR required check 显示 queued/running/final 结果；旧 SHA 不盖新 SHA |
| Checkout 与 token | 私有仓库 clone；job 尝试读取其他仓库和写 repo API | 本 repo 只读 checkout 可行；跨 repo、写操作被拒；job 结束后 token 过期/撤销；日志不包含 token |
| Fork PR 信任 | Fork PR 在 workflow 中输出所有环境/secret/context，并尝试调用写 API/访问 runner 文件 | Fork 不获得任何 repository/org/deploy secret；token 为只读 scope；不能读 runner 主机或其他 job 数据 |
| 日志流/重连 | 输出多段 log，浏览器断开重连，输出 ANSI 控制字节和长行 | 序号无重复丢失，续传可用；危险字符转义；行/日志大小有硬上限；脱敏测试无泄漏 |
| 取消/超时 | `sleep` 与 fork 子进程；从 UI/API 取消；另一个 job 超时 | 有界时间内终止整个进程树并销毁 VM；最终状态 cancelled/timed_out；重复 cancel 幂等 |
| Runner 失联/重复投递 | job 执行中断开 runner 网络，过期 lease 后重新派发；模拟重复完成回调 | run 状态可恢复；重复 ack/log/finalize 不造成错误双终态；retry 生成新 attempt 与独立目录 |
| Artifact | 上传合法小文件、超限文件、路径穿越文件；用过期 URL 下载 | 限额生效；拒绝路径穿越；artifact 绑定 run/repo/attempt；URL 过期后拒绝 |
| 并发/资源 | 配置 capacity=1/2，提交超过队列容量的 job | 排队次序与 UI 一致；无超额资源使用；用户能看到排队、被取消或拒绝原因 |
| 部署审批 | trusted main 上的 deploy job 进入 production；PR 试图进同环境 | 审批前 job 未执行、部署 secrets 未注入；只有具权限人员能批准；审批和部署审计可查询 |
| Docker/主机隔离 | 在 job 内检查 Docker socket、宿主文件、内网路由、相邻 job workspace | 默认不可用宿主 docker API、宿主凭据、其他 job 文件及管理网段；结束后 VM 被销毁 |
| 许可和可维护性 | 对 pin 版本 act / act_runner / Forgejo Runner 与全部传递依赖做 SBOM/许可证核查 | 发布清单、notice 和待评审义务明确；对 Forgejo runner 在拿到仓库 LICENSE 前标为未核实，不进入产品依赖 |

验收通过后再决定是否增加单个成熟 action 兼容点。如果 PoC 中为了跑一个真实 workflow 就必须扩展到动态表达式、第三方 Docker action 和 cache/artifact 私有 API，重新评估独立 CI 集成或 Gitea control-plane 兼容的总成本，避免边界在实现中悄然扩大。

## 风险清单

1. **把 YAML 当配置而非代码**：任意 `run`/`uses` 都是执行代码；漏掉一个未支持字段静默放行就可能绕开限制。措施：预先 schema 校验、未知字段拒绝、表达式资源限额、workflow 文件与提交 SHA 绑定。
2. **Runner 被反向控制或凭据泄漏**：不可信 workflow 可尝试访问 runner credential、同机任务、Docker daemon 和内网。措施：一次性隔离 VM、短期 scoped token、外网 allowlist/内网隔离、runner 主机不放业务秘密。
3. **复用执行器造成隐性兼容承诺**：对 act_runner/Forgejo Runner 只实现“能连上”还不够，Actions 对应的 Git/API/token/status/log/cancel/cache/artifact 都需要匹配；上游更新还可能改变协议。措施：FitHub v1 明确 workflow 子集和兼容测试集。
4. **外部 actions 供应链风险**：可变 tag 与第三方依赖可被替换；action 可以访问运行期 token/secrets。措施：首期不开放任意 `uses`；官方内建步骤不可变发布；未来按 allowlist+SHA 固定并保留审计。
5. **重试重复外部副作用**：构建的重新执行与部署的重新执行性质不同。措施：attempt 隔离、部署默认不自动重试、下游幂等键、审批与审计。
6. **许可误判**：Gitea runner 和 act 显示 MIT，但实际组合还包含依赖、镜像和 action 独立许可；Forgejo Runner 许可未能从官方源确认。措施：在锁定版本后进行 SPDX/SBOM 清点并由法务确认分发方式；不以主项目许可推断所有组件许可。
7. **产物/缓存成为横向投毒入口**：不可信 PR 写入内容可能被更高权限 job 恢复执行。措施：一开始不提供共享可写缓存；artifact 作为不可信输入消费；按 repo、信任级别、ref 隔离。

