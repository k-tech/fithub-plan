# FitHub implementation

This directory contains the **first working development slice** of FitHub. It is not a production release. Product decisions and the full delivery plan are in the [parent plan](../README.md).

## What works now

- Local-only development sign-in plus Google Workspace OIDC authorization-code login with PKCE, state, nonce, signed ID token validation, and server-side revocable sessions. Google accounts are keyed by stable `sub`, require verified email and the configured hosted-domain claim, and start as teamless employees. Employee/admin roles, team directory, employee join requests, admin review, and withdrawal are available.
- Team-owned repositories with public/internal/private read visibility, explicit creator and optional team write grants. Repository administrators can add registered, Team-approved employees as read/write/admin collaborators. No personal repositories or Forks.
- PostgreSQL-backed users, teams, repositories, grants, and 30-day read/write/admin PATs. Token use intersects current account and repository permissions; revocation applies to new requests.
- Native Git bare repositories and Smart HTTP through `git-http-backend`. Standard Git clone/fetch/push works. A database-backed `pre-receive` hook rechecks the pushing account's current repository write access, so revoking access takes effect on the next push. It blocks non-branch/tag ref namespaces and only allows repository administrators to change workflow definitions; any branch, including the default branch, can otherwise be updated by a user with write access.
- SSH public-key registration/list/deletion and a restricted `fithub-ssh` OpenSSH gateway are implemented. The gateway emits a per-key forced command, rechecks the active account and the same repository policy as HTTPS, accepts only Git upload-pack/receive-pack for a known repository, and executes native Git without a shell. The OpenSSH daemon configuration has not yet been end-to-end verified on a deployed host.
- React pages for login, public repository browsing, team applications and review, repository creation/detail, file and blob browsing, PR diff, collaborator, SSH-key management, README preview, and token management. Repository administrators can manage collaborators, deployment keys, and the default branch from repository settings.
- Branch list and commit history/detail from native Git, including changed-file status and line counts. Issues share a per-repository number sequence with PRs; listing/filtering, creation, comments, closure, reopening, and PR references are available through API and Web. Issues also carry repository labels (name, `#RRGGBB` color, description), assignees, and a per-repository milestone; lists can be filtered by state, label, assignee, and milestone. Assignable accounts are exactly those that already have read access to the repository, so assignment cannot become an account directory. Anonymous readers can view public repository history and Issues.
- A limited text-commit API writes one UTF-8 regular file (up to 64 KiB) on any existing branch using Git's object/index/commit plumbing and an atomic expected-head update. It refuses symlinks/submodules and Git/workflow configuration paths. CLI and MCP expose this API; ordinary multi-file development still uses local Git.
- Same-repository pull requests can be proposed from a development branch, reviewed, compared as a bounded patch, closed, or merged into the default branch. Reviews are optional feedback; repository writers can merge without approvals or passing CI. Reviewers with write access can leave inline comments on a specific diff line, reply in a thread, and see a thread written against an earlier revision marked outdated instead of silently moving. An inline anchor is validated against the diff Git produces for the current revision, so a comment cannot be stored on a line that does not exist. `Refs #N` and `Fixes #N` link an Issue from a PR body, and a successful merge closes linked `Fixes` Issues. Native Git builds a two-parent merge commit; an atomic ref transaction verifies branch tips, and a durable database attempt allows startup reconciliation. Public PRs are readable anonymously.
- Versioned CI (Actions) with a narrow, fully validated schema: `.fithub/workflows/*.yml` with `version: 1`, explicit `on: push|pull_request|workflow_dispatch` triggers, a static `needs` dependency graph, per-step `run` scripts plus the built-in `checkout` and `upload-artifact` steps, and an explicit `shell` of `bash` or `sh`. Unknown fields, duplicate keys, expressions, dynamic matrices, and external actions are rejected, so what runs is exactly what was reviewed.
- Workflow definitions are read from the default branch and only repository administrators may change them. CI runs are informational and do not govern pushes or merges.
- A registered runner leases one job at a time, downloads that single revision as a read-only archive, executes the steps in an isolated container, and streams the output back. The container has no host mounts beyond its workspace, a read-only root filesystem, all capabilities dropped, no network unless the operator enables it, and CPU, memory, and process limits. The image must be pinned by digest. The runner credential stays outside the container.
- In-app notifications. An event is recorded as a side effect of the write that caused it (Issue comment and assignment, PR inline comment, review, merge, and a failed run), so no background process is needed. Repository access is re-evaluated on every read rather than snapshotted into the row: revoking someone's access makes their existing entries disappear from both the list and the unread count without deleting anything, and restoring access brings them back with their read state intact. Actors are never notified of their own actions, only newly added assignees are notified, and each event carries an idempotency key so a repeated write cannot inflate the inbox or the badge. A `run_failed` entry is attributed to the system rather than to a person. The inbox is reachable from the header bell, the `fh notify` CLI, and the `notify_list`/`notify_read` MCP tools; every one of those reads the caller's own inbox only.

