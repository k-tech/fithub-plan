import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CircleAlert, Clock, MessageSquarePlus, Send } from 'lucide-react'
import { api, type PullReviewComment } from './api'
import { Markdown } from './Markdown'
import { issueHrefFor } from './markdown-links'
import { parsePatch } from './diff-parse'

function groupThreads(comments: PullReviewComment[]) {
  const roots = new Map<string, PullReviewComment[]>()
  const replies = new Map<string, PullReviewComment[]>()
  for (const comment of comments) {
    if (comment.replyToId) {
      const list = replies.get(comment.replyToId) ?? []
      list.push(comment)
      replies.set(comment.replyToId, list)
    } else {
      const key = `${comment.path}:${comment.side}:${comment.line}`
      const list = roots.get(key) ?? []
      list.push(comment)
      roots.set(key, list)
    }
  }
  return { roots, replies }
}

function timestamp(value: string) {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? '' : parsed.toLocaleString()
}

interface Props {
  team: string
  repo: string
  number: number
  patch: string
  baseSha: string
  headSha: string
  canComment: boolean
}

// PullDiffReview renders the diff with one comment affordance per line and
// shows existing threads inline. It requests the current revision's comments so
// an outdated thread is labelled instead of silently moving to another line.
export function PullDiffReview({ team, repo, number, patch, baseSha, headSha, canComment }: Props) {
  // 稳定引用，避免每条评论重复解析
  const issueHref = useMemo(() => issueHrefFor(team, repo), [team, repo])
  const queryClient = useQueryClient()
  const [active, setActive] = useState<string | null>(null)
  const [draft, setDraft] = useState('')
  const [replyTo, setReplyTo] = useState<string | null>(null)
  const [replyDraft, setReplyDraft] = useState('')
  const comments = useQuery({
    queryKey: ['pullReviewComments', team, repo, number],
    queryFn: () => api.pullReviewComments(team, repo, number),
  })
  const create = useMutation({
    mutationFn: (input: { path: string; line: number; side: 'new' | 'old'; body: string; replyToId?: string }) =>
      api.createPullReviewComment(team, repo, number, input),
    onSuccess: async () => {
      setDraft('')
      setReplyDraft('')
      setActive(null)
      setReplyTo(null)
      await queryClient.invalidateQueries({ queryKey: ['pullReviewComments', team, repo, number] })
    },
  })

  const lines = useMemo(() => parsePatch(patch), [patch])
  const { roots, replies } = useMemo(() => groupThreads(comments.data?.comments ?? []), [comments.data])
  // Comments written against an older head are collected at the bottom so they
  // stay visible without implying they still point at the current code.
  const orphaned = (comments.data?.comments ?? []).filter(comment => comment.outdated && !comment.replyToId)
  // A thread written against an older revision is shown only in the outdated
  // list. Rendering it inline as well would show the same discussion twice and
  // would imply the stale anchor still points at the current code.
  const outdatedRoots = new Set(orphaned.map(comment => comment.id))

  if (!patch.trim()) return <p className="pull-diff-empty">没有可显示的文本变更。</p>

  return <>
    {comments.data && comments.data.outdatedCount > 0 && <div className="info-box"><Clock size={16} />有 {comments.data.outdatedCount} 条评论写在更早的版本上，已在下方单独列出。</div>}
    {comments.isError && <div className="issue-error" role="alert"><CircleAlert size={17} />无法加载评论。</div>}
    <div className="diff-view">
      {lines.map((line, index) => {
        const anchor = line.newLine ?? line.oldLine
        const side: 'new' | 'old' = line.newLine !== undefined ? 'new' : 'old'
        const key = `${line.path}:${side}:${anchor}`
        const threads = line.path && anchor !== undefined ? (roots.get(key) ?? []).filter(thread => !outdatedRoots.has(thread.id)) : []
        const commentable = canComment && Boolean(line.path) && anchor !== undefined && line.kind !== 'meta' && line.kind !== 'file' && line.kind !== 'hunk'
        return <div className="diff-block" key={index}>
          <div className={`diff-line diff-${line.kind}`}>
            <span className="diff-number">{line.oldLine ?? ''}</span>
            <span className="diff-number">{line.newLine ?? ''}</span>
            <span className="diff-marker">{line.kind === 'add' ? '+' : line.kind === 'del' ? '−' : line.kind === 'context' ? ' ' : ''}</span>
            <code className="diff-text">{line.text}</code>
            {commentable && <button type="button" className="diff-comment-button" 
              onClick={() => { setActive(active === key ? null : key); setReplyTo(null) }}>
              <MessageSquarePlus size={14} />
            </button>}
          </div>
          {threads.map(thread => <CommentThread key={thread.id} comment={thread} replies={replies.get(thread.id) ?? []} issueHref={issueHref}
            canComment={canComment} replyOpen={replyTo === thread.id} replyDraft={replyDraft} pending={create.isPending}
            onOpenReply={() => { setReplyTo(replyTo === thread.id ? null : thread.id); setReplyDraft('') }}
            onReplyDraft={setReplyDraft}
            onSubmitReply={() => create.mutate({ path: thread.path, line: thread.line, side: thread.side, body: replyDraft.trim(), replyToId: thread.id })} />)}
          {active === key && <div className="diff-comment-form">
            <textarea className="pull-textarea" rows={3} maxLength={65536} value={draft} autoFocus
              placeholder={`评论 ${line.path} 第 ${anchor} 行…`} onChange={event => setDraft(event.target.value)} />
            {create.isError && <div className="issue-error" role="alert"><CircleAlert size={17} />{create.error instanceof Error ? create.error.message : '提交失败。'}</div>}
            <div className="diff-comment-actions">
              <button type="button" className="button button-secondary button-small" onClick={() => { setActive(null); setDraft('') }}>取消</button>
              <button type="button" className="button button-primary button-small" disabled={create.isPending || !draft.trim()}
                onClick={() => create.mutate({ path: line.path!, line: anchor!, side, body: draft.trim() })}>
                <Send size={14} />{create.isPending ? '提交中…' : '发表评论'}
              </button>
            </div>
          </div>}
        </div>
      })}
    </div>
    {orphaned.length > 0 && <section className="diff-outdated">
      <h4>早前版本的评论</h4>
      <p className="muted">这些评论写在 {baseSha.slice(0, 7)}…{headSha.slice(0, 7)} 之前的版本上，代码已变化。</p>
      {orphaned.map(thread => <div className="diff-outdated-item" key={thread.id}>
        <code>{thread.path}:{thread.line}</code>
        <CommentThread comment={thread} replies={replies.get(thread.id) ?? []} issueHref={issueHref} canComment={false}
          replyOpen={false} replyDraft="" pending={false} onOpenReply={() => {}} onReplyDraft={() => {}} onSubmitReply={() => {}} />
      </div>)}
    </section>}
  </>
}

