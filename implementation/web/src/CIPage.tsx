import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from 'react-router-dom'
import {
  ArrowLeft, Ban, CheckCircle2, ChevronRight, CircleAlert, CircleDashed, Clock3,
  GitBranch, GitCommitHorizontal, PlayCircle, RefreshCw, RotateCw, ScrollText,
  ServerCog, XCircle,
} from 'lucide-react'
import { api, type CIRun, type CIRunState } from './api'
import { RepoLayout } from './RepoLayout'
import { Pagination, Segmented } from './ui'
import './ci.css'

function path(team: string, repo: string, suffix = '') {
  return `/${encodeURIComponent(team)}/${encodeURIComponent(repo)}${suffix}`
}

function date(value?: string) {
  if (!value) return '—'
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', {
    month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit',
  }).format(parsed)
}

// duration renders how long a run took, or how long it has been going.
function duration(run: CIRun) {
  if (!run.startedAt) return '—'
  const start = new Date(run.startedAt).getTime()
  const end = run.finishedAt ? new Date(run.finishedAt).getTime() : Date.now()
  if (Number.isNaN(start) || Number.isNaN(end) || end < start) return '—'
  const seconds = Math.round((end - start) / 1000)
  if (seconds < 60) return `${seconds} 秒`
  const minutes = Math.floor(seconds / 60)
  return `${minutes} 分 ${seconds % 60} 秒`
}

function ErrorBox({ error }: { error: unknown }) {
  return <div className="ci-error" role="alert"><CircleAlert size={17} />{error instanceof Error ? error.message : '操作失败，请稍后重试。'}</div>
}

const stateLabels: Record<CIRunState, string> = {
  queued: '排队中',
  running: '运行中',
  succeeded: '通过',
  failed: '未通过',
  cancelled: '已取消',
  timed_out: '超时',
  infrastructure_failed: '环境故障',
}

// RunState explains a run in one glance. The distinction between a failed check
// and a broken runner matters operationally, so the two are shown differently.
export function RunState({ state, compact = false }: { state: CIRunState; compact?: boolean }) {
  const icon = state === 'succeeded' ? <CheckCircle2 size={14} />
    : state === 'failed' ? <XCircle size={14} />
      : state === 'running' ? <RefreshCw size={14} className="ci-spin" />
        : state === 'queued' ? <Clock3 size={14} />
          : state === 'timed_out' ? <Clock3 size={14} />
            : state === 'infrastructure_failed' ? <ServerCog size={14} />
              : <Ban size={14} />
  return <span className={`ci-state ci-state-${state}${compact ? ' ci-state-compact' : ''}`}>{icon}{stateLabels[state]}</span>
}

function eventLabel(event: CIRun['event']) {
  return event === 'push' ? '推送' : event === 'pull_request' ? 'PR 更新' : '手动触发'
}

function shortSha(sha: string) {
  return sha.slice(0, 8)
}

const filters: Array<{ value: CIRunState | 'all'; label: string }> = [
  { value: 'all', label: '全部' },
  { value: 'running', label: '运行中' },
  { value: 'queued', label: '排队中' },
  { value: 'succeeded', label: '通过' },
  { value: 'failed', label: '未通过' },
]

function RunRow({ run, team, repo }: { run: CIRun; team: string; repo: string }) {
  return <Link className="ci-run-row" to={path(team, repo, `/actions/${run.number}`)}>
    <RunState state={run.state} />
    <span className="ci-run-copy">
      <strong>{run.workflowName}<span className="ci-run-number">#{run.number}</span></strong>
      <small>{eventLabel(run.event)} · <code>{run.ref.replace('refs/heads/', '')}</code> · {shortSha(run.headSha)} · {date(run.createdAt)}{run.jobCount > 0 && ` · ${run.jobCount} 个任务`}</small>
    </span>
    <span className="ci-run-duration">{duration(run)}</span>
    <ChevronRight size={16} />
  </Link>
}

