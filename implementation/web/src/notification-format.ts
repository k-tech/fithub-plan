// Pure presentation logic for the inbox, kept apart from the JSX so it can be
// tested directly. Getting a link target wrong sends someone to the wrong page,
// which is the kind of defect that only shows up when a real notification
// arrives, so it is worth pinning down here.
import type { Notification, NotificationKind } from './api'

// describe renders what happened. The switch is exhaustive over the kind union:
// adding an event type without wording is a compile-time error rather than a
// blank row.
export function describeNotification(kind: NotificationKind, actor: string): string {
  const who = actor || '有人'
  switch (kind) {
    case 'issue_comment': return `${who} 评论了 Issue`
    case 'issue_assigned': return `${who} 把 Issue 指派给你`
    case 'pull_comment': return `${who} 在 PR 中留下行内评论`
    case 'pull_review': return `${who} 审查了 PR`
    case 'pull_merged': return `${who} 合并了 PR`
    case 'run_failed': return '自动检查没有通过'
  }
}

// notificationTarget resolves where an entry points. A subject number is
// required for a link to a specific item, so an entry missing one falls back to
// the repository rather than linking to a guessed number.
export function notificationTarget(item: Notification): string {
  const base = `/${encodeURIComponent(item.teamSlug)}/${encodeURIComponent(item.repoSlug)}`
  if (item.kind === 'run_failed') {
    return item.runNumber ? `${base}/actions/${item.runNumber}` : base
  }
  if (!item.subjectNumber) return base
  if (item.kind === 'pull_comment' || item.kind === 'pull_review' || item.kind === 'pull_merged') {
    return `${base}/pulls/${item.subjectNumber}`
  }
  return `${base}/issues/${item.subjectNumber}`
}

// relativeTime renders a short age. A future or unparseable timestamp returns an
// empty string instead of a nonsense duration.
export function relativeTime(value: string, now: number = Date.now()): string {
  const parsed = new Date(value).getTime()
  if (Number.isNaN(parsed)) return ''
  const minutes = Math.floor((now - parsed) / 60000)
  if (minutes < 0) return ''
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes} 分钟前`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时前`
  return `${Math.floor(hours / 24)} 天前`
}
