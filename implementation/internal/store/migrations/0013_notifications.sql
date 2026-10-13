-- In-app notifications.
--
-- A notification is a durable pointer at an event the recipient is expected to
-- care about. It deliberately stores no permission snapshot: whether the
-- recipient may still see the referenced subject is decided when the list is
-- read, by re-checking the current repository access. Revoking access therefore
-- hides old notifications without deleting rows, which keeps the record honest
-- and makes the hiding reversible if access is granted again.
CREATE TABLE notifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- recipient_id is whose inbox the row belongs to; actor_id is who caused it.
    -- An event a user caused themselves is never recorded, so a user cannot fill
    -- their own inbox.
    recipient_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    -- kind is a closed set so the interface can label every row without knowing
    -- about new event types, and so a typo cannot create an unrenderable row.
    kind text NOT NULL CHECK (kind IN (
        'issue_comment', 'issue_assigned',
        'pull_comment', 'pull_review', 'pull_merged',
        'run_failed'
    )),
    -- subject_number is the issue or pull request number; run_number is the CI
    -- run number for the CI kinds. Both are nullable because a row carries only
    -- the one that applies to its kind.
    subject_number bigint CHECK (subject_number IS NULL OR subject_number > 0),
    run_number bigint CHECK (run_number IS NULL OR run_number > 0),
    -- subject_title is a short copy of the referenced title so a list can render
    -- without joining, and without exposing anything the reader may not read: it
    -- is only ever returned for rows that already passed the access check.
    subject_title text NOT NULL CHECK (octet_length(subject_title) BETWEEN 1 AND 300),
    -- event_key makes delivery idempotent per recipient and event, so a retried
    -- write cannot produce a second notification for the same happening.
    event_key text NOT NULL CHECK (octet_length(event_key) BETWEEN 1 AND 200),
    read_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX notifications_event_key_idx ON notifications (recipient_id, event_key);
-- The inbox query is "my unread rows, newest first", then "all of my rows,
-- newest first", so both orders are indexed.
CREATE INDEX notifications_unread_idx ON notifications (recipient_id, created_at DESC)
    WHERE read_at IS NULL;
CREATE INDEX notifications_inbox_idx ON notifications (recipient_id, created_at DESC, id);
-- Deleting a account removes its own notifications through the cascade above; the
-- repository cascade keeps a deleted repository from leaving rows that point at
-- nothing and could never pass the access check again.
CREATE INDEX notifications_repo_idx ON notifications (repo_id);
