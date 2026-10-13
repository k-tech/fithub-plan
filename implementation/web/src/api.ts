export type Role = 'employee' | 'admin'
export type Visibility = 'public' | 'internal' | 'private'
export type JoinStatus = 'pending' | 'approved' | 'rejected' | 'cancelled'

export interface Team {
  id: string
  slug: string
  name: string
  description?: string
  memberCount?: number
}

export interface User {
  id: string
  name: string
  email: string
  role: Role
  status?: 'active' | 'suspended'
  teams: Team[]
}

export interface JoinRequest {
  id: string
  teamId: string
  team?: Team
  user?: Pick<User, 'id' | 'name' | 'email'>
  status: JoinStatus
  createdAt: string
  reviewedAt?: string
  reason?: string
}

export interface Repository {
  id: string
  name: string
  slug: string
  description?: string
  team: Team
  visibility: Visibility
  defaultBranch?: string
  updatedAt?: string
  cloneUrl?: string
  sshUrl?: string
  empty?: boolean
  canWrite?: boolean
  canAdmin?: boolean
  readme?: string
  files?: Array<string | { name: string; path: string; type: 'file' | 'directory' }>
}

export type IssueState = 'open' | 'closed'

export interface IssueLabel {
  id: string
  name: string
  color: string
  description: string
}

export interface IssueAssignee {
  userId: string
  name: string
  email: string
}

export interface Issue {
  id: string
  repoId: string
  number: number
  title: string
  body: string
  state: IssueState
  authorId: string
  authorName: string
  createdAt: string
  updatedAt: string
  closedAt?: string | null
  closedByPullNumber?: number | null
  labels: IssueLabel[]
  assignees: IssueAssignee[]
  milestoneNumber?: number | null
  milestoneTitle?: string | null
}

export interface Milestone {
  id: string
  number: number
  title: string
  description: string
  state: IssueState
  dueOn?: string | null
  createdAt: string
  updatedAt: string
  closedAt?: string | null
  openIssues: number
  closedIssues: number
}

export interface IssueComment {
  id: string
  issueId: string
  authorId: string
  authorName: string
  body: string
  createdAt: string
  updatedAt: string
}

export interface IssueCommentPage {
  comments: IssueComment[]
  page: number
  perPage: number
  hasMore: boolean
}

export interface IssuePage {
  issues: Issue[]
  page: number
  perPage: number
  hasMore: boolean
}

export interface GitBranch {
  name: string
  sha: string
  isDefault: boolean
  // Last commit on the branch, so the list can show whether it is still active.
  subject?: string
  authorName?: string
  committedAt?: string
}

// A tag as shown on the tags page. An annotated tag wraps a tag object, so the
// commit it points at is reported separately from the tag object's own id.
export interface GitTag {
  name: string
  sha: string
  tagSha?: string
  annotated: boolean
  subject?: string
  createdAt?: string
}

// A notification points at an event in a repository the reader could read when
// it was listed. The server omits events the reader may no longer see, so a
// rendered entry is always openable.
export type NotificationKind = 'issue_comment' | 'issue_assigned' | 'pull_comment' | 'pull_review' | 'pull_merged' | 'run_failed'

export interface Notification {
  id: string
  kind: NotificationKind
  actorId?: string
  actorName: string
  repoId: string
  teamSlug: string
  repoSlug: string
  subjectNumber?: number
  runNumber?: number
  subjectTitle: string
  read: boolean
  createdAt: string
  readAt?: string
}

export interface NotificationPage {
  notifications: Notification[]
  page: number
  perPage: number
  hasMore: boolean
  unreadCount: number
}

export type CIRunState = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'timed_out' | 'infrastructure_failed'

export interface CIRun {
  id: string
  number: number
  workflowPath: string
  workflowName: string
  event: 'push' | 'pull_request' | 'workflow_dispatch'
  headSha: string
  baseSha?: string
  ref: string
  state: CIRunState
  candidate: boolean
  trusted: boolean
  jobCount: number
  pullNumber?: number
  actorId?: string
  createdAt: string
  startedAt?: string
  finishedAt?: string
  conclusionReason?: string
}

export interface CIRunPage {
  runs: CIRun[]
  page: number
  perPage: number
  hasMore: boolean
}

export interface CIJob {
  id: string
  jobId: string
  name: string
  runsOn: string
  needs: string[]
  state: CIRunState
  attempt: number
  createdAt: string
  startedAt?: string
  finishedAt?: string
}

