import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { ArrowLeft, ChevronRight, CircleAlert, Clock3, Code2, FileCode2, GitCommitHorizontal } from 'lucide-react'
import { api, type CommitFile, type GitCommit } from './api'
import { RepoLayout, BranchPicker } from './RepoLayout'
import './commits.css'

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
  return <div className="commit-error" role="alert"><CircleAlert size={17} />{error instanceof Error ? error.message : '无法读取提交历史。'}</div>
}

function CommitRow({ commit, team, repo }: { commit: GitCommit; team: string; repo: string }) {
  return <Link className="commit-row" to={path(team, repo, `/commits/${commit.sha}`)}>
    <span className="commit-row-icon"><GitCommitHorizontal size={18} /></span>
    <span className="commit-row-copy"><strong>{commit.subject || '无提交说明'}</strong><small>{commit.authorName} · {date(commit.committedAt)}</small></span>
    <code>{commit.shortSha || commit.sha.slice(0, 8)}</code><ChevronRight size={16} className="commit-chevron" />
  </Link>
}

export function CommitsPage() {
  const { team = '', repo = '' } = useParams()
  const [searchParams, setSearchParams] = useSearchParams()
  const [offset, setOffset] = useState(0)
  const repository = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo) })
  const branches = useQuery({ queryKey: ['branches', team, repo], queryFn: () => api.branches(team, repo), enabled: repository.isSuccess })
  const branch = searchParams.get('ref') || 'main'
  const history = useQuery({ queryKey: ['commits', team, repo, branch, offset], queryFn: () => api.commits(team, repo, branch, offset), enabled: repository.isSuccess && branches.isSuccess })
  if (repository.isLoading) return <div className="loading-block"><span className="spinner" />正在打开仓库…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  const branchNames = branches.data?.branches || []
  return <RepoLayout section="commits" repo={repository.data} wide toolbar={<>
    <BranchPicker branches={branchNames} selected={branch} onChange={name => { setOffset(0); setSearchParams(name === 'main' ? {} : { ref: name }) }} />
  </>}>
    {branches.isError && <ErrorBox error={branches.error} />}
    {branches.data?.truncated && <div className="commit-info">分支数量过多，仅展示前一部分。</div>}
    <section className="panel commit-list-panel">
      {history.isLoading && <div className="loading-block"><span className="spinner" />正在读取提交…</div>}
      {history.isError && <ErrorBox error={history.error} />}
      {history.data?.commits.length === 0 && <div className="commit-empty"><GitCommitHorizontal size={30} /><h3>这个分支还没有提交</h3><p>在本地提交并推送后，历史会出现在这里。</p></div>}
      {history.data?.commits.map(commit => <CommitRow key={commit.sha} commit={commit} team={team} repo={repo} />)}
      {history.data && (offset > 0 || history.data.hasMore) && <div className="commit-pagination"><button className="button button-secondary button-small" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 20))}>上一页</button><span>第 {Math.floor(offset / 20) + 1} 页</span><button className="button button-secondary button-small" disabled={!history.data.hasMore} onClick={() => setOffset(history.data!.nextOffset)}>下一页</button></div>}
    </section>
  </RepoLayout>
}

function FileRow({ file }: { file: CommitFile }) {
  const labels: Record<string, string> = { added: '新增', deleted: '删除', renamed: '重命名', copied: '复制', modified: '修改', typechange: '类型变更' }
  const status = file.status.toLowerCase()
  return <div className="commit-file-row"><span className={`commit-file-status ${status}`}>{labels[status] || '修改'}</span><FileCode2 size={15} /><div><strong>{file.path}</strong>{file.previousPath && <small>原路径：{file.previousPath}</small>}</div><span className="commit-file-count">{file.binary ? '二进制' : <><b>+{file.additions}</b> <em>−{file.deletions}</em></>}</span></div>
}

export function CommitDetailPage() {
  const { team = '', repo = '', sha = '' } = useParams()
  const repository = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo) })
  const commit = useQuery({ queryKey: ['commit', team, repo, sha], queryFn: () => api.commit(team, repo, sha), enabled: repository.isSuccess && /^[a-fA-F0-9]{40,64}$/.test(sha) })
  if (repository.isLoading || commit.isLoading) return <div className="loading-block"><span className="spinner" />正在读取提交…</div>
  if (repository.isError || !repository.data) return <ErrorBox error={repository.error} />
  if (!/^[a-fA-F0-9]{40,64}$/.test(sha)) return <ErrorBox error={new Error('提交编号无效。')} />
  if (commit.isError || !commit.data) return <ErrorBox error={commit.error} />
  const item = commit.data
  return <RepoLayout section="commits" repo={repository.data} wide><Link className="commit-back" to={path(team, repo, '/commits')}><ArrowLeft size={15} />返回提交历史</Link>
    <section className="panel commit-detail-panel"><div className="commit-detail-top"><span className="commit-detail-symbol"><GitCommitHorizontal size={22} /></span><div><h2>{item.subject || '无提交说明'}</h2><p><strong>{item.authorName}</strong> 提交于 {date(item.committedAt)}</p></div></div><div className="commit-message">{item.message || item.subject}</div><div className="commit-meta"><span>完整 SHA</span><code>{item.sha}</code></div>{item.parents.length > 0 && <div className="commit-meta"><span>父提交</span><code>{item.parents.join(', ')}</code></div>}</section>
    <div className="commit-change-heading"><div><h2>变更文件</h2><p>{item.stats.filesChanged} 个文件 · <span className="added">+{item.stats.additions}</span> <span className="deleted">−{item.stats.deletions}</span></p></div><span><Clock3 size={15} />{date(item.committedAt)}</span></div>
    <section className="panel commit-files-panel">{item.files.length ? item.files.map((file, index) => <FileRow key={`${file.path}-${index}`} file={file} />) : <div className="commit-empty"><Code2 size={27} /><h3>没有可显示的文件变更</h3></div>}</section>
    {item.truncated && <div className="commit-info">此提交的变更文件或统计超过显示限制，页面只展示部分结果。请使用本地 Git 查看完整 diff。</div>}
  </RepoLayout>
}
