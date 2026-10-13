# 决策记录与开源复用方法

日期：2026-09-24。记录“现在按什么做”和“什么条件会改变决定”，避免开发期间反复推翻基础概念。

## ADR 摘要

| ID | 已采用的计划决策 | 理由 | 重新评估条件 |
|---|---|---|---|
| ADR-001 | 从零维护FitHub业务；不fork整个Gitea | 符合用户希望自己拥有产品实现的方向 | 公司目标改成最快部署现成forge |
| ADR-002 | 原生Git + bare repo + Smart HTTP/OpenSSH | 标准客户端兼容；复用已成熟协议与对象格式 | 超大仓库或多地域可用性需求得到验证 |
| ADR-003 | Go模块化单体 + Postgres + React/TS | 一套业务授权、部署简单、适合10–100人 | 团队既有技术能力明显偏向其他栈 |
| ADR-004 | 所有repo属于team，无个人repo；员工自行Google注册登录后选team申请，管理员审核通过才授予成员资格 | 用户明确选择团队模式，并明确不需要预先邀请；审核放在申请加入环节 | 用户后来要求个人试验空间或改变入团队流程 |
| ADR-005 | visibility独立于read/write/admin | 公开/内部可读不意味着可push | 首版保持不变，属于数据安全语义 |
| ADR-006 | Token范围与权限级别相交；普通Token不提供平台管理 | 避免user token成为自动化万能钥匙 | 需平台自动化时另设受限管理凭据 |
| ADR-007 | P1测试身份、生产前Google OIDC | 认证集成可后置，权限模型不能后置 | 若提前试点，提前Google任务 |
| ADR-008 | merge commit+squash、本地解冲突 | 覆盖日常流程，限制历史重写复杂度 | 用户真实需求要求rebase/网页冲突编辑 |
| ADR-009 | FitHub Workflow v1；窄控制面+runner协议 | 直连act_runner有兼容层成本，不冒充GitHub全兼容 | 大量现有workflow必须不改运行 |
| ADR-010 | 独立runner VM + rootless job容器；无外部fork运行 | 小团队可运营，隔离主站与构建 | 外部代码/更强隔离要求→每jobVM或经验证沙箱 |
| ADR-011 | 本地stdio MCP + CLI Token登录 | 满足员工AI工具接入，减少首版OAuth服务负担 | 需要远程托管MCP或免手工Token体验 |
| ADR-012 | DB outbox/jobs，不先引入消息集群 | 规模内事务+幂等足够，减少运维项 | 明确队列延迟/吞吐瓶颈 |
| ADR-013 | 先单写Git节点，协调备份和明确恢复窗口 | 优先数据一致性与可恢复性 | RPO/RTO或可用性要求高于现方案 |
| ADR-014 | 匿名只读public；员工账号才可协作 | 内部平台同时能公开代码，无需立即做社区治理 | 外部贡献者与fork成为明确目标 |
| ADR-015 | PR必须做，当前不做Fork；仅同仓库开发分支到目标分支的PR | 有仓库write权限的团队成员无需Fork即可协作，避免额外仓库副本与跨仓同步/授权复杂度 | 只读贡献者需提交代码，或明确需要独立长期维护衍生仓库 |
| ADR-016 | 成熟底层优先直接依赖，其次薄适配；不重写Git、SSH/TLS、密码学、解析器和容器运行时 | 用户明确要求避免重复造轮子；自研重点为FitHub产品业务 | 现有实现有经验证的能力/许可/维护障碍；新增通用实现前须记录比较结论 |
| ADR-017 | 前端交互组件用 Radix 基础组件，不自己写弹窗/菜单/选择器 | 弹窗、下拉、选择器、提示曾全部手写，缺少焦点陷阱、ESC、方向键导航和屏幕阅读器语义；Radix 只提供行为不给样式，视觉仍由本项目样式表控制，符合 ADR-016 的“先直接依赖”顺序 | Radix 不再维护或无障碍能力不满足；或改用基于它的整套样式库，但需重新统一视觉 |
| ADR-018 | 界面视觉对齐 GitHub/Gitea：无彩色主体 + 颜色仅作状态信号，单行顶部导航 | 用户明确要求学 GitHub 和 Gitea；颜色只表示 open/merged/closed/failed/pending 与链接，不做品牌主题色 | 用户改变视觉方向 |
| ADR-019 | 不做分支保护；当前仓库有 write 权限即可直接推送默认分支或合并 PR，review 与 CI 结果仅作协作信息 | 用户明确要求删掉分支保护，不要额外审批或必需检查门槛 | 用户明确提出重新引入时才评估 |

