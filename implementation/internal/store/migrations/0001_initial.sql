-- FitHub's first vertical slice. The application applies this file in one transaction.
CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL UNIQUE CHECK (email = lower(email) AND length(email) <= 320),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    role text NOT NULL DEFAULT 'employee' CHECK (role IN ('employee', 'admin')),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE teams (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE team_join_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id uuid NOT NULL REFERENCES teams(id),
    user_id uuid NOT NULL REFERENCES users(id),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
    reviewed_by uuid REFERENCES users(id),
    reviewed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (status = 'pending' AND reviewed_at IS NULL AND reviewed_by IS NULL)
        OR (status IN ('approved', 'rejected') AND reviewed_at IS NOT NULL AND reviewed_by IS NOT NULL)
        OR (status = 'cancelled' AND reviewed_at IS NOT NULL AND reviewed_by IS NULL)
    )
);
CREATE UNIQUE INDEX team_join_requests_one_pending_idx
    ON team_join_requests (team_id, user_id) WHERE status = 'pending';
CREATE INDEX team_join_requests_user_created_idx
    ON team_join_requests (user_id, created_at DESC, id);
CREATE INDEX team_join_requests_pending_created_idx
    ON team_join_requests (created_at, id) WHERE status = 'pending';
CREATE INDEX team_join_requests_reviewer_idx ON team_join_requests (reviewed_by)
    WHERE reviewed_by IS NOT NULL;

CREATE TABLE team_members (
    team_id uuid NOT NULL REFERENCES teams(id),
    user_id uuid NOT NULL REFERENCES users(id),
    approved_request_id uuid UNIQUE REFERENCES team_join_requests(id),
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_members_user_idx ON team_members (user_id, team_id);

CREATE TABLE repositories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id uuid NOT NULL REFERENCES teams(id),
    slug text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9._-]{0,99}$'),
    visibility text NOT NULL DEFAULT 'internal'
        CHECK (visibility IN ('public', 'internal', 'private')),
    state text NOT NULL DEFAULT 'initializing'
        CHECK (state IN ('initializing', 'active', 'failed')),
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (team_id, slug)
);
CREATE INDEX repositories_creator_idx ON repositories (created_by);
CREATE INDEX repositories_state_created_idx ON repositories (state, created_at DESC, id);

CREATE TABLE repo_grants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    user_id uuid REFERENCES users(id),
    team_id uuid REFERENCES teams(id),
    role text NOT NULL CHECK (role IN ('read', 'write', 'admin')),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((user_id IS NOT NULL) <> (team_id IS NOT NULL))
);
CREATE UNIQUE INDEX repo_grants_user_unique_idx ON repo_grants (repo_id, user_id)
    WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX repo_grants_team_unique_idx ON repo_grants (repo_id, team_id)
    WHERE team_id IS NOT NULL;
CREATE INDEX repo_grants_user_idx ON repo_grants (user_id, repo_id)
    WHERE user_id IS NOT NULL;
CREATE INDEX repo_grants_team_idx ON repo_grants (team_id, repo_id)
    WHERE team_id IS NOT NULL;

CREATE TABLE access_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    scope text NOT NULL CHECK (scope IN ('repository', 'user')),
    repo_id uuid REFERENCES repositories(id),
    level text NOT NULL CHECK (level IN ('read', 'write', 'admin')),
    prefix text NOT NULL CHECK (length(prefix) BETWEEN 4 AND 32),
    secret_hash text NOT NULL UNIQUE CHECK (secret_hash ~ '^[0-9a-f]{64}$'),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    last_used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((scope = 'repository' AND repo_id IS NOT NULL)
        OR (scope = 'user' AND repo_id IS NULL)),
    CHECK (expires_at > created_at)
);
CREATE INDEX access_tokens_user_created_idx ON access_tokens (user_id, created_at DESC, id);
CREATE INDEX access_tokens_repo_idx ON access_tokens (repo_id) WHERE repo_id IS NOT NULL;
