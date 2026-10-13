import assert from 'node:assert/strict'
import { test } from 'node:test'
import { api } from './api.ts'

// These tests pin the request shape of the Issue metadata endpoints, because a
// wrong verb, path, or body key would only surface as a 404/400 at runtime.
test('issue list sends filters as query parameters and omits empty ones', async () => {
  const originalFetch = globalThis.fetch
  const calls: string[] = []
  globalThis.fetch = async (input, options) => {
    calls.push(`${options?.method || 'GET'} ${String(input)}`)
    return new Response(JSON.stringify({ issues: [], page: 1, perPage: 20, hasMore: false }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    await api.issues('engineering', 'demo', { state: 'all', page: 2, labelIds: ['label-1', 'label-2'], assignees: ['user-1'], milestone: 4 })
    await api.issues('engineering', 'demo', { state: 'open' })
    const filtered = new URL(calls[0].split(' ')[1], 'http://localhost')
    assert.equal(filtered.pathname, '/api/v1/repos/engineering/demo/issues')
    assert.equal(filtered.searchParams.get('state'), 'all')
    assert.equal(filtered.searchParams.get('page'), '2')
    assert.equal(filtered.searchParams.get('perPage'), '20')
    // One parameter per selected value: several labels mean "all of these", so
    // each has to arrive separately rather than joined into one opaque string.
    assert.deepEqual(filtered.searchParams.getAll('labelId'), ['label-1', 'label-2'])
    assert.deepEqual(filtered.searchParams.getAll('assignee'), ['user-1'])
    assert.equal(filtered.searchParams.get('milestone'), '4')
    const plain = new URL(calls[1].split(' ')[1], 'http://localhost')
    assert.equal(plain.searchParams.get('state'), 'open')
    assert.equal(plain.searchParams.has('labelId'), false)
    assert.equal(plain.searchParams.has('assignee'), false)
    assert.equal(plain.searchParams.has('milestone'), false)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('issue metadata mutations use PUT with the documented bodies', async () => {
  const originalFetch = globalThis.fetch
  const calls: { method: string; path: string; body: unknown }[] = []
  globalThis.fetch = async (input, options) => {
    calls.push({ method: options?.method || 'GET', path: String(input), body: options?.body ? JSON.parse(String(options.body)) : undefined })
    return new Response(JSON.stringify({ labels: [], assignees: [], milestone: null }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    await api.setIssueLabels('engineering', 'demo', 7, ['label-1', 'label-2'])
    await api.setIssueAssignees('engineering', 'demo', 7, ['user-1'])
    await api.setIssueMilestone('engineering', 'demo', 7, null)
    assert.deepEqual(calls, [
      { method: 'PUT', path: '/api/v1/repos/engineering/demo/issues/7/labels', body: { labelIds: ['label-1', 'label-2'] } },
      { method: 'PUT', path: '/api/v1/repos/engineering/demo/issues/7/assignees', body: { userIds: ['user-1'] } },
      { method: 'PUT', path: '/api/v1/repos/engineering/demo/issues/7/milestone', body: { milestoneNumber: null } },
    ])
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('label and milestone endpoints encode repository and resource identifiers', async () => {
  const originalFetch = globalThis.fetch
  const calls: string[] = []
  globalThis.fetch = async (input, options) => {
    calls.push(`${options?.method || 'GET'} ${String(input)}`)
    return new Response(JSON.stringify({ labels: [], milestones: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    await api.createLabel('my team', 'demo', 'Bug', '#FF0000', 'broken')
    await api.deleteLabel('my team', 'demo', 'label-1')
    await api.milestones('my team', 'demo', 'open')
    await api.createMilestone('my team', 'demo', 'v1.0', '', '2026-06-30')
    assert.deepEqual(calls, [
      'POST /api/v1/repos/my%20team/demo/labels',
      'DELETE /api/v1/repos/my%20team/demo/labels/label-1',
      'GET /api/v1/repos/my%20team/demo/milestones?state=open',
      'POST /api/v1/repos/my%20team/demo/milestones',
    ])
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('inline review comments use the documented path, verb, and body', async () => {
  const originalFetch = globalThis.fetch
  const calls: string[] = []
  let body: unknown
  globalThis.fetch = async (input, options) => {
    calls.push(`${options?.method || 'GET'} ${String(input)}`)
    if (options?.method === 'POST') body = JSON.parse(String(options.body))
    return new Response(JSON.stringify({ comments: [], hasMore: false, baseSha: 'a', headSha: 'b', outdatedCount: 0 }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    await api.pullReviewComments('team one', 'repo/one', 7)
    await api.createPullReviewComment('team one', 'repo/one', 7, { path: 'internal/a.go', line: 12, side: 'old', body: 'rename', replyToId: 'c1' })
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.deepEqual(calls, [
    'GET /api/v1/repos/team%20one/repo%2Fone/pulls/7/comments?page=1&perPage=200',
    'POST /api/v1/repos/team%20one/repo%2Fone/pulls/7/comments',
  ])
  assert.deepEqual(body, { path: 'internal/a.go', line: 12, side: 'old', body: 'rename', replyToId: 'c1' })
})
