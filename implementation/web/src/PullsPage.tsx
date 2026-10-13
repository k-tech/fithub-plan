import { useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, Check, CheckCircle2, ChevronRight, CircleAlert, GitBranch, GitMerge, GitPullRequest, Plus, X } from 'lucide-react'
import { api, type PullRequest, type PullState } from './api'
import { PullDiffReview } from './PullDiffReview'
import { RepoLayout } from './RepoLayout'
import { Picker, Pagination, Segmented } from './ui'
import { Markdown } from './Markdown'
import { issueHrefFor } from './markdown-links'
import './pulls.css'
import './diff-review.css'

function path(team: string, repo: string, suffix = '') {
  return `/${encodeURIComponent(team)}/${encodeURIComponent(repo)}${suffix}`
}

function date(value: string) {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
  }).format(parsed)
}

function ErrorBox({ error }: { error: unknown }) {
  return <div className="pull-error" role="alert"><CircleAlert size={17} />{error instanceof Error ? error.message : '操作失败，请稍后重试。'}</div>
}

function State({ state }: { state: PullState }) {
  return <span className={`pull-state pull-state-${state}`}>{state === 'open' ? <GitPullRequest size={14} /> : state === 'merged' ? <GitMerge size={14} /> : <CheckCircle2 size={14} />}{state === 'open' ? '进行中' : state === 'merged' ? '已合并' : '已关闭'}</span>
}

function PullRow({ item, team, repo }: { item: PullRequest; team: string; repo: string }) {
  return <Link className="pull-row" to={path(team, repo, `/pulls/${item.number}`)}><span className={`pull-row-icon ${item.state}`}>{item.state === 'merged' ? <GitMerge size={18} /> : <GitPullRequest size={18} />}</span><span className="pull-row-copy"><strong>{item.title}</strong><small>#{item.number} · {item.authorName} · {item.headRef} → {item.baseRef} · {date(item.createdAt)}</small></span><ChevronRight size={16} /></Link>
}

export function PullsPage() {
  const { team = '', repo = '' } = useParams()
  const [state, setState] = useState<PullState | 'all'>('open')
  const [page, setPage] = useState(1)
  const repository = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo) })
  const pulls = useQuery({ queryKey: ['pulls', team, repo, state, page], queryFn: () => api.pulls(team, repo, state, page), enabled: repository.isSuccess })
  if (repository.isLoading) return <div className="loading-block"><span className="spinner" />正在打开仓库…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  const item = repository.data
  return <RepoLayout section="pulls" repo={item} wide toolbar={<>
    <span className="repo-toolbar-spacer" />
    {item.canWrite && <Link className="button button-primary button-small" to={path(team, repo, '/pulls/new')}><Plus size={15} />新建 PR</Link>}
  </>}>
    <section className="panel pull-list-panel"><div className="pull-filterbar"><Segmented ariaLabel="PR 状态" value={state} onChange={value => { setState(value); setPage(1) }}
        options={[{ value: 'open', label: '进行中' }, { value: 'merged', label: '已合并' }, { value: 'closed', label: '已关闭' }, { value: 'all', label: '全部' }]} /></div>
      {pulls.isLoading && <div className="loading-block"><span className="spinner" />正在加载 PR…</div>}
      {pulls.isError && <ErrorBox error={pulls.error} />}
      {pulls.data?.pulls.length === 0 && <div className="pull-empty"><GitPullRequest size={30} /><h3>没有符合条件的 PR</h3><p>{item.canWrite ? '先推送一个开发分支，再创建合并请求。' : '这个仓库目前没有符合条件的合并请求。'}</p></div>}
      {pulls.data?.pulls.map(pull => <PullRow key={pull.id} item={pull} team={team} repo={repo} />)}
      {pulls.data && <Pagination label="PR 分页" page={page} hasMore={pulls.data.hasMore} onPage={setPage} />}
    </section>
  </RepoLayout>
}

