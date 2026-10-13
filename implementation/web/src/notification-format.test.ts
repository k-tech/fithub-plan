import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  describeNotification, notificationTarget, relativeTime,
} from './notification-format.ts'
import type { Notification } from './api.ts'

function entry(overrides: Partial<Notification> = {}): Notification {
  return {
    id: 'n1', kind: 'issue_comment', actorName: 'Bob',
    repoId: 'r1', teamSlug: 'engineering', repoSlug: 'demo',
    subjectNumber: 7, subjectTitle: '示例', read: false, createdAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

test('every kind has wording and never falls through to empty', () => {
  const kinds = ['issue_comment', 'issue_assigned', 'pull_comment', 'pull_review', 'pull_merged', 'run_failed'] as const
  for (const kind of kinds) {
    assert.ok(describeNotification(kind, 'Bob').length > 0, `${kind} must have wording`)
  }
  // The automatic notice must not be attributed to a colleague.
  assert.equal(describeNotification('run_failed', ''), '自动检查没有通过')
  assert.ok(describeNotification('issue_comment', '').startsWith('有人'), 'a missing actor must not render a gap')
})

test('issue events link to the issue, pull events to the pull', () => {
  assert.equal(notificationTarget(entry()), '/engineering/demo/issues/7')
  assert.equal(notificationTarget(entry({ kind: 'issue_assigned' })), '/engineering/demo/issues/7')
  assert.equal(notificationTarget(entry({ kind: 'pull_comment' })), '/engineering/demo/pulls/7')
  assert.equal(notificationTarget(entry({ kind: 'pull_review' })), '/engineering/demo/pulls/7')
  assert.equal(notificationTarget(entry({ kind: 'pull_merged' })), '/engineering/demo/pulls/7')
})

test('a failure links to the run, and falls back to the repository without a run number', () => {
  assert.equal(notificationTarget(entry({ kind: 'run_failed', runNumber: 42, subjectNumber: undefined })), '/engineering/demo/actions/42')
  assert.equal(notificationTarget(entry({ kind: 'run_failed', subjectNumber: undefined })), '/engineering/demo')
})

test('a pull event without a number must not link to a guessed number', () => {
  // A wrong link is worse than no link: it silently sends the reader elsewhere.
  assert.equal(notificationTarget(entry({ kind: 'pull_merged', subjectNumber: undefined })), '/engineering/demo')
})

test('slugs are escaped so a crafted name cannot rewrite the path', () => {
  const target = notificationTarget(entry({ teamSlug: 'a/../b', repoSlug: 'x y' }))
  assert.ok(!target.includes('../'), `path traversal leaked: ${target}`)
  assert.ok(target.includes('a%2F..%2Fb'), `team slug not escaped: ${target}`)
})

test('relative time never reports a negative age', () => {
  const now = Date.parse('2026-01-01T12:00:00Z')
  assert.equal(relativeTime('2026-01-01T11:59:30Z', now), '刚刚')
  assert.equal(relativeTime('2026-01-01T11:30:00Z', now), '30 分钟前')
  assert.equal(relativeTime('2026-01-01T09:00:00Z', now), '3 小时前')
  assert.equal(relativeTime('2025-12-30T12:00:00Z', now), '2 天前')
  // A clock skew that puts an event in the future must not print "-5 分钟前".
  assert.equal(relativeTime('2026-01-01T12:05:00Z', now), '')
  assert.equal(relativeTime('not a timestamp', now), '')
})
