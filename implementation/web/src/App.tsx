import { useState, type FormEvent, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, NavLink, Route, Routes, useNavigate, useParams } from 'react-router-dom'
import {
  ArrowRight, Bell, Check, CheckCircle2, ChevronDown, ChevronRight, CircleAlert,
  Clock3, Code2, Copy, Download, FileCode2, FolderClosed, GitBranch,
  KeyRound, Layers3, LockKeyhole, LogOut, Plus, RefreshCw, Search, ShieldCheck,
  Sparkles, UsersRound,
} from 'lucide-react'
import { api, ApiError, type AccessToken, type JoinRequest, type Meta, type Repository, type User, type Visibility } from './api'
import { IssuesPage, NewIssuePage, IssueDetailPage } from './IssuesPage'
import { CommitsPage, CommitDetailPage } from './CommitsPage'
import { BranchesPage } from './BranchesPage'
import { PullsPage, NewPullPage, PullDetailPage } from './PullsPage'
import { BrowsePage } from './BrowsePage'
import { CollaboratorsPage } from './CollaboratorsPage'
import { DeployKeyPage } from './DeployKeyPage'
import { SSHKeysPage } from './SSHKeysPage'
import { LabelsPage, MilestonesPage } from './RepoMetadataPages'
import { CIRunsPage, CIRunDetailPage } from './CIPage'
import { NotificationsPage, NotificationBell } from './NotificationsPage'
import { Menu, MenuItem, MenuLabel, MenuSeparator, Modal, useCopy, ConfirmDialog, Segmented, Picker, Hint, Checkbox, RadioGroup, Disclosure } from './ui'
import { RepoLayout } from './RepoLayout'

const slugPattern = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])$/

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : '操作失败，请稍后再试。'
}

function formatDate(value?: string) {
  if (!value) return '刚刚'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }).format(date)
}

function repoPath(repo: Repository) {
  return `/${encodeURIComponent(repo.team.slug)}/${encodeURIComponent(repo.slug)}`
}

function Logo({ compact = false }: { compact?: boolean }) {
  return <span className="logo-lockup"><span className="logo-mark"><GitBranch size={20} strokeWidth={2.8} /></span>{!compact && <span>fit<span className="logo-accent">hub</span><span className="logo-dot">.</span></span>}</span>
}