export function CIRunsPage() {
  const { team = '', repo = '' } = useParams()
  const [state, setState] = useState<CIRunState | 'all'>('all')
  const [page, setPage] = useState(1)
  const queryClient = useQueryClient()
  const repository = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo) })
  const runs = useQuery({
    queryKey: ['runs', team, repo, state, page],
    queryFn: () => api.runs(team, repo, { state, page }),
    enabled: repository.isSuccess,
    // A queued or running job changes on its own, so the list follows it.
    refetchInterval: query => (query.state.data?.runs.some(run => run.state === 'queued' || run.state === 'running') ? 4000 : false),
  })
  const dispatch = useMutation({
    mutationFn: () => api.dispatchRun(team, repo),
    onSuccess: async () => { setState('all'); setPage(1); await queryClient.invalidateQueries({ queryKey: ['runs', team, repo] }) },
  })

  if (repository.isLoading) return <div className="loading-block"><span className="spinner" />正在打开仓库…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  const item = repository.data

  return <RepoLayout section="actions" repo={item} wide toolbar={<>
    <span className="repo-toolbar-spacer" />
    {item.canWrite && <button className="button button-primary button-small" type="button" disabled={dispatch.isPending} onClick={() => dispatch.mutate()}>
      <PlayCircle size={15} />{dispatch.isPending ? '正在触发…' : '手动运行'}
    </button>}
  </>}>
    {dispatch.isError && <ErrorBox error={dispatch.error} />}
    {dispatch.isSuccess && <div className="ci-success"><CheckCircle2 size={16} />已排队一个手动运行，稍后出现在下面的列表里。</div>}
    <section className="panel ci-runs-panel">
      <div className="ci-filterbar"><Segmented ariaLabel="运行状态" value={state} onChange={value => { setState(value); setPage(1) }}
        options={filters.map(entry => ({ value: entry.value, label: entry.label }))} /></div>
      {runs.isLoading && <div className="loading-block"><span className="spinner" />正在加载运行记录…</div>}
      {runs.isError && <ErrorBox error={runs.error} />}
      {runs.data?.runs.length === 0 && <div className="ci-empty">
        <PlayCircle size={30} />
        <h3>还没有运行记录</h3>
        <p>{item.canWrite ? '推送一份包含 .fithub/workflows 定义的提交，或在右上角手动运行。' : '这个仓库目前没有自动检查记录。'}</p>
      </div>}
      {runs.data?.runs.map(run => <RunRow key={run.id} run={run} team={team} repo={repo} />)}
      {runs.data && <Pagination label="运行分页" page={page} hasMore={runs.data.hasMore} onPage={setPage} />}
    </section>
  </RepoLayout>
}

// LogViewer shows one job's output and follows it while the job is still
// running. It reconnects with the last cursor it saw, so a dropped connection
// resumes instead of repeating or skipping lines.
function LogViewer({ team, repo, number, job, active }: { team: string; repo: string; number: number; job: string; active: boolean }) {
  const [lines, setLines] = useState<string[]>([])
  const [error, setError] = useState<unknown>(null)
  const boxRef = useRef<HTMLPreElement>(null)
  const followRef = useRef(true)

  useEffect(() => {
    let cancelled = false
    let timer: number | undefined
    // The cursor lives only inside this effect. Different jobs have independent
    // sequence numbers, so a cursor kept in React state would leak from one job
    // into the next and make a freshly selected job look empty.
    let at = -1
    setLines([])
    setError(null)
    const tick = async () => {
      try {
        const result = await api.runLogs(team, repo, number, at)
        if (cancelled) return
        const entry = result.jobs.find(item => item.jobId === job)
        if (entry) {
          if (entry.lines.length > 0) setLines(previous => [...previous, ...entry.lines])
          at = entry.cursor
        }
        setError(null)
      } catch (caught) {
        if (!cancelled) setError(caught)
      }
      // Only keep polling while the job can still produce output. A finished job
      // is read once; the effect re-runs when its state changes or the user picks
      // another job, so a fresh read still happens then.
      if (!cancelled && active) timer = window.setTimeout(tick, 2500)
    }
    void tick()
    return () => { cancelled = true; if (timer) window.clearTimeout(timer) }
  }, [team, repo, number, job, active])

  useEffect(() => {
    const node = boxRef.current
    if (node && followRef.current) node.scrollTop = node.scrollHeight
  }, [lines])

  return <div className="ci-log">
    <div className="ci-log-head">
      <ScrollText size={15} />
      <strong>{job}</strong>
      {active && <span className="ci-log-following"><span className="ci-pulse" />实时跟随</span>}
    </div>
    {error ? <ErrorBox error={error} /> : null}
    <pre className="ci-log-body" ref={boxRef} onScroll={event => {
      const node = event.currentTarget
      followRef.current = node.scrollHeight - node.scrollTop - node.clientHeight < 40
    }}>{lines.length > 0 ? lines.join('') : '这个任务还没有输出。'}</pre>
  </div>
}

