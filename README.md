# FitHub 自研平台：总体计划

**规划日期：2026-09-24｜适用规模：10–100人｜部署：公司自托管、少量服务器**

这份计划的结论是：**可以从头实现FitHub业务平台，但不要重写Git本身；先完整交付日常研发闭环，再逐步扩大Actions兼容范围。** Gitea（你提到的“Git EA”大概率指它）是主要实现参考，不作为要修改的整个平台底座。

本目录同时保存经过官方资料调研的产品规格、技术决策、执行任务、验收方案，以及已经启动的[第一批可运行代码](implementation/README.md)。当前代码仍是仅限本地的开发版本，尚未部署为公司服务。调研按要求由3个Luna子代理完成。

### 当前开发进度

已实现并通过本地自动化测试：Google Workspace OIDC登录与可撤销会话（使用本地签名的测试身份源验证，尚未连接真实租户）、员工自行申请加入Team及管理员审核、团队仓库与三种可见性、仓库和用户Token、原生Git Smart HTTP推拉（有写权限即可直接写入默认分支）、文件与提交浏览、Issue及评论、Issue标签/指派/里程碑及其筛选、PR行内评论与线程（含过期标记）、同仓库PR的冲突检测、diff、可选review反馈、有写权限即可合并PR与Issue自动关闭，以及协作者权限管理。推送与PR合并只校验当前仓库写权限，不设分支保护、审批或CI合并门槛；CLI与本地stdio MCP已能调用仓库/Issue/PR等API，并可在已有开发分支上按预期SHA提交单个小型文本文件；OpenSSH密钥和受限Git入口已实现，尚待实际sshd端到端验收。Actions/CI已实现：受限版本化工作流schema、从默认分支读取可信定义、执行机在隔离容器内运行单版本并回传日志、检查结果和日志可在网页、CLI/MCP查看，但不会阻止推送或PR合并。站内通知已实现：事件随写操作记录，读取时按当前仓库权限重新判定，因此撤权后旧通知立即隐藏但不删除、恢复权限后连同未读状态一起回来。**备份恢复、负载与安全审查按决定推迟**，详见[实现说明](implementation/README.md)的当前边界一节；当前版本不能用于公司生产代码，也不可当作可恢复状态。

## 1. 最终要做出来的东西

员工用Google自行注册/登录 → 进入未分配团队状态 → 选择管理员已创建的Team并申请加入 → 管理员审核通过，成为团队成员 → 创建团队仓库 → 用本地Git工具clone/commit/push → 建Issue与开发分支 → 有写权限即可直接推送默认分支，也可选择提PR并使用review/CI协作 → 合并并关联关闭Issue → 查看历史、日志和产物。

注册和登录不需要管理员邀请。管理员的审核发生在加入Team的申请环节；申请待审核期间不获得该Team的成员权限。尚未获批加入任何Team的员工可以浏览可申请的团队、提交申请并查看审核状态。

同时提供Web、REST API、CLI和MCP入口，共用一套权限。CLI/MCP登录员工账号后可以管理Issue/PR、查看CI；MCP还可在指定分支提交有限文本变更。完整仓库传输使用标准Git。

| 需求 | 计划决定 |
|---|---|
| 自己写一套 | 自研业务、数据模型、页面、API和工作流；复用Git、SSH、数据库等组件 |
| 团队与仓库 | 不设个人仓库；员工先自行登录，再选择team申请加入，管理员审核通过后成为成员；使用公司仓库前须至少有一个有效team |
| 三种可见性 | public：互联网匿名读；internal：有效公司成员读；private：显式受邀者读 |
| 用户/仓库权限 | 用户employee/admin；仓库read/write/admin，可给用户或team授权 |
| Token | repository/user两种范围 × read/write/admin；始终受当前用户真实权限限制 |
| Git | HTTPS+SSH、分支/tag、历史/diff、标准clone/fetch/push；commit先在本地完成 |
| Issue | 评论、标签、指派、里程碑、引用、关联PR和时间线 |
| PR | 可选协作流程；有仓库write权限的成员可创建PR、review和查看CI结果，也可直接推送或无需审批/检查合并；支持merge commit+squash |
| Fork | 当前不做；团队协作使用同仓库分支，不创建额外仓库副本 |
| Actions | 自有Workflow v1：测试/构建/日志/产物/检查，独立runner；明确支持子集 |
| 登录 | 权限模型现在设计；Google实现可后排，真实试点前必须完成 |
| CLI/MCP | `fh`命令行和本地stdio MCP首发；远程MCP OAuth后续增强 |