function ErrorMessage({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  return <div className="error-message" role="alert"><CircleAlert size={18} /><span>{errorMessage(error)}</span>{onRetry && <button type="button" className="text-button" onClick={onRetry}>重试</button>}</div>
}

function LoadingBlock({ label = '正在加载…' }: { label?: string }) {
  return <div className="loading-block"><span className="spinner" />{label}</div>
}

function PageHeading({ eyebrow, title, description, action }: { eyebrow: string; title: string; description?: string; action?: ReactNode }) {
  return <div className="page-heading"><div><div className="eyebrow">{eyebrow}</div><h1>{title}</h1>{description && <p>{description}</p>}</div>{action && <div className="page-action">{action}</div>}</div>
}

function EmptyState({ icon, title, description, action }: { icon: ReactNode; title: string; description: string; action?: ReactNode }) {
  return <div className="empty-state"><div className="empty-icon">{icon}</div><h3>{title}</h3><p>{description}</p>{action}</div>
}

function VisibilityBadge({ visibility }: { visibility: Visibility }) {
  const labels = { public: '公开', internal: '内部', private: '私有' }
  return <span className={`visibility-badge visibility-${visibility}`}>{visibility === 'private' ? <LockKeyhole size={12} /> : <span className="visibility-dot" />}{labels[visibility]}</span>
}

function StatusBadge({ status }: { status: JoinRequest['status'] }) {
  const labels = { pending: '待审核', approved: '已通过', rejected: '未通过', cancelled: '已撤回' }
  return <span className={`status-badge status-${status}`}>{status === 'pending' && <Clock3 size={13} />}{status === 'approved' && <Check size={13} />}{labels[status]}</span>
}

function CopyField({ value, label }: { value: string; label?: string }) {
  const { copied, copy } = useCopy()
  const done = copied === value
  return <div className="copy-field">
    {label && <span className="copy-label">{label}</span>}
    <input value={value} readOnly aria-label={label || '可复制内容'} onFocus={event => event.target.select()} />
    <Hint label={done ? '已复制' : '复制'}>
      <button type="button" onClick={() => copy(value)} aria-label={done ? '已复制' : '复制'}>{done ? <Check size={15} /> : <Copy size={15} />}</button>
    </Hint>
  </div>
}

function AppShell({ user, meta, children }: { user: User; meta: Meta; children: ReactNode }) {
  const [switchOpen, setSwitchOpen] = useState(false)
  const logout = useMutation({ mutationFn: api.logout, onSuccess: () => window.location.replace('/') })
  const initials = user.name.trim().slice(0, 1).toUpperCase() || 'U'
  const nav = [
    { to: '/', label: '工作台', end: true },
    { to: '/repos', label: '仓库' },
    { to: '/teams', label: '团队' },
    ...(user.role === 'admin' ? [{ to: '/admin', label: '管理' }] : []),
  ]
  return <div className="app-frame">
    <header className="topnav">
      <Link to="/" className="topnav-logo" aria-label="FitHub 首页"><Logo /></Link>
      <nav className="topnav-links" aria-label="主导航">{nav.map(item => <NavLink key={item.to} to={item.to} end={item.end} className={({ isActive }) => `topnav-link${isActive ? ' active' : ''}`}>{item.label}</NavLink>)}</nav>
      <span className="topnav-spacer" />
      <div className="topnav-right">
        <NotificationBell />
        <Menu label="账号菜单" trigger={<><span className="topnav-avatar">{initials}</span><ChevronDown size={13} /></>}>
          <MenuLabel>{user.name} · {user.email}</MenuLabel>
          <MenuSeparator />
          <MenuItem asChild><Link to="/notifications"><Bell size={15} />通知</Link></MenuItem>
          <MenuItem asChild><Link to="/settings/tokens"><KeyRound size={15} />访问令牌</Link></MenuItem>
          <MenuItem asChild><Link to="/settings/ssh-keys"><GitBranch size={15} />SSH 公钥</Link></MenuItem>
          {user.role === 'admin' && <MenuItem asChild><Link to="/admin"><ShieldCheck size={15} />管理控制台</Link></MenuItem>}
          <MenuSeparator />
          {meta.devAuth && <MenuItem onSelect={() => window.setTimeout(() => setSwitchOpen(true), 0)}><RefreshCw size={15} />切换测试账号</MenuItem>}
          <MenuItem disabled={logout.isPending} onSelect={() => logout.mutate()}><LogOut size={15} />退出登录</MenuItem>
        </Menu>
      </div>
    </header>
    {logout.isError && <div className="shell-error"><ErrorMessage error={logout.error} /></div>}
    <main className="content">{children}</main>
    <SwitchAccountDialog open={switchOpen} onOpenChange={setSwitchOpen} current={user.email} />
  </div>
}

function SwitchAccountDialog({ open, onOpenChange, current }: { open: boolean; onOpenChange: (open: boolean) => void; current: string }) {
  const [email, setEmail] = useState(current)
  const [name, setName] = useState('')
  const switchAccount = useMutation({ mutationFn: () => api.devLogin(email.trim(), name.trim() || email.split('@')[0]), onSuccess: () => window.location.replace('/') })
  const logout = useMutation({ mutationFn: api.logout, onSuccess: () => window.location.replace('/') })
  return <Modal open={open} onOpenChange={onOpenChange} title="切换测试账号"
    description="使用管理员与员工账号检查申请、审核和仓库权限流程。仅限本地开发。"
    footer={<>
      <button type="button" className="text-button" disabled={logout.isPending} onClick={() => logout.mutate()}>退出当前账号</button>
      <span className="ui-dialog-spacer" />
      <button type="button" className="button button-secondary" onClick={() => onOpenChange(false)}>取消</button>
      <button type="button" className="button button-primary" disabled={switchAccount.isPending || !email.trim()} onClick={() => switchAccount.mutate()}>{switchAccount.isPending ? '正在切换…' : '切换'}</button>
    </>}>
    <form onSubmit={event => { event.preventDefault(); switchAccount.mutate() }}>
      <label className="field-label" htmlFor="switch-email">邮箱</label>
      <input id="switch-email" className="field-input" type="email" value={email} onChange={event => setEmail(event.target.value)} required />
      <label className="field-label" htmlFor="switch-name">显示名称</label>
      <input id="switch-name" className="field-input" value={name} onChange={event => setName(event.target.value)} placeholder="留空则用邮箱前缀" />
      <div className="quick-accounts">
        <button type="button" onClick={() => { setEmail('admin@fithub.local'); setName('管理员') }}>管理员</button>
        <button type="button" onClick={() => { setEmail('alice@fithub.local'); setName('Alice') }}>员工 Alice</button>
      </div>
      {switchAccount.isError && <ErrorMessage error={switchAccount.error} />}
    </form>
  </Modal>
}

function LoginPage({ meta }: { meta: Meta }) {
  const [email, setEmail] = useState('alice@fithub.local')
  const [name, setName] = useState('Alice')
  const mutation = useMutation({ mutationFn: () => api.devLogin(email.trim(), name.trim()), onSuccess: () => window.location.replace('/') })
  return <div className="login-page"><div className="login-form-side"><div className="login-card"><div className="login-mobile-logo"><Logo /></div><div className="login-eyebrow">WELCOME TO FITHUB</div><h2>登录工作空间</h2><p className="login-lead">继续使用你的公司账号，进入团队的代码空间。</p>{new URLSearchParams(window.location.search).has('auth_error') && <ErrorMessage error={new Error('Google 登录失败或账号不可用，请重试或联系管理员。')} />}{meta.googleAuth && <a className="button button-primary full-width login-google" href={meta.googleLoginUrl || '/api/v1/auth/google/start'}>使用 Google 登录 <ArrowRight size={17} /></a>}{meta.devAuth && <><div className="dev-login-label"><span />本地开发账号</div><form onSubmit={(event: FormEvent) => { event.preventDefault(); mutation.mutate() }}><label className="field-label" htmlFor="login-email">邮箱地址</label><input id="login-email" className="field-input" type="email" autoComplete="email" value={email} onChange={event => setEmail(event.target.value)} required /><label className="field-label" htmlFor="login-name">显示名称</label><input id="login-name" className="field-input" value={name} onChange={event => setName(event.target.value)} required /><div className="quick-accounts"><button type="button" onClick={() => { setEmail('alice@fithub.local'); setName('Alice') }}>员工体验</button><button type="button" onClick={() => { setEmail('admin@fithub.local'); setName('管理员') }}>管理员体验</button></div>{mutation.isError && <ErrorMessage error={mutation.error} />}<button type="submit" className="button button-primary full-width" disabled={mutation.isPending}> {mutation.isPending ? '正在进入…' : '进入 FitHub'} <ArrowRight size={18} /></button></form></>}{!meta.devAuth && !meta.googleAuth && <div className="info-box"><CircleAlert size={18} />当前实例尚未配置登录方式。请联系管理员。</div>}<div className="login-footnote"><ShieldCheck size={15} />访问权限由团队资格和仓库设置决定</div><Link className="login-public-link" to="/repos">浏览公开仓库 <ArrowRight size={15} /></Link></div></div></div>
}

function TeamDirectory({ user, onboarding = false }: { user: User; onboarding?: boolean }) {
  const queryClient = useQueryClient()
  const teams = useQuery({ queryKey: ['teams'], queryFn: api.teams })
  const requests = useQuery({ queryKey: ['myJoinRequests'], queryFn: api.myJoinRequests })
  const [joiningId, setJoiningId] = useState<string | null>(null)
  const joinMutation = useMutation({ mutationFn: (id: string) => api.joinTeam(id), onSuccess: async () => { setJoiningId(null); await queryClient.invalidateQueries({ queryKey: ['myJoinRequests'] }) }, onError: () => setJoiningId(null) })
  const cancelMutation = useMutation({ mutationFn: (id: string) => api.cancelJoinRequest(id), onSuccess: () => queryClient.invalidateQueries({ queryKey: ['myJoinRequests'] }) })
  const refresh = async () => { await Promise.all([queryClient.invalidateQueries({ queryKey: ['me'] }), queryClient.invalidateQueries({ queryKey: ['myJoinRequests'] }), queryClient.invalidateQueries({ queryKey: ['teams'] })]) }
  const myTeamIds = new Set(user.teams.map(team => team.id))
  const latestRequest = (requests.data || []).reduce<Record<string, JoinRequest>>((map, request) => { if (!map[request.teamId] || new Date(request.createdAt).getTime() > new Date(map[request.teamId].createdAt).getTime()) map[request.teamId] = request; return map }, {})
  return <>
    {onboarding && <div className="welcome-banner"><div className="welcome-icon"><UsersRound size={19} /></div><div><h2>找到你的团队，开始协作。</h2><p>提交申请后，管理员审核通过即可创建和访问授权仓库。</p></div></div>}
    <div className="section-heading"><div><h2>{onboarding ? '可申请的团队' : '探索团队'}</h2><p>团队目录只显示基本信息；加入后才会获得相应访问权限。</p></div><button className="button button-secondary button-small" onClick={refresh}><RefreshCw size={15} />刷新状态</button></div>
    {(teams.isLoading || requests.isLoading) && <LoadingBlock label="正在读取团队目录…" />}
    {teams.isError && <ErrorMessage error={teams.error} onRetry={() => teams.refetch()} />}
    {requests.isError && <ErrorMessage error={requests.error} onRetry={() => requests.refetch()} />}
    {joinMutation.isError && <ErrorMessage error={joinMutation.error} />}
    {cancelMutation.isError && <ErrorMessage error={cancelMutation.error} />}
    {teams.data?.length === 0 && <EmptyState icon={<UsersRound size={28} />} title="暂无可申请的团队" description="管理员创建团队后，它会出现在这里。你可以稍后回来查看。" />}
    {teams.data && teams.data.length > 0 && <div className="team-grid">{teams.data.map((team, index) => {
      const request = latestRequest[team.id]
      const isMember = myTeamIds.has(team.id)
      return <article className="team-card" key={team.id}><div className="team-card-top"><div className={`team-symbol team-symbol-${index % 4}`}>{team.name.slice(0, 1).toUpperCase()}</div>{isMember ? <span className="member-pill"><Check size={13} />已加入</span> : request && <StatusBadge status={request.status} />}</div><h3>{team.name}</h3><div className="team-handle">/{team.slug}</div><p>{team.description || '团队成员的代码、协作与交付空间。'}</p><div className="team-card-footer">{isMember ? <span className="card-muted">你已经是团队成员</span> : request?.status === 'pending' ? <span className="card-muted">申请于 {formatDate(request.createdAt)} 提交</span> : request?.status === 'rejected' ? <span className="card-muted">可重新提交申请</span> : <span className="card-muted">加入后开始协作</span>}{!isMember && request?.status !== 'pending' && <button className="icon-action" onClick={() => { setJoiningId(team.id); joinMutation.mutate(team.id) }} disabled={joinMutation.isPending} aria-label={`申请加入 ${team.name}`}>{joiningId === team.id ? <span className="spinner spinner-small" /> : <ArrowRight size={18} />}</button>}</div></article>
    })}</div>}
    {requests.data && requests.data.length > 0 && <div className="request-history"><div className="section-heading compact"><div><h2>我的申请</h2><p>每个团队的审核结果相互独立。</p></div></div><div className="list-card">{[...requests.data].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt)).map(request => <div className="history-row" key={request.id}><span className="history-icon"><UsersRound size={16} /></span><div><strong>{request.team?.name || teams.data?.find(team => team.id === request.teamId)?.name || '团队申请'}</strong><span>{formatDate(request.createdAt)} 提交</span></div><StatusBadge status={request.status} />{request.status === 'pending' && <button type="button" className="text-button" disabled={cancelMutation.isPending} onClick={() => cancelMutation.mutate(request.id)}>撤回</button>}</div>)}</div></div>}
  </>
}

