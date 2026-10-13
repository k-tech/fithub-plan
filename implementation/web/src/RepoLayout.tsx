import type { ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { BookOpen, Code2, GitBranch, GitPullRequest, GitCommitHorizontal, LockKeyhole, PlayCircle, Settings, Tag, UsersRound } from 'lucide-react'
import { api, ApiError, type Repository, type Visibility } from './api'
import { Picker } from './ui'
import './repo-layout.css'

// One header for every repository page.
//
// Every repository tab used to draw its own heading, which is why the page
// title changed when you switched tabs — "提交历史", "Issue", "PR" each became
// the h1. On GitHub and Gitea the repository name is the constant: it is the
// thing you need to know you are in the right place, and the tab bar below it
// is the only part that moves. This component owns both so the title cannot
// drift again, and the pages below only render their own toolbar and content.
export function repoBase(team: string, repo: string) {
  return `/${encodeURIComponent(team)}/${encodeURIComponent(repo)}`
}

export function VisibilityLabel({ visibility }: { visibility: Visibility }) {
  const labels: Record<Visibility, string> = { public: '公开', internal: '内部', private: '私有' }
  return <span className={`repo-visibility repo-visibility-${visibility}`}>{visibility === 'private' && <LockKeyhole size={11} />}{labels[visibility]}</span>
}

// A tab is a link plus the tab it represents. The set is fixed so that adding a
// page forces a decision about where it belongs in the bar.
function Tab({ to, active, icon, label }: { to: string; active: boolean; icon: ReactNode; label: string }) {
  return <Link to={to} className={`repo-tab${active ? ' active' : ''}`}>{icon}{label}</Link>
}

export interface RepoLayoutProps {
  // Which tab is current. Passed explicitly rather than inferred from the URL so
  // a nested route (a single issue, a single run) still highlights its section.
  section: 'code' | 'commits' | 'issues' | 'pulls' | 'actions' | 'labels' | 'milestones' | 'settings'
  // Toolbar row rendered under the tabs: branch picker, search, or the
  // section's primary action. Optional because some pages have nothing to put
  // there.
  toolbar?: ReactNode
  children: ReactNode
  // The page may already hold the repository from its own query; passing it in
  // avoids a second request and keeps the header from flickering.
  repo?: Repository
  // The ref being browsed, shown next to the branch picker in the code tab.
  activeRef?: string
  // Width hint: diffs and file trees want the full page, prose wants a column.
  wide?: boolean
}

export function RepoLayout({ section, toolbar, children, repo: provided, wide = false }: RepoLayoutProps) {
  const { team = '', repo: repoSlug = '' } = useParams()
  const query = useQuery({
    queryKey: ['repo', team, repoSlug],
    queryFn: () => api.repo(team, repoSlug),
    enabled: Boolean(team && repoSlug),
    // The header should not refetch on every tab switch; the repository's
    // visibility and default branch change rarely.
    staleTime: 30_000,
  })
  const repo = provided ?? query.data
  const base = repoBase(team, repoSlug)
  const defaultRef = repo?.defaultBranch || 'main'
  const tree = `${base}/tree?ref=${encodeURIComponent(defaultRef)}`

  if (!repo) {
    // The header is meaningless without the repository, so a load or a 404 is
    // reported in its own terms rather than as a bare heading.
    if (query.isLoading) return <div className="repo-layout-loading">正在打开仓库…</div>
    if (query.isError) {
      const missing = query.error instanceof ApiError && query.error.status === 404
      return <div className="repo-layout-missing">
        <h1>{missing ? '找不到这个仓库' : '仓库暂时无法打开'}</h1>
        <p>{missing ? '它可能已被删除，或者你没有查看权限。' : '请稍后重试。'}</p>
        <Link className="button button-secondary" to="/repos">返回仓库列表</Link>
      </div>
    }
    return null
  }

  const canAdmin = Boolean(repo.canAdmin)
  return <div className={`repo-layout${wide ? ' repo-layout-wide' : ''}`}>
    <div className="repo-head">
      <div className="repo-head-title">
        <span className="repo-head-icon"><BookOpen size={17} /></span>
        <h1><Link to={base}>{repo.team?.slug || team}</Link><span className="repo-head-slash">/</span><Link to={base} className="repo-head-name">{repo.name || repo.slug}</Link></h1>
        <VisibilityLabel visibility={repo.visibility} />
      </div>
      {repo.description && <p className="repo-head-description">{repo.description}</p>}
    </div>

    <nav className="repo-tabbar" aria-label="仓库导航">
      <Tab to={tree} active={section === 'code'} icon={<Code2 size={15} />} label="代码" />
      <Tab to={`${base}/commits`} active={section === 'commits'} icon={<GitCommitHorizontal size={15} />} label="提交" />
      <Tab to={`${base}/issues`} active={section === 'issues'} icon={<GitPullRequest size={15} />} label="Issue" />
      <Tab to={`${base}/pulls`} active={section === 'pulls'} icon={<GitPullRequest size={15} />} label="PR" />
      <Tab to={`${base}/actions`} active={section === 'actions'} icon={<PlayCircle size={15} />} label="自动检查" />
      {canAdmin && <Tab to={`${base}/labels`} active={section === 'labels'} icon={<Tag size={15} />} label="标签" />}
      {canAdmin && <Tab to={`${base}/settings/deploy`} active={section === 'settings'} icon={<Settings size={15} />} label="设置" />}
    </nav>

    {toolbar && <div className="repo-toolbar">{toolbar}</div>}
    <div className="repo-body">{children}</div>
  </div>
}

// The ref picker shown on the code and history tabs. It is a compact control in
// the toolbar rather than a large select beside the title: switching refs is a
// frequent action, so it belongs next to the content it filters.
//
// Tags are offered as well as branches, because browsing a release is the same
// act as browsing a branch. Leaving them out made the control show no value at
// all while a tag's contents were on screen.
export function BranchPicker({ branches, tags = [], selected, onChange }: {
  branches: { name: string; isDefault: boolean }[]
  tags?: { name: string }[]
  selected: string
  onChange: (name: string) => void
}) {
  const options = [
    ...branches.map(branch => ({ value: branch.name, label: branch.name, hint: branch.isDefault ? '默认' : undefined })),
    ...tags.map(tag => ({ value: tag.name, label: tag.name, hint: '标签' })),
  ]
  // While the tag list is still loading, or when it is truncated, the current ref
  // must still be displayed rather than appearing unset.
  if (selected && !options.some(option => option.value === selected)) {
    options.unshift({ value: selected, label: selected, hint: '当前' })
  }
  return <Picker ariaLabel="选择分支或标签" value={selected} onChange={onChange} searchable icon={<GitBranch size={14} />} options={options} />
}

// Re-exported so the settings pages can show the same collaborator affordance
// in their own headers without importing the icon library separately.
export { UsersRound }