可见性只决定谁能读，不自动给写权限。user/admin Token权限面较广，但不是全平台超级管理员凭据。公开仓库也不会自动开放匿名写入或外部PR。

## 2. 技术栈拍板

**Go + PostgreSQL + React/TypeScript + 原生Git + OpenSSH + 独立CI runner。**

**成熟底层优先直接复用。** Git对象/传输/diff/冲突/合并算法、SSH/TLS、身份验证库、解析器和容器运行时不重写；FitHub自研团队、权限、Issue、PR审查与自动化编排等产品业务。各模块先考虑现成组件和薄适配，再决定是否需要写新实现。

- Go模块化单体负责团队、权限、Issue、PR、API与CI调度；Postgres同时承载事务、outbox和小规模任务队列。
- React/TypeScript/Vite负责界面；REST/OpenAPI供Web、CLI、MCP统一使用。
- Git仓库是本地SSD上的bare repository，Git官方程序提供Smart HTTP、pack和合并原语；SSH由受限OpenSSH入口负责。
- 附件/产物通过S3兼容API存储；备份放独立故障域。主站初期一台，runner另一个VM，避免CI任务接触主站磁盘和凭据。
- CI首版自研窄控制面与runner协议；专用runner VM中使用rootless临时容器。容器内核隔离仍有边界，外部不可信代码需要更强的一次性VM方案。
- 首版不引入Kubernetes、微服务、消息集群、复杂RBAC语言或分布式Git存储。

完整的选型理由、部署图、数据模型和备份方案见 [02 技术架构](02-architecture-and-stack.md)。

## 3. 需要提前接受的范围取舍

**明确取舍：PR保留为可选协作流程，Fork不纳入当前实施范围，也不做分支保护。** 有仓库write权限的成员可以直接推送默认分支，也可选择 `main → feature/xxx → push → PR → 审查与CI → 合并main`；review与CI结果只作协作信息，不构成合并门槛。只有未来出现“没有源仓库写权限的人也要贡献”或“需要独立长期维护衍生仓库”的明确需求，才重新评估Fork；它不是PR的前置条件。

其他首版暂不承诺的能力：个人空间、外部协作者、网页解冲突、rebase merge、完整GitHub Actions/Marketplace、Packages/Pages、全文代码搜索、远程MCP OAuth和高可用多地域部署。

其中LFS是条件项：P0检查真实仓库，已有LFS的repo必须先完成独立对象服务/迁移/备份验收，才能迁入。Git history导入也不代表Issue/PR/附件自动迁移。