function HomePage({ user }: { user: User }) {
  const repos = useQuery({ queryKey: ['repos'], queryFn: api.repos, enabled: user.teams.length > 0 })
  if (user.teams.length === 0) return <><PageHeading eyebrow="YOUR WORKSPACE" title={`你好，${user.name.split(' ')[0]}`} description="你的账号已经就绪。加入团队后，就能开始使用 FitHub。" /><TeamDirectory user={user} onboarding /></>
  return <><PageHeading eyebrow="OVERVIEW" title={`你好，${user.name.split(' ')[0]}`} description="从这里继续你的团队工作。" action={<Link className="button button-primary" to="/repos/new"><Plus size={17} />新建仓库</Link>} /><div className="dashboard-hero"><div><h2>让下一次提交，从这里开始。</h2><p>整理好仓库，与团队一起协作，把想法稳稳地推向完成。</p></div><Link to="/repos" className="hero-link">查看所有仓库 <ArrowRight size={16} /></Link></div><div className="dashboard-grid"><div className="main-panel"><div className="section-heading"><div><h2>最近仓库</h2><p>你有权限访问的代码空间。</p></div><Link to="/repos" className="subtle-link">查看全部 <ArrowRight size={15} /></Link></div>{repos.isLoading && <LoadingBlock />}{repos.isError && <ErrorMessage error={repos.error} onRetry={() => repos.refetch()} />}{repos.data?.length === 0 && <EmptyState icon={<Code2 size={28} />} title="还没有仓库" description="创建第一个仓库，让团队代码有一个家。" action={<Link to="/repos/new" className="button button-primary"><Plus size={16} />新建仓库</Link>} />}{repos.data && repos.data.length > 0 && <div className="repo-list">{repos.data.slice(0, 5).map(repo => <RepoRow key={repo.id} repo={repo} />)}</div>}</div><div className="side-panel"><div className="section-heading"><div><h2>我的团队</h2><p>当前参与的工作空间。</p></div></div><div className="membership-list">{user.teams.map((team, index) => <div className="membership-row" key={team.id}><div className={`mini-team-symbol team-symbol-${index % 4}`}>{team.name.slice(0, 1)}</div><div><strong>{team.name}</strong><span>/{team.slug}</span></div><CheckCircle2 size={17} /></div>)}</div><Link className="side-panel-link" to="/teams">探索更多团队 <ArrowRight size={15} /></Link></div></div></>
}

