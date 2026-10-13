# FitHub CLI and MCP

This document describes the CLI and stdio MCP adapter in `cmd/fh`. Both call the current `/api/v1` HTTP API. Git transport stays with the standard `git` executable; this client does not reimplement Git.

## Build and configure

From `fithub-plan/implementation`:

```sh
go build -o ./bin/fh ./cmd/fh
export FITHUB_URL=http://127.0.0.1:8080
```

The server address can also be passed as `--server URL`. API tokens are read from `FITHUB_TOKEN` and take precedence over the saved local development session. Set `FITHUB_CONFIG` to choose the credential file location. Without it, credentials are stored in the operating system's user configuration directory under `fithub/credentials.json`, with directory mode `0700` and file mode `0600` on Unix systems.

The server has no OAuth device flow or CLI token exchange endpoint. `fh auth login --email EMAIL [--name NAME]` therefore works only when the server advertises local development auth. It saves the 12-hour development session cookie, whose hash is stored in PostgreSQL and survives server restarts until sign-out or expiry. For a Google account or automation, create a FitHub API token in the web UI and provide it as `FITHUB_TOKEN`; the CLI never prints it. `fh auth logout` revokes the saved session on the server; an explicitly set `FITHUB_TOKEN` remains in effect until removed from the environment.

## Commands

```sh
fh auth status
fh auth login --email alice@example.test --name Alice
fh auth logout

fh repo list
fh repo view engineering/demo
fh repo branches engineering/demo
fh repo commit engineering/demo --branch feature/docs --head <current-sha> --path docs/note.txt --file ./note.txt --message "Add note"

fh issue list engineering/demo --state open
fh issue list engineering/demo --label <LABEL_ID> --assignee <USER_ID> --milestone 1
fh issue create engineering/demo --title "Fix login" --body "Describe the problem"
fh issue view engineering/demo 1
fh issue labels engineering/demo 1 --set <LABEL_ID>,<LABEL_ID>
fh issue assign engineering/demo 1 --users <USER_ID>
fh issue milestone engineering/demo 1 --number 1

fh label list engineering/demo
fh label create engineering/demo --name Bug --color ff0000 --description "Something is broken"
fh label update engineering/demo <LABEL_ID> --name Defect --color 0000ff
fh label delete engineering/demo <LABEL_ID>

fh milestone list engineering/demo --state open
fh milestone create engineering/demo --title "v1.0" --due 2026-06-30
fh milestone update engineering/demo 1 --title "v1.0" --state closed
fh milestone delete engineering/demo 1

fh pr list engineering/demo --state open
fh pr create engineering/demo --head feature/login --title "Fix login" --body "Changes"
fh pr view engineering/demo 2
fh pr diff engineering/demo 2
fh pr reviews engineering/demo 2
fh pr approve engineering/demo 2 --body "Looks good"
fh pr request-changes engineering/demo 2 --body "Please update the tests"
fh pr merge engineering/demo 2

fh run list engineering/demo --state all --page 1
fh run view engineering/demo 7
fh run logs engineering/demo 7
fh run logs engineering/demo 7 --job test --after 12
fh run cancel engineering/demo 7
fh run dispatch engineering/demo
fh run dispatch engineering/demo --workflow .fithub/workflows/verify.yml

fh notify list
fh notify list --unread --page 2
fh notify read
fh notify read --ids <notification-id>,<notification-id>
```

Add `--json` to emit the API response as JSON. List endpoints currently use the server's default page size; the API can page results, while the first CLI slice does not expose page controls. `repo branches` gives the SHA required by `repo commit`. That command reads one local UTF-8 file and asks the server to commit its content at `--path` on any existing branch, including the default branch. The server rejects workflow paths, stale SHAs, unsafe Git entries, and files over 64 KiB; a successful commit moves only that branch. `pr approve` and `pr request-changes` first read the current base/head SHAs; the server rejects stale reviews and author self-review. `pr merge` first reads the current head SHA and sends it to the server as an optimistic concurrency check. Any repository writer can merge a clean PR without an approval or passing CI; review and CI results are informational. `run logs` prints each job's output and accepts `--job JOBID` and `--after CURSOR`; the cursor is the value returned by the previous read, so a dropped connection can resume without repeating or skipping lines. `fh notify` reads only the caller's own inbox; there is no account argument, because a token cannot widen it. Notifications are filtered by current repository access on every read, so an entry that becomes unreadable disappears from `--unread` without being deleted, and comes back if access is restored. `fh notify read` with no `--ids` marks the whole visible inbox, which is why it cannot silently consume entries the caller can no longer see.

## MCP stdio server

Run `fh mcp` as the MCP server process. Set `FITHUB_URL` and either `FITHUB_TOKEN` or a credential file created by `fh auth login`. The process uses newline-delimited JSON-RPC on stdin/stdout and sends diagnostics to stderr. Example MCP client configuration:

```json
{
  "mcpServers": {
    "fithub": {
      "command": "/absolute/path/to/fh",
      "args": ["mcp"],
      "env": {
        "FITHUB_URL": "https://fithub.example.internal",
        "FITHUB_TOKEN": "${FITHUB_TOKEN}"
      }
    }
  }
}
```

The server implements `initialize`, `notifications/initialized`, `ping`, `tools/list`, and `tools/call`. Its tools are `auth_status`, `repo_list`, `repo_view`, `repo_branches`, `commit_changes`, `issue_list`, `issue_create`, `issue_view`, `issue_set_labels`, `issue_set_assignees`, `issue_set_milestone`, `label_list`, `label_create`, `label_delete`, `milestone_list`, `milestone_create`, `milestone_update`, `milestone_delete`, `pr_list`, `pr_create`, `pr_view`, `pr_diff`, `pr_reviews`, `pr_review`, `pr_merge`, `run_list`, `run_view`, `run_logs`, `run_dispatch`, `run_cancel`, `notify_list`, and `notify_read`. `commit_changes` accepts `team`, `repo`, `branch`, `expectedHeadSha`, `path`, `content`, and `message`. `issue_list` also accepts `labelId`, `assignee`, and `milestone` filters; `issue_set_labels` and `issue_set_assignees` take an array (or comma-separated string) and an empty value clears the set. Each tool maps to an existing HTTP endpoint and is bounded by the HTTP client's timeout and response size limit. Repository write permissions are enforced by FitHub's API.

The CI tools read and control runs through the same API as the web interface. `run_list` and `run_view` report state; `run_logs` follows the same cursor protocol as `fh run logs`; `run_dispatch` queues a run on the default branch using the trusted definitions; `run_cancel` stops a queued or running run. CI results are informational and do not block pushes or merges. `notify_list` and `notify_read` cover the caller's own inbox: `notify_list` accepts `unread=true` and `page`, and `notify_read` takes a comma-separated `notificationIds` or marks the whole visible inbox when it is omitted. Both are filtered by current repository access, so a returned entry is always one the caller may open. The MCP commit tool handles one small regular text file per call; full repository transport and larger changes still use standard Git. There are no MCP tools for pushing Git packs, defining workflows, or executing arbitrary commands on a runner. Google OAuth for the CLI, OS keychain integration, pagination controls, and a production deployment are also not implemented by this CLI/MCP slice.