Gitea runner需要它自己的后端协议和运行时服务，不能仅换URL接入FitHub。因此首版只承诺明确的YAML子集，基于真实现有workflow评估后续兼容。[Gitea Actions设计](https://docs.gitea.com/next/usage/actions/design/)

开源代码可以按许可合法借用：优先参考Git官方原语和Gitea模块；摘取具体代码时保留来源、commit与版权声明。Forgejo当前与历史版本许可不同，不一概按Gitea的MIT处理。[Gitea LICENSE](https://github.com/go-gitea/gitea/blob/main/LICENSE)、[Forgejo许可说明](https://forgejo.org/2024-08-gpl/)

## 4. 执行顺序与资源

**基线：2名有经验工程师 + 共享运维/安全/测试支持，16周实施，另留2–4周缓冲。** 这是假设下的估算，不是对AI开发速度的保证。单人实施建议按约6–9个月评估。

| 时间 | 交付 |
|---|---|
| 第1周 | Git接入、并发合并、runner隔离三个薄PoC；确定LFS/既有workflow需求 |
| 第2–3周 | 团队/身份/权限、仓库、Token、HTTPS/SSH push和clone |
| 第4–5周 | 仓库浏览、commit/diff、分支/tag、协作者和设置 |
| 第6–8周 | Issue、PR、review、冲突、合并与引用时间线 |
| 第9–11周 | Workflow v1、runner、PR checks、日志/产物、staging部署样例 |
| 第12–13周 | 正式Google登录、CLI、stdio MCP、Webhook |
| 第14–16周 | 安全/压力/故障测试、整机恢复、真实团队迁移试点 |

要提前试点就提前Google与备份任务，不能把本地开发身份用于真实公司数据。AI适合加速实现，但验收必须用真实Git客户端、真实故障注入与恢复演练。

具体任务编号、依赖、负责人类型、工程人日和完成标准见 [05 执行路线](05-execution-roadmap.md)。

## 5. 最容易低估的部分

1. Git协议接上后，HTTP/SSH、API提交和服务端merge仍须执行同一权限与分支策略。
2. PR合并必须处理base/head变化、强推、过期审查、旧CI、两个合并同时发生，以及Git成功但数据库失败。
3. 行内评论要锚定原始commit和左右行，不能只保存一个行号。
4. Actions是在执行代码：需要短期job凭据、PR无Secret、独立环境、取消/失联清理和可信check来源。
5. 撤权要影响已有Token/SSH/session；public/internal/private不能通过搜索、引用、下载和通知漏信息。
6. 备份必须把Git、数据库、LFS、配置和加密密钥一起恢复验证；初始目标RPO≤24h、RTO≤4h。

这些均有明确设计与测试项，详见 [03 Git与PR](03-git-issues-and-pull-requests.md)、[04 Actions与工具](04-actions-api-cli-mcp.md)、[06 验收与运维](06-acceptance-security-and-operations.md)。

## 6. 文档导航

| 文档 | 使用方式 |
|---|---|
| [01 产品与权限规格](01-product-and-permissions.md) | 产品、设计和后端共同确认角色、可见性、Token、页面与交互 |
| [02 技术架构与选型](02-architecture-and-stack.md) | 搭建工程、模块、存储、部署与运维基础 |
| [03 Git、Issue、PR规格](03-git-issues-and-pull-requests.md) | 实现协议入口、事件、diff、审查与并发安全合并 |
| [04 Actions、API、CLI、MCP](04-actions-api-cli-mcp.md) | 实现工作流和自动化工具入口，控制兼容边界 |
| [05 执行路线与Backlog](05-execution-roadmap.md) | 可直接按FHB任务建Issue、排期、验收 |
| [06 验收、安全与运维](06-acceptance-security-and-operations.md) | 测试矩阵、容量目标、风险与上线门槛 |
| [决策记录与复用方法](decisions/README.md) | ADR、具体源码导航、许可记录和待确认部署条件 |
| [研究：Git与开源复用](research/01-git-and-reuse.md) | 官方Git/Gitea/Forgejo证据与可参考代码位置 |
| [研究：Actions与runner](research/02-actions-and-runners.md) | 执行器比较、协议成本、兼容和隔离边界 |
| [研究：身份、CLI、MCP](research/03-identity-cli-mcp.md) | Google、MCP、CLI登录和GitHub协作语义的官方来源 |
| [第一批实现与本地运行](implementation/README.md) | 当前可运行功能、开发启动方法、验证与尚未完成的边界 |

建议先读本页和01/05，再由工程负责人按02–04执行。研究文档保留备选方案；**产品与实施范围以主文档01–06为准**。