function TeamsPage({ user }: { user: User }) {
  return <><PageHeading eyebrow="PEOPLE & TEAMS" title="团队" description="找到你的伙伴，申请加入新的团队。" /><TeamDirectory user={user} /></>
}

function RepoRow({ repo }: { repo: Repository }) {
  return <Link to={repoPath(repo)} className="repo-row"><div className="repo-row-icon"><Code2 size={21} /></div><div className="repo-row-main"><div className="repo-row-title"><strong>{repo.team?.name || repo.team?.slug} <span>/</span> {repo.name || repo.slug}</strong><VisibilityBadge visibility={repo.visibility} /></div><p>{repo.description || '暂无仓库描述'}</p><div className="repo-row-meta"><GitBranch size={14} />{repo.defaultBranch || 'main'}{repo.updatedAt && <><span className="meta-separator">·</span>创建于 {formatDate(repo.updatedAt)}</>}</div></div><ChevronRight className="repo-chevron" size={18} /></Link>
}

function ReposPage({ user, anonymous = false }: { user: Pick<User, 'teams'>; anonymous?: boolean }) {
  const [search, setSearch] = useState('')
  const repos = useQuery({ queryKey: ['repos'], queryFn: api.repos })
  const filtered = repos.data?.filter(repo => `${repo.team?.name} ${repo.team?.slug} ${repo.name} ${repo.slug}`.toLowerCase().includes(search.toLowerCase()))
  return <>
    <PageHeading eyebrow="SOURCE CONTROL" title="仓库" description="浏览你有权限访问的代码空间。" action={user.teams.length > 0 && <Link to="/repos/new" className="button button-primary"><Plus size={17} />新建仓库</Link>} />
    {user.teams.length === 0 && <div className="info-box"><UsersRound size={17} />{anonymous ? '公开仓库无需登录即可浏览；登录后可以申请加入团队。' : '你尚未加入团队，可以先浏览公开仓库；获批加入团队后可创建仓库并访问授权内容。'}<Link to={anonymous ? '/' : '/teams'} className="subtle-link">{anonymous ? '登录' : '选择团队'}</Link></div>}
    <div className="panel">
      <div className="panel-toolbar"><div className="search-box"><Search size={18} /><input value={search} onChange={event => setSearch(event.target.value)} placeholder="搜索仓库或团队…" aria-label="搜索仓库或团队" /></div><span className="result-count">{filtered?.length ?? 0} 个仓库</span></div>
      {repos.isLoading && <LoadingBlock label="正在加载仓库…" />}
      {repos.isError && <ErrorMessage error={repos.error} onRetry={() => repos.refetch()} />}
      {repos.data?.length === 0 && <EmptyState icon={<Code2 size={28} />} title="还没有可见仓库" description={user.teams.length ? '创建第一个仓库，把代码放进来。' : '目前没有公开仓库。'} action={user.teams.length ? <Link to="/repos/new" className="button button-primary"><Plus size={16} />新建仓库</Link> : <Link to={anonymous ? '/' : '/teams'} className="button button-primary">{anonymous ? '登录 FitHub' : '选择团队'}</Link>} />}
      {filtered?.length === 0 && repos.data && repos.data.length > 0 && <EmptyState icon={<Search size={26} />} title="没有找到匹配的仓库" description="试试团队名或其他关键词。" />}
      {filtered && filtered.length > 0 && <div className="repo-list">{filtered.map(repo => <RepoRow key={repo.id} repo={repo} />)}</div>}
    </div>
  </>
}

function NewRepoPage({ user }: { user: User }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [teamId, setTeamId] = useState(user.teams[0]?.id || '')
  const [slug, setSlug] = useState('')
  const [visibility, setVisibility] = useState<Visibility>('internal')
  const [grantTeamWrite, setGrantTeamWrite] = useState(false)
  const mutation = useMutation({
    mutationFn: () => api.createRepo(teamId, slug.trim().toLowerCase(), visibility, grantTeamWrite),
    onSuccess: async result => {
      const team = user.teams.find(item => item.id === teamId)
      await queryClient.invalidateQueries({ queryKey: ['repos'] })
      navigate(result.repo?.team?.slug && result.repo?.slug ? repoPath(result.repo) : `/${team?.slug}/${slug.trim().toLowerCase()}`)
    },
  })
  const validSlug = slugPattern.test(slug.trim().toLowerCase())
  if (user.teams.length === 0) return <><PageHeading eyebrow="NEW REPOSITORY" title="新建仓库" /><EmptyState icon={<UsersRound size={28} />} title="还没有可用团队" description="先申请并获批加入团队，再创建仓库。" action={<Link to="/teams" className="button button-primary">查看团队</Link>} /></>
  return <>
    <PageHeading eyebrow="NEW REPOSITORY" title="新建仓库" description="为项目选一个清晰的名字，设置合适的可见范围。" />
    <div className="form-layout">
      <form className="form-panel" onSubmit={event => { event.preventDefault(); if (validSlug) mutation.mutate() }}>
        <div className="form-section">
          <div className="form-section-title"><span className="step-number">01</span><div><h2>仓库归属与名称</h2><p>所有仓库都属于一个团队。</p></div></div>
          <div className="field-grid">
            <div><label className="field-label" htmlFor="repo-team">所属团队</label><Picker ariaLabel="所属团队" value={teamId} onChange={setTeamId} options={user.teams.map(team => ({ value: team.id, label: team.name }))} /></div>
            <div><label className="field-label" htmlFor="repo-slug">仓库名称</label><input id="repo-slug" className="field-input" value={slug} onChange={event => setSlug(event.target.value)} placeholder="例如 product-api" autoCapitalize="none" autoComplete="off" required /><span className="field-hint">使用小写字母、数字与连字符，长度 2–63 个字符。</span>{slug && !validSlug && <span className="field-error">名称格式不正确，请检查开头、结尾与字符。</span>}</div>
          </div>
        </div>
        <div className="form-section">
          <div className="form-section-title"><span className="step-number">02</span><div><h2>可见范围</h2><p>公开仓库也会公开历史、Issue 和 PR。</p></div></div>
          <RadioGroup name="visibility" ariaLabel="可见范围" value={visibility} onValueChange={value => setVisibility(value as Visibility)}
            options={[
              { value: 'internal', label: '内部', description: '有效公司员工可读取，写入仍需授权。推荐默认选项。', icon: <UsersRound size={17} /> },
              { value: 'private', label: '私有', description: '仅创建者及明确授权的协作者可以访问。', icon: <LockKeyhole size={17} /> },
              { value: 'public', label: '公开', description: '任何人都能读取代码、历史、Issue 与 PR。', icon: <Layers3 size={17} /> },
            ]} />
        </div>
        <div className="form-section">
          <div className="form-section-title"><span className="step-number">03</span><div><h2>团队写入权限</h2><p>创建者始终拥有仓库管理权限。</p></div></div>
          <Checkbox id="grant-team-write" checked={grantTeamWrite} onCheckedChange={setGrantTeamWrite}
            label="授予所属团队写入权限" description="团队成员可以直接推送和合并代码。" />
        </div>
        {mutation.isError && <ErrorMessage error={mutation.error} />}
        <div className="form-actions"><Link to="/repos" className="button button-secondary">取消</Link><button type="submit" className="button button-primary" disabled={!validSlug || !teamId || mutation.isPending}>{mutation.isPending ? '正在创建…' : '创建仓库'} <ArrowRight size={17} /></button></div>
      </form>
      <aside className="form-aside"><div className="aside-icon"><Sparkles size={22} /></div><h3>下一步做什么？</h3><p>创建完成后，仓库首页会提供 HTTPS 地址。你可以在本地 clone，再推送第一个提交。</p><div className="aside-divider" /><div className="aside-tip"><ShieldCheck size={17} /><span>有写入权限的成员可以直接推送或合并代码，也可以选择使用 PR 协作。</span></div></aside>
    </div>
  </>
}