export function CIRunDetailPage() {
  const { team = '', repo = '', number = '' } = useParams()
  const runNumber = Number(number)
  const [selectedJob, setSelectedJob] = useState('')
  const queryClient = useQueryClient()
  const repository = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo) })
  const detail = useQuery({
    queryKey: ['run', team, repo, runNumber],
    queryFn: () => api.run(team, repo, runNumber),
    enabled: repository.isSuccess && Number.isSafeInteger(runNumber) && runNumber > 0,
    // Follow a run until it reaches a terminal state.
    refetchInterval: query => {
      const state = query.state.data?.run.state
      return state === 'queued' || state === 'running' ? 3000 : false
    },
  })
  const cancel = useMutation({
    mutationFn: () => api.cancelRun(team, repo, runNumber),
    onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: ['run', team, repo, runNumber] }) },
  })

  const jobs = detail.data?.jobs ?? []
  const currentJob = useMemo(() => selectedJob || jobs[0]?.jobId || '', [selectedJob, jobs])
  const run = detail.data?.run
  const isActive = run?.state === 'queued' || run?.state === 'running'

  if (repository.isLoading || detail.isLoading) return <div className="loading-block"><span className="spinner" />正在加载运行详情…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  if (!Number.isSafeInteger(runNumber) || runNumber < 1) return <ErrorBox error={new Error('运行编号无效。')} />
  if (detail.isError || !run) return <ErrorBox error={detail.error} />

  return <RepoLayout section="actions" repo={repository.data} wide>
    <Link className="ci-back" to={path(team, repo, '/actions')}><ArrowLeft size={15} />全部运行</Link>

    <section className="panel ci-summary">
      <div className="ci-summary-head">
        <div>
          <h2>{run.workflowName}<span className="ci-run-number">#{run.number}</span></h2>
          <div className="ci-summary-meta">
            <RunState state={run.state} />
            <span>{eventLabel(run.event)}</span>
            <span className="ci-dot" />
            <GitBranch size={13} /><code>{run.ref.replace('refs/heads/', '')}</code>
            <span className="ci-dot" />
            <GitCommitHorizontal size={13} /><code>{shortSha(run.headSha)}</code>
            <span className="ci-dot" />
            <span>{date(run.createdAt)}</span>
            <span className="ci-dot" />
            <span>耗时 {duration(run)}</span>
          </div>
        </div>
        <div className="ci-summary-actions">
          {isActive && repository.data.canWrite && <button className="button button-secondary" type="button" disabled={cancel.isPending} onClick={() => cancel.mutate()}>
            <Ban size={15} />{cancel.isPending ? '正在取消…' : '取消运行'}
          </button>}
          <button className="button button-secondary" type="button" onClick={() => { void detail.refetch() }}><RotateCw size={15} />刷新</button>
        </div>
      </div>
      {cancel.isError && <ErrorBox error={cancel.error} />}
      {run.conclusionReason && <p className="ci-conclusion"><CircleAlert size={14} />{run.conclusionReason}</p>}
      {!run.trusted && <p className="ci-warning"><ShieldNotice />这次运行的检查定义来自非默认分支，仅供参考，不能作为合并依据。</p>}
    </section>

    <section className="panel ci-jobs">
      <div className="ci-jobs-head"><h3>任务</h3><span>{jobs.length} 个</span></div>
      {jobs.length === 0 && <p className="ci-jobs-empty">这次运行没有任务。</p>}
      {jobs.map(job => <button key={job.id} type="button" className={`ci-job-row${job.jobId === currentJob ? ' active' : ''}`} onClick={() => setSelectedJob(job.jobId)}>
        <RunState state={job.state} compact />
        <span className="ci-job-copy"><strong>{job.name}</strong><small>jobId <code>{job.jobId}</code> · 运行于 <code>{job.runsOn}</code>{job.needs.length > 0 && ` · 依赖 ${job.needs.join(', ')}`}{job.attempt > 1 && ` · 第 ${job.attempt} 次尝试`}</small></span>
        <span className="ci-job-time">{job.startedAt ? date(job.startedAt) : '等待中'}</span>
      </button>)}
    </section>

    {currentJob && <LogViewer team={team} repo={repo} number={runNumber} job={currentJob} active={Boolean(isActive)} />}
  </RepoLayout>
}

function ShieldNotice() {
  return <CircleDashed size={14} />
}
