import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, ShieldCheck, Trash2 } from 'lucide-react'
import { Link, useParams } from 'react-router-dom'
import { api } from './api'
import { RepoLayout, repoBase } from './RepoLayout'
import './deploy-key.css'

function ErrorNotice({ error }: { error: unknown }) {
  return <div className="deploy-key-error">{error instanceof Error ? error.message : '操作失败，请稍后再试。'}</div>
}

export function DeployKeyPage() {
  const { team = '', repo = '' } = useParams()
  const queryClient = useQueryClient()
  const current = useQuery({ queryKey: ['deploy-key', team, repo], queryFn: () => api.deployKey(team, repo) })
  const [name, setName] = useState('Production')
  const [privateKey, setPrivateKey] = useState('')
  const save = useMutation({
    mutationFn: () => api.saveDeployKey(team, repo, name, privateKey),
    onSuccess: async () => { setPrivateKey(''); await queryClient.invalidateQueries({ queryKey: ['deploy-key', team, repo] }) },
  })
  const remove = useMutation({
    mutationFn: () => api.deleteDeployKey(team, repo),
    onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: ['deploy-key', team, repo] }) },
  })
  const key = current.data?.key
  const base = repoBase(team, repo)
  return <RepoLayout section="settings" wide><div className="deploy-key-page">
    <nav className="deploy-key-nav" aria-label="仓库设置"><Link to={`${base}/settings/collaborators`}>成员权限</Link><Link className="active" to={`${base}/settings/deploy`}>SSH 部署</Link></nav>
    <div className="deploy-key-heading"><span><KeyRound size={22} /></span><div><h2>SSH 部署密钥</h2><p>保存一把服务器私钥，在默认分支推送后由工作流连接服务器。</p></div></div>
    <section className="panel deploy-key-panel">
      {current.isError && <ErrorNotice error={current.error} />}
      {key?.configured ? <div className="deploy-key-status"><ShieldCheck size={17} /><span>已配置：<strong>{key.name}</strong>。私钥不会再次显示。</span><button type="button" className="button button-secondary button-small" onClick={() => remove.mutate()} disabled={remove.isPending}><Trash2 size={14} />{remove.isPending ? '删除中…' : '删除密钥'}</button></div> : <p className="deploy-key-empty">还没有部署密钥。请粘贴目标服务器使用的 OpenSSH 私钥。</p>}
      <label className="field-label" htmlFor="deploy-key-name">密钥名称</label>
      <input id="deploy-key-name" className="field-input" value={name} onChange={event => setName(event.target.value)} maxLength={128} />
      <label className="field-label" htmlFor="deploy-private-key">SSH 私钥</label>
      <textarea id="deploy-private-key" className="field-input deploy-key-textarea" value={privateKey} onChange={event => setPrivateKey(event.target.value)} placeholder={key?.configured ? '输入新私钥以替换当前密钥' : '-----BEGIN OPENSSH PRIVATE KEY-----'} spellCheck={false} />
      <button type="button" className="button button-primary" onClick={() => save.mutate()} disabled={save.isPending || !name.trim() || !privateKey.trim()}>{save.isPending ? '保存中…' : key?.configured ? '替换部署密钥' : '保存部署密钥'}</button>
      {save.isError && <ErrorNotice error={save.error} />}{remove.isError && <ErrorNotice error={remove.error} />}
    </section>
    <section className="panel deploy-key-guide"><h3>工作流里使用</h3><p>在需要部署的任务中声明密钥引用，并把 SSH 命令写在任务步骤里：</p><pre>{`secret-refs: [DEPLOY_SSH_KEY]\nsteps:\n  - run: |\n      mkdir -p ~/.ssh\n      printf '%s\\n' "$FITHUB_DEPLOY_SSH_KEY" > ~/.ssh/id_ed25519\n      chmod 600 ~/.ssh/id_ed25519\n      tar czf /workspace/release.tar.gz -C /workspace/repo .
      scp -i ~/.ssh/id_ed25519 -o StrictHostKeyChecking=no /workspace/release.tar.gz user@server:/tmp/release.tar.gz
      ssh -i ~/.ssh/id_ed25519 -o StrictHostKeyChecking=no user@server 'mkdir -p /app && tar xzf /tmp/release.tar.gz -C /app && cd /app && ./deploy.sh'`}</pre><p>只有默认分支的 push 工作流会收到这把密钥；runner 镜像需要包含 SSH 客户端。</p></section>
  </div></RepoLayout>
}