function AdminPage({ user }: { user: User }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [processingId, setProcessingId] = useState<string | null>(null)
  const teams = useQuery({ queryKey: ['teams'], queryFn: api.teams, enabled: user.role === 'admin' })
  const requests = useQuery({ queryKey: ['adminJoinRequests'], queryFn: api.adminJoinRequests, enabled: user.role === 'admin' })
  const create = useMutation({ mutationFn: () => api.createTeam(name.trim(), slug.trim().toLowerCase()), onSuccess: async () => { setName(''); setSlug(''); await queryClient.invalidateQueries({ queryKey: ['teams'] }) } })
  const decide = useMutation({ mutationFn: ({ id, decision }: { id: string; decision: 'approved' | 'rejected' }) => api.decideJoinRequest(id, decision), onSuccess: async () => { setProcessingId(null); await queryClient.invalidateQueries({ queryKey: ['adminJoinRequests'] }) }, onError: () => setProcessingId(null) })
  if (user.role !== 'admin') return <><PageHeading eyebrow="ADMINISTRATION" title="管理控制台" /><EmptyState icon={<LockKeyhole size={28} />} title="没有访问权限" description="只有平台管理员可以管理团队和审核申请。" /></>
  const pending = requests.data?.filter(item => item.status === 'pending') || []
  return <><PageHeading eyebrow="ADMINISTRATION" title="管理控制台" description="创建团队，处理员工的加入申请。" /><div className="admin-summary"><div><span>待审核申请</span><strong>{requests.isLoading ? '—' : pending.length}</strong><small>等待你的决定</small></div><div><span>已创建团队</span><strong>{teams.isLoading ? '—' : teams.data?.length ?? 0}</strong><small>当前可申请</small></div><div className="summary-note"><ShieldCheck size={22} /><span>批准申请后，成员才会获得有效团队资格。</span></div></div><div className="admin-grid"><section className="panel admin-review"><div className="panel-header"><div><h2>加入申请</h2><p>审核前，请确认申请人和目标团队。</p></div><span className="count-pill">{pending.length} 待处理</span></div>{requests.isLoading && <LoadingBlock />}{requests.isError && <ErrorMessage error={requests.error} onRetry={() => requests.refetch()} />}{decide.isError && <ErrorMessage error={decide.error} />}{requests.data && pending.length === 0 && <EmptyState icon={<Bell size={27} />} title="申请已处理完毕" description="新的加入申请会显示在这里。" />}{pending.map(request => <div className="review-row" key={request.id}><div className="review-avatar">{(request.user?.name || request.user?.email || '?').slice(0, 1).toUpperCase()}</div><div className="review-info"><strong>{request.user?.name || '未知用户'}</strong><span>{request.user?.email || ''}</span><small>申请加入 <b>{request.team?.name || teams.data?.find(team => team.id === request.teamId)?.name || '团队'}</b> · {formatDate(request.createdAt)}</small></div><div className="review-actions"><button className="button button-secondary button-small" onClick={() => { setProcessingId(request.id); decide.mutate({ id: request.id, decision: 'rejected' }) }} disabled={decide.isPending}>拒绝</button><button className="button button-primary button-small" onClick={() => { setProcessingId(request.id); decide.mutate({ id: request.id, decision: 'approved' }) }} disabled={decide.isPending}>{processingId === request.id && decide.isPending ? '处理中…' : '批准'}</button></div></div>)}</section><section className="panel admin-create"><div className="panel-header"><div><h2>创建团队</h2><p>新团队会立即出现在员工申请目录。</p></div><div className="panel-header-icon"><Plus size={18} /></div></div><form onSubmit={event => { event.preventDefault(); if (slugPattern.test(slug.trim().toLowerCase())) create.mutate() }}><label className="field-label" htmlFor="team-name">团队名称</label><input id="team-name" className="field-input" value={name} onChange={event => setName(event.target.value)} placeholder="例如 产品工程" required maxLength={80} /><label className="field-label" htmlFor="team-slug">团队标识</label><input id="team-slug" className="field-input" value={slug} onChange={event => setSlug(event.target.value)} placeholder="例如 product-eng" required /><span className="field-hint">用于仓库路径 /团队标识/仓库名</span>{slug && !slugPattern.test(slug.trim().toLowerCase()) && <span className="field-error">只可使用小写字母、数字与连字符。</span>}{create.isError && <ErrorMessage error={create.error} />}{create.isSuccess && <div className="success-message"><CheckCircle2 size={16} />团队已创建，员工现在可以申请加入。</div>}<button type="submit" className="button button-primary full-width" disabled={!name.trim() || !slugPattern.test(slug.trim().toLowerCase()) || create.isPending}>{create.isPending ? '正在创建…' : '创建团队'}<ArrowRight size={17} /></button></form></section></div>{teams.data && teams.data.length > 0 && <section className="managed-teams"><div className="section-heading"><div><h2>团队目录</h2><p>所有已创建的团队。</p></div></div><div className="managed-team-grid">{teams.data.map((team, index) => <div className="managed-team" key={team.id}><div className={`mini-team-symbol team-symbol-${index % 4}`}>{team.name.slice(0, 1)}</div><div><strong>{team.name}</strong><span>/{team.slug}</span></div></div>)}</div></section>}</>
}

