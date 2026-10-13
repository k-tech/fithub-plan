-- Comments are attached to Issues, not to a repository-level free-text stream.
CREATE TABLE issue_comments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    author_id uuid NOT NULL REFERENCES users(id),
    body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 65536),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX issue_comments_issue_created_idx ON issue_comments (issue_id, created_at, id);

-- An explicit relation keeps PR-to-Issue references stable even if a PR body
-- is edited in a future release. Only same-repository Issues may be linked.
CREATE TABLE pull_issue_links (
    pull_id uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    issue_id uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    closes_on_merge boolean NOT NULL DEFAULT false,
    PRIMARY KEY (pull_id, issue_id)
);
CREATE INDEX pull_issue_links_issue_idx ON pull_issue_links (issue_id, pull_id);

ALTER TABLE issues ADD COLUMN closed_by_pull_id uuid REFERENCES pull_requests(id) ON DELETE SET NULL;
