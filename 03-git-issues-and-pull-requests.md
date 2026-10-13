# Git、Issue 与 PR 的实现规格

本文把核心开发工作拆到可编写 API、界面和验收用例的程度。基础原语参考 [Git/Gitea 调研](research/01-git-and-reuse.md)，产品规则以 [产品与权限](01-product-and-permissions.md) 为准。

## 1. 哪些 Git 命令需要服务器支持

`git init / add / commit / status / diff / log / branch / switch / checkout / stash / rebase / merge / cherry-pick / reset` 主要是本地 Git 功能，不在 FitHub 重新实现一遍。用户先在本地 commit，再 push；FitHub 接收提交对象并展示历史。

服务端必须正确支持标准协议里的 `clone / fetch / pull / push / ls-remote`，分支/tag 创建删除和受规则约束的 force push。`pull` 本身由客户端 fetch 后 merge/rebase。图形 Git 工具只要支持标准 HTTPS/SSH 就可以连接，不需要 FitHub 插件。

```bash
# 域名是部署时替换的占位符，命令不包含密码或 Token。
git clone https://git.example.com/engineering/demo.git
git switch -c fix/42
git add .
git commit -m "Fix parsing error"
git push -u origin fix/42
fh pr create --repo engineering/demo --base main --head fix/42 \
  --title "Fix parsing error" --body "Fixes #42"
```

SSH remote 对应 `git@git.example.com:engineering/demo.git`。员工公钥映射平台用户，SSH 用户名固定 `git`，不能通过改变 SSH 用户名冒充其他员工。

## 2. HTTPS 与 SSH 接入

### HTTPS

1. 区分 `GET info/refs?service=git-upload-pack`、`POST git-upload-pack` 与 `git-receive-pack`，分别执行 read/write 授权。
2. 解析 slug → 仓库不可变 ID → 受控真实路径。禁用未经授权的静态 `.git`/objects 文件访问及 dumb HTTP 路径。
3. 只向 `git-http-backend` 传递白名单 CGI 环境与协议协商信息；不能信任客户端自称的 REMOTE_USER 等身份字段。
4. 以流式方式转发 request/response，不把大 pack 全读入内存；处理取消、超时、错误响应及连接清理。
5. 匿名 public 可以 fetch；private/internal 必须先验证凭据。Git HTTPS 使用 FitHub Token，通过 Git credential helper 存储，不使用 Google 密码。
6. 网关、反向代理、Git进程的请求大小和超时协调；服务端拒绝 push 时，客户端必须看到可理解原因。

