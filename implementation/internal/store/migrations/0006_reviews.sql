-- Reviews are append-only. The newest review from each reviewer controls the
-- decision for a specific base/head pair; pushing a new commit makes it stale.
CREATE TABLE pull_reviews (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pull_id uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    reviewer_id uuid NOT NULL REFERENCES users(id),
    state text NOT NULL CHECK (state IN ('approved', 'changes_requested')),
    body text NOT NULL DEFAULT '' CHECK (octet_length(body) <= 65536),
    base_sha text NOT NULL CHECK (base_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    head_sha text NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pull_reviews_pull_created_idx ON pull_reviews (pull_id, created_at DESC, id DESC);
CREATE INDEX pull_reviews_reviewer_idx ON pull_reviews (reviewer_id);