function TokensPage({ user }: { user: User }) {
  const queryClient = useQueryClient()
  const [scope, setScope] = useState<'user' | 'repository'>('repository')
  const [level, setLevel] = useState<'read' | 'write' | 'admin'>('write')
  const [repoId, setRepoId] = useState('')
  const [created, setCreated] = useState<string | null>(null)
  const tokens = useQuery({ queryKey: ['tokens'], queryFn: api.tokens })
  const repos = useQuery({ queryKey: ['repos'], queryFn: api.repos, enabled: user.teams.length > 0 })
  const selectedRepoId = repoId || repos.data?.[0]?.id || ''
  const create = useMutation({
    mutationFn: () => api.createToken(scope, level, scope === 'repository' ? selectedRepoId : undefined),
    onSuccess: async result => { setCreated(result.token); await queryClient.invalidateQueries({ queryKey: ['tokens'] }) },
  })
  const revoke = useMutation({
    mutationFn: (id: string) => api.revokeToken(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['tokens'] }),
  })
  const activeTokens = (tokens.data || []).filter(token => !token.revokedAt)
  return <>
    <PageHeading eyebrow="PERSONAL SECURITY" title="访问令牌" description="管理 Git HTTPS 和 API 使用的个人凭据。令牌权限不会超过你当前的实际权限。" />
    <div className="tokens-grid">
      <section className="panel token-create-card">
        <div className="panel-header"><div><h2>创建新令牌</h2><p>明文只显示一次，请创建后立即保存。</p></div><KeyRound size={19} className="panel-muted-icon" /></div>
        <div className="token-create-body">
          {created ? <><div className="success-message"><CheckCircle2 size={17} />令牌已创建。现在复制并安全保存；离开此页后将无法再次查看。</div><CopyField value={created} label="新令牌" /><button type="button" className="button button-secondary full-width" onClick={() => setCreated(null)}>完成</button></> : <>
            <label className="field-label" htmlFor="token-scope">适用范围</label><Picker ariaLabel="适用范围" value={scope} onChange={value => setScope(value as typeof scope)} options={[{ value: 'repository', label: '单个仓库' }, { value: 'user', label: '我有权访问的仓库' }]} />
            {scope === 'repository' && <><label className="field-label" htmlFor="token-repo">仓库</label><Picker ariaLabel="仓库" value={selectedRepoId} onChange={setRepoId} disabled={!repos.data?.length} options={(repos.data || []).map(repo => ({ value: repo.id, label: `${repo.team.slug}/${repo.slug}` }))} />{repos.isError && <ErrorMessage error={repos.error} />}{repos.data?.length === 0 && <span className="field-hint">先创建或加入一个可访问的仓库。</span>}</>}
            <label className="field-label" htmlFor="token-level">最高权限级别</label><Picker ariaLabel="最高权限级别" value={level} onChange={value => setLevel(value as typeof level)} options={[{ value: 'read', label: '只读' }, { value: 'write', label: '读写' }, { value: 'admin', label: '管理' }]} />
            <div className="token-security-note"><ShieldCheck size={16} /><span>令牌会按当前成员资格、仓库 ACL 和分支规则重新检查权限。</span></div>
            {create.isError && <ErrorMessage error={create.error} />}
            <button type="button" className="button button-primary full-width" onClick={() => create.mutate()} disabled={create.isPending || (scope === 'repository' && !selectedRepoId)}>{create.isPending ? '正在创建…' : '创建令牌'}<ArrowRight size={16} /></button>
          </>}
        </div>
      </section>
      <section className="panel token-list-card"><div className="panel-header"><div><h2>现有令牌</h2><p>不用的令牌可以随时撤销。</p></div><span className="count-pill">{activeTokens.length} 个有效</span></div>{tokens.isLoading && <LoadingBlock />}{tokens.isError && <ErrorMessage error={tokens.error} onRetry={() => tokens.refetch()} />}{tokens.data && activeTokens.length === 0 && <EmptyState icon={<KeyRound size={26} />} title="还没有访问令牌" description="创建后，可将它用于 Git HTTPS 或 API。" />}{activeTokens.map(token => <TokenRow key={token.id} token={token} repo={repos.data?.find(repo => repo.id === token.repoId)} busy={revoke.isPending} onRevoke={() => revoke.mutate(token.id)} />)}{revoke.isError && <ErrorMessage error={revoke.error} />}</section>
    </div>
  </>
}

function TokenRow({ token, repo, onRevoke, busy }: { token: AccessToken; repo?: Repository; onRevoke: () => void; busy: boolean }) {
  const [confirming, setConfirming] = useState(false)
  const levelName = { read: '只读', write: '读写', admin: '管理' }[token.level]
  const name = token.scope === 'repository' ? (repo ? `${repo.team.slug}/${repo.slug}` : token.repoName || '仓库令牌') : '用户令牌'
  return <div className="token-row">
    <div className="token-row-icon"><KeyRound size={17} /></div>
    <div className="token-row-info"><strong>{name}</strong><span>{token.prefix ? `${token.prefix}••••` : '访问令牌'} · {levelName}{token.expiresAt && ` · 到期 ${formatDate(token.expiresAt)}`}</span></div>
    <button className="text-button" onClick={() => setConfirming(true)}>撤销</button>
    <ConfirmDialog open={confirming} onOpenChange={setConfirming}
      title="撤销这个令牌？"
      description={`${name} 将立即失效，正在使用它的 Git 客户端和脚本会开始失败。这一步无法撤销。`}
      confirmLabel="撤销令牌" busy={busy} onConfirm={onRevoke} />
  </div>
}