Git 官方对这些入口和配置边界的定义见 [`git-http-backend`](https://git-scm.com/docs/git-http-backend)。

### SSH

- OpenSSH 强制执行受限包装器；仅接受 `git-upload-pack` 和 `git-receive-pack` 的预定语法，客户端 command 字符串视作不可信输入。
- 包装器从被验证的 key ID 找用户，再进行与 HTTPS 完全相同的授权。禁止 shell、PTY、agent forwarding、TCP forwarding 和任意 exec。
- 系统 Git 通过直接 argv 调用，不将 SSH_ORIGINAL_COMMAND 传给 `sh -c`。
- SSH Key 注册/删除需用户会话；校验支持的算法、key 长度和重复 fingerprint；key comment 不是身份来源。
- Git路径、hooks、配置和包装器归专用服务用户所有；员工没有主机文件写权限。数据库不可用/授权接口不可用时拒绝写入，不能 fail open。

## 3. push 策略与事件

### pre-receive 同步准入

读取本次全部 `old_oid / new_oid / refname`，逐条验证：用户与凭据未撤销、仓库未归档/删除、refs命名合法、分支/tag策略、force/delete限制、配额、工作流文件修改规则。拒绝任何一条策略即拒绝本次 push，禁止只检查第一条 ref。

禁止客户端写 `refs/fithub/*` 等内部命名空间。新建分支/删除分支的全零 OID、空仓库、annotated tag 和非 commit tag 需要各自处理。默认 tag 不允许被改写或删除；仓库管理员通过有审计的管理入口处理特殊需求。全新空仓库允许repo admin在一次引导操作中首次写入默认分支，成功后立即启用保护；不能把“空仓库例外”扩展为后续直接push豁免。

**策略拒绝的整体失败不等于所有 push 天然原子。** 普通非 atomic push 仍可能因后续并发 ref 锁失败而部分成功；只有客户端请求 `git push --atomic` 且服务端支持时，承诺同次 refs 事务原子。post-receive 必须以实际更新成功的 ref 列表产生事件，UI 如实展示；数据库与 Git 不在同一事务中。

`pre-receive` 中不得执行用户脚本、联网下载依赖或跑CI；否则 push 被不可信耗时工作阻塞。校验失败返回稳定原因，如 `BRANCH_PROTECTED`、`TOKEN_READ_ONLY`、`REPO_ARCHIVED`、`QUOTA_EXCEEDED`。

### post-receive 与对账

记录 repo ID、actor、credential ID（不是secret）、transport、old/new OID、实际ref、时间和 event ID。投递 outbox/持久事件；更新 PR head、审查失效、关联、通知和 workflow 触发均异步处理。

post-receive 失败不撤销 push，这是 Git 本身的语义。事件入口不可用时落本地有界持久 spool 并告警；worker 重放去重。定期扫描 refs 对账能修复“分支当前状态”，但未必还原两次快速覆盖之间的每一个短暂历史，因此可靠 spool/审计不能完全用定时扫描替代。[Git receive-pack 与 hooks](https://git-scm.com/docs/git-receive-pack)

撤权语义：新请求立即检查；push 在 pre-receive 再查一次当前授权；合并在最终 ref 更新前再查。长连接 fetch 已发出的字节不能追回，停用时尽力终止活跃进程与任务；不得把“即时撤权”写成可撤回已下载代码的承诺。授权和 ref 更新的极短竞争窗口需通过共享写入协调锁/策略版本检测减少，并在压测中验证。

## 4. 代码浏览与 Commit UI

- 文件树、blob、history 使用 Git机器可解析输出；含空格、换行、tab和非UTF-8的文件名采用 `-z` 等安全边界，不能按空格拆行。
- branch/tag名先解析成合法ref，再固定为 SHA；路径独立传参，使用 `--`，禁用外部diff与不必要的配置/过滤器执行。
- README/Markdown严格消毒；HTML/SVG等不可信内容默认下载或隔离展示，不能在带认证Cookie的主站 origin 原样执行。
- symbolic link展示目标文本，不让服务器沿链接读取主机文件；submodule展示gitlink的SHA和URL，默认不自动抓取远端。
- 提交页区分 author 与 committer，支持多父提交；merge commit 的diff采用明确父提交比较选择，不能把缺失diff当“没有改动”。
- 大文件、二进制、重命名/复制、文件权限变化、CRLF、无换行结尾、空提交和删除文件有专门呈现。
- commit SHA永久链接固定内容，分支链接会变；归档下载固定到SHA。文件/目录历史查询设置超时与分页。
- 首版支持普通/浅clone和deepen；partial clone及`--filter`延后，不能宣称未经测试的组合已兼容。LFS单独判断，见路线图。

## 5. PR 状态与差异

PR业务状态为 `open / merged / closed`；draft是独立标志。合并可行性另存 `checking / clean / conflicting / stale / missing_ref / unrelated_history / error`，不要将“检测失败”显示成“无冲突”。

创建 PR 时记录 base repo/ref、head repo/ref、head SHA、base SHA、作者、描述。当前仅实现同仓库不同分支PR，不实现Fork；API校验head repo与base repo相同，不接受跨仓来源。有仓库write权限的成员创建并push开发分支即可发起PR，无需额外仓库副本。head缺失、base=head、没有有效差异或无共同祖先时给出明确处理，不偷偷启用 `--allow-unrelated-histories`。

差异分两类：

- **PR审查差异**：base与head的merge-base → head（三点比较），让用户看到本次分支引入的改动。
- **实际合并结果**：最新base与head进行真实试合并，检查冲突与最终候选提交。三点diff不等于“保证可合并”。

多merge-base历史交由原生Git合并算法处理；不要随意取第一个祖先后声称结果完整。第一周PoC覆盖criss-cross样本，若支持能力不足，显示特殊历史错误并要求本地整理历史。来源见 [`git-merge-base`](https://git-scm.com/docs/git-merge-base)。

head或base变化后重新计算diff与冲突状态；review记录作为协作反馈保留在其原始版本上，不作为合并依据。CI按工作流触发并展示结果，但不参与合并许可判断。原始review快照与PR head/candidate对象通过服务端内部refs保留，避免force-push后被GC而无法查看；内部refs禁止客户端写，归档/删除时按保留策略清理。

## 6. Review 与评论

审查状态 `comment / approve / request_changes`；作者不能批准自己的PR。review须来自当前有仓库写权限的有效用户，只提供协作反馈，不决定能否合并。

行内评论保存：原始base/head SHA、path、左右侧、行号或行区间、原始hunk/context。PR有新提交时尝试映射，无法可靠映射即显示outdated，保留原始diff；不把同一行号的后来代码误当被评论代码。对照 [GitHub review comment 字段](https://docs.github.com/en/rest/pulls/comments)。

线程可回复、resolve/reopen；`request_changes`与未解决线程均是协作状态，不会阻止有写权限的用户合并。

审查提交、删除评论、管理性dismiss操作均记录时间线。批量review可先采用一组草稿评论+一次提交，避免每条自动通知打扰所有人；可在P3后段交付。

## 7. 合并算法与并发控制

支持 `merge commit` 和 `squash`；不先做rebase merge。API必须携带客户端看到的 `expected_head_sha`，避免用户在旧页面点击合并意外包含新代码。

1. 校验当前用户有write、仓库正常、PR open且非draft。
2. 读取最新base/head，检查expected head；锁定该repo目标ref的合并临界区。审查和CI结果不作为合并门槛。
3. 在隔离临时工作目录/临时index创建候选提交；Git不执行仓库提供的自定义hooks/filters。
4. 有冲突即停止，不改变任何目标ref；保存冲突文件列表，不生成带冲突标记的假成功commit。
5. CI运行结果只作为页面上的协作信息，不阻止合并。最终写入使用当前已计算的候选commit；输入发生变化则重新计算。
6. 再次读取当前用户的仓库写权限，采用 `git update-ref <ref> <candidate> <expected_old>` 条件写入；base有变化则返回409并重新计算。
7. merge attempt保存预期/结果commit；ref更新成功后标PR merged、关闭符合规则Issue、触发push CI/通知。宕机恢复按实际Git ref和attempt对账。

[`git update-ref`](https://git-scm.com/docs/git-update-ref) 能避免把并发更新的目标分支覆盖；它本身不会调用receive-pack的pre-receive，因此应用合并入口必须重新校验当前仓库写权限并记录操作者，不能绕过授权。FitHub不设置分支保护、审批或CI门槛。

merge author/committer：保留原始提交作者；候选merge commit使用固定服务器committer，实际执行合并用户记录在时间线/审计；squash默认以PR作者为author、服务器为committer，提供co-author文本但不伪造签名。身份元数据与commit message在候选检查前确定并持久化，最终合并不改写已测试SHA。具体邮箱策略在P3确定。

## 8. 冲突处理体验

首版检测冲突并提供清楚的本地解决命令，不做浏览器冲突编辑器。示例按真实base/head生成，先提示保持工作区干净：

```bash
git fetch origin
git switch fix/42
git merge origin/main
# 解决冲突，随后：
git add <已解决的文件>
git commit
git push origin fix/42
```

允许有仓库write权限的用户自行rebase后`--force-with-lease`更新head分支；平台不额外限制普通Git更新操作。push后PR继续同一个编号，重新显示diff、使旧审查过期并重跑CI。

## 9. Issue、引用与时间线实现

把Issue/PR通用内容放work_items，引用关系独立存储。Markdown解析后在可引用的文本节点识别`#N`等语法，跳过code block、inline code和链接文本中不应识别的位置；避免全字符串正则误触发。

关闭指令使用有测试的窄语法，明确支持大小写、多个Issue和标点。只有PR描述的有效关闭意图在默认分支成功合并时执行；普通commit引用、非默认分支merge、关闭未合并PR都不自动关闭Issue。

关联事件需具备来源ID和版本：例如comment被编辑后移除引用，旧timeline保留“曾引用”或撤销状态；列表中的当前关联不能继续误导。跨仓库引用不暴露私有repo标题、人员、链接预览和计数。

## 10. 首版导入与迁移

先迁一个非关键真实仓库，暂停原站写入后推送全部heads/tags，比较ref与OID并执行`git fsck`；核对默认分支、LFS、submodule、clone地址和CI流程。首次导入不直接对已被用户使用的目标执行`--mirror`，因为它可能删除目标refs；对新空仓库也须过滤源平台的私有PR/internal refs。

网页导入任意URL有SSRF与凭据泄露风险，首版优先管理员CLI在受控环境执行导入，不开放任意互联网import API。旧Issue/PR/附件不会随Git导入出现，若需要，另建迁移工程并保留旧编号映射。试点保留原平台只读回退期，不能让两个remote同时作为写入真源。
