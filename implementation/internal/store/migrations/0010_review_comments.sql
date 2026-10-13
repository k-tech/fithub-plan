-- Inline review comments anchor to one line of a pull request diff. Every
-- comment stores the exact revision it was written against, so the UI can mark
-- a comment stale after new commits instead of silently moving it to another
-- line. The anchor is always a file path plus a line number in the *new* side
-- of the diff; the diff itself is recomputed from Git rather than stored.
CREATE TABLE pull_review_comments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pull_id uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    author_id uuid NOT NULL REFERENCES users(id),
    -- reply_to_id makes a thread: a reply keeps its own anchor so it stays
    -- readable even when the parent is collapsed.
    reply_to_id uuid REFERENCES pull_review_comments(id) ON DELETE CASCADE,
    path text NOT NULL CHECK (octet_length(path) BETWEEN 1 AND 4096 AND path NOT LIKE '/%' AND path NOT LIKE '%..%'),
    line integer NOT NULL CHECK (line BETWEEN 1 AND 10000000),
    side text NOT NULL CHECK (side IN ('new', 'old')),
    body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 65536),
    -- Anchored revision. A comment whose head_sha no longer matches the pull
    -- request head is shown as outdated.
    base_sha text NOT NULL CHECK (base_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    head_sha text NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pull_review_comments_pull_created_idx ON pull_review_comments (pull_id, created_at, id);
CREATE INDEX pull_review_comments_thread_idx ON pull_review_comments (reply_to_id);
CREATE INDEX pull_review_comments_author_idx ON pull_review_comments (author_id);