export interface CIRunDetail {
  run: CIRun
  jobs: CIJob[]
}

export interface CIJobLog {
  jobId: string
  name: string
  state: CIRunState
  lines: string[]
  cursor: number
  attempt: number
}

export interface BrowseEntry {
  name: string
  path: string
  type: 'file' | 'directory' | 'symlink' | 'other'
  sha: string
  // Present when the server could determine the entry's most recent change.
  lastCommit?: { sha: string; subject: string; authorName: string; committedAt: string }
}

export interface RepositoryTree {
  ref: string
  commitSha: string
  path: string
  entries: BrowseEntry[]
  truncated: boolean
}

export interface RepositoryBlob {
  ref: string
  commitSha: string
  path: string
  blobSha: string
  size: number
  text: string
  binary: boolean
  tooLarge: boolean
}

export interface GitCommit {
  sha: string
  shortSha: string
  subject: string
  authorName: string
  authorEmail: string
  authoredAt: string
  committedAt: string
  parents: string[]
}

export interface CommitFile {
  path: string
  status: string
  previousPath?: string
  additions: number
  deletions: number
  binary: boolean
}

export interface CommitDetail extends GitCommit {
  message: string
  files: CommitFile[]
  stats: { filesChanged: number; additions: number; deletions: number }
  truncated: boolean
}

export type PullState = 'open' | 'closed' | 'merged'

export interface PullIssueLink {
  number: number
  title: string
  state: IssueState
  closesOnMerge: boolean
}

export interface PullReview {
  id: string
  pullId: string
  reviewerId: string
  reviewerName: string
  state: 'approved' | 'changes_requested'
  body: string
  baseSha: string
  headSha: string
  createdAt: string
}

export interface PullReviewPage {
  reviews: PullReview[]
  page: number
  perPage: number
  hasMore: boolean
}

export interface ReviewSummary {
  approvals: number
  changesRequested: number
}

export interface PullDiff {
  baseSha: string
  headSha: string
  mergeBaseSha: string
  patch: string
}

/** One inline review comment anchored to a diff line. */
export interface PullReviewComment {
  id: string
  pullId: string
  authorId: string
  authorName: string
  replyToId: string | null
  path: string
  line: number
  side: 'new' | 'old'
  body: string
  baseSha: string
  headSha: string
  createdAt: string
  updatedAt: string
  outdated: boolean
}

export interface PullReviewCommentPage {
  comments: PullReviewComment[]
  hasMore: boolean
  baseSha: string
  headSha: string
  outdatedCount: number
}

export interface PullRequest {
  id: string
  repoId: string
  number: number
  title: string
  body: string
  authorId: string
  authorName: string
  baseRef: string
  headRef: string
  state: PullState
  baseShaAtCreation: string
  headShaAtCreation: string
  mergedSha?: string | null
  createdAt: string
  updatedAt: string
  mergedAt?: string | null
  currentBaseSha?: string
  currentHeadSha?: string
  mergeability?: 'clean' | 'conflicting' | 'stale' | 'missing_ref' | 'empty' | 'error'
  filesChanged?: number
  additions?: number
  deletions?: number
  issues?: PullIssueLink[]
  review?: ReviewSummary
}

export interface PullPage {
  pulls: PullRequest[]
  page: number
  perPage: number
  hasMore: boolean
}

export interface Meta {
  devAuth: boolean
  googleAuth?: boolean
  googleLoginUrl?: string
  sshAuth?: boolean
}

export interface TokenResult {
  token: string
}

export interface AccessToken {
  id: string
  prefix?: string
  scope: 'user' | 'repository'
  level: 'read' | 'write' | 'admin'
  repoId?: string
  repoName?: string
  createdAt?: string
  expiresAt?: string
  revokedAt?: string
}

export interface Collaborator {
  userId: string
  name: string
  email: string
  role: 'read' | 'write' | 'admin'
  status: 'active' | 'suspended'
  source: 'creator' | 'direct'
}

export interface SSHKey {
  id: string
  name: string
  keyType: string
  publicKey: string
  fingerprint: string
  createdAt: string
  lastUsedAt?: string
}

export class ApiError extends Error {
  status: number
  code?: string

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  let response: Response
  try {
    response = await fetch(`/api/v1${path}`, {
      credentials: 'same-origin',
      headers: { Accept: 'application/json', ...(options.body ? { 'Content-Type': 'application/json' } : {}), ...options.headers },
      ...options,
    })
  } catch {
    throw new ApiError('无法连接到 FitHub 服务。请确认后端已启动。', 0, 'network_error')
  }