function TokenPanel({ repo }: { repo: Repository }) {
  const queryClient = useQueryClient()
  const [level, setLevel] = useState<'read' | 'write' | 'admin'>('write')
  const [token, setToken] = useState<string | null>(null)
  const mutation = useMutation({ mutationFn: () => api.createToken('repository', level, repo.id), onSuccess: async result => { setToken(result.token); await queryClient.invalidateQueries({ queryKey: ['tokens'] }) } })
  return <Disclosure title="Git HTTPS 凭据" description="为这个仓库创建访问令牌" icon={<KeyRound size={16} />}>
    {token ? <>
      <div className="success-message"><CheckCircle2 size={16} />令牌已创建。请现在复制并安全保存；关闭后将无法再次查看。</div>
      <CopyField value={token} label="访问令牌" />
      <button type="button" className="text-button" onClick={() => setToken(null)}>完成</button>
    </> : <>
      <p className="ui-disclosure-hint">Git HTTPS 推拉需要令牌。最终权限仍受你的当前仓库权限限制。</p>
      <div className="token-form-row">
        <Picker ariaLabel="令牌权限级别" value={level} onChange={value => setLevel(value as typeof level)} options={[{ value: 'read', label: '只读' }, { value: 'write', label: '读写' }, { value: 'admin', label: '管理' }]} />
        <button type="button" className="button button-primary button-small" onClick={() => mutation.mutate()} disabled={mutation.isPending}>{mutation.isPending ? '创建中…' : '创建令牌'}</button>
      </div>
      {mutation.isError && <ErrorMessage error={mutation.error} />}
    </>}
  </Disclosure>
}

function RepoDetailPage({ canCreateToken = false }: { canCreateToken?: boolean }) {
  const { team, repo } = useParams()
  const result = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team!, repo!), enabled: Boolean(team && repo) })
  const [cloneMode, setCloneMode] = useState<'https' | 'ssh'>('https')
  if (result.isLoading) return <LoadingBlock label="正在打开仓库…" />
  if (result.isError) return <div className="detail-error"><EmptyState icon={<CircleAlert size={28} />} title={result.error instanceof ApiError && result.error.status === 404 ? '找不到这个仓库' : '仓库暂时无法打开'} description={errorMessage(result.error)} action={<Link to="/repos" className="button button-secondary">返回仓库列表</Link>} /></div>
  if (!result.data) return null
  const item = result.data
  const cloneUrl = cloneMode === 'https' ? item.cloneUrl : item.sshUrl
  // The overview is reached from the repository name rather than a tab, so the
  // bar highlights "代码" — the closest thing to "you are looking at the
  // repository itself" without inventing a tab GitHub does not have.
  return <RepoLayout section="code" repo={item} wide><div className="repo-detail-grid"><div className="repo-primary"><section className="panel clone-panel"><div className="panel-header"><div><h2>获取代码</h2><p>选择协议，复制仓库地址到本地。</p></div><GitBranch size={20} className="panel-muted-icon" /></div><Segmented ariaLabel="克隆协议" value={cloneMode} onChange={setCloneMode} options={[{ value: 'https', label: 'HTTPS' }, { value: 'ssh', label: 'SSH' }]} />{cloneUrl ? <CopyField value={cloneUrl} label={`${cloneMode.toUpperCase()} clone 地址`} /> : <div className="info-box"><CircleAlert size={17} />当前实例还没有配置 {cloneMode.toUpperCase()} 地址。</div>}<div className="clone-hint"><ShieldCheck size={16} />{cloneMode === 'https' ? (item.visibility === 'public' ? '公开仓库可匿名读取；写入需要个人令牌。' : '使用个人令牌作为 Git HTTPS 凭据。') : '使用已登记的 SSH Key 进行身份验证。'}</div>{!item.empty && <div className="clone-downloads"><span className="clone-downloads-label">打包下载</span><a className="button button-secondary button-small" href={api.archiveUrl(team!, repo!, item.defaultBranch || 'main', 'zip')} download><Download size={15} />ZIP</a><a className="button button-secondary button-small" href={api.archiveUrl(team!, repo!, item.defaultBranch || 'main', 'tar.gz')} download><Download size={15} />tar.gz</a></div>}</section>{item.empty ? <section className="panel getting-started"><div className="empty-repo-head"><div className="empty-repo-icon"><FileCode2 size={23} /></div><div><h2>这个仓库还是空的</h2><p>从本地推送第一份代码，或初始化一个 README。</p></div></div>{item.canWrite ? <><div className="command-section"><span>从本地已有项目推送</span><pre>{`git remote add origin ${item.cloneUrl || '<仓库地址>'}\ngit branch -M ${item.defaultBranch || 'main'}\ngit push -u origin ${item.defaultBranch || 'main'}`}</pre></div><div className="command-section"><span>创建一个新项目</span><pre>{`git clone ${item.cloneUrl || '<仓库地址>'}\ncd ${item.slug}\necho "# ${item.name || item.slug}" > README.md\ngit add README.md\ngit commit -m "Initial commit"\ngit push -u origin ${item.defaultBranch || 'main'}`}</pre></div></> : <div className="info-box">这个仓库还没有提交。获得写入权限后才能推送第一份代码。</div>}</section> : <><section className="panel files-panel"><div className="files-toolbar"><GitBranch size={16} /><strong>{item.defaultBranch || 'main'}</strong><span>仓库文件</span></div>{item.files?.length ? item.files.map(file => { const path = typeof file === 'string' ? file : file.path; const name = typeof file === 'string' ? file : file.name; const directory = typeof file === 'string' ? file.endsWith('/') : file.type === 'directory'; return <div className="file-row" key={path}>{directory ? <FolderClosed size={17} /> : <FileCode2 size={17} />}<span>{name}</span></div> }) : <div className="files-placeholder">文件列表将在仓库浏览功能接入后显示。</div>}</section>{item.readme && <section className="panel readme-panel"><div className="readme-head"><FileCode2 size={17} /> README.md</div><pre>{item.readme}</pre></section>}</>}</div><aside className="repo-aside"><section className="panel about-panel"><div className="panel-header"><div><h2>关于仓库</h2><p>{item.description || '暂无描述'}</p></div></div><div className="about-fact"><span>所属团队</span><strong>{item.team?.name || team}</strong></div><div className="about-fact"><span>可见范围</span><VisibilityBadge visibility={item.visibility} /></div><div className="about-fact"><span>默认分支</span><strong>{item.defaultBranch || 'main'}</strong></div></section>{canCreateToken && <TokenPanel repo={item} />}<div className="repo-aside-note"><Sparkles size={18} /><p>把改动放在开发分支，通过 PR 进入默认分支；合并需要另一位有写入权限的员工批准。</p></div></aside></div></RepoLayout>
}

