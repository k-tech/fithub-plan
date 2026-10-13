import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from 'react-router-dom'
import { CheckCircle2, CircleAlert, CircleDot, Plus, Tag, Trash2 } from 'lucide-react'
import { api, type IssueState, type Milestone } from './api'
import { LabelChip } from './IssuesPage'
import { RepoLayout, repoBase } from './RepoLayout'
import './issues.css'

function message(error: unknown) {
  return error instanceof Error ? error.message : '操作失败，请稍后重试。'
}

function ErrorBox({ error }: { error: unknown }) {
  return <div className="issue-error" role="alert"><CircleAlert size={17} />{message(error)}</div>
}

function date(value: string) {
  const parsed = new Date(`${value}T00:00:00`)
  return Number.isNaN(parsed.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'short', day: 'numeric' }).format(parsed)
}

// A fixed palette instead of a free colour picker. The previous control let
// anyone choose any colour, including ones whose text is unreadable on the
// light chip background; these values are chosen to stay legible.
const LABEL_COLORS = [
  { value: '#1f883d', name: '绿' },
  { value: '#0969da', name: '蓝' },
  { value: '#8250df', name: '紫' },
  { value: '#cf222e', name: '红' },
  { value: '#9a6700', name: '琥珀' },
  { value: '#57606a', name: '灰' },
  { value: '#bf3989', name: '玫红' },
  { value: '#0a3069', name: '深蓝' },
]

export function LabelsPage() {
  const { team = '', repo = '' } = useParams()
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [color, setColor] = useState('#6b7280')
  const [description, setDescription] = useState('')
  const repository = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo) })
  const labels = useQuery({ queryKey: ['labels', team, repo], queryFn: () => api.labels(team, repo), enabled: repository.isSuccess })
  const create = useMutation({
    mutationFn: () => api.createLabel(team, repo, name.trim(), color, description.trim()),
    onSuccess: async () => { setName(''); setDescription(''); await queryClient.invalidateQueries({ queryKey: ['labels', team, repo] }) },
  })
  const remove = useMutation({
    mutationFn: (labelId: string) => api.deleteLabel(team, repo, labelId),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['labels', team, repo] })
      // Labels are embedded in Issue responses, so refresh those too.
      await queryClient.invalidateQueries({ queryKey: ['issues', team, repo] })
      await queryClient.invalidateQueries({ queryKey: ['issue', team, repo] })
    },
  })
  if (repository.isLoading) return <div className="loading-block"><span className="spinner" />正在加载仓库…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  const item = repository.data
  const canWrite = item.canWrite
  return <RepoLayout section="labels" repo={item}>
    <div className="issue-toolbar"><div><h2>仓库标签</h2><p>用颜色和名称给 Issue 分类，便于筛选和汇总。</p></div></div>
    {canWrite && <form className="panel issue-form issue-label-form" onSubmit={(event: FormEvent) => { event.preventDefault(); if (name.trim()) create.mutate() }}>
      <div className="issue-label-form-row">
        <label className="issue-label-form-field"><span className="field-label">名称</span><input className="field-input" value={name} maxLength={50} required placeholder="例如 Bug" onChange={event => setName(event.target.value)} /></label>
        <div className="issue-label-form-field issue-label-color-field">
          <span className="field-label">颜色</span>
          <div className="issue-color-choices" role="radiogroup" aria-label="标签颜色">
            {LABEL_COLORS.map(choice => <button key={choice.value} type="button" role="radio" aria-checked={color.toLowerCase() === choice.value} aria-label={choice.name} title={choice.name}
              className={`issue-color-choice${color.toLowerCase() === choice.value ? ' selected' : ''}`} style={{ background: choice.value }} onClick={() => setColor(choice.value)} />)}
          </div>
        </div>
        <label className="issue-label-form-field"><span className="field-label">说明</span><input className="field-input" value={description} maxLength={200} placeholder="可选" onChange={event => setDescription(event.target.value)} /></label>
      </div>
      {create.isError && <ErrorBox error={create.error} />}
      <div className="issue-form-actions"><span className="issue-label-preview">预览：<LabelChip label={{ id: 'preview', name: name || '标签', color: color.replace('#', ''), description }} /></span><button className="button button-primary" disabled={create.isPending || !name.trim()}><Plus size={16} />{create.isPending ? '正在创建…' : '创建标签'}</button></div>
    </form>}
    <section className="panel issue-list-panel">
      {labels.isLoading && <div className="loading-block"><span className="spinner" />正在加载标签…</div>}
      {labels.isError && <ErrorBox error={labels.error} />}
      {remove.isError && <ErrorBox error={remove.error} />}
      {labels.data?.labels.length === 0 && <div className="issue-empty"><Tag size={30} /><h3>还没有标签</h3><p>{canWrite ? '创建第一个标签，之后就能给 Issue 分类和筛选。' : '这个仓库还没有定义标签。'}</p></div>}
      {labels.data?.labels.map(label => <div className="issue-label-row" key={label.id}>
        <LabelChip label={label} />
        <span className="issue-label-description">{label.description || <span className="muted">无说明</span>}</span>
        {canWrite && <button className="button button-secondary button-small" type="button" disabled={remove.isPending} onClick={() => remove.mutate(label.id)}><Trash2 size={14} />删除</button>}
      </div>)}
    </section>
  </RepoLayout>
}