  const body = await response.json().catch(() => null)
  if (!response.ok) {
    const details = body?.error
    const message = typeof details === 'string' ? details : details?.message || body?.message || body?.detail
    throw new ApiError(message || `请求失败（${response.status}）`, response.status, details?.code || body?.code)
  }
  return body as T
}

const post = <T>(path: string, body: object) => request<T>(path, { method: 'POST', body: JSON.stringify(body) })

export const api = {
  meta: () => request<Meta>('/meta'),
  me: async () => {
    try {
      const result = await request<User | { user: User }>('/me')
      return 'user' in result ? result.user : result
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return null
      throw error
    }
  },
  devLogin: (email: string, name: string) => post<{ user: User }>('/dev/login', { email, name }),
  logout: () => post<{ ok: boolean }>('/auth/logout', {}),
  teams: async () => {
    const result = await request<{ teams: Team[] } | Team[]>('/teams')
    return Array.isArray(result) ? result : result.teams
  },
  createTeam: (name: string, slug: string) => post<{ team: Team }>('/teams', { name, slug }),
  joinTeam: (teamId: string) => post<{ request: JoinRequest }>(`/teams/${encodeURIComponent(teamId)}/join-requests`, {}),
  cancelJoinRequest: (id: string) => post<{ request: JoinRequest }>(`/me/join-requests/${encodeURIComponent(id)}/cancel`, {}),
  myJoinRequests: async () => {
    const result = await request<{ requests: JoinRequest[] } | JoinRequest[]>('/me/join-requests')
    return Array.isArray(result) ? result : result.requests
  },
  adminJoinRequests: async () => {
    const result = await request<{ requests: JoinRequest[] } | JoinRequest[]>('/admin/join-requests')
    return Array.isArray(result) ? result : result.requests
  },
  decideJoinRequest: (id: string, decision: 'approved' | 'rejected') =>
    post<{ request: JoinRequest }>(`/admin/join-requests/${encodeURIComponent(id)}/decision`, { decision }),
  repos: async () => {
    const result = await request<{ repos: Repository[] } | Repository[]>('/repos')
    return Array.isArray(result) ? result : result.repos
  },
  createRepo: (teamId: string, slug: string, visibility: Visibility, grantTeamWrite: boolean) =>
    post<{ repo: Repository }>('/repos', { teamId, slug, visibility, grantTeamWrite }),
  repo: async (team: string, repo: string) => {
    const result = await request<{ repo: Repository } | Repository>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}`)
    return 'repo' in result ? result.repo : result
  },
  deployKey: (team: string, repo: string) =>
    request<{ key: { name: string; configured: boolean } }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/deploy-key`),
  saveDeployKey: (team: string, repo: string, name: string, privateKey: string) =>
    request<{ key: { name: string; configured: boolean } }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/deploy-key`, { method: 'PUT', body: JSON.stringify({ name, privateKey }) }),
  deleteDeployKey: (team: string, repo: string) =>
    request<{ ok: boolean }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/deploy-key`, { method: 'DELETE' }),
  collaborators: (team: string, repo: string) =>
    request<{ collaborators: Collaborator[] }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/collaborators`),
  setCollaborator: (team: string, repo: string, email: string, role: Collaborator['role']) =>
    post<{ collaborator: Collaborator }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/collaborators`, { email, role }),
  setDefaultBranch: (team: string, repo: string, branch: string) =>
    request<{ defaultBranch: string }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/default-branch`, {
      method: 'PATCH', body: JSON.stringify({ branch }),
    }),
  deleteCollaborator: (team: string, repo: string, userId: string) =>
    request<{ ok: boolean }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/collaborators/${encodeURIComponent(userId)}`, { method: 'DELETE' }),
  sshKeys: () => request<{ keys: SSHKey[] }>('/ssh-keys'),
  createSSHKey: (name: string, publicKey: string) => post<{ key: SSHKey }>('/ssh-keys', { name, publicKey }),
  deleteSSHKey: (id: string) => request<{ ok: boolean }>(`/ssh-keys/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  // Label and assignee filters accept several values and require all of them:
  // picking two labels narrows the list rather than widening it. A filter is
  // sent once per selected value, which the server also accepts comma-separated.
  issues: (team: string, repo: string, options: { state?: IssueState | 'all'; page?: number; labelIds?: string[]; assignees?: string[]; milestone?: number } = {}) => {
    const query = new URLSearchParams({ state: options.state ?? 'open', page: String(options.page ?? 1), perPage: '20' })
    for (const labelId of options.labelIds ?? []) query.append('labelId', labelId)
    for (const assignee of options.assignees ?? []) query.append('assignee', assignee)
    if (options.milestone) query.set('milestone', String(options.milestone))
    return request<IssuePage>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/issues?${query.toString()}`)
  },
  issue: async (team: string, repo: string, number: number) => {
    const result = await request<{ issue: Issue }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/issues/${number}`)
    return result.issue
  },
  createIssue: (team: string, repo: string, title: string, body: string) =>
    post<{ issue: Issue }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/issues`, { title, body }),
  updateIssueState: (team: string, repo: string, number: number, state: IssueState) =>
    request<{ issue: Issue }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/issues/${number}`, {
      method: 'PATCH', body: JSON.stringify({ state }),
    }),
  issueComments: (team: string, repo: string, number: number, page = 1) =>
    request<IssueCommentPage>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/issues/${number}/comments?page=${page}&perPage=20`),
  createIssueComment: (team: string, repo: string, number: number, body: string) =>
    post<{ comment: IssueComment }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/issues/${number}/comments`, { body }),
  labels: (team: string, repo: string) =>
    request<{ labels: IssueLabel[] }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/labels`),
  createLabel: (team: string, repo: string, name: string, color: string, description: string) =>
    post<{ label: IssueLabel }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/labels`, { name, color, description }),
  deleteLabel: (team: string, repo: string, labelId: string) =>
    request<{ deleted: boolean }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/labels/${encodeURIComponent(labelId)}`, { method: 'DELETE' }),
  setIssueLabels: (team: string, repo: string, number: number, labelIds: string[]) =>
    request<{ labels: IssueLabel[] }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/issues/${number}/labels`, {
      method: 'PUT', body: JSON.stringify({ labelIds }),
    }),
  setIssueAssignees: (team: string, repo: string, number: number, userIds: string[]) =>
    request<{ assignees: IssueAssignee[] }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/issues/${number}/assignees`, {
      method: 'PUT', body: JSON.stringify({ userIds }),
    }),
  setIssueMilestone: (team: string, repo: string, number: number, milestoneNumber: number | null) =>
    request<{ milestone: Milestone | null }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/issues/${number}/milestone`, {
      method: 'PUT', body: JSON.stringify({ milestoneNumber }),
    }),
  assignableUsers: (team: string, repo: string, q = '') =>
    request<{ users: IssueAssignee[] }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/assignable-users${q ? `?q=${encodeURIComponent(q)}` : ''}`),
  milestones: (team: string, repo: string, state: IssueState | 'all' = 'all') =>
    request<{ milestones: Milestone[] }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/milestones?state=${state}`),
  createMilestone: (team: string, repo: string, title: string, description: string, dueOn: string) =>
    post<{ milestone: Milestone }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/milestones`, { title, description, dueOn }),
  updateMilestone: (team: string, repo: string, number: number, title: string, description: string, dueOn: string, state: IssueState) =>
    request<{ milestone: Milestone }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/milestones/${number}`, {
      method: 'PATCH', body: JSON.stringify({ title, description, dueOn, state }),
    }),
  tree: (team: string, repo: string, ref = 'main', path = '') => {
    const query = new URLSearchParams({ ref, path })
    return request<RepositoryTree>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/tree?${query.toString()}`)
  },
  blob: (team: string, repo: string, ref: string, path: string) => {
    const query = new URLSearchParams({ ref, path })
    return request<RepositoryBlob>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/blob?${query.toString()}`)
  },
  branches: (team: string, repo: string) =>
    request<{ defaultBranch: string; branches: GitBranch[]; truncated: boolean }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/branches`),
  tags: (team: string, repo: string) =>
    request<{ tags: GitTag[]; truncated: boolean }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/tags`),
  // Downloads are plain links rather than fetches: the browser handles the
  // stream, the progress UI, and the retry, and a multi-megabyte body never has
  // to pass through JavaScript. Authentication rides on the same session cookie
  // every other request uses.
  rawUrl: (team: string, repo: string, ref: string, path: string) => {
    const query = new URLSearchParams({ ref, path })
    return `/api/v1/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/raw?${query.toString()}`
  },
  archiveUrl: (team: string, repo: string, ref: string, format: 'zip' | 'tar.gz' = 'zip') => {
    const query = new URLSearchParams({ ref, format })
    return `/api/v1/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/archive?${query.toString()}`
  },
  commits: (team: string, repo: string, ref = 'main', offset = 0) =>
    request<{ ref: string; commits: GitCommit[]; hasMore: boolean; nextOffset: number }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/commits?ref=${encodeURIComponent(ref)}&limit=20&offset=${offset}`),
  commit: async (team: string, repo: string, sha: string) => {
    const result = await request<{ commit: CommitDetail }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/commits/${encodeURIComponent(sha)}`)
    return result.commit
  },
  pulls: (team: string, repo: string, state: PullState | 'all' = 'open', page = 1) =>
    request<PullPage>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/pulls?state=${state}&page=${page}&perPage=20`),
  pull: async (team: string, repo: string, number: number) => {
    const result = await request<{ pull: PullRequest }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/pulls/${number}`)
    return result.pull
  },
  pullReviews: (team: string, repo: string, number: number, page = 1) =>
    request<PullReviewPage>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/pulls/${number}/reviews?page=${page}&perPage=20`),
  pullDiff: (team: string, repo: string, number: number) =>
    request<PullDiff>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/pulls/${number}/diff`),
  pullReviewComments: (team: string, repo: string, number: number, page = 1) =>
    request<PullReviewCommentPage>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/pulls/${number}/comments?page=${page}&perPage=200`),
  createPullReviewComment: (team: string, repo: string, number: number, input: { path: string; line: number; side: 'new' | 'old'; body: string; replyToId?: string }) =>
    post<{ comment: PullReviewComment }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/pulls/${number}/comments`, input),
  createPullReview: (team: string, repo: string, number: number, state: PullReview['state'], body: string, expectedBaseSha: string, expectedHeadSha: string) =>
    post<{ review: PullReview }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/pulls/${number}/reviews`, { state, body, expectedBaseSha, expectedHeadSha }),
  createPull: (team: string, repo: string, title: string, body: string, headRef: string) =>
    post<{ pull: PullRequest }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/pulls`, { title, body, headRef, baseRef: 'main' }),
  closePull: (team: string, repo: string, number: number) =>
    request<{ pull: PullRequest }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/pulls/${number}`, { method: 'PATCH', body: JSON.stringify({ state: 'closed' }) }),
  mergePull: (team: string, repo: string, number: number, expectedHeadSha: string) =>
    post<{ pull: PullRequest }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/pulls/${number}/merge`, { expectedHeadSha }),
  runs: (team: string, repo: string, options: { state?: CIRunState | 'all'; page?: number } = {}) => {
    const query = new URLSearchParams({ state: options.state ?? 'all', page: String(options.page ?? 1) })
    return request<CIRunPage>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/runs?${query.toString()}`)
  },
  run: (team: string, repo: string, number: number) =>
    request<CIRunDetail>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/runs/${number}`),
  runLogs: (team: string, repo: string, number: number, after = -1) =>
    request<{ jobs: CIJobLog[] }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/runs/${number}/logs?after=${after}`),
  dispatchRun: (team: string, repo: string, workflowPath?: string) =>
    post<{ eventId: string; state: string }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/runs/demo`, workflowPath ? { workflowPath } : {}),
  cancelRun: (team: string, repo: string, number: number) =>
    post<{ run: CIRun }>(`/repos/${encodeURIComponent(team)}/${encodeURIComponent(repo)}/runs/${number}/cancel`, {}),
  notifications: (options: { unread?: boolean; page?: number } = {}) => {
    const query = new URLSearchParams({ page: String(options.page ?? 1) })
    if (options.unread) query.set('unread', 'true')
    return request<NotificationPage>(`/notifications?${query.toString()}`)
  },
  markNotificationsRead: (ids: string[] = []) =>
    post<{ marked: number; unreadCount: number }>('/notifications/read', { ids }),
  createToken: (scope: 'user' | 'repository', level: 'read' | 'write' | 'admin', repoId?: string) =>
    post<{ token: AccessToken; credential: string }>('/tokens', { scope, level, ...(repoId ? { repoId } : {}) })
      .then((result): TokenResult => ({ token: result.credential })),
  tokens: async () => {
    const result = await request<{ tokens: Array<AccessToken & { repo_id?: string; created_at?: string; expires_at?: string; revoked_at?: string }> }>('/tokens')
    return result.tokens.map(token => ({
      ...token,
      repoId: token.repo_id,
      createdAt: token.created_at,
      expiresAt: token.expires_at,
      revokedAt: token.revoked_at,
    }))
  },
  revokeToken: (id: string) => request<void>(`/tokens/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}
