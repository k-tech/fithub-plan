# Actions、API、CLI 与 MCP 实施规格

## 1. Actions 的产品承诺

第一版提供 **FitHub Workflow v1**：仓库里的配置文件触发构建/测试，由独立执行节点运行，结果显示在PR和Actions页面。它借用熟悉的YAML概念，但不是GitHub Actions完整兼容实现。

必须区分三个部件：

1. **控制面**：读workflow、校验配置、触发、排队、签发凭据、记录状态、检查权限、接收日志产物、阻止不满足条件的merge。
2. **Runner agent**：注册/领取任务、心跳、创建执行环境、运行步骤、回报结果、销毁环境。
3. **Job执行环境**：真正运行仓库代码，与主站和其他job隔离。

Gitea `act_runner`依赖Gitea的注册/任务协议与运行时服务；`act`则主要是本地执行workflow的工具。两者都不能替代FitHub控制面。[Gitea Actions design](https://docs.gitea.com/next/usage/actions/design/)、[act项目](https://github.com/nektos/act)。完整比较见 [Actions调研](research/02-actions-and-runners.md)。

**决定：首版自己实现小控制面和窄runner协议；shell执行使用成熟OS/容器能力。** 不以实现Gitea协议兼容层作为首版关键路径。P0用一个真实现有workflow验证迁移成本；如果业务要求大量第三方`uses`原样运行，必须重新评估范围和排期，不能悄悄扩大解释器。

这里的自研范围是FitHub任务与权限的接入，不是重写shell、Git checkout算法、YAML解析器或容器引擎。实施前先比较可复用执行器/库，若薄适配能满足已定义契约，就采用成熟实现；保留FitHub特有的调度、授权、审查检查与事件逻辑。不能因为现成runner不能直接换URL接入，就推导出其所有内部能力都要重写。

## 2. 首版语法范围

文件路径 `.fithub/workflows/*.yml`，显式 `version: 1`。允许多个job、静态`needs`依赖图和每job顺序steps；拒绝循环依赖。没有动态matrix、动态表达式或外部action下载。

```yaml
version: 1
name: verify
on:
  push:
    branches: [main]
  pull_request: {}
  workflow_dispatch: {}
jobs:
  test:
    runs-on: linux-x64
    timeout-minutes: 15
    steps:
      - name: Checkout exact revision
        builtin: checkout
      - name: Unit tests
        run: go test ./...
      - name: Build
        run: go build -o out/server ./cmd/server
      - name: Save binary
        builtin: upload-artifact
        with:
          name: server
          path: out/server
```

上例是待实现的 **FitHub自有schema示例**，不是目前可执行的GitHub workflow，也不代表FitHub平台本身已实现。镜像由管理员发布的`linux-x64`标签映射到固定digest，示例假设镜像包含Go工具链。YAML解析按1.2规则，防止将`on`误解析成布尔值。

| 能力 | 首版决定 |
|---|---|
| 触发 | push、PR opened/reopened/synchronize、手动dispatch；base更新也可重算PR检查 |
| step | 内建checkout/upload-artifact；`run`、name、env、working-directory、timeout |
| 条件 | step仅允许`success`（默认）/`failure`/`always`枚举；不是任意表达式 |
| job依赖 | 静态`needs: [job-id]`；前置失败时跳过后续，除非明确支持的清理步骤 |
| 变量 | 控制面注入固定`FITHUB_*`环境变量；首版不支持`${{ ... }}`通用表达式 |
| Secret | job级`secret-refs`名单，仅可信分支获准job可注入；PR job一律没有 |
| shell | 固定Linux bash，`-e -o pipefail`；不提供用户自选shell二进制 |
| 资源 | job超时、CPU/内存/磁盘、并发、最大日志/产物受平台上限约束 |
| 不支持 | 任意`uses`、Docker action、service containers、动态matrix、workflow复用、OIDC federation、`pull_request_target`、自定义宿主卷/socket |

未知字段、重复YAML键、过深嵌套、过多aliases、非法路径、无效needs、超大workflow在入队前报错。不能静默忽略配置后显示“运行成功”。

## 3. Workflow 来源、CI结果与权限

PR与push都可以按workflow触发CI；工作流文件权限与CI运行结果是两个不同概念。

- PR job使用默认分支上的workflow版本运行PR检查；结果关联本次触发的代码版本，仅作协作信息，不作为合并条件。
- 首版`.fithub/workflows/**`修改限定repo admin：push、API/MCP commit和merge入口都检查，普通write不能直接提交这类变更。仓库管理员可直接更新默认分支。
- 空仓库的首个workflow由repo admin在引导流程加入；没有workflow时显式显示“未配置CI”，不影响推送或合并。
- 普通分支push的workflow按默认分支定义执行、无Secret；默认分支push可运行已配置的workflow，并按job声明读取仓库部署Secret。
- CI运行结果由主站按真实job执行结果记录；workflow作者不能伪造job成功状态。无论成功、失败或未配置CI，都不限制PR合并。
- 运行状态由主站根据已分配job结果记录；普通write Token不能伪造运行成功。将来第三方CI需要独立注册的集成身份和限定状态名称。

每次CI运行记录触发事件、代码SHA和job结果。更新代码后按workflow触发规则创建新运行；历史运行只作为对应提交的记录，不决定是否允许合并。

PR存在合并冲突时仍可按触发规则运行检查；CI结果不会绕过冲突，也不会额外限制有写权限者的合并操作。首版无merge queue。

## 4. Runner 协议与状态机

控制面持久化 `run → job → attempt → step`。run属于repo、event、workflow blob、输入revision和信任级别。job attempt不复用ID。

状态：`queued → leased → running → succeeded / failed / cancelled / timed_out / infrastructure_failed`。lease有超时与fencing token，旧attempt在被撤销后不能覆写新attempt结果。

最小端点：register、poll/claim、heartbeat、append-log、complete、get-cancellation、request-artifact-upload。所有调用认证runner及其当前job lease，不仅依靠猜不到的job ID。注册token一次性短有效，注册后换runner身份；管理员可撤销runner。

Runner默认出站连接主站，无需主站主动连入runner内网。最小权限为领取被分配的job，不能任意列出所有repo或获取Secret。job token单repo/单run/单attempt，默认只读Git，可上传本job产物；不使用员工PAT替runner工作。

失联与取消：租约过期先标未知/基础设施失败并撤销job token；必须确认旧执行环境终止，才安全重新派发有副作用任务。日志/结果幂等回传不能阻止外部部署重复发生，因此deploy默认人工重试，不宣称exactly-once。

## 5. 执行隔离的落地选择

针对当前小团队且首版无外部fork贡献者：**独立runner VM，rootless Podman管理每job独立容器，主站与runner不共用数据盘**。初始最多2个并发job。runner agent在执行容器外；job内不包含注册凭据、宿主socket或数据库连接。

禁止privileged、host network、任意host mount；仅分配该job的临时工作目录/文件系统。根文件系统尽量只读、capabilities收敛、seccomp/进程数/CPU/内存/磁盘限额；镜像由管理员固定digest。网络默认只允许FitHub和必需的依赖源，阻断云metadata、管理网段、主站数据库与无关内网目标，IPv4/IPv6均测试。

**剩余风险明确：容器共用runner VM内核，不能视为对恶意代码的绝对隔离。** 首版只承载公司授权开发者代码；生产部署使用独立部署runner/明确目标允许列表，默认不开通生产Secret。若需要跑外部fork、未知用户代码，或对跨job隔离要求更高，上线门槛改成每job一次性VM/强隔离沙箱，并重新估算资源，不把更强隔离留成一句未验证承诺。

未来的VM executor通过runner内部执行接口替换容器executor；不改变workflow/权限模型。P0验证当前基础设施能否实现rootless容器网络与清理策略，不能验证时改为一次性VM。

## 6. 日志、产物、Secret 与部署

- 日志按step/attempt/sequence追加、断线续传，主站转义HTML/ANSI控制符。每job默认50 MiB，单行限64 KiB；超限后明确标截断并继续保留最终状态。
- 对已知Secret做脱敏，但脱敏不能防恶意代码编码/网络外传；PR根本不给Secret才是主要保护。
- 产物绑定repo/run/attempt，默认30天过期，单job初始1 GiB配额。下载前检查当前员工权限；预签URL若启用默认60秒，撤权后至URL到期仍可能有效，敏感部署可改授权代理下载。
- 产物路径拒绝`..`、绝对路径、symlink逃逸；压缩包下载不在主站自动解压执行。不同仓库/信任级别不共享可写缓存，首版缓存延后。
- Secret使用成熟AEAD库加密，密钥独立保管；只显示名称和更新时间，不提供回显明文接口；审计读用时机但不记录值。
- 演示版SSH部署由默认分支push触发，部署步骤显式写在工作流中；不依赖分支保护、PR审批或CI检查门槛。仓库部署密钥仅传给声明该密钥的部署任务。
- CD首版只提供一个staging目标的部署样例；production是开关，需完成具体目标的发布/回滚演练。数据库迁移回滚不能一概自动化。
- job取消先停进程组、再硬杀、最后销毁容器/工作区；清理失败隔离该runner，不直接复用残留目录。

## 7. REST API

统一 `/api/v1` + OpenAPI；Web、CLI、MCP调用同一领域服务，禁止“CLI绕过网页限制”。

| 资源 | 首版主要动作 |
|---|---|
| `/me`、`/me/tokens`、`/me/ssh-keys` | 查看本人；安全设置使用交互式登录权限；Token不自我提权 |
| `/teams`、`/teams/{id}/members` | 已登录有效员工（含无团队）可读有限团队申请目录；成员列表按团队访问规则读取；建团队/管理成员仅平台管理员会话 |
| `/teams/{id}/join-requests`、`/me/join-requests` | 员工会话提交/撤回本人申请、查看本人结果，不以已有team为前提；管理员会话查看待审列表、批准或拒绝；普通PAT不能审核 |
| `/repos`、`/repos/{id}`、`/collaborators` | 创建、查看、更新、协作者；敏感动作审计 |
| `/repos/{id}/tree`、`/blob`、`/commits`、`/compare` | 按固定ref/SHA读取与分页，大小限制 |
| `/repos/{id}/changes` | 在有写权限的分支上一次提交多文件变更，必须expected SHA |
| `/repos/{id}/issues`、`/comments`、`/labels`、`/milestones` | Issue与时间线 |
| `/repos/{id}/pulls`、`/reviews`、`/threads`、`/merge` | PR、审查、显式合并与条件更新 |
| `/repos/{id}/runs`、`/jobs`、`/logs`、`/artifacts` | CI状态、触发、取消、重试、产物 |
| `/repos/{id}/webhooks`、`/deliveries` | 配置、测试、查看投递；Secret只写 |

接口约定：分页游标；统一error code/request ID；GET无副作用；创建/merge/dispatch支持Idempotency-Key；更新使用version或If-Match；409表示并发冲突；超限429和Retry-After；只返回调用者可见资源。

API创建commit使用Git plumbing/隔离索引构建tree与commit，在`expected_old_sha`不匹配时拒绝；与push一样校验当前仓库写权限，工作流文件仍仅允许仓库管理员修改。首版限小型UTF-8文本修改、明确文件列表与删除列表、最大1 MiB请求，不提供任意shell执行或自动上传整个本地目录。

Webhook：push/issue/PR/review/workflow事件，HMAC签名、event/delivery ID、有限重试和手动重投。默认只允许HTTPS外部地址；DNS解析与重解析、重定向、IPv6、metadata地址均做SSRF限制；如需要内部接收端，由平台管理员配置明确域名/IP允许列表。只发送该仓库原本可授权的payload，日志不记录secret。

## 8. CLI

命令轮廓：

```text
fh auth login|status|logout
fh repo list|view|create|clone
fh issue list|view|create|edit|close
fh pr list|view|create|review|merge
fh run list|view|watch|cancel|rerun
fh api <endpoint> --method ...
fh mcp serve
```

首版`auth login`引导用户在浏览器登录FitHub并创建repo/user Token，CLI通过安全输入读取，保存到系统keychain；无图形环境支持受控环境变量或stdin。避免要求CLI实现Google登录或在终端粘贴Google ID token。`logout`清理本机凭据；可选显式撤销服务端Token，不能只清本机后声称已撤权。

浏览器一键CLI授权（FitHub授权码+PKCE/loopback）作为首版后的体验增强；若加device flow，需要FitHub自己提供设备授权端点，不依赖“Google登录天然包含它”。这样首版不必为了CLI/MCP再建设完整OAuth授权服务器。[RFC 8252](https://www.rfc-editor.org/rfc/rfc8252)、[RFC 8628](https://www.rfc-editor.org/rfc/rfc8628)。

`fh repo clone`委托标准git，后续本地commit/push继续用git；CLI管理Git credential helper而不把Token塞进remote URL。不默认将私人SSH Key上传；注册公钥要求明确操作。所有命令提供`--json`、稳定退出码、非交互参数、超时与清晰错误。

交付macOS arm64、Linux amd64首发；Windows amd64在试点需求明确后加入发布矩阵。生成校验和/签名与版本信息，文档包含升级和撤销凭据步骤。

## 9. MCP

首版`fh mcp serve`提供 **本地stdio MCP**，复用已登录CLI账号和Token。在客户端配置这个进程即可操作FitHub；stdout仅协议消息，日志走stderr。多实例凭据按FitHub域名隔离。

只开放一组明确的工具，不提供`execute_shell`或无约束HTTP代理：

| 工具 | 权限与约束 |
|---|---|
| `repo_list / repo_read_file / commit_list / diff_get` | read；固定ref；分页和内容限额 |
| `issue_list / issue_get` | read |
| `issue_create / issue_update / comment_create` | write |
| `repo_commit_changes` | write；显式文件补丁、目标分支、expected SHA；受分支规则约束 |
| `pr_create / pr_review` | write；参数校验、禁止自我批准 |
| `pr_merge` | write+当前规则+expected SHA；显式独立工具调用，不隐含在pr_create里 |
| `run_get / run_logs` | 有效员工read；日志/产物限制 |
| `run_dispatch / run_cancel` | write；部署批准仍只给repo admin |

这满足“AI登录自己的账号后直接提交”：小文本变更可经commit工具生成分支提交并开PR；本地完整工程仍由本机git处理。MCP不传输Git pack，也不代替clone/push协议。

工具声明只读/破坏性/幂等提示，但真正权限在服务端执行。Issue、README、日志内容都属于不可信数据，不能被工具适配层当成额外系统指令；写工具使用显式资源与操作参数，不因读到文档中的指令自动提权。

**远程HTTP MCP延后**：需要单独完成OAuth授权服务器/资源发现、audience、PKCE、撤销与目标客户端协议兼容验证，不把通用REST PAT无条件当作任意MCP audience凭据。规范版本、事实与待验证点见 [身份/MCP调研](research/03-identity-cli-mcp.md)。

## 10. 同一用户的端到端验收

Web创建Issue → 本机git创建分支并push → CLI创建含`Fixes #N`的PR → MCP读取diff并留下审查评论 → 新push使旧批准/check失效 → CI测试正确候选 → 第二位员工批准 → CLI或MCP显式merge → Issue自动关闭 → 各界面显示同一时间线。撤销该用户Token后，CLI/MCP/Git HTTPS全部拒绝相应新操作；SSH仍由独立key和当前账号权限控制。
