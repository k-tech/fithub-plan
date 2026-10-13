-- CI control plane: run -> job -> attempt -> step.
--
-- The state machine is persisted here rather than kept in memory so a restart
-- cannot lose a queued job or resurrect a cancelled one. Every state change that
-- a runner can trigger carries the attempt's fencing token, so a late callback
-- from an attempt that was already superseded cannot overwrite the newer result.

-- runners must be explicitly registered and can be revoked by an administrator.
CREATE TABLE ci_runners (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128),
    -- Only the hash of the runner token is stored; the plaintext is shown once.
    token_hash text NOT NULL UNIQUE CHECK (octet_length(token_hash) BETWEEN 32 AND 128),
    labels text[] NOT NULL DEFAULT '{}',
    -- A revoked runner keeps its history but can no longer claim work.
    state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'revoked')),
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);
CREATE INDEX ci_runners_state_idx ON ci_runners (state, created_at);

-- A run is one execution of one workflow file at one revision. The workflow
-- source is stored with the run so the definition cannot change underneath it.
CREATE TABLE ci_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    -- number is per repository so the UI can show #1, #2 like a pull request.
    number bigint NOT NULL,
    workflow_path text NOT NULL CHECK (octet_length(workflow_path) BETWEEN 1 AND 512),
    workflow_name text NOT NULL CHECK (octet_length(workflow_name) BETWEEN 1 AND 128),
    -- The exact validated definition this run executes.
    workflow_source text NOT NULL CHECK (octet_length(workflow_source) <= 131072),
    -- event is the trigger: push, pull_request, or workflow_dispatch.
    event text NOT NULL CHECK (event IN ('push', 'pull_request', 'workflow_dispatch')),
    -- The revision tested. For a pull request this is the candidate merge commit.
    head_sha text NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    base_sha text CHECK (base_sha IS NULL OR base_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    ref text NOT NULL CHECK (octet_length(ref) BETWEEN 1 AND 512),
    pull_number bigint,
    -- A pull request run tests a generated candidate, never the branch tip.
    candidate boolean NOT NULL DEFAULT false,
    -- trusted is true only when the revision comes from the repository's own
    -- protected default branch, which is the only case where secrets are injected.
    trusted boolean NOT NULL DEFAULT false,
    -- The pushing or opening account, used for the timeline.
    actor_id uuid REFERENCES users(id),
    state text NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'timed_out', 'infrastructure_failed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    -- Why the run ended, shown to the author. Never contains secret values.
    conclusion_reason text CHECK (conclusion_reason IS NULL OR octet_length(conclusion_reason) <= 1024),
    UNIQUE (repo_id, number)
);
CREATE INDEX ci_runs_repo_created_idx ON ci_runs (repo_id, created_at DESC, id);
CREATE INDEX ci_runs_state_created_idx ON ci_runs (state, created_at);
-- One open run per workflow/revision/event prevents duplicate triggers from a
-- retried request or a double event delivery.
CREATE UNIQUE INDEX ci_runs_dedupe_idx ON ci_runs (repo_id, workflow_path, head_sha, event)
    WHERE state IN ('queued', 'running');

-- A job belongs to one run and runs on one runner. jobs are created up front so
-- the UI can show pending jobs before a runner picks them up.
CREATE TABLE ci_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id uuid NOT NULL REFERENCES ci_runs(id) ON DELETE CASCADE,
    job_id text NOT NULL CHECK (octet_length(job_id) BETWEEN 1 AND 64),
    name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128),
    runs_on text NOT NULL CHECK (octet_length(runs_on) BETWEEN 1 AND 32),
    needs text[] NOT NULL DEFAULT '{}',
    -- The validated job definition, so a runner receives exactly what was
    -- approved rather than re-reading a file that could have changed.
    definition jsonb NOT NULL,
    state text NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'waiting', 'leased', 'running', 'succeeded', 'failed', 'cancelled', 'timed_out', 'infrastructure_failed')),
    current_attempt integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    UNIQUE (run_id, job_id)
);
CREATE INDEX ci_jobs_run_idx ON ci_jobs (run_id, job_id);
CREATE INDEX ci_jobs_state_idx ON ci_jobs (state, created_at);

-- One attempt is one lease of one job by one runner. Attempts never reuse an ID,
-- and the fencing token invalidates every callback from a superseded attempt.
CREATE TABLE ci_job_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_pk uuid NOT NULL REFERENCES ci_jobs(id) ON DELETE CASCADE,
    attempt integer NOT NULL CHECK (attempt >= 1),
    runner_id uuid REFERENCES ci_runners(id),
    -- token_hash authenticates the job token; the plaintext is never stored.
    token_hash text NOT NULL UNIQUE CHECK (octet_length(token_hash) BETWEEN 32 AND 128),
    state text NOT NULL DEFAULT 'leased'
        CHECK (state IN ('leased', 'running', 'succeeded', 'failed', 'cancelled', 'timed_out', 'infrastructure_failed')),
    signature text NOT NULL CHECK (octet_length(signature) BETWEEN 16 AND 128),
    leased_at timestamptz NOT NULL DEFAULT now(),
    -- expires_at is the lease deadline; an expired lease is reclaimed.
    expires_at timestamptz NOT NULL,
    heartbeat_at timestamptz,
    finished_at timestamptz,
    exit_code integer,
    error_message text CHECK (error_message IS NULL OR octet_length(error_message) <= 4096),
    UNIQUE (job_pk, attempt)
);
CREATE INDEX ci_job_attempts_job_idx ON ci_job_attempts (job_pk, attempt DESC);
CREATE INDEX ci_job_attempts_runner_idx ON ci_job_attempts (runner_id);
CREATE INDEX ci_job_attempts_lease_idx ON ci_job_attempts (state, expires_at);

-- Logs are appended per attempt and sequence so a reconnecting client can resume
-- from the last sequence it received.
CREATE TABLE ci_log_chunks (
    attempt_id uuid NOT NULL REFERENCES ci_job_attempts(id) ON DELETE CASCADE,
    sequence bigint NOT NULL CHECK (sequence >= 0),
    content text NOT NULL CHECK (octet_length(content) <= 65536),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (attempt_id, sequence)
);

-- Artifacts are owned by one attempt and expire. The stored path is relative to
-- the artifact root and was validated before insert.
CREATE TABLE ci_artifacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    attempt_id uuid NOT NULL REFERENCES ci_job_attempts(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    storage_path text NOT NULL CHECK (octet_length(storage_path) BETWEEN 1 AND 1024 AND storage_path NOT LIKE '/%' AND storage_path NOT LIKE '%..%'),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX ci_artifacts_attempt_idx ON ci_artifacts (attempt_id);
CREATE INDEX ci_artifacts_expiry_idx ON ci_artifacts (expires_at);
