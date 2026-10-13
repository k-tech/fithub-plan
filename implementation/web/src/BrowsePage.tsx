import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useLocation, useParams, useSearchParams } from 'react-router-dom'
import { AlertTriangle, ChevronRight, Download, Files, FileCode2, FolderClosed, RotateCw } from 'lucide-react'
import { api, ApiError, type BrowseEntry } from './api'
import { RepoLayout, BranchPicker } from './RepoLayout'
import { Hint } from './ui'
import './browse.css'

function readableSize(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

function routeFor(team: string, repo: string, kind: 'tree' | 'blob', ref: string, path = '') {
  const query = new URLSearchParams({ ref, path })
  return `/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/${kind}?${query.toString()}`
}

function BrowseError({ error, retry }: { error: unknown; retry: () => void }) {
  const missing = error instanceof ApiError && error.status === 404
  return <div className="browse-error"><AlertTriangle size={19} /><div><strong>{missing ? '找不到这个分支或路径' : '暂时无法读取仓库内容'}</strong><span>{error instanceof Error ? error.message : '请求失败'}</span></div><button type="button" className="button button-secondary button-small" onClick={retry}><RotateCw size={15} />重试</button></div>
}

function Breadcrumbs({ team, repo, ref, path }: { team: string; repo: string; ref: string; path: string }) {
  const parts = path ? path.split('/') : []
  return <div className="browse-breadcrumbs"><Link to={routeFor(team, repo, 'tree', ref)}>{repo}</Link>{parts.map((part, index) => {
    const current = index === parts.length - 1
    const currentPath = parts.slice(0, index + 1).join('/')
    return <span className="browse-crumb" key={`${currentPath}-${index}`}><ChevronRight size={15} />{current ? <strong>{part}</strong> : <Link to={routeFor(team, repo, 'tree', ref, currentPath)}>{part}</Link>}</span>
  })}</div>
}

// Downloading is a plain link, not a script-driven fetch, so the browser owns
// the progress and the retry and a large file never passes through JavaScript.
function ToolbarActions({ children, meta }: { children: ReactNode; meta: string }) {
  return <div className="browse-toolbar-actions"><span className="browse-file-meta">{meta}</span>{children}</div>
}

function DownloadLink({ href, label, hint }: { href: string; label: string; hint: string }) {
  return <Hint label={hint}><a className="button button-secondary button-small" href={href} download><Download size={15} />{label}</a></Hint>
}

function date(value?: string) {
  if (!value) return ''
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'short', day: 'numeric' }).format(parsed)
}

function EntryRow({ entry, team, repo, ref }: { entry: BrowseEntry; team: string; repo: string; ref: string }) {
  const commit = entry.lastCommit
  const content = <><span className="browse-entry-icon">{entry.type === 'directory' ? <FolderClosed size={17} /> : entry.type === 'symlink' ? <AlertTriangle size={16} /> : <FileCode2 size={17} />}</span><span className="browse-entry-name">{entry.name}</span>{commit && <span className="browse-entry-commit"><span>{commit.subject || '无提交说明'}</span><small>{commit.authorName} · {date(commit.committedAt)}</small></span>}<span className={`browse-entry-type ${entry.type}`}>{entry.type === 'directory' ? '目录' : entry.type === 'symlink' ? '符号链接' : entry.type === 'file' ? '文件' : '不支持'}</span></>
  if (entry.type === 'directory') return <Link className="browse-entry" to={routeFor(team, repo, 'tree', ref, entry.path)}>{content}<ChevronRight className="browse-entry-chevron" size={16} /></Link>
  if (entry.type === 'file') return <Link className="browse-entry" to={routeFor(team, repo, 'blob', ref, entry.path)}>{content}<ChevronRight className="browse-entry-chevron" size={16} /></Link>
  return <Hint label="为安全起见，不跟随仓库里的符号链接"><div className="browse-entry browse-entry-disabled">{content}</div></Hint>
}

