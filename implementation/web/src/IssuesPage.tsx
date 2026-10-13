import { useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { ArrowLeft, Check, CheckCircle2, ChevronRight, CircleAlert, CircleDot, MessageSquareText, Plus, Tag, X } from 'lucide-react'
import { api, ApiError, type Issue, type IssueAssignee, type IssueLabel, type Milestone, type IssueState } from './api'
import { RepoLayout } from './RepoLayout'
import { Picker, Pagination, Hint, CheckboxControl, Segmented, MultiPicker } from './ui'
import { Markdown } from './Markdown'
import { issueHrefFor } from './markdown-links'
import './issues.css'

function path(team: string, repo: string, suffix = '') {
  return `/${encodeURIComponent(team)}/${encodeURIComponent(repo)}${suffix}`
}

function message(error: unknown) {
  return error instanceof Error ? error.message : '操作失败，请稍后重试。'
}

function date(value: string) {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
  }).format(parsed)
}

function ErrorBox({ error }: { error: unknown }) {
  return <div className="issue-error" role="alert"><CircleAlert size={17} />{message(error)}</div>
}

// labelStyle renders the stored RRGGBB color as a readable chip: a tinted
// background with a border of the same hue, so light colors stay legible.
function labelStyle(color: string) {
  const normalized = color.replace(/[^0-9a-fA-F]/g, '').padEnd(6, '0').slice(0, 6)
  return {
    backgroundColor: `#${normalized}22`,
    borderColor: `#${normalized}`,
    color: `#${normalized}dd`,
  }
}

export function LabelChip({ label }: { label: IssueLabel }) {
  return <Hint label={label.description || label.name}><span className="issue-label-chip" style={labelStyle(label.color)}>{label.name}</span></Hint>
}

// CheckboxWithChip pairs the shared accessible checkbox with arbitrary content
// (a colour chip, a person's name) instead of the plain text label the standard
// Checkbox expects. The wrapping <label> supplies the accessible name.
function CheckboxWithChip({ checked, disabled, chip, onChange }: { checked: boolean; disabled?: boolean; chip: ReactNode; onChange: () => void }) {
  return <>
    <CheckboxControl checked={checked} disabled={disabled} onCheckedChange={onChange} />
    {chip}
  </>
}

function useRepo(team: string, repo: string) {
  return useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo) })
}

function IssueStateMark({ state }: { state: IssueState }) {
  return <span className={`issue-state issue-state-${state}`}>
    {state === 'open' ? <CircleDot size={15} /> : <CheckCircle2 size={15} />}
    {state === 'open' ? '进行中' : '已关闭'}
  </span>
}

function IssueRow({ item, team, repo }: { item: Issue; team: string; repo: string }) {
  const names = item.assignees.map(assignee => assignee.name).join('、')
  return <Link className="issue-row" to={path(team, repo, `/issues/${item.number}`)}>
    <span className={`issue-row-symbol ${item.state}`}>{item.state === 'open' ? <CircleDot size={19} /> : <CheckCircle2 size={19} />}</span>
    <span className="issue-row-copy">
      <strong>{item.title}</strong>
      <span className="issue-row-meta">
        <small>#{item.number} · {item.authorName} 创建于 {date(item.createdAt)}</small>
        {item.labels.map(label => <LabelChip key={label.id} label={label} />)}
        {item.milestoneNumber != null && <span className="issue-milestone-chip"><Tag size={12} />{item.milestoneTitle}</span>}
        {names && <small className="issue-row-assignee">指派给 {names}</small>}
      </span>
    </span>
    <ChevronRight size={17} className="issue-row-chevron" />
  </Link>
}