export function NewPullPage() {
  const { team = '', repo = '' } = useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [headRef, setHeadRef] = useState('')
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const repository = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo) })
  const branches = useQuery({ queryKey: ['branches', team, repo], queryFn: () => api.branches(team, repo), enabled: repository.isSuccess })
  const options = branches.data?.branches.filter(branch => branch.name !== 'main') || []
  const selected = headRef || options[0]?.name || ''
  const create = useMutation({ mutationFn: () => api.createPull(team, repo, title.trim(), body.trim(), selected), onSuccess: async ({ pull }) => {
    await queryClient.invalidateQueries({ queryKey: ['pulls', team, repo] })
    navigate(path(team, repo, `/pulls/${pull.number}`))
  } })
  if (repository.isLoading) return <div className="loading-block"><span className="spinner" />正在打开仓库…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  if (!repository.data.canWrite) return <ErrorBox error={new Error('你没有这个仓库的写入权限。')} />
  return <RepoLayout section="pulls" repo={repository.data}><div className="pull-toolbar"><div><h2>选择要合并的开发分支</h2><p>源分支与目标 main 都在这个仓库，不会产生 Fork 副本。</p></div></div>
    <form className="panel pull-form" onSubmit={(event: FormEvent) => { event.preventDefault(); if (title.trim() && selected) create.mutate() }}>
      <div className="pull-branch-row"><label><span>源分支</span><Picker ariaLabel="源分支" value={selected} onChange={setHeadRef} placeholder="没有开发分支"
            options={options.map(branch => ({ value: branch.name, label: branch.name }))} /></label><span className="pull-arrow">→</span><label><span>目标分支</span><strong><GitBranch size={15} />main</strong></label></div>
      {branches.isLoading && <div className="loading-block"><span className="spinner" />正在读取分支…</div>}
      {branches.isError && <ErrorBox error={branches.error} />}
      {!branches.isLoading && !options.length && <div className="pull-notice"><CircleAlert size={17} />先从本地把 feature 分支推送到仓库，再回来创建 PR。</div>}
      <label className="field-label" htmlFor="pull-title">标题</label><input className="field-input" id="pull-title" value={title} onChange={event => setTitle(event.target.value)} maxLength={200} required placeholder="概括这次改动" />
      <label className="field-label" htmlFor="pull-body">说明</label><textarea className="pull-textarea" id="pull-body" value={body} onChange={event => setBody(event.target.value)} maxLength={65536} rows={9} placeholder="描述背景、测试方法以及可能影响的部分…" />
      <p className="pull-form-hint">在说明中单独写一行「Refs #1」可关联 Issue；写「Fixes #1」会在成功合并后自动关闭它。PR 是协作方式，不需要额外审批即可合并。</p>
      {create.isError && <ErrorBox error={create.error} />}
      <div className="pull-form-actions"><Link className="button button-secondary" to={path(team, repo, '/pulls')}><ArrowLeft size={15} />取消</Link><button className="button button-primary" disabled={create.isPending || !title.trim() || !selected}>{create.isPending ? '正在创建…' : '创建 PR'}</button></div>
    </form>
  </RepoLayout>
}

