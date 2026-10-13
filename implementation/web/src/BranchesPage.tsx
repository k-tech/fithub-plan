import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { GitBranch, GitCommitHorizontal, Search, Tag } from 'lucide-react'
import { api, type GitBranch as GitBranchModel, type GitTag } from './api'
import { RepoLayout } from './RepoLayout'
import { Segmented } from './ui'
import './branches.css'

// One page for both branch and tag listings, as the two questions ("what is
// being worked on" and "what has been released") are answered by the same
// lookup and are reached from the same place.
type Tab = 'branches' | 'tags'

function shortSha(sha: string) {
  return sha.slice(0, 8)
}

function date(value?: string) {
  if (!value) return ''
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }).format(parsed)
}

function treeLink(team: string, repo: string, ref: string) {
  const query = new URLSearchParams({ ref, path: '' })
  return `/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/tree?${query.toString()}`
}

function BranchRow({ branch, team, repo }: { branch: GitBranchModel; team: string; repo: string }) {
  return <Link className="ref-row" to={treeLink(team, repo, branch.name)}>
    <span className="ref-row-icon"><GitBranch size={17} /></span>
    <span className="ref-row-copy">
      <span className="ref-row-name">
        <strong>{branch.name}</strong>
        {branch.isDefault && <span className="ref-badge">默认</span>}
      </span>
      {branch.subject && <small className="ref-row-subject">{branch.subject}</small>}
    </span>
    <span className="ref-row-meta">
      {branch.authorName && <span>{branch.authorName}</span>}
      {branch.committedAt && <span>{date(branch.committedAt)}</span>}
      <code>{shortSha(branch.sha)}</code>
    </span>
  </Link>
}

function TagRow({ tag, team, repo }: { tag: GitTag; team: string; repo: string }) {
  return <Link className="ref-row" to={treeLink(team, repo, tag.name)}>
    <span className="ref-row-icon"><Tag size={17} /></span>
    <span className="ref-row-copy">
      <span className="ref-row-name">
        <strong>{tag.name}</strong>
        {tag.annotated && <span className="ref-badge">附注标签</span>}
      </span>
      {tag.subject && <small className="ref-row-subject">{tag.subject}</small>}
    </span>
    <span className="ref-row-meta">
      {tag.createdAt && <span>{date(tag.createdAt)}</span>}
      <code>{shortSha(tag.sha)}</code>
    </span>
  </Link>
}

export function BranchesPage() {
  const { team = '', repo = '' } = useParams()
  const [tab, setTab] = useState<Tab>('branches')
  const [filter, setFilter] = useState('')
  const repository = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo), enabled: Boolean(team && repo) })
  const branches = useQuery({ queryKey: ['branches', team, repo], queryFn: () => api.branches(team, repo), enabled: Boolean(team && repo) })
  const tags = useQuery({ queryKey: ['tags', team, repo], queryFn: () => api.tags(team, repo), enabled: Boolean(team && repo) })

  const needle = filter.trim().toLowerCase()
  const shownBranches = useMemo(() => (branches.data?.branches || []).filter(branch => !needle || branch.name.toLowerCase().includes(needle)), [branches.data, needle])
  const shownTags = useMemo(() => (tags.data?.tags || []).filter(tag => !needle || tag.name.toLowerCase().includes(needle)), [tags.data, needle])

  const active = tab === 'branches' ? branches : tags
  const total = tab === 'branches' ? branches.data?.branches.length : tags.data?.tags.length
  const shown = tab === 'branches' ? shownBranches : shownTags

  return <RepoLayout section="code" repo={repository.data} wide toolbar={<>
    <div className="ref-filter">
      <Search size={14} />
      <input value={filter} onChange={event => setFilter(event.target.value)} placeholder={tab === 'branches' ? '按分支名筛选' : '按标签名筛选'} aria-label={tab === 'branches' ? '按分支名筛选' : '按标签名筛选'} />
    </div>
    <span className="repo-toolbar-spacer" />
    <Segmented ariaLabel="分支或标签" value={tab} onChange={value => { setTab(value); setFilter('') }}
      options={[{ value: 'branches', label: '分支' }, { value: 'tags', label: '标签' }]} />
  </>}>
    <section className="panel ref-panel">
      {active.isLoading && <div className="loading-block"><span className="spinner" />正在读取…</div>}
      {active.isError && <div className="ref-empty"><strong>暂时读不到列表</strong><p>请稍后重试。</p></div>}
      {!active.isLoading && !active.isError && shown.length === 0 && <div className="ref-empty">
        <strong>{needle ? '没有匹配项' : tab === 'branches' ? '还没有分支' : '还没有标签'}</strong>
        <p>{needle ? '换一个关键词，或清空筛选。' : tab === 'branches' ? '第一次推送后，分支会显示在这里。' : '在本地执行 git tag 并推送后，版本标签会显示在这里。'}</p>
      </div>}
      {tab === 'branches'
        ? shownBranches.map(branch => <BranchRow key={branch.name} branch={branch} team={team} repo={repo} />)
        : shownTags.map(tag => <TagRow key={tag.name} tag={tag} team={team} repo={repo} />)}
      {!active.isLoading && shown.length > 0 && <div className="ref-panel-foot">
        <GitCommitHorizontal size={14} />
        共 {shown.length} 个{tab === 'branches' ? '分支' : '标签'}{typeof total === 'number' && shown.length !== total ? `（已筛选，全部 ${total} 个）` : ''}
      </div>}
    </section>
  </RepoLayout>
}