function PublicShell({ children }: { children: ReactNode }) {
  return <div className="public-shell"><header className="public-header"><Link to="/repos" aria-label="FitHub 公开仓库"><Logo /></Link><Link to="/" className="button button-secondary button-small">登录 FitHub</Link></header><main className="content">{children}</main></div>
}

export default function App() {
  const meta = useQuery({ queryKey: ['meta'], queryFn: api.meta, retry: 1, staleTime: 60_000 })
  const me = useQuery({ queryKey: ['me'], queryFn: api.me, retry: (count, error) => !(error instanceof ApiError && error.status === 401) && count < 1 })
  if (me.isLoading || meta.isLoading) return <div className="boot-screen"><Logo /><LoadingBlock label="正在连接工作空间…" /></div>
  if (me.isError) return <div className="boot-screen"><Logo /><ErrorMessage error={me.error} onRetry={() => me.refetch()} /></div>
  if (meta.isError) return <div className="boot-screen"><Logo /><ErrorMessage error={meta.error} onRetry={() => meta.refetch()} /></div>
  if (!me.data) return <Routes>
    <Route path="/" element={<LoginPage meta={meta.data!} />} />
    <Route path="/repos" element={<PublicShell><ReposPage user={{ teams: [] }} anonymous /></PublicShell>} />
    <Route path="/:team/:repo" element={<PublicShell><RepoDetailPage /></PublicShell>} />
    <Route path="/:team/:repo/tree" element={<PublicShell><BrowsePage /></PublicShell>} />
    <Route path="/:team/:repo/branches" element={<PublicShell><BranchesPage /></PublicShell>} />
    <Route path="/:team/:repo/blob" element={<PublicShell><BrowsePage /></PublicShell>} />
    <Route path="/:team/:repo/commits" element={<PublicShell><CommitsPage /></PublicShell>} />
    <Route path="/:team/:repo/commits/:sha" element={<PublicShell><CommitDetailPage /></PublicShell>} />
    <Route path="/:team/:repo/issues" element={<PublicShell><IssuesPage /></PublicShell>} />
    <Route path="/:team/:repo/issues/:number" element={<PublicShell><IssueDetailPage /></PublicShell>} />
    <Route path="/:team/:repo/labels" element={<PublicShell><LabelsPage /></PublicShell>} />
    <Route path="/:team/:repo/milestones" element={<PublicShell><MilestonesPage /></PublicShell>} />
    <Route path="/:team/:repo/pulls" element={<PublicShell><PullsPage /></PublicShell>} />
    <Route path="/:team/:repo/pulls/:number" element={<PublicShell><PullDetailPage /></PublicShell>} />
    <Route path="/:team/:repo/actions" element={<PublicShell><CIRunsPage /></PublicShell>} />
    <Route path="/:team/:repo/actions/:number" element={<PublicShell><CIRunDetailPage /></PublicShell>} />
    <Route path="*" element={<LoginPage meta={meta.data!} />} />
  </Routes>
  return <AppShell user={me.data} meta={meta.data!}><Routes><Route path="/" element={<HomePage user={me.data} />} /><Route path="/teams" element={<TeamsPage user={me.data} />} /><Route path="/repos" element={<ReposPage user={me.data} />} /><Route path="/repos/new" element={<NewRepoPage user={me.data} />} /><Route path="/admin" element={<AdminPage user={me.data} />} /><Route path="/notifications" element={<NotificationsPage />} /><Route path="/settings/tokens" element={<TokensPage user={me.data} />} /><Route path="/settings/ssh-keys" element={<SSHKeysPage sshConfigured={Boolean(meta.data?.sshAuth)} />} /><Route path="/:team/:repo" element={<RepoDetailPage canCreateToken />} /><Route path="/:team/:repo/settings/collaborators" element={<CollaboratorsPage />} /><Route path="/:team/:repo/settings/deploy" element={<DeployKeyPage />} /><Route path="/:team/:repo/tree" element={<BrowsePage />} /><Route path="/:team/:repo/branches" element={<BranchesPage />} /><Route path="/:team/:repo/blob" element={<BrowsePage />} /><Route path="/:team/:repo/commits" element={<CommitsPage />} /><Route path="/:team/:repo/commits/:sha" element={<CommitDetailPage />} /><Route path="/:team/:repo/issues" element={<IssuesPage />} /><Route path="/:team/:repo/issues/new" element={<NewIssuePage />} /><Route path="/:team/:repo/issues/:number" element={<IssueDetailPage />} /><Route path="/:team/:repo/labels" element={<LabelsPage />} /><Route path="/:team/:repo/milestones" element={<MilestonesPage />} /><Route path="/:team/:repo/pulls" element={<PullsPage />} /><Route path="/:team/:repo/pulls/new" element={<NewPullPage />} /><Route path="/:team/:repo/pulls/:number" element={<PullDetailPage />} /><Route path="/:team/:repo/actions" element={<CIRunsPage />} /><Route path="/:team/:repo/actions/:number" element={<CIRunDetailPage />} /><Route path="*" element={<EmptyState icon={<CircleAlert size={28} />} title="页面不存在" description="这个地址没有对应页面。" action={<Link className="button button-primary" to="/">返回工作台</Link>} />} /></Routes></AppShell>
}