export function PullDetailPage() {
  const { team = '', repo = '', number = '' } = useParams()
  const pullNumber = Number(number)
  // Stable so the Markdown block does not re-parse on every render.
  const issueHref = useMemo(() => issueHrefFor(team, repo), [team, repo])
  const [reviewBody, setReviewBody] = useState('')
  const [reviewPage, setReviewPage] = useState(1)
  const queryClient = useQueryClient()
  const me = useQuery({ queryKey: ['me'], queryFn: api.me })
  const repository = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo) })
  const pull = useQuery({ queryKey: ['pull', team, repo, pullNumber], queryFn: () => api.pull(team, repo, pullNumber), enabled: repository.isSuccess && Number.isSafeInteger(pullNumber) && pullNumber > 0 })
  const diff = useQuery({ queryKey: ['pullDiff', team, repo, pullNumber], queryFn: () => api.pullDiff(team, repo, pullNumber), enabled: pull.isSuccess })
  const reviews = useQuery({ queryKey: ['pullReviews', team, repo, pullNumber, reviewPage], queryFn: () => api.pullReviews(team, repo, pullNumber, reviewPage), enabled: pull.isSuccess })
  const refresh = async () => { await Promise.all([queryClient.invalidateQueries({ queryKey: ['pull', team, repo, pullNumber] }), queryClient.invalidateQueries({ queryKey: ['pullDiff', team, repo, pullNumber] }), queryClient.invalidateQueries({ queryKey: ['pulls', team, repo] }), queryClient.invalidateQueries({ queryKey: ['repo', team, repo] }), queryClient.invalidateQueries({ queryKey: ['issues', team, repo] }), queryClient.invalidateQueries({ queryKey: ['issue', team, repo] })]) }
  const merge = useMutation({ mutationFn: () => api.mergePull(team, repo, pullNumber, pull.data!.currentHeadSha!), onSuccess: refresh })
  const close = useMutation({ mutationFn: () => api.closePull(team, repo, pullNumber), onSuccess: refresh })
  const review = useMutation({ mutationFn: (state: 'approved' | 'changes_requested') => api.createPullReview(team, repo, pullNumber, state, reviewBody.trim(), pull.data!.currentBaseSha!, pull.data!.currentHeadSha!), onSuccess: async () => {
    setReviewBody('')
    setReviewPage(1)
    await Promise.all([refresh(), queryClient.invalidateQueries({ queryKey: ['pullReviews', team, repo, pullNumber] })])
  } })
  if (repository.isLoading || pull.isLoading) return <div className="loading-block"><span className="spinner" />正在加载 PR…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  if (!Number.isSafeInteger(pullNumber) || pullNumber < 1) return <ErrorBox error={new Error('PR 编号无效。')} />
  if (pull.isError || !pull.data) return <ErrorBox error={pull.error} />
  const item = pull.data
  const currentReview = item.review
  const canMerge = item.state === 'open' && item.mergeability === 'clean' && Boolean(item.currentHeadSha) && repository.data.canWrite
  const canReview = item.state === 'open' && repository.data.canWrite && Boolean(me.data && me.data.id !== item.authorId) && Boolean(item.currentBaseSha && item.currentHeadSha)
  return <RepoLayout section="pulls" repo={repository.data} wide><Link className="pull-back" to={path(team, repo, '/pulls')}><ArrowLeft size={15} />全部 PR</Link>
    <div className="pull-detail-heading"><h2>{item.title}<span>#{item.number}</span></h2><div><State state={item.state} /><span>{item.authorName} 于 {date(item.createdAt)} 从 <code>{item.headRef}</code> 向 <code>{item.baseRef}</code> 提交</span></div></div>
    <section className="panel pull-description"><div className="pull-description-author"><span>{item.authorName.trim().slice(0, 1).toUpperCase() || 'U'}</span><strong>{item.authorName}</strong> 描述了这次改动</div><div className="pull-description-body">{item.body ? <Markdown source={item.body} issueHref={issueHref} /> : <span className="muted">暂无说明。</span>}</div></section>
    {!!item.issues?.length && <section className="panel pull-issues"><h3>关联 Issue</h3>{item.issues.map(issue => <Link key={issue.number} to={path(team, repo, `/issues/${issue.number}`)}><span>#{issue.number} · {issue.title}</span><small>{issue.state === 'closed' ? '已关闭' : issue.closesOnMerge ? '合并后关闭' : '仅关联'}</small></Link>)}</section>}
    <section className="panel pull-diff"><div className="pull-diff-heading"><h3>代码变更</h3>{diff.data && <span>{diff.data.mergeBaseSha.slice(0, 7)} → {diff.data.headSha.slice(0, 7)}</span>}</div>
      {diff.isLoading && <div className="loading-block"><span className="spinner" />正在计算变更…</div>}
      {diff.isError && <ErrorBox error={diff.error} />}
      {diff.data && (diff.data.patch ? <PullDiffReview team={team} repo={repo} number={pullNumber} patch={diff.data.patch}
        baseSha={diff.data.mergeBaseSha} headSha={diff.data.headSha}
        canComment={item.state === 'open' && Boolean(repository.data.canWrite)} /> : <p className="pull-diff-empty">没有可显示的文本变更。</p>)}
    </section>
    <section className="panel pull-reviews"><div className="pull-reviews-heading"><h3>代码审查</h3>{item.state === 'open' && currentReview && <span>{currentReview.approvals} 份批准 · {currentReview.changesRequested} 份修改请求</span>}</div>
      {reviews.isLoading && <div className="loading-block"><span className="spinner" />正在加载审查记录…</div>}
      {reviews.isError && <ErrorBox error={reviews.error} />}
      {reviews.data?.reviews.length === 0 && <p className="pull-reviews-empty">{item.state === 'merged' ? '这条已合并 PR 没有审查记录。' : '还没有审查记录。可以在这里留下自愿的审查意见。'}</p>}
      {reviews.data?.reviews.map(entry => { const current = entry.baseSha === item.currentBaseSha && entry.headSha === item.currentHeadSha; return <article className="pull-review" key={entry.id}><div><strong>{entry.reviewerName}</strong><span className={entry.state === 'approved' ? 'approved' : 'changes'}>{entry.state === 'approved' ? '批准' : '要求修改'}</span>{item.state === 'open' && !current && <small>旧版本</small>}<time>{date(entry.createdAt)}</time></div>{entry.body && <Markdown className="pull-review-body" source={entry.body} issueHref={issueHref} />}</article> })}
      {reviews.data && <Pagination label="审查分页" page={reviewPage} hasMore={reviews.data.hasMore} onPage={setReviewPage} />}
      {canReview && <div className="pull-review-form"><label className="field-label" htmlFor="pull-review-body">留下审查意见</label><textarea id="pull-review-body" className="pull-textarea" value={reviewBody} onChange={event => setReviewBody(event.target.value)} maxLength={65536} rows={4} placeholder="说明你的建议或验收结果…" />{review.isError && <ErrorBox error={review.error} />}<div><button className="button button-secondary" disabled={review.isPending || !reviewBody.trim()} onClick={() => review.mutate('changes_requested')}>要求修改</button><button className="button button-primary" disabled={review.isPending} onClick={() => review.mutate('approved')}>批准此版本</button></div></div>}
    </section>
    {item.state === 'open' && <section className="panel pull-merge-panel"><div className={`pull-merge-symbol${item.mergeability === 'clean' ? '' : ' blocked'}`}>{item.mergeability === 'clean' ? <Check size={21} /> : <CircleAlert size={21} />}</div><div className="pull-merge-copy"><h3>{item.mergeability === 'clean' ? '可以合并' : item.mergeability === 'conflicting' ? '存在合并冲突' : item.mergeability === 'missing_ref' ? '分支已不存在' : item.mergeability === 'empty' ? '没有待合并改动' : '暂时无法判断合并状态'}</h3><p>{item.mergeability === 'clean' ? `${item.filesChanged ?? 0} 个文件变更 · +${item.additions ?? 0} −${item.deletions ?? 0}。合并前会再次校验最新 HEAD。` : item.mergeability === 'conflicting' ? '请先在本地把 main 合入开发分支并解决冲突，然后推送。' : item.mergeability === 'empty' ? '开发分支当前没有可合并的文件变更。' : '刷新页面或确认分支仍存在。'}</p>{item.mergeability === 'conflicting' && <pre>{`git fetch origin\ngit switch ${item.headRef}\ngit merge origin/main\n# 解决冲突后提交并推送`}</pre>}</div></section>}
    {item.state === 'merged' && item.mergedSha && <div className="pull-merged-note"><GitMerge size={18} />已合并为 <Link to={path(team, repo, `/commits/${item.mergedSha}`)}>{item.mergedSha.slice(0, 10)}</Link></div>}
    {repository.data.canWrite && item.state === 'open' && <div className="pull-actions">{merge.isError && <ErrorBox error={merge.error} />}{close.isError && <ErrorBox error={close.error} />}<button className="button button-secondary" disabled={close.isPending || merge.isPending} onClick={() => close.mutate()}><X size={15} />关闭 PR</button><button className="button button-primary" disabled={!canMerge || merge.isPending || close.isPending} onClick={() => merge.mutate()}><GitMerge size={16} />{merge.isPending ? '正在合并…' : '合并到 main'}</button></div>}
  </RepoLayout>
}