export function IssuesPage() {
  const { team = '', repo = '' } = useParams()
  const [searchParams] = useSearchParams()
  const [state, setState] = useState<IssueState | 'all'>('open')
  const [page, setPage] = useState(1)
  const [labelIds, setLabelIds] = useState<string[]>([])
  const [assignees, setAssignees] = useState<string[]>([])
  const [milestone, setMilestone] = useState(() => searchParams.get('milestone') ?? '')
  const repository = useRepo(team, repo)
  const ready = repository.isSuccess
  const labels = useQuery({ queryKey: ['labels', team, repo], queryFn: () => api.labels(team, repo), enabled: ready })
  const milestones = useQuery({ queryKey: ['milestones', team, repo, 'all'], queryFn: () => api.milestones(team, repo, 'all'), enabled: ready })
  const people = useQuery({ queryKey: ['assignableUsers', team, repo], queryFn: () => api.assignableUsers(team, repo), enabled: ready })
  const issues = useQuery({
    queryKey: ['issues', team, repo, state, page, labelIds, assignees, milestone],
    queryFn: () => api.issues(team, repo, {
      state, page,
      labelIds,
      assignees,
      milestone: milestone ? Number(milestone) : undefined,
    }),
    enabled: ready,
  })
  const filtered = labelIds.length > 0 || assignees.length > 0 || Boolean(milestone)
  const reset = () => { setLabelIds([]); setAssignees([]); setMilestone(''); setPage(1) }
  if (repository.isLoading) return <div className="loading-block"><span className="spinner" />正在加载仓库…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  const item = repository.data
  return <RepoLayout section="issues" repo={item} wide toolbar={<>
    <span className="repo-toolbar-spacer" />
    {item.canWrite && <Link className="button button-primary button-small" to={path(team, repo, '/issues/new')}><Plus size={15} />新建 Issue</Link>}
  </>}>
    <section className="panel issue-list-panel">
      <div className="issue-filterbar">
        <Segmented ariaLabel="Issue 状态" value={state} onChange={value => { setState(value); setPage(1) }}
          options={[{ value: 'open', label: '进行中' }, { value: 'closed', label: '已关闭' }, { value: 'all', label: '全部' }]} />
      </div>
      <div className="issue-filterbar issue-filter-selects">
        <label><span>标签</span><MultiPicker ariaLabel="按标签筛选" selected={labelIds} placeholder="全部标签"
          onChange={values => { setLabelIds(values); setPage(1) }}
          options={(labels.data?.labels || []).map(label => ({ value: label.id, label: label.name }))} /></label>
        <label><span>负责人</span><MultiPicker ariaLabel="按负责人筛选" selected={assignees} placeholder="全部成员"
          onChange={values => { setAssignees(values); setPage(1) }}
          options={(people.data?.users || []).map(person => ({ value: person.userId, label: person.name }))} /></label>
        <label><span>里程碑</span><Picker ariaLabel="按里程碑筛选" value={milestone} onChange={value => { setMilestone(value); setPage(1) }}
          options={[{ value: '', label: '全部里程碑' }, ...(milestones.data?.milestones || []).map(entry => ({ value: String(entry.number), label: `#${entry.number} ${entry.title}` }))]} /></label>
        {filtered && <button type="button" className="button button-secondary button-small" onClick={reset}><X size={14} />清除筛选</button>}
      </div>
      {issues.isLoading && <div className="loading-block"><span className="spinner" />正在加载 Issue…</div>}
      {issues.isError && <ErrorBox error={issues.error} />}
      {issues.data?.issues.length === 0 && <div className="issue-empty"><MessageSquareText size={30} /><h3>{filtered ? '没有符合条件的 Issue' : state === 'open' ? '没有进行中的 Issue' : state === 'closed' ? '没有已关闭的 Issue' : '还没有 Issue'}</h3><p>{filtered ? '换一个标签、负责人或里程碑，或清除筛选。' : item.canWrite ? '创建一个 Issue，让团队知道下一步要解决什么。' : '这个仓库目前没有符合条件的 Issue。'}</p>{filtered ? <button className="button button-secondary" type="button" onClick={reset}>清除筛选</button> : item.canWrite && <Link className="button button-secondary" to={path(team, repo, '/issues/new')}>新建 Issue</Link>}</div>}
      {issues.data?.issues.map(issue => <IssueRow key={issue.id} item={issue} team={team} repo={repo} />)}
      {issues.data && <Pagination label="Issue 分页" page={page} hasMore={issues.data.hasMore} onPage={setPage} />}
    </section>
  </RepoLayout>
}