function milestoneStateLabel(state: IssueState) {
  return state === 'open' ? '进行中' : '已关闭'
}

export function MilestonesPage() {
  const { team = '', repo = '' } = useParams()
  const queryClient = useQueryClient()
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [dueOn, setDueOn] = useState('')
  const repository = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo) })
  const milestones = useQuery({ queryKey: ['milestones', team, repo, 'all'], queryFn: () => api.milestones(team, repo, 'all'), enabled: repository.isSuccess })
  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ['milestones', team, repo] })
  }
  const create = useMutation({
    mutationFn: () => api.createMilestone(team, repo, title.trim(), description.trim(), dueOn),
    onSuccess: async () => { setTitle(''); setDescription(''); setDueOn(''); await invalidate() },
  })
  const toggle = useMutation({
    mutationFn: (entry: Milestone) => api.updateMilestone(team, repo, entry.number, entry.title, entry.description, entry.dueOn ?? '', entry.state === 'open' ? 'closed' : 'open'),
    onSuccess: invalidate,
  })
  if (repository.isLoading) return <div className="loading-block"><span className="spinner" />正在加载仓库…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  const item = repository.data
  const canWrite = item.canWrite
  return <RepoLayout section="milestones" repo={item}>
    <div className="issue-toolbar"><div><h2>里程碑</h2><p>把一批 Issue 归到一个目标下，跟踪整体完成情况。</p></div></div>
    {canWrite && <form className="panel issue-form" onSubmit={(event: FormEvent) => { event.preventDefault(); if (title.trim()) create.mutate() }}>
      <label className="field-label" htmlFor="milestone-title">标题</label>
      <input className="field-input" id="milestone-title" value={title} maxLength={200} required placeholder="例如 v1.0 发布" onChange={event => setTitle(event.target.value)} />
      <label className="field-label" htmlFor="milestone-description">说明</label>
      <textarea className="issue-textarea" id="milestone-description" value={description} maxLength={65536} rows={3} placeholder="可选：这个里程碑的验收标准" onChange={event => setDescription(event.target.value)} />
      <label className="field-label" htmlFor="milestone-due">截止日期</label>
      <input className="field-input" id="milestone-due" type="date" value={dueOn} onChange={event => setDueOn(event.target.value)} />
      {create.isError && <ErrorBox error={create.error} />}
      <div className="issue-form-actions"><button className="button button-primary" disabled={create.isPending || !title.trim()}><Plus size={16} />{create.isPending ? '正在创建…' : '创建里程碑'}</button></div>
    </form>}
    <section className="panel issue-list-panel">
      {milestones.isLoading && <div className="loading-block"><span className="spinner" />正在加载里程碑…</div>}
      {milestones.isError && <ErrorBox error={milestones.error} />}
      {toggle.isError && <ErrorBox error={toggle.error} />}
      {milestones.data?.milestones.length === 0 && <div className="issue-empty"><Tag size={30} /><h3>还没有里程碑</h3><p>{canWrite ? '创建第一个里程碑，把相关 Issue 归到一起。' : '这个仓库还没有里程碑。'}</p></div>}
      {milestones.data?.milestones.map(entry => <div className="issue-milestone-row" key={entry.id}>
        <span className={`issue-row-symbol ${entry.state}`}>{entry.state === 'open' ? <CircleDot size={19} /> : <CheckCircle2 size={19} />}</span>
        <div className="issue-milestone-copy">
          <strong>#{entry.number} {entry.title}</strong>
          <small>{milestoneStateLabel(entry.state)}{entry.dueOn ? ` · 截止 ${date(entry.dueOn)}` : ''} · 进行中 {entry.openIssues} · 已完成 {entry.closedIssues}</small>
          {entry.description && <p>{entry.description}</p>}
        </div>
        <div className="issue-milestone-actions">
          <Link className="button button-secondary button-small" to={`${repoBase(team, repo)}/issues?milestone=${entry.number}`}>查看 Issue</Link>
          {canWrite && <button className="button button-secondary button-small" type="button" disabled={toggle.isPending} onClick={() => toggle.mutate(entry)}>{entry.state === 'open' ? '关闭' : '重新打开'}</button>}
        </div>
      </div>)}
    </section>
  </RepoLayout>
}
