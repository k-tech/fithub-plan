import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CircleAlert, Fingerprint, KeyRound, ShieldCheck, Trash2 } from 'lucide-react'
import { api } from './api'
import './ssh-keys.css'

export function SSHKeysPage({ sshConfigured }: { sshConfigured: boolean }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [publicKey, setPublicKey] = useState('')
  const [confirm, setConfirm] = useState<string | null>(null)
  const keys = useQuery({ queryKey: ['ssh-keys'], queryFn: api.sshKeys })
  const add = useMutation({
    mutationFn: () => api.createSSHKey(name.trim(), publicKey.trim()),
    onSuccess: async () => {
      setName('')
      setPublicKey('')
      await queryClient.invalidateQueries({ queryKey: ['ssh-keys'] })
    },
  })
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteSSHKey(id),
    onSuccess: async () => {
      setConfirm(null)
      await queryClient.invalidateQueries({ queryKey: ['ssh-keys'] })
    },
  })
  const onSubmit = (event: FormEvent) => {
    event.preventDefault()
    if (name.trim() && publicKey.trim()) add.mutate()
  }
  return <div className="ssh-keys-page">
    <div className="ssh-keys-heading"><span><KeyRound size={24} /></span><div><div className="eyebrow">PERSONAL SECURITY</div><h1>SSH 公钥</h1><p>用已登记的密钥，通过本地 Git 客户端连接仓库。</p></div></div>
    <div className="ssh-keys-grid">
      <section className="panel ssh-key-add"><h2>添加公钥</h2><p>粘贴一整行 OpenSSH 公钥，例如 <code>ssh-ed25519 AAAA…</code>。不要粘贴私钥。</p>
        <form onSubmit={onSubmit}><label htmlFor="ssh-key-name">名称</label><input id="ssh-key-name" className="field-input" value={name} onChange={event => setName(event.target.value)} maxLength={100} placeholder="工作电脑" required />
          <label htmlFor="ssh-public-key">公钥</label><textarea id="ssh-public-key" value={publicKey} onChange={event => setPublicKey(event.target.value)} placeholder="ssh-ed25519 AAAA..." rows={5} spellCheck={false} required />
          {add.isError && <div className="ssh-key-error"><CircleAlert size={17} />{add.error instanceof Error ? add.error.message : '添加失败'}</div>}
          <button className="button button-primary" disabled={add.isPending}>{add.isPending ? '保存中…' : '添加公钥'}</button>
        </form>
      </section>
      <section className="panel ssh-key-list"><div className="ssh-key-list-header"><h2>已登记密钥</h2><span>{keys.data?.keys.length ?? 0} 把</span></div>
        {keys.isLoading && <p>正在加载…</p>}
        {keys.isError && <div className="ssh-key-error"><CircleAlert size={17} />{keys.error instanceof Error ? keys.error.message : '读取失败'}</div>}
        {keys.data?.keys.length === 0 && <div className="ssh-key-empty"><Fingerprint size={26} /><strong>还没有 SSH 公钥</strong><span>{sshConfigured ? '添加后可以使用仓库页面显示的 SSH 地址。' : '可以先登记密钥；当前实例尚未配置 SSH 入口。'}</span></div>}
        {keys.data?.keys.map(key => <div className="ssh-key-row" key={key.id}><span className="ssh-key-icon"><Fingerprint size={19} /></span><div><strong>{key.name}</strong><small>{key.keyType} · {key.fingerprint}</small><small>添加于 {new Date(key.createdAt).toLocaleDateString('zh-CN')}{key.lastUsedAt ? ` · 最近使用 ${new Date(key.lastUsedAt).toLocaleDateString('zh-CN')}` : ''}</small></div>{confirm === key.id ? <div className="ssh-key-actions"><button type="button" className="button button-secondary button-small" onClick={() => setConfirm(null)}>取消</button><button type="button" className="button button-danger button-small" disabled={remove.isPending} onClick={() => remove.mutate(key.id)}>确认移除</button></div> : <button type="button" className="ssh-key-delete" aria-label={`移除 ${key.name}`} onClick={() => setConfirm(key.id)}><Trash2 size={17} /></button>}</div>)}
        {remove.isError && <div className="ssh-key-error"><CircleAlert size={17} />{remove.error instanceof Error ? remove.error.message : '移除失败'}</div>}
      </section>
    </div>
    <div className="ssh-key-note"><ShieldCheck size={17} />{sshConfigured ? '删除密钥或停用账号后，新的 SSH 连接会重新检查身份与仓库权限。' : '当前实例尚未启用 SSH Git；管理员配置并验证 OpenSSH 后，仓库页面才会显示可用地址。'}</div>
  </div>
}