- `fh` CLI and `fh mcp` stdio server cover account status, repository/branch inspection, limited text commits, Issue list/create/detail, labels, milestones, assignees, inline review comments, PR list/create/detail/diff/review/merge, CI runs (list, detail, logs with a resume cursor, manual dispatch, and cancel), and the caller's own notification inbox through the same REST API. CLI interactive login currently supports only the loopback development account picker; production automation uses a PAT, while Google sign-in uses Web.

Git objects, packs, transport, refs, and repository format come from the installed Git program. FitHub adds routing, permissions, lifecycle, and presentation.

## Run locally

Prerequisites: Go 1.26, Node.js 24, Git with `git-http-backend`, Docker/Compose. The development services listen on loopback only.

```sh
cd /Users/hao/Desktop/feedmob/fithub-plan/implementation
docker compose -p fithub-dev -f deploy/compose.dev.yml up -d
cp .env.example .env
set -a
source .env
set +a
go run ./cmd/fithub
```

In a second terminal:

```sh
cd /Users/hao/Desktop/feedmob/fithub-plan/implementation/web
npm ci
npm run dev
```

Open [http://127.0.0.1:5173/](http://127.0.0.1:5173/). Use `admin@fithub.local` in the clearly marked local development login to create a team. Switch to an employee account, request membership, then switch back to approve it. The approved employee can create a repository. Choose “grant owning team write” only when team-wide push access is desired.

To exercise Google sign-in locally, create a Google OAuth **Web application** client with the exact redirect URI `http://127.0.0.1:8080/api/v1/auth/google/callback`, then set `FITHUB_GOOGLE_CLIENT_ID`, `FITHUB_GOOGLE_CLIENT_SECRET`, and `FITHUB_GOOGLE_HOSTED_DOMAIN` in `.env`. The configured domain must match Google's signed `hd` claim; an email suffix alone is not accepted. The browser returns to `FITHUB_APP_URL`. A Google account is never automatically linked to an existing development account merely because the email matches.

To use Git HTTPS, create a repository Token on its page. Clone the shown URL with a normal Git client. When Git asks for credentials, use any username such as `fithub` and the Token as the password; keep the Token out of the remote URL and shell history. An empty repository permits one initial `main` push. Any repository writer can push directly to `main` or another branch, or use a PR with optional reviews and CI status. Reviews and CI do not block pushes or merges; Git conflicts and stale branch tips still need to be resolved. Use only test repositories.

Run the meaningful integration suite against a disposable database:

```sh
cd /Users/hao/Desktop/feedmob/fithub-plan/implementation
FITHUB_TEST_DATABASE_URL='postgres://fithub:fithub-dev-only@127.0.0.1:55439/fithub?sslmode=disable' go test ./... -count=1
FITHUB_TEST_DATABASE_URL='postgres://fithub:fithub-dev-only@127.0.0.1:55439/fithub?sslmode=disable' go test -race ./... -count=1
cd web
npm test
npm run build
```

The database tests create and drop their own uniquely named schemas. Stop the local database with `docker compose -p fithub-dev -f deploy/compose.dev.yml down`; omitting `-v` retains its volume.

## Branch protection and the receive hook

Every repository carries a `pre-receive` hook. Build `go build -o fithub-hook ./cmd/fithub-hook` and install that executable at a fixed absolute path owned by root and not writable by other users, then set `FITHUB_HOOK_BINARY` to it and `FITHUB_HOOK_CONFIG` to a private configuration file. The configuration file is root-owned, mode `0600`, contains a single JSON object with the PostgreSQL URL (`{"databaseUrl":"postgres://..."}`), and is the only place the hook reads the password from, so the credential never appears in a repository or in a process argument list.

The same executable is installed on every existing repository at startup, which also upgrades the provisional hook shipped by earlier versions. If the hook cannot be installed for every repository, startup fails rather than serving without current write-permission checks. Without `FITHUB_HOOK_BINARY`, the hook still enforces the fixed ref-namespace rule but cannot check current account or repository permissions.

Each push re-reads the pushing account's status and current repository write access, so suspending a user or removing a write grant takes effect on the next push. Branch protection is not supported: users with write access can update any branch, including the default branch. Pull requests and CI are optional collaboration tools, not merge gates.

## Google authentication deployment boundary

Without `FITHUB_DEV_MODE=1`, startup requires all three Google settings, an HTTPS `FITHUB_PUBLIC_URL`, and an `FITHUB_APP_URL` on the same HTTPS origin. Register `<FITHUB_PUBLIC_URL>/api/v1/auth/google/callback` exactly in Google Cloud. Serve the built Web app and reverse-proxy `/api/v1` and Git URLs from one public HTTPS origin, preserving the public Host header; the Go process can listen behind that proxy. Browser sessions are stored as SHA-256 hashes in PostgreSQL, expire after 12 hours, survive process restarts, and are revoked on sign-out. Suspending an account revokes its existing browser sessions and PATs, including after later reactivation. The development account picker remains available only when development mode is set and requests reach it from loopback.

Google sign-in never promotes itself to administrator. After the first trusted administrator signs in, an operator with database access must verify that account's stable Google subject and explicitly set `users.role='admin'` for that `google_sub`; matching an email address is insufficient. Newly registered employees have no Team and must apply to one for administrator review. This auth flow has automated tests with signed local ID tokens; it has not been exercised against a live Google tenant in this workspace.

## SSH Git gateway setup and boundary

Build `go build -o fithub-ssh ./cmd/fithub-ssh` and install the executable at a fixed absolute path owned by root and not writable by other users. Copy [ssh.example.json](deploy/ssh.example.json) to a private `/etc/fithub/ssh.json` (root-owned, group-readable by the dedicated `git` and `fithub-keylookup` accounts, not world-readable). Set the PostgreSQL URL, existing private Git repository root, absolute native Git binary, and SSH login user. The lookup account needs database access but no repository filesystem access; the `git` account needs database and repository access. Both should be dedicated service accounts.

Apply the [OpenSSH Match example](deploy/sshd_config.fithub.example) after reviewing the host's complete `sshd_config`. It disables password authentication, user `authorized_keys` files, forwarding, PTY, user environment, and user RC scripts for the dedicated `git` account. OpenSSH calls `fithub-ssh authorized-key --config /etc/fithub/ssh.json --user %u --fingerprint %f`; the program returns only the canonical registered key with `restrict` and a per-key forced command. The forced command is `fithub-ssh serve --config /etc/fithub/ssh.json --key-id <id>`, which reads `SSH_ORIGINAL_COMMAND` and permits only `git-upload-pack` or `git-receive-pack` for `team/repo.git`. It looks up that key again and applies the same live read/write decision as HTTPS before starting native Git with argv. The user's submitted key comment is discarded.

Validate the OpenSSH configuration with `sshd -t -f /etc/ssh/sshd_config`, then exercise clone and push with an actual SSH client before setting `FITHUB_SSH_BASE_URL=ssh://git@git.example.com` (or an explicit port). Setting that variable makes the SSH clone URL visible in repository responses. Keys can be managed with browser-session-only `GET`, `POST`, and `DELETE` requests at `/api/v1/ssh-keys`; `POST` accepts `{"name":"Laptop","publicKey":"ssh-ed25519 ..."}`. The automated tests use real `ssh-keygen` keys and database-backed permission checks, but an OpenSSH daemon end-to-end run is still required. Revoking access during a running Git process does not stop that process, but the receive hook re-reads the account and its access on every push.

## Running CI

CI has three parts: the control plane (the FitHub server), one or more runners, and the container runtime each runner drives.

Register a runner as a platform administrator, which returns its token exactly once:

```sh
fh run list engineering/demo            # confirm the server is reachable
# Registration is an administrator API call; the token is shown only in the response.
curl -sS -X POST "$FITHUB_URL/api/v1/admin/ci/runners" -b "$COOKIE" \
  -H 'Content-Type: application/json' \
  -d '{"name":"local-docker","labels":["linux-x64"]}'
```

Start a runner. `FITHUB_RUNNER_IMAGE` must be immutable: use `image@sha256:…`, or a local Docker image ID (`sha256:…`) for a server-built demo runner image. A moving tag is rejected.

```sh
go build -o ./bin/fithub-runner ./cmd/fithub-runner
FITHUB_RUNNER_SERVER=http://127.0.0.1:8080 \
FITHUB_RUNNER_TOKEN=<token from registration> \
FITHUB_RUNNER_IMAGE='alpine@sha256:<digest>' \
FITHUB_RUNNER_WORK_ROOT=/var/lib/fithub-runner \
./bin/fithub-runner
```

Useful knobs: `FITHUB_RUNNER_JOB_TIMEOUT_SECONDS` (30–7200, default 1800) caps one job; `FITHUB_RUNNER_ALLOW_NETWORK=1` grants all jobs network access and is off by default; a job that explicitly requests the demo SSH deployment key gets network access on its own. CPU and memory limits use the built-in defaults of 2 CPUs and 2048 MB.

Write a workflow at `.fithub/workflows/<name>.yml` on the default branch:

```yaml
version: 1
name: verify
on:
  push: {}
  pull_request: {}
jobs:
  build:
    runs-on: linux-x64
    steps:
      - name: Check out this revision
        builtin: checkout
      - name: Compile
        shell: sh
        run: echo "building $FITHUB_SHA"
  test:
    runs-on: linux-x64
    needs: [build]
    steps:
      - name: Test
        shell: sh
        run: echo "tests passed"
```

CI runs report build and test results, but do not block direct pushes or pull-request merges.

### SSH 部署（演示版）

仓库管理员打开 **仓库 → 设置 → SSH 部署** 保存服务器私钥。工作流任务通过 `secret-refs: [DEPLOY_SSH_KEY]` 显式申请 `FITHUB_DEPLOY_SSH_KEY`；只有默认分支的 push 任务会拿到它，并自动获得网络访问。普通 CI 任务仍保持断网。runner 镜像需自带 `ssh` 客户端，目标服务器需允许该公钥登录。

```yaml
  deploy:
    runs-on: linux-x64
    needs: [test]
    secret-refs: [DEPLOY_SSH_KEY]
    steps:
      - name: Deploy over SSH
        shell: sh
        run: |
          mkdir -p ~/.ssh
          printf '%s\n' "$FITHUB_DEPLOY_SSH_KEY" > ~/.ssh/id_ed25519
          chmod 600 ~/.ssh/id_ed25519
          tar czf /workspace/release.tar.gz -C /workspace/repo .
          scp -i ~/.ssh/id_ed25519 -o StrictHostKeyChecking=no /workspace/release.tar.gz deploy@server:/tmp/release.tar.gz
          ssh -i ~/.ssh/id_ed25519 -o StrictHostKeyChecking=no deploy@server 'mkdir -p /app && tar xzf /tmp/release.tar.gz -C /app && cd /app && ./deploy.sh'
```

这是一次性演示能力：密钥按明文保存在数据库中；请只用临时测试密钥与演示服务器，不要放生产凭据。

### Routine operations

**Upgrade the job image.** Pull the new image, read its digest with `docker inspect --format '{{index .RepoDigests 0}}' <image>:<tag>`, update `FITHUB_RUNNER_IMAGE`, and restart the runner. Jobs started before the restart finish on the old image; jobs not yet leased use the new one. Reverting is the same procedure with the previous digest.

**Runner lost or offline.** A lease expires on its own, and the control plane then makes the job claimable again. Nothing has to be done by hand, but until a runner returns, runs stay `queued`. If a runner is gone for good, revoke it as an administrator so its token can no longer claim work; revoking does not disturb runs already finished.

**Cancel a run.** An operator cancels from the run page, `fh run cancel TEAM/REPO NUMBER`, or `run_cancel` over MCP. The runner learns through its lease heartbeat and stops the job container. A cancelled run is terminal; dispatch a new one instead of expecting the old one to resume.

**A busy or wedged host.** Stop the runner process. Its containers are named `fithub-job-*`; `docker ps -a --filter name=fithub-job` lists leftovers and `docker rm -f` removes them. Workspaces live under `FITHUB_RUNNER_WORK_ROOT` and are deleted per job; after a crash, remove any `job-*` directory left behind. The runner also clears leftovers for its own workspace root at startup.

**Log and artifact retention.** Job logs are stored in PostgreSQL per attempt and are the only copy. A moved or deleted attempt takes its log with it. There is no automatic expiry yet, so the retention window is whatever the operator instates by policy; plan for that before logs accumulate.

### Diagnosing a failed run

The run state tells an operator which side to look at first.

- `failed` with a non-zero exit code is a real check failure. Open the job's log; the last lines before the failing step name are the cause.
- `infrastructure_failed` means the job could not be run at all: an unusable image, a missing workspace, or a runner that could not prepare the job. The message says which. The revision was not judged.
- `timed_out` means the step exceeded its `timeout-minutes` or the runner's job timeout. Raise the step's limit only if the work legitimately takes longer.
- `cancelled` means an operator or the control plane stopped it. It is not a verdict on the code.
- `queued` for longer than expected means no matching runner is available, or no runner has the label the job requests.

Three situations are easy to confuse, so check them in this order:

1. **No run appears at all.** The workflow file must live on the default branch and its `on:` must cover the event. A file that fails validation is recorded on the trigger event rather than as a run, and the reason is in `ci_trigger_events.last_error`.
2. **The run is green but the merge is still blocked.** The check is evaluated for the revision at the top of the pull request, not for the branch tip. A new push invalidates the previous result by design.
3. **A push that only changes workflow files is refused.** That is the administrator-only rule for `.fithub/workflows/**`; an administrator must make or merge that change.

## Current boundary and next implementation tasks

Development mode refuses a non-loopback listen address. Its account picker is deliberately untrusted and must never be placed behind a public reverse proxy. The demo has one repository-level SSH deployment key; it is stored unencrypted, only intended for temporary test credentials, and is not production-ready. Artifact storage, deployment approvals, matrix jobs, and reusable workflows are not implemented; the schema rejects them rather than accepting them and doing something else. Squash merging and OpenSSH deployment validation remain open tasks from the [roadmap](../05-execution-roadmap.md). Branch protection is database-backed, configurable per repository, and re-evaluated on every push by the receive hook.

Three roadmap items are deliberately deferred until the implemented features have been exercised by hand: coordinated backup/recovery and key-recovery rehearsal (FHB-060), load/disk-full/GC limit tuning (FHB-061), and the authorization, input, and runner security review with an SBOM/NOTICE dependency inventory (FHB-062). None of them is started, and outbound webhooks (FHB-053) are out of scope by decision — notifications are in-app only. The database and the Git object store are backed up separately today, so there is no single frozen point in time across both, and no restore has been rehearsed; do not treat the current state as recoverable.

Next, validate the gateway against an actual OpenSSH daemon and complete a real-tenant Google sign-in. HTTP and SSH Git transport now use one repository authorization decision so those entry points cannot grant broader access than the repository policy.