## 怎样“借鉴/抄优秀实现”

用户允许参考和复制合适的实现，但没有要求把所有代码自己重新发明。建议按下面三种方式管理：

| 方式 | 应用范围 | 操作方法 |
|---|---|---|
| 直接依赖成熟组件 | Git、OpenSSH、TLS、Postgres、加密与解析库 | 锁版本、跟安全更新、核对分发要求；不改核心协议 |
| 阅读后自行实现 | 权限模型、时间线、UI流程、PR状态、runner控制面 | 将我们需要的行为写成规格和测试，再按FitHub模型实现 |
| 最小片段摘取 | Gitea中独立的diff/引用/错误处理等适配代码 | 核对具体文件许可、保留版权、记录commit、写覆盖我们场景的测试 |

Gitea主项目MIT适合做优先研究对象；复制仍需保留适用版权与许可，单独核对依赖和资源。Forgejo主服务自v9起GPLv3+，v8及以前的许可不同；不能把当前Forgejo源码一概按MIT处理。runner的许可也不能由主服务推出。[Gitea LICENSE](https://github.com/go-gitea/gitea/blob/main/LICENSE)、[Forgejo官方许可说明](https://forgejo.org/2024-08-gpl/)、[Forgejo FAQ](https://forgejo.org/faq/)。

公司内部使用、对外提供服务、分发二进制/容器/源码可能涉及不同义务；选定实际文件与交付方式后由公司合规负责人确认。本计划不作“闭源一定安全”或“一定必须开源”的笼统判断。首版默认不直接复制许可未核实或不适合当前发布方式的文件。

### 开发时的参考地图

| FitHub任务 | 首选参考 | 参考重点 |
|---|---|---|
| Git HTTP | [Git官方backend](https://git-scm.com/docs/git-http-backend) | 请求/服务发现、鉴权环境、协议变量 |
| SSH入口 | [Gitea cmd/serv.go](https://github.com/go-gitea/gitea/blob/main/cmd/serv.go) | key到用户映射、命令白名单与Git执行 |
| push授权 | [Gitea pre-receive](https://github.com/go-gitea/gitea/blob/main/routers/private/hook_pre_receive.go) | multi-ref、保护分支、actor与权限边界 |
| push后事件 | [Gitea post-receive](https://github.com/go-gitea/gitea/blob/main/routers/private/hook_post_receive.go) | 准入和副作用分离、事件恢复 |
| Git/diff封装 | [Gitea modules/git](https://github.com/go-gitea/gitea/tree/main/modules/git) | 机器解析、取消/超时、特殊文件与错误 |
| PR merge | [Gitea services/pull/merge.go](https://github.com/go-gitea/gitea/blob/main/services/pull/merge.go) | 临时环境、策略、冲突、expected revision |
| Review | [Gitea services/pull/review.go](https://github.com/go-gitea/gitea/blob/main/services/pull/review.go)、[GitHub字段文档](https://docs.github.com/en/rest/pulls/comments) | 原始diff锚点、过期行、审批生命周期 |
| Issue关联 | [GitHub关闭关键字](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue) | 关联与关闭语义，不复制完整规则集 |
| CI控制面 | [Gitea Actions设计](https://docs.gitea.com/next/usage/actions/design/) | runner协议、执行/控制分离、隐含服务 |
| CI安全 | [Forgejo runner安全](https://forgejo.org/docs/v15.0/admin/actions/security/) | host/socket/容器/凭据边界 |

这些链接是导航，不是已选择复制的代码清单；`main/next/latest`会变化。实际摘取时先固定tag/commit并用永久链接记录，检查上游安全公告与修复版本，不能只按今天文档标题复制。

### 前端基础组件选择（ADR-017 补充说明）

`02-architecture-and-stack.md` 原本就把 UI 层定为 “Tailwind CSS + Radix 基础组件”，但在实现阶段一度把手写的弹窗、下拉、选择器、提示当作“页面代码”处理，没有登记为通用组件，结果缺了焦点陷阱、ESC 关闭、菜单方向键导航和屏幕阅读器角色。这些不是细节：键盘用户无法操作，是功能缺陷。现已改为直接依赖 Radix。

为什么不是整库 UI 框架：Radix 只提供行为与无障碍语义，不给视觉。我们的样式表仍然完全控制外观，因此刚刚统一的 GitHub/Gitea 视觉不会被组件的默认皮肤覆盖。

已接入并替换的手写实现：

| 能力 | 原实现 | 现实现 |
|---|---|---|
| 弹窗 | 手写遮罩 + `role=dialog`，无焦点管理 | Radix Dialog：焦点陷阱、ESC、滚动锁定、返回焦点 |
| 下拉菜单 | 手写，靠外层遮罩关闭 | Radix DropdownMenu：方向键、type-ahead、ESC、返回焦点 |
| 选择器（13 处） | 原生 `<select>` 包一层 | 共用 Picker：可搜索、自定义渲染 |
| 复选框（5 处） | 真 input 被 `opacity:0` + `pointer-events:none` 藏起，旁边画一个假方块 | Radix Checkbox：真控件留在无障碍树，键盘焦点可见 |
| 单选框（3 处） | 同上，用假圆圈冒充选中态 | Radix RadioGroup：方向键导航、`radiogroup` 角色 |
| 过滤切换组（4 处） | 每页各写一遍按钮 + `active` class | 共用 Segmented：`aria-pressed` 状态 |
| 提示 | `title=` 属性 | Radix Tooltip：可样式化、键盘可达 |
| 分页（7 处） | 每页各写一遍上一页/下一页 | 一个 Pagination 组件 |
| 复制按钮 | 每个调用点自己管理“已复制”状态 | 一个 CopyProvider |
| 确认撤销 | 行内展开两个按钮 | 共用 ConfirmDialog |

仍需保留的手写部分：业务页面、业务语义与主题样式——这是 FitHub 自己的产品内容，符合 ADR-016 的边界。

## 第三方登记模板

实施仓库维护`THIRD_PARTY.yml`和`NOTICE`，每次摘取或增加依赖登记：

```yaml
- component: "待填组件名"
  upstream_url: "待填官方来源"
  revision: "待填确切commit或版本"
  files: ["待填源文件及本地目标文件"]
  use: "dependency | adapted | copied"
  license: "待核实SPDX标识"
  copyright_notice: "待填需保留的声明"
  modifications: "具体修改和选择原因"
  reviewed_by: "负责人"
  security_update_owner: "负责人"
```

不得留下模板占位值后标记为已审核。摘取代码和对应测试也可能各有许可；AI重新排版、翻译注释或改变量名不消除来源义务。我们对上游修复的跟踪责任不会因为“不是fork”而消失。

## 上线前需要确定的少量条件

这些不会阻塞本轮计划；当前默认值已经足够开始P0。

| 条件 | 当前默认/工作假设 | 最迟确认 |
|---|---|---|
| 人员投入 | 2名有经验工程师+共享运维/测试 | 排期承诺前 |
| 公司Google域/员工范围 | 允许的公司Workspace域内自行注册登录，无需邀请；选team申请后由管理员审核；不开放外部账号 | P1模型核对、P5集成前 |
| 正式域名/TLS/公网范围 | 一个Git/Web域名，public经HTTPS可达；SSH可按网络政策开放 | 部署PoC；要互联网public就不能只放VPN内 |
| 服务器/runner与备份资源 | 主站1台、runner独立VM、备份独立故障域 | P0 |
| 现有repo的大小/LFS/submodule | 抽样真实repo；LFS存在则补任务或延后迁移 | P0 |
| 现有workflow类型 | 至少选一个真实测试/构建workflow转换 | P0；决定兼容投入 |
| Secret与部署目的地 | 首版staging单目标；production默认关闭 | P4 |
| PR审查与CI | 可选协作信息；不作为推送或合并门槛 | 当前决定 |
| RPO/RTO和保留期 | 24h/4h；日志14d、产物30d、审计180d | P6恢复演练前 |
| 外部发布/开源意图 | 先内部使用，但逐文件保留许可记录 | 第一次对外发布/分发前 |

## 调研范围与可信度说明

调研按用户要求交给3个 **Luna子代理**，分别负责Git/复用、Actions、身份/CLI/MCP。主代理进行方案收敛、跨文档一致性核对和执行任务拆解，没有把调研当作已经实现/验证的功能。

资料核对日为2026-09-24；研究文件区分官方事实、设计建议和待PoC问题。MCP版本与Google Directory字段另做过定点复核。Forgejo runner具体许可未拿到充分原文证据，明确保留待核实，首版不引入该代码。

有些研究文件讨论fork PR、OAuth或其他备选方案，只是解释边界；**首版范围以主计划和01–06规格为准**。实际生产前需要用固定版本重新确认，特别是Git安全修复、MCP SDK与runner依赖。