export function NewIssuePage() {
  const { team = '', repo = '' } = useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const repository = useRepo(team, repo)
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const create = useMutation({ mutationFn: () => api.createIssue(team, repo, title.trim(), body.trim()), onSuccess: async ({ issue }) => {
    await queryClient.invalidateQueries({ queryKey: ['issues', team, repo] })
    navigate(path(team, repo, `/issues/${issue.number}`))
  } })
  if (repository.isLoading) return <div className="loading-block"><span className="spinner" />正在加载仓库…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  if (!repository.data.canWrite) return <ErrorBox error={new ApiError('你没有这个仓库的写入权限。', 403)} />
  return <RepoLayout section="issues" repo={repository.data}><div className="issue-toolbar"><div><h2>描述要解决的事</h2><p>清晰的标题和背景能帮助团队更快推进。</p></div></div>
    <form className="panel issue-form" onSubmit={(event: FormEvent) => { event.preventDefault(); if (title.trim()) create.mutate() }}>
      <label className="field-label" htmlFor="issue-title">标题</label><input className="field-input" id="issue-title" value={title} onChange={event => setTitle(event.target.value)} maxLength={200} required placeholder="一句话说明问题或任务" />
      <label className="field-label" htmlFor="issue-body">详细说明</label><textarea className="issue-textarea" id="issue-body" value={body} onChange={event => setBody(event.target.value)} maxLength={65536} placeholder="补充复现步骤、预期结果或验收标准…" rows={12} />
      <p className="issue-form-hint">内容以纯文本展示。创建后可添加标签、指派负责人和关联里程碑；PR 描述可用 Refs #编号 或 Fixes #编号 关联。</p>
      {create.isError && <ErrorBox error={create.error} />}
      <div className="issue-form-actions"><Link className="button button-secondary" to={path(team, repo, '/issues')}><ArrowLeft size={15} />取消</Link><button className="button button-primary" disabled={create.isPending || !title.trim()}>{create.isPending ? '正在创建…' : '创建 Issue'}</button></div>
    </form>
  </RepoLayout>
}

