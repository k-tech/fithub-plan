import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import {
  Bell, CheckCheck, CircleAlert, GitMerge, GitPullRequest, MessageSquare,
  PlayCircle, UserPlus,
} from 'lucide-react'
import { api, type NotificationKind } from './api'
import { describeNotification, notificationTarget, relativeTime } from './notification-format'
import { Pagination, Hint, Segmented } from './ui'
import './notifications.css'

function KindIcon({ kind }: { kind: NotificationKind }) {
  switch (kind) {
    case 'issue_comment': return <MessageSquare size={16} />
    case 'issue_assigned': return <UserPlus size={16} />
    case 'pull_comment': return <MessageSquare size={16} />
    case 'pull_review': return <GitPullRequest size={16} />
    case 'pull_merged': return <GitMerge size={16} />
    case 'run_failed': return <PlayCircle size={16} />
  }
}

export function NotificationsPage() {
  const [unreadOnly, setUnreadOnly] = useState(false)
  const [page, setPage] = useState(1)
  const queryClient = useQueryClient()
  const inbox = useQuery({
    queryKey: ['notifications', unreadOnly, page],
    queryFn: () => api.notifications({ unread: unreadOnly, page }),
    // The inbox is a live view: a colleague's comment should appear without a
    // manual refresh, and the unread badge needs to stay honest.
    refetchInterval: 20000,
  })
  const markAll = useMutation({
    mutationFn: () => api.markNotificationsRead(),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['notifications'] })
      await queryClient.invalidateQueries({ queryKey: ['notificationCount'] })
    },
  })
  const markOne = useMutation({
    mutationFn: (id: string) => api.markNotificationsRead([id]),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['notifications'] })
      await queryClient.invalidateQueries({ queryKey: ['notificationCount'] })
    },
  })

  const unread = inbox.data?.unreadCount ?? 0
  return <>
    <div className="page-heading">
      <div>
        <div className="eyebrow">INBOX</div>
        <h1>通知</h1>
        <p>与你相关的评论、审查、合并与检查结果。看不到的仓库不会有通知。</p>
      </div>
      <div className="page-action">
        {unread > 0 && <button className="button button-secondary" type="button" disabled={markAll.isPending} onClick={() => markAll.mutate()}>
          <CheckCheck size={16} />{markAll.isPending ? '正在处理…' : '全部标为已读'}
        </button>}
      </div>
    </div>

    {markAll.isError && <div className="notification-error" role="alert"><CircleAlert size={17} />操作失败，请稍后重试。</div>}
    {markOne.isError && <div className="notification-error" role="alert"><CircleAlert size={17} />操作失败，请稍后重试。</div>}

    <section className="panel notification-panel">
      <div className="notification-filterbar">
        <Segmented ariaLabel="通知筛选" value={unreadOnly ? 'unread' : 'all'} onChange={value => { setUnreadOnly(value === 'unread'); setPage(1) }}
          options={[{ value: 'all', label: '全部' }, { value: 'unread', label: unread > 0 ? `未读 ${unread}` : '未读' }]} />
      </div>

      {inbox.isLoading && <div className="loading-block"><span className="spinner" />正在加载通知…</div>}
      {inbox.isError && <div className="notification-error" role="alert"><CircleAlert size={17} />通知暂时无法加载。<button type="button" className="text-button" onClick={() => inbox.refetch()}>重试</button></div>}
      {inbox.data?.notifications.length === 0 && <div className="notification-empty">
        <Bell size={30} />
        <h3>{unreadOnly ? '没有未读通知' : '还没有通知'}</h3>
        <p>当有人评论你参与的 Issue 或 PR、请求你审查、合并改动，或你触发的自动检查失败时，会显示在这里。</p>
      </div>}

      {inbox.data?.notifications.map(item => <div className={`notification-row${item.read ? '' : ' unread'}`} key={item.id}>
        <span className={`notification-icon kind-${item.kind}`}><KindIcon kind={item.kind} /></span>
        <div className="notification-copy">
          <div className="notification-line">
            <Link to={notificationTarget(item)} className="notification-subject">{describeNotification(item.kind, item.actorName)}<span>「{item.subjectTitle}」</span></Link>
            {!item.read && <span className="notification-dot" aria-label="未读" />}
          </div>
          <small>
            <code>{item.teamSlug}/{item.repoSlug}</code>
            {item.subjectNumber !== undefined && <> · #{item.subjectNumber}</>}
            {item.runNumber !== undefined && <> · 运行 #{item.runNumber}</>}
            {' · '}<Hint label={new Date(item.createdAt).toLocaleString('zh-CN')}><span>{relativeTime(item.createdAt)}</span></Hint>
          </small>
        </div>
        {!item.read && <button type="button" className="text-button notification-mark" disabled={markOne.isPending} onClick={() => markOne.mutate(item.id)}>标为已读</button>}
      </div>)}

      {inbox.data && <Pagination label="通知分页" page={page} hasMore={inbox.data.hasMore} onPage={setPage} />}
    </section>
  </>
}

// NotificationBell is the header shortcut: it shows the unread count and links to
// the inbox. The count comes from the same access-checked query as the list, so
// the badge never promises something the inbox will not show.
export function NotificationBell() {
  const unread = useQuery({
    queryKey: ['notificationCount'],
    queryFn: () => api.notifications({ unread: true }).then(result => result.unreadCount),
    refetchInterval: 30000,
  })
  const count = unread.data ?? 0
  return <Link className="notification-bell" to="/notifications" aria-label={count > 0 ? `通知，${count} 条未读` : '通知'}>
    <Bell size={17} />
    {count > 0 && <span className="notification-badge">{count > 99 ? '99+' : count}</span>}
  </Link>
}
