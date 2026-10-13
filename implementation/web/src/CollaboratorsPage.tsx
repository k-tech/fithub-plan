import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from 'react-router-dom'
import { ArrowLeft, CircleAlert, ShieldCheck, Trash2, UserPlus } from 'lucide-react'
import { api, type Collaborator } from './api'
import { RepoLayout } from './RepoLayout'
import { Picker } from './ui'
import './collaborators.css'

export function CollaboratorsPage() {
  const { team = '', repo = '' } = useParams()
  const queryClient = useQueryClient()
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<Collaborator['role']>('read')
  const repository = useQuery({ queryKey: ['repo', team, repo], queryFn: () => api.repo(team, repo) })
  const collaborators = useQuery({ queryKey: ['collaborators', team, repo], queryFn: () => api.collaborators(team, repo), enabled: repository.isSuccess && Boolean(repository.data?.canAdmin) })
  const add = useMutation({ mutationFn: () => api.setCollaborator(team, repo, email.trim(), role), onSuccess: async () => {
    setEmail('')
    await queryClient.invalidateQueries({ queryKey: ['collaborators', team, repo] })
  } })
  const remove = useMutation({ mutationFn: (userId: string) => api.deleteCollaborator(team, repo, userId), onSuccess: async () => {
    await queryClient.invalidateQueries({ queryKey: ['collaborators', team, repo] })
  } })
  const base = `/${encodeURIComponent(team)}/${encodeURIComponent(repo)}`
  if (repository.isLoading) return <div className="loading-block"><span className="spinner" />正在加载仓库…</div>
  if (repository.isError || !repository.data) return <div className="collab-error"><CircleAlert size={18} />{repository.error instanceof Error ? repository.error.message : '仓库不可用'}</div>
  if (!repository.data.canAdmin) return <RepoLayout section="settings" repo={repository.data}><div className="collab-error"><CircleAlert size={18} />只有仓库管理员可以管理协作者。</div></RepoLayout>
  return <RepoLayout section="settings" repo={repository.data}>
    <div className="collab-page">
    <Link className="collab-back" to={base}><ArrowLeft size={16} />返回 {team}/{repo}</Link>
    <section className="panel collab-add-panel"><div className="collab-panel-title"><UserPlus size={18} /><h2>添加或调整权限</h2></div><p>员工须先登录 FitHub 并加入至少一个 Team。公开或内部可见性只影响读取，不授予写入。</p>
      <form onSubmit={(event: FormEvent) => { event.preventDefault(); if (email.trim()) add.mutate() }}><label><span>员工邮箱</span><input className="field-input" type="email" value={email} onChange={event => setEmail(event.target.value)} placeholder="employee@company.com" required /></label><label><span>仓库权限</span><Picker ariaLabel="仓库权限" value={role} onChange={value => setRole(value as Collaborator['role'])}
        options={[{ value: 'read', label: '只读' }, { value: 'write', label: '可写' }, { value: 'admin', label: '管理员' }]} /></label><button className="button button-primary" disabled={add.isPending || !email.trim()}>{add.isPending ? '正在保存…' : '保存权限'}</button></form>
      {add.isError && <div className="collab-error"><CircleAlert size={17} />{add.error instanceof Error ? add.error.message : '保存失败'}</div>}
    </section>
    <section className="panel collab-list-panel"><div className="collab-panel-title"><ShieldCheck size={18} /><h2>当前协作者</h2></div>
      {collaborators.isLoading && <div className="loading-block"><span className="spinner" />正在加载协作者…</div>}
      {collaborators.isError && <div className="collab-error"><CircleAlert size={17} />{collaborators.error instanceof Error ? collaborators.error.message : '读取失败'}</div>}
      {collaborators.data?.collaborators.map(item => <div className="collab-row" key={item.userId}><span className="collab-avatar">{item.name.trim().slice(0, 1).toUpperCase() || 'U'}</span><div className="collab-person"><strong>{item.name}</strong><small>{item.email}{item.status === 'suspended' ? ' · 已停用' : ''}</small></div><span className="collab-role">{item.role === 'admin' ? '管理员' : item.role === 'write' ? '可写' : '只读'}</span>{item.source === 'creator' ? <span className="collab-creator">创建者</span> : <button type="button" className="collab-remove" disabled={remove.isPending} title="移除直接授权" aria-label={`移除 ${item.name} 的直接授权`} onClick={() => remove.mutate(item.userId)}><Trash2 size={16} /></button>}</div>)}
      {remove.isError && <div className="collab-error"><CircleAlert size={17} />{remove.error instanceof Error ? remove.error.message : '移除失败'}</div>}
      <p className="collab-footnote">这里列出直接授权；如果仓库同时向整个 Team 授权，移除个人授权后，该员工仍可能通过 Team 获得访问权限。</p>
    </section>
    </div>
  </RepoLayout>
}
