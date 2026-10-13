import assert from 'node:assert/strict'
import { test } from 'node:test'
import { api } from './api.ts'

test('token API exposes the one-time credential and normalizes metadata', async () => {
  const originalFetch = globalThis.fetch
  const calls: string[] = []
  globalThis.fetch = async (input, options) => {
    calls.push(`${options?.method || 'GET'} ${String(input)}`)
    if (options?.method === 'POST') {
      return new Response(JSON.stringify({ token: { id: 'token-1' }, credential: 'fh_secret-for-test' }), {
        status: 201,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    return new Response(JSON.stringify({ tokens: [{
      id: 'token-1', scope: 'repository', level: 'read', prefix: 'fh_secret',
      repo_id: 'repo-1', created_at: '2026-09-24T00:00:00Z',
      expires_at: '2026-10-24T00:00:00Z', revoked_at: null,
    }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    const created = await api.createToken('repository', 'read', 'repo-1')
    assert.equal(created.token, 'fh_secret-for-test')
    const tokens = await api.tokens()
    assert.equal(tokens[0].repoId, 'repo-1')
    assert.equal(tokens[0].expiresAt, '2026-10-24T00:00:00Z')
    assert.equal(tokens[0].revokedAt, null)
    assert.deepEqual(calls, ['POST /api/v1/tokens', 'GET /api/v1/tokens'])
  } finally {
    globalThis.fetch = originalFetch
  }
})
