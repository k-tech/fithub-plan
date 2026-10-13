-- PRs and Issues allocate numbers from repositories.issue_counter in one
-- transaction. The two entity tables retain their own data and state rules.
CREATE TABLE pull_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    number bigint NOT NULL CHECK (number > 0),
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body text NOT NULL DEFAULT '' CHECK (octet_length(body) <= 65536),
    author_id uuid NOT NULL REFERENCES users(id),
    base_ref text NOT NULL DEFAULT 'main' CHECK (base_ref = 'main'),
    head_ref text NOT NULL CHECK (length(head_ref) BETWEEN 1 AND 255 AND head_ref <> base_ref),
    base_sha_at_creation text NOT NULL CHECK (base_sha_at_creation ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    head_sha_at_creation text NOT NULL CHECK (head_sha_at_creation ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed', 'merged')),
    merged_sha text CHECK (merged_sha IS NULL OR merged_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz,
    merged_at timestamptz,
    UNIQUE (repo_id, number),
    CHECK (
        (state = 'open' AND closed_at IS NULL AND merged_at IS NULL AND merged_sha IS NULL)
        OR (state = 'closed' AND closed_at IS NOT NULL AND merged_at IS NULL AND merged_sha IS NULL)
        OR (state = 'merged' AND closed_at IS NULL AND merged_at IS NOT NULL AND merged_sha IS NOT NULL)
    )
);
CREATE UNIQUE INDEX pull_requests_one_open_pair_idx
    ON pull_requests (repo_id, base_ref, head_ref) WHERE state = 'open';
CREATE INDEX pull_requests_repo_state_number_idx
    ON pull_requests (repo_id, state, number DESC);
CREATE INDEX pull_requests_repo_number_desc_idx
    ON pull_requests (repo_id, number DESC);
CREATE INDEX pull_requests_author_idx ON pull_requests (author_id);

-- A pending attempt is a durable recovery record written before Git's
-- compare-and-swap update of main. Its candidate SHA identifies the commit
-- to compare with main after a crash or uncertain Git response.
CREATE TABLE pull_merge_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pull_id uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    actor_id uuid NOT NULL REFERENCES users(id),
    expected_base_sha text NOT NULL CHECK (expected_base_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    expected_head_sha text NOT NULL CHECK (expected_head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    candidate_sha text NOT NULL CHECK (candidate_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'failed')),
    error_message text NOT NULL DEFAULT '' CHECK (octet_length(error_message) <= 2000),
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CHECK ((status = 'pending' AND completed_at IS NULL AND error_message = '')
        OR (status = 'succeeded' AND completed_at IS NOT NULL AND error_message = '')
        OR (status = 'failed' AND completed_at IS NOT NULL))
);
CREATE UNIQUE INDEX pull_merge_attempts_one_pending_idx
    ON pull_merge_attempts (pull_id) WHERE status = 'pending';
CREATE INDEX pull_merge_attempts_pull_started_idx
    ON pull_merge_attempts (pull_id, started_at DESC, id);
CREATE INDEX pull_merge_attempts_actor_idx ON pull_merge_attempts (actor_id);
CREATE INDEX pull_merge_attempts_pending_started_idx
    ON pull_merge_attempts (started_at, id) WHERE status = 'pending';