export function BrowsePage() {
  const { team = '', repo = '' } = useParams()
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()
  const isBlob = location.pathname.endsWith('/blob')
  const path = searchParams.get('path') || ''
  const ref = searchParams.get('ref') || 'main'
  const repoQuery = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo), enabled: Boolean(team && repo) })
  const namespaceQuery = useQuery({ queryKey: ['namespaces', team, repo], queryFn: () => Promise.all([api.branches(team, repo), api.tags(team, repo)]), enabled: Boolean(team && repo) })
  const treeQuery = useQuery({ queryKey: ['tree', team, repo, ref, path], queryFn: () => api.tree(team, repo, ref, path), enabled: Boolean(team && repo && !isBlob) })
  const blobQuery = useQuery({ queryKey: ['blob', team, repo, ref, path], queryFn: () => api.blob(team, repo, ref, path), enabled: Boolean(team && repo && isBlob) })
  const branches = namespaceQuery.data?.[0]?.branches || []
  const tags = namespaceQuery.data?.[1]?.tags || []
  const selectedBranch = ref
  const blob = blobQuery.data
  const setRef = (name: string) => setSearchParams({ ref: name, path })

  if (repoQuery.isLoading) return <div className="loading-block"><span className="spinner" />正在加载仓库…</div>
  if (repoQuery.isError) return <RepoLayout section="code"><div className="panel browse-panel"><BrowseError error={repoQuery.error} retry={() => repoQuery.refetch()} /></div></RepoLayout>
  const repoInfo = repoQuery.data!
  if (repoInfo.empty) return <RepoLayout section="code" repo={repoInfo}><div className="panel browse-panel browse-empty"><FileCode2 size={24} /><h2>仓库还没有提交</h2><p>第一次推送代码后，文件会显示在这里。</p><Link to={`/${encodeURIComponent(team)}/${encodeURIComponent(repo)}`} className="button button-secondary">返回仓库页</Link></div></RepoLayout>

  const retry = () => { if (isBlob) void blobQuery.refetch(); else void treeQuery.refetch() }
  return <RepoLayout section="code" repo={repoInfo} wide toolbar={<>
    <BranchPicker branches={branches} tags={tags} selected={selectedBranch} onChange={setRef} />
    <Hint label="查看全部分支和版本标签"><Link className="ref-all-link" to={`/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/branches`}><Files size={15} />全部分支</Link></Hint>
  </>}>
    <div className="browse-page">
    {namespaceQuery.isError && <BrowseError error={namespaceQuery.error} retry={() => namespaceQuery.refetch()} />}
    {isBlob ? <section className="panel browse-panel">
      {blobQuery.isLoading && <div className="loading-block"><span className="spinner" />正在读取文件…</div>}
      {blobQuery.isError && <BrowseError error={blobQuery.error} retry={retry} />}
      {blob && <><div className="browse-toolbar"><Breadcrumbs team={team} repo={repo} ref={selectedBranch} path={path} /><ToolbarActions meta={`${readableSize(blob.size)} · ${blob.blobSha.slice(0, 8)}`}><DownloadLink href={api.rawUrl(team, repo, selectedBranch, path)} label="下载原文件" hint="按原样下载这个文件" /></ToolbarActions></div>
        {blob.binary ? <div className="browse-message"><AlertTriangle size={19} /><div><strong>二进制文件</strong><span>网页浏览不会读取二进制内容；请使用 Git clone 在本地查看。</span></div></div> : blob.tooLarge ? <div className="browse-message"><AlertTriangle size={19} /><div><strong>文件过大</strong><span>此处只显示小于 512 KB 的文本文件，请在本地 clone 后查看。</span></div></div> : <pre className="browse-code"><code>{blob.text}</code></pre>}
      </>}
    </section> : <section className="panel browse-panel">
      {treeQuery.isLoading && <div className="loading-block"><span className="spinner" />正在读取目录…</div>}
      {treeQuery.isError && <BrowseError error={treeQuery.error} retry={retry} />}
      {treeQuery.data && <><div className="browse-toolbar"><Breadcrumbs team={team} repo={repo} ref={selectedBranch} path={path} /><ToolbarActions meta={treeQuery.data.commitSha.slice(0, 8)}><DownloadLink href={api.archiveUrl(team, repo, selectedBranch, 'zip')} label="下载 zip" hint="打包下载当前分支的整份代码" /></ToolbarActions></div>
        {path && <Link className="browse-entry browse-parent" to={routeFor(team, repo, 'tree', selectedBranch, path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '')}><span className="browse-entry-icon"><FolderClosed size={17} /></span><span className="browse-entry-name">..</span><span className="browse-entry-type">上一级</span><ChevronRight className="browse-entry-chevron" size={16} /></Link>}
        {treeQuery.data.entries.length ? treeQuery.data.entries.map(entry => <EntryRow key={`${entry.type}:${entry.path}`} entry={entry} team={team} repo={repo} ref={selectedBranch} />) : <div className="browse-directory-empty">这个目录为空。</div>}
        {treeQuery.data.truncated && <div className="browse-truncated">目录条目超过 500 个，已截取前 500 项。</div>}
      </>}
    </section>}
    </div>
  </RepoLayout>
}