// IssueMetadataPanel lets a writer change labels, assignees, and the milestone
// from the Issue detail page. Each control saves immediately and reflects the
// server response, so the view never shows a change that was rejected.
function IssueMetadataPanel({ team, repo, issue, canWrite, assignable, labels, milestones }: {
  team: string, repo: string, issue: Issue, canWrite: boolean
  assignable: IssueAssignee[], labels: IssueLabel[], milestones: Milestone[]
}) {
  const queryClient = useQueryClient()
  const [selectedLabels, setSelectedLabels] = useState<string[]>(issue.labels.map(label => label.id))
  const [selectedPeople, setSelectedPeople] = useState<string[]>(issue.assignees.map(person => person.userId))
  const [milestoneNumber, setMilestoneNumber] = useState(issue.milestoneNumber ? String(issue.milestoneNumber) : '')
  const refresh = async () => { await queryClient.invalidateQueries({ queryKey: ['issue', team, repo, issue.number] }) }

  const saveLabels = useMutation({ mutationFn: (ids: string[]) => api.setIssueLabels(team, repo, issue.number, ids), onSuccess: refresh })
  const savePeople = useMutation({ mutationFn: (ids: string[]) => api.setIssueAssignees(team, repo, issue.number, ids), onSuccess: refresh })
  const saveMilestone = useMutation({ mutationFn: (number: number | null) => api.setIssueMilestone(team, repo, issue.number, number), onSuccess: refresh })

  if (!canWrite) {
    return <section className="panel issue-metadata-panel">
      <div className="issue-metadata-readonly">
        <div><span className="issue-metadata-label">标签</span>{issue.labels.length ? issue.labels.map(label => <LabelChip key={label.id} label={label} />) : <span className="muted">未设置</span>}</div>
        <div><span className="issue-metadata-label">负责人</span>{issue.assignees.length ? issue.assignees.map(person => <span key={person.userId} className="issue-assignee-chip">{person.name}</span>) : <span className="muted">未指派</span>}</div>
        <div><span className="issue-metadata-label">里程碑</span>{issue.milestoneNumber != null ? <span className="issue-milestone-chip"><Tag size={12} />#{issue.milestoneNumber} {issue.milestoneTitle}</span> : <span className="muted">未关联</span>}</div>
      </div>
    </section>
  }

  const error = saveLabels.error ?? savePeople.error ?? saveMilestone.error
  return <section className="panel issue-metadata-panel">
    <div className="issue-metadata-heading"><Tag size={17} /><h3>分类与指派</h3><span className="muted">改动立即生效</span></div>
    <div className="issue-metadata-grid">
      <div className="issue-metadata-field">
        <span className="issue-metadata-label">标签</span>
        <div className="issue-checkbox-list">
          {labels.length === 0 && <span className="muted">仓库还没有标签，可先在「标签」页创建。</span>}
          {labels.map(label => {
            const checked = selectedLabels.includes(label.id)
            return <label key={label.id} className="issue-checkbox">
              <CheckboxWithChip checked={checked} disabled={saveLabels.isPending} chip={<LabelChip label={label} />} onChange={() => {
                const next = checked ? selectedLabels.filter(id => id !== label.id) : [...selectedLabels, label.id]
                setSelectedLabels(next)
                saveLabels.mutate(next)
              }} />
            </label>
          })}
        </div>
      </div>
      <div className="issue-metadata-field">
        <span className="issue-metadata-label">负责人</span>
        <div className="issue-checkbox-list">
          {assignable.length === 0 && <span className="muted">没有可指派的成员。</span>}
          {assignable.map(person => {
            const checked = selectedPeople.includes(person.userId)
            return <label key={person.userId} className="issue-checkbox">
              <CheckboxWithChip checked={checked} disabled={savePeople.isPending} chip={<span>{person.name}<small className="muted"> · {person.email}</small></span>} onChange={() => {
                const next = checked ? selectedPeople.filter(id => id !== person.userId) : [...selectedPeople, person.userId]
                setSelectedPeople(next)
                savePeople.mutate(next)
              }} />
            </label>
          })}
        </div>
      </div>
      <div className="issue-metadata-field">
        <span className="issue-metadata-label">里程碑</span>
        <Picker ariaLabel="里程碑" value={milestoneNumber} disabled={saveMilestone.isPending}
          onChange={value => { setMilestoneNumber(value); saveMilestone.mutate(value ? Number(value) : null) }}
          options={[{ value: '', label: '未关联' }, ...milestones.map(entry => ({ value: String(entry.number), label: `#${entry.number} ${entry.title}`, hint: entry.state === 'closed' ? '已关闭' : undefined }))]} />
      </div>
    </div>
    {error && <ErrorBox error={error} />}
  </section>
}

export function IssueDetailPage() {
  const { team = '', repo = '', number = '' } = useParams()
  // Stable so the Markdown blocks do not re-parse on every render.
  const issueHref = useMemo(() => issueHrefFor(team, repo), [team, repo])
  const issueNumber = Number(number)
  const [commentBody, setCommentBody] = useState('')
  const [commentPage, setCommentPage] = useState(1)
  const queryClient = useQueryClient()
  const repository = useRepo(team, repo)
  const enabled = repository.isSuccess && Number.isSafeInteger(issueNumber) && issueNumber > 0
  const issue = useQuery({ queryKey: ['issue', team, repo, issueNumber], queryFn: () => api.issue(team, repo, issueNumber), enabled })
  const comments = useQuery({ queryKey: ['issueComments', team, repo, issueNumber, commentPage], queryFn: () => api.issueComments(team, repo, issueNumber, commentPage), enabled: issue.isSuccess })
  const labels = useQuery({ queryKey: ['labels', team, repo], queryFn: () => api.labels(team, repo), enabled })
  const milestones = useQuery({ queryKey: ['milestones', team, repo, 'all'], queryFn: () => api.milestones(team, repo, 'all'), enabled })
  const people = useQuery({ queryKey: ['assignableUsers', team, repo], queryFn: () => api.assignableUsers(team, repo), enabled })
  const update = useMutation({ mutationFn: (state: IssueState) => api.updateIssueState(team, repo, issueNumber, state), onSuccess: async ({ issue: updated }) => {
    queryClient.setQueryData(['issue', team, repo, issueNumber], updated)
    await queryClient.invalidateQueries({ queryKey: ['issues', team, repo] })
  } })
  const addComment = useMutation({ mutationFn: () => api.createIssueComment(team, repo, issueNumber, commentBody.trim()), onSuccess: async () => {
    setCommentBody('')
    setCommentPage(1)
    await queryClient.invalidateQueries({ queryKey: ['issueComments', team, repo, issueNumber] })
  } })
  if (repository.isLoading || issue.isLoading) return <div className="loading-block"><span className="spinner" />正在加载 Issue…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  if (!Number.isSafeInteger(issueNumber) || issueNumber < 1) return <ErrorBox error={new ApiError('Issue 编号无效。', 400)} />
  if (issue.isError || !issue.data) return <ErrorBox error={issue.error} />
  const item = issue.data
  return <RepoLayout section="issues" repo={repository.data} wide><Link className="issue-back" to={path(team, repo, '/issues')}><ArrowLeft size={15} />全部 Issue</Link>
    <div className="issue-detail-heading"><h2>{item.title}<span>#{item.number}</span></h2><div><IssueStateMark state={item.state} /><span>{item.authorName} 创建于 {date(item.createdAt)}{item.closedAt ? ` · 关闭于 ${date(item.closedAt)}` : ''}</span></div></div>
    <section className="panel issue-body-panel">
      <div className="issue-body-author"><span className="issue-author-avatar">{item.authorName.trim().slice(0, 1).toUpperCase() || 'U'}</span><strong>{item.authorName}</strong><span>创建了这个 Issue</span></div>
      <div className="issue-body-content">{item.body ? <Markdown source={item.body} issueHref={issueHref} /> : <p className="muted">暂无详细说明。</p>}</div>
    </section>
    <IssueMetadataPanel team={team} repo={repo} issue={item} canWrite={Boolean(repository.data.canWrite)}
      labels={labels.data?.labels ?? []} milestones={milestones.data?.milestones ?? []} assignable={people.data?.users ?? []} />
    {item.closedByPullNumber && <div className="issue-linked-pr"><CheckCircle2 size={17} />由 <Link to={path(team, repo, `/pulls/${item.closedByPullNumber}`)}>PR #{item.closedByPullNumber}</Link> 合并后关闭</div>}
    <section className="panel issue-comments-panel"><div className="issue-comments-heading"><MessageSquareText size={18} /><h3>讨论</h3></div>
      {comments.isLoading && <div className="loading-block"><span className="spinner" />正在加载评论…</div>}
      {comments.isError && <ErrorBox error={comments.error} />}
      {comments.data?.comments.length === 0 && <p className="issue-comments-empty">还没有评论。</p>}
      {comments.data?.comments.map(comment => <article className="issue-comment" key={comment.id}><div className="issue-comment-head"><span className="issue-author-avatar">{comment.authorName.trim().slice(0, 1).toUpperCase() || 'U'}</span><strong>{comment.authorName}</strong><span>{date(comment.createdAt)}</span></div><Markdown className="issue-comment-body" source={comment.body} issueHref={issueHref} /></article>)}
      {comments.data && <Pagination label="评论分页" page={commentPage} hasMore={comments.data.hasMore} onPage={setCommentPage} />}
      {repository.data.canWrite && <form className="issue-comment-form" onSubmit={(event: FormEvent) => { event.preventDefault(); if (commentBody.trim()) addComment.mutate() }}><label className="field-label" htmlFor="issue-comment-body">添加评论</label><textarea id="issue-comment-body" className="issue-textarea" value={commentBody} onChange={event => setCommentBody(event.target.value)} maxLength={65536} rows={4} placeholder="补充进展、问题或验收结果…" />{addComment.isError && <ErrorBox error={addComment.error} />}<button className="button button-primary" disabled={addComment.isPending || !commentBody.trim()}>{addComment.isPending ? '正在发布…' : '发布评论'}</button></form>}
    </section>
    {repository.data.canWrite && <div className="issue-detail-actions">{update.isError && <ErrorBox error={update.error} />}<button className="button button-secondary" disabled={update.isPending} onClick={() => update.mutate(item.state === 'open' ? 'closed' : 'open')}>{item.state === 'open' ? <Check size={16} /> : <CircleDot size={16} />}{update.isPending ? '正在更新…' : item.state === 'open' ? '关闭 Issue' : '重新打开'}</button></div>}
  </RepoLayout>
}
