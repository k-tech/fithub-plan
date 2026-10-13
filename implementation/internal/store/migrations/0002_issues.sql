-- The repository row serializes number allocation for concurrent Issue creates.
ALTER TABLE repositories ADD COLUMN issue_counter bigint NOT NULL DEFAULT 0
    CHECK (issue_counter >= 0);

CREATE TABLE issues (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    number bigint NOT NULL CHECK (number > 0),
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body text NOT NULL DEFAULT '' CHECK (octet_length(body) <= 65536),
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed')),
    author_id uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz,
    UNIQUE (repo_id, number),
    CHECK ((state = 'open' AND closed_at IS NULL)
        OR (state = 'closed' AND closed_at IS NOT NULL))
);
CREATE INDEX issues_repo_state_number_idx ON issues (repo_id, state, number DESC);
CREATE INDEX issues_repo_number_desc_idx ON issues (repo_id, number DESC);