function CommentThread({ comment, replies, issueHref, canComment, replyOpen, replyDraft, pending, onOpenReply, onReplyDraft, onSubmitReply }: {
  comment: PullReviewComment
  replies: PullReviewComment[]
  issueHref: (number: number) => string
  canComment: boolean
  replyOpen: boolean
  replyDraft: string
  pending: boolean
  onOpenReply: () => void
  onReplyDraft: (value: string) => void
  onSubmitReply: () => void
}) {
  return <div className={`diff-thread${comment.outdated ? ' outdated' : ''}`}>
    <Comment comment={comment} issueHref={issueHref} />
    {replies.map(reply => <Comment key={reply.id} comment={reply} issueHref={issueHref} reply />)}
    {canComment && <div className="diff-thread-actions">
      {!replyOpen && <button type="button" className="link-button" onClick={onOpenReply}>回复</button>}
      {replyOpen && <div className="diff-comment-form">
        <textarea className="pull-textarea" rows={2} maxLength={65536} value={replyDraft} autoFocus
          placeholder="回复这个讨论…" onChange={event => onReplyDraft(event.target.value)} />
        <div className="diff-comment-actions">
          <button type="button" className="button button-secondary button-small" onClick={onOpenReply}>取消</button>
          <button type="button" className="button button-primary button-small" disabled={pending || !replyDraft.trim()} onClick={onSubmitReply}>
            <Send size={14} />回复
          </button>
        </div>
      </div>}
    </div>}
  </div>
}

function Comment({ comment, issueHref, reply }: { comment: PullReviewComment; issueHref: (number: number) => string; reply?: boolean }) {
  return <article className={`diff-comment${reply ? ' diff-comment-reply' : ''}`}>
    <div className="diff-comment-head">
      <strong>{comment.authorName}</strong>
      <time>{timestamp(comment.createdAt)}</time>
      {comment.outdated && <span className="diff-outdated-tag">已过期</span>}
    </div>
    <Markdown className="diff-comment-body" source={comment.body} issueHref={issueHref} />
  </article>
}
