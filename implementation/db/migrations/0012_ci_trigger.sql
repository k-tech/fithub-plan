-- CI triggering is decoupled from the push itself.
--
-- The Git receive hook runs in its own process and must stay fast and purely
-- authoritative about ref updates; it must not block a push while workflows are
-- read and validated. It therefore records an event here, and the server's
-- worker turns events into runs. A unique key makes event delivery idempotent, so
-- a retried hook or a replayed event cannot start a run twice.
CREATE TABLE ci_trigger_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    event text NOT NULL CHECK (event IN ('push', 'pull_request', 'workflow_dispatch')),
    ref text NOT NULL CHECK (octet_length(ref) BETWEEN 1 AND 512),
    old_sha text CHECK (old_sha IS NULL OR old_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    new_sha text NOT NULL CHECK (new_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    pull_number bigint,
    actor_id uuid REFERENCES users(id),
    -- A dispatched event may name one workflow file; otherwise every matching
    -- workflow in the repository runs.
    workflow_path text,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'done', 'failed')),
    attempts integer NOT NULL DEFAULT 0,
    last_error text CHECK (last_error IS NULL OR octet_length(last_error) <= 1024),
    created_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz
);
CREATE INDEX ci_trigger_events_pending_idx ON ci_trigger_events (state, created_at) WHERE state = 'pending';
-- One event per repository, ref, revision, and pull request. A workflow_dispatch
-- carries its own path so two dispatches for one revision can still both run.
CREATE UNIQUE INDEX ci_trigger_events_dedupe_idx ON ci_trigger_events
    (repo_id, event, ref, new_sha, COALESCE(pull_number, 0), COALESCE(workflow_path, ''));

-- Required checks name the jobs that must succeed before a protected branch can
-- be merged into. Names are configured by a repository administrator and are
-- matched against completed jobs; a workflow author cannot satisfy a required
-- check by renaming or deleting a job.
ALTER TABLE branch_protections ADD COLUMN required_checks text[] NOT NULL DEFAULT '{}';
