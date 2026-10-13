-- Branch protection replaces the provisional shell pre-receive hook.
--
-- Each repository keeps at most one protection rule. The rule is applied by a
-- policy helper that re-reads the deciding account and repository on every
-- push, so a long-running push cannot inherit a stale decision. Server-side
-- update-ref (the merge path) bypasses Git receive hooks entirely and applies
-- the same rule in Go.

ALTER TABLE repositories ADD COLUMN default_branch text NOT NULL DEFAULT 'main'
    CHECK (default_branch ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$'
           AND default_branch NOT LIKE '%..%'
           AND default_branch NOT LIKE '%//%'
           AND default_branch NOT LIKE '%.lock'
           AND default_branch NOT LIKE '%/'
           AND default_branch NOT LIKE '%@{%'
           AND default_branch NOT LIKE '%\\%'
           AND default_branch NOT IN ('@', '.', '..'));

-- Pull requests may target the configured default branch instead of only main.
ALTER TABLE pull_requests DROP CONSTRAINT pull_requests_base_ref_check;
ALTER TABLE pull_requests ADD CONSTRAINT pull_requests_base_ref_check
    CHECK (length(base_ref) BETWEEN 1 AND 255 AND base_ref <> head_ref);

CREATE TABLE branch_protections (
    repo_id uuid PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    pattern text NOT NULL CHECK (length(btrim(pattern)) BETWEEN 1 AND 100),
    require_pull_request boolean NOT NULL DEFAULT true,
    required_approvals integer NOT NULL DEFAULT 1 CHECK (required_approvals BETWEEN 0 AND 10),
    allow_admin_override boolean NOT NULL DEFAULT false,
    updated_by uuid REFERENCES users(id),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Every accepted push records which refs were allowed and which account was
-- decided on at push time. This is the audit trail for an enforcement point
-- that runs outside the HTTP server, so it must be written by the hook itself.
CREATE TABLE ref_decisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    actor_id uuid NOT NULL REFERENCES users(id),
    actor_kind text NOT NULL CHECK (actor_kind IN ('token', 'session', 'ssh')),
    reference text NOT NULL CHECK (length(reference) BETWEEN 1 AND 300),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ref_decisions_repo_created_idx ON ref_decisions (repo_id, created_at DESC, id);
CREATE INDEX ref_decisions_actor_created_idx ON ref_decisions (actor_id, created_at DESC, id);
CREATE INDEX ref_decisions_created_idx ON ref_decisions (created_at, id);
