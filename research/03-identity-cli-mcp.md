# FitHub 身份、CLI 与 MCP 研究

**资料核对日期：2026-09-24。** 下列协议结论来自 Google、MCP、GitHub、Git 与 IETF/RFC Editor 官方原始资料。已打开 MCP 官方发布页，标题为 “The 2026-07-28 Specification”，页面日期为 2026-07-28；原文短摘：“Today, we’re officially pushing the release button on the next version of the MCP specification, `2026-07-28`”。MCP 官方授权规范页将 Dynamic Client Registration 标为弃用；原文短摘：“Dynamic Client Registration is deprecated and retained for backwards compatibility”。授权页本身没有显示发布日期，因此其状态以已打开的当前授权页和上述官方发布日期页交叉记录，不从 URL 或当前日期推断。RFC 8252（2017-10）是 Native App OAuth 最佳实践，RFC 8628（2019-08）定义设备授权。来源：[MCP 发布页](https://blog.modelcontextprotocol.io/posts/2026-07-28/)；[MCP 授权规范](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)。下文区分“官方事实”“研究建议”和“FitHub 已定决定”。

## 建议先固定的身份与权限模型

把**人员身份、平台角色、团队、仓库权限、凭证**建成不同概念：

| 维度 | FitHub 语义建议 |
| --- | --- |
| 人员 | Google `sub` 映射到不可变的 FitHub 用户 ID；邮箱和姓名只是可变资料。用户有 `active/disabled` 状态。首期只允许公司员工登录。 |
| 平台角色 | `member` / `platform_admin`。平台管理员是少数运维控制面角色，应单独审计；普通成员不能由其 repo token 升级为平台管理员。 |
| 团队 | 按用户修订的产品决定，员工无需邀请即可先Google注册登录，未分配团队时可选team提交申请；平台管理员审核通过后才创建成员关系。至少一个team获批后才能使用公司仓库；团队申请、成员关系与仓库授权分别存储。 |
| 仓库访问 | `read` / `write` / `admin` 是仓库范围权限；仓库 `admin` 只管理该仓库。团队授予仓库角色，用户也可以按邀请获得角色。 |
| 凭证 | Web session、FitHub API/PAT、Git HTTPS 凭证、SSH key 分开签发、存储和撤销。首版 MCP stdio 从本机安全存储/受控环境读取 PAT。凭证只证明主体和上限；每次请求都再检查主体当前状态、团队/邀请授权和目标仓库权限。 |

仓库可见性的初始决策表：

| 可见性 | 读取规则 | 写入规则 |
| --- | --- | --- |
| `public` | 互联网匿名可读（网页、API 和 Git clone/fetch 都要一致）。匿名身份只能读公开内容，不能枚举内部/private 仓库。 | 需已登录用户的显式 `write` 或 `admin` 仓库权限。 |
| `internal` | 活跃公司成员可读；按 FitHub 的团队前置要求，需已加入管理员创建的 team。 | 需显式 `write` 或 `admin`。 |
| `private` | 仅受邀用户/团队可读。邀请记录应明确授权角色；FitHub 需求中的“邀请者读写”建议落为显式 `write` 权限，不能把仓库邀请解释成平台管理员。 | `write` 或 `admin`。 |

**核心鉴权判定：** account active 且所属有效公司；PAT 的 repo 绑定和权限范围；团队/邀请授予的仓库角色；请求动作需要的角色，这几项取交集。用户绑定 token 跟随用户当前授权并可立即被封禁；仓库绑定 token 限定到单个仓库。PAT 的最高权限是其绑定范围内的仓库 `admin`，不能只看 token scope 跳过实时授权。**FitHub 已定决定：任何普通 `user` / `admin` token 都不能调用平台管理接口，即使该 token 主体具有 `platform_admin` 角色；平台管理接口必须使用独立的管理面认证机制。**

## Google 登录与员工离职

Google 文档说明 OIDC ID token 应使用 Discovery 文档公布的 `jwks_uri` 验签；核对 `iss`、`aud`、时效以及一次性 `nonce` 后，按 Google 当前 issuer 约束接受。身份键必须用 `sub`：Google 明确指出邮箱可能变化或不唯一，不能用邮箱作为主键。仅按 email 后缀判断公司成员也不够；Google 明确要求域名限制检查 ID token 的 `hd` claim。限制公司域时要求 `hd` 精确匹配配置的 Workspace 域，`email_verified=true` 是邮箱校验状态的辅助信号，不能代替 `hd` 和 token 验签。Google 文档指出 `hd` 可能缺失，缺失必须拒绝公司域限制登录。[Google OIDC Discovery 与 ID token claims](https://developers.google.com/identity/openid-connect/reference)；[Google OpenID Connect 登录指南](https://developers.google.com/identity/openid-connect/openid-connect)

**离职停用不能依赖下一次 Google 登录才生效。** FitHub 应有本地 `disabled_at` / `status` 撤权路径：一次禁用操作撤销 Web session、刷新令牌、API/PAT、MCP 凭证授权及 SSH key，且后端每次请求都检查状态。若希望跟随 Workspace 停权，可用 Admin SDK Directory API User 资源的 `suspended` 布尔字段同步，或由管理员在 FitHub 同步封禁；该资源另提供用户查询、变更通知和 `signOut`，但这不等于 FitHub 已吊销自己的凭证。目录轮询/通知延迟需要在 PoC 中测清；员工离职流程仍应先调用 FitHub 禁用。**字段核对：** 已打开官方 `REST Resource: users` 页面（页面更新日期 **2026-06-03 UTC**）；JSON schema 中字段为 `suspended`，原文短摘：“`suspended` | `boolean` Indicates if user is suspended.” 查询过滤文档中的 `isSuspended` 是另一名称，不能误写成 User 对象字段。[Directory API User 资源](https://developers.google.com/workspace/admin/directory/reference/rest/v1/users)

**Web 登录和 Git 认证分离。** 浏览器 session cookie 只用于 FitHub Web；不接受它作为 Git Smart HTTP 凭证。HTTPS Git 应使用 Git 专用的 FitHub token/凭证帮助程序，在每次 upload-pack/receive-pack 请求上按仓库权限鉴权；SSH 应使用单独注册、可撤销的 SSH 公钥，并通过 SSH transport 的 Git 服务执行 fetch/push。Git 官方 wire protocol 文档明确说明 packfile 在 SSH/HTTP 等传输上交换，而认证由传输层处理，因此 Web session、REST 调用和 Git pack 数据传输是不同层次。[Git pack protocol](https://git-scm.com/docs/gitprotocol-pack)；[Git HTTP protocol](https://git-scm.com/docs/http-protocol)

## CLI 登录建议

桌面/笔记本 CLI 的**研究备选方案**：`fithub auth login` 打开系统浏览器，走 FitHub 自己的 OAuth/OIDC 授权码 + PKCE；CLI 注册成 public native client，不在二进制里放共享 client secret。用 RFC 8252 推荐的 loopback IP 临时回调（只监听 `127.0.0.1`/`::1`，随机端口，收到响应即关闭端口），用系统安全存储保存短时 FitHub 凭证。RFC 8252 要求 native app 使用外部浏览器，不用嵌入式 WebView；loopback 回调可使用随机端口。[RFC 8252](https://www.rfc-editor.org/rfc/rfc8252)

对远程主机/无浏览器终端，**研究备选方案**是 RFC 8628 device authorization：CLI 显示短码和验证 URL，用户在另一台设备批准；需要 FitHub 授权服务器提供设备授权端点、限流、用户代码过期、轮询间隔/`slow_down`、以及防止钓鱼的清晰确认。Google OIDC Discovery 文档确实列有 `device_authorization_endpoint`，但这不代表 FitHub 自己的授权服务器天然支持该 grant。[RFC 8628](https://www.rfc-editor.org/rfc/rfc8628)；[Google Discovery 文档字段](https://developers.google.com/identity/openid-connect/reference)

CLI 的 FitHub REST token 与 HTTPS Git token可分不同 audience 或凭证类型；`git clone/push` 必须走实际 Git smart transport，由 Git credential helper/SSH agent 提供凭证。用户绑定 token按当前成员授权求交集；仓库绑定 token固定 repo、权限和有效期。撤销任何一个 token 不应要求注销浏览器 session，反之亦然。**首版决定：CLI/MCP 使用 FitHub PAT；上述 OAuth browser/device 流程仅供后续评估，不纳入首版。**

## MCP：远程 HTTP 与本地 stdio

**官方事实：** 按本次官方发布公告核对，当前正式规范为 MCP 2026-07-28。官方当前授权页说明：HTTP transport SHOULD 遵循该 OAuth 授权规范；stdio SHOULD NOT 照搬，凭证应从环境读取。不同 binding 上 MCP 消息语义相同。来源：[MCP 发布页](https://blog.modelcontextprotocol.io/posts/2026-07-28/)；[MCP 2026-07-28 Transports](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports)；[Streamable HTTP](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http)；[stdio](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/stdio)

授权规范明确限定于 HTTP transport：支持 HTTP 的实现 SHOULD 遵循；stdio 实现 SHOULD NOT 照搬这套 OAuth 流程，应从环境取得凭证。授权页及发布公告将DCR列为兼容旧授权服务器的保留机制，推荐方向转向CIMD。远程 MCP 若启用 OAuth，服务器作为 resource server，至少实现 Protected Resource Metadata；客户端通过资源元数据发现 AS，再从 OAuth AS Metadata 或 OIDC Discovery 发现端点。远程 MCP access token 必须验证且 audience 只能是该 MCP server；通过 `Authorization: Bearer` header 发送，不能放 URL，也不能把 Google ID token、Web cookie 或给别的 API 的 bearer token直接转发给 MCP。[MCP 2026-07-28 Authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)

**FitHub 已定首版：只提供本地 stdio MCP + PAT，不提供远程 MCP，也不实现远程 MCP OAuth。** stdio 子进程从 OS keychain 或受控环境读取 FitHub PAT，并调用与 REST 相同的 repo/team/visibility policy；不得因本地进程启动就获得额外权限。**研究建议：** 若未来增加远程 MCP，再按当前 HTTP 授权规范实现 PRM/OAuth discovery、MCP audience-bound token 和 scope challenge；不能把当前建议误写为首版范围。平台管理接口无条件拒绝所有普通 `user` / `admin` PAT，即使用户具有 `platform_admin` 角色。MCP 只是 JSON-RPC 工具调用传输，Git fetch/push 仍需走 Git Smart HTTP 或 SSH pack protocol，MCP tool 不能代替 Git pack 数据通道。

## 可借鉴的 GitHub 语义（有限调研）

GitHub 将 Issue 和 PR 都作为可交叉引用的工作项，但“提及/关联”与“自动关闭”语义应分开。PR 描述中 `Closes #10`、`Fixes org/repo#100` 等 closing keyword 在 PR 合并到目标仓库默认分支时自动关闭 issue；若目标分支不是默认分支，关键字不生效。支持同仓、跨仓和多个 issue；手工关联可以不自动关闭。建议 FitHub 显式存储 issue↔PR 引用边、引用来源（手工/语法）和 closing 规则，而不是把任意 `#123` 文本提及视为关闭操作。[GitHub：Linking a pull request to an issue](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue)

Review diff comment 定位至少是 `commit_id + path + line + side`；多行评论还要 `start_line + start_side`。GitHub 将 `position`（相对 diff hunk 的旧字段）标为 deprecated/closing down，建议新设计按文件 diff 行号及左右侧定位。commit SHA 必须关联评论时的 diff 快照，否则后续改动可能使行锚过期；需要验证评论在新 commit 后保留原始锚点，并显示 outdated/无法映射状态。[GitHub REST：Pull request review comments](https://docs.github.com/en/rest/pulls/comments)

## 必须进验收的边界 / 待 PoC

1. Google 登录：拒绝签名错误、错误 `iss`/`aud`、过期 ID token、nonce 不匹配、缺少 `hd`、非公司 `hd`；同一 Google `sub` 即使邮箱改名仍落到同一 FitHub 用户。邮箱相同但 `sub` 不同不能自动合并账号。
2. 离职：禁用后所有活跃 Web、API、Git HTTPS、SSH、MCP PAT 即刻失败；既有 Web cookie/refresh token 需验证是否也被实时撤销。Workspace `suspended` 同步的最长撤权时延需 PoC 定义。
3. Team/权限：合规员工无需邀请可先注册登录、读取有限team目录、申请加入并查看自己申请；待审核不授予成员资格，管理员批准才生效。普通用户不能自行造 team、自批申请或跳过 team 成员要求；只靠 public 匿名读取公开仓库；internal 对有效公司成员可读，非成员不能；private 仅受邀协作者可读；三种权限逐一验证网页/API/clone/fetch/push/MCP 一致性；跨仓 token 拒绝访问。
4. 提权：repo `admin` 与 platform admin 是不同角色；任何普通 `user` / `admin` PAT 均不能调用平台管理端点，即使主体是 platform admin。repo-bound token 的 `admin` 只代表该仓库 admin。用户被移出 team/撤邀请/降权后既存 token 不保留旧授权。
5. 凭证隔离：浏览器 session cookie 不能认证 Git HTTPS；首版 stdio 使用普通 FitHub PAT，必须调用同一 REST policy，不能因此获得额外权限；Git push 只有在该 PAT 对目标仓库当前有 `write`/`admin` 时才成功。SSH key 独立撤销。敏感 token 不得进入 URL、CLI 日志或 repo remote URL 的明文配置。
6. CLI/MCP PoC：首版只测 CLI PAT 撤销/存储和 stdio MCP 从配置来源取 token、调用同一 REST policy、拒绝平台管理端点。RFC 8252 loopback + PKCE、device flow 及远程 MCP OAuth 仅为后续研究项；如启动后续工作，需测试 macOS/Linux/Windows 回调、防端口抢占、设备流限速和过期、Protected Resource Metadata/OAuth discovery、正确 audience、scope challenge 和撤销。协议版本兼容清单以 2026-07-28 为正式基线。
7. Issue/PR：支持无关闭的普通关联、跨仓引用、默认分支合并才按 closing keyword 关闭、多 issue 关闭规则；review comment 用 commit/path/line/side 锚定，测删除行、上下文行、多行评论以及新 commit 造成的过期定位。

### 关键来源

- Google OIDC：[Discovery、ID token 与 claims](https://developers.google.com/identity/openid-connect/reference)，[OIDC 登录指南](https://developers.google.com/identity/openid-connect/openid-connect)。
- Google Workspace：[Admin SDK Directory API User 资源](https://developers.google.com/workspace/admin/directory/reference/rest/v1/users)。
- Native CLI OAuth：[RFC 8252](https://www.rfc-editor.org/rfc/rfc8252)，[RFC 8628](https://www.rfc-editor.org/rfc/rfc8628)。
- MCP：[2026-07-28 Authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)，[Transports](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports)。
- Git transport：[gitprotocol-pack](https://git-scm.com/docs/gitprotocol-pack)，[gitprotocol-http](https://git-scm.com/docs/http-protocol)。
- GitHub 参考语义：[Issue/PR 关联和 closing keywords](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue)，[Review diff comment API](https://docs.github.com/en/rest/pulls/comments)。
