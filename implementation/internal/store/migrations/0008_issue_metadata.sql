-- Issue metadata: labels, assignees, and milestones.
--
-- Labels and milestones are owned by one repository. Issue/PR numbering is
-- shared and never reused; milestones keep their own per-repository sequence so
-- adding one does not disturb issue numbers.

ALTER TABLE repositories ADD COLUMN milestone_counter bigint NOT NULL DEFAULT 0
    CHECK (milestone_counter >= 0);

CREATE TABLE labels (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 50),
    color text NOT NULL CHECK (color ~ '^[0-9a-f]{6}$'),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 200),
    created_at timestamptz NOT NULL DEFAULT now()
);
-- Case-insensitive uniqueness: "Bug" and "bug" are the same label.
CREATE UNIQUE INDEX labels_repo_name_key ON labels (repo_id, lower(name));

CREATE TABLE milestones (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    number bigint NOT NULL CHECK (number > 0),
    title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '' CHECK (octet_length(description) <= 65536),
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed')),
    due_on date,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz,
    UNIQUE (repo_id, number),
    CHECK ((state = 'open' AND closed_at IS NULL)
        OR (state = 'closed' AND closed_at IS NOT NULL))
);
CREATE INDEX milestones_repo_state_number_idx ON milestones (repo_id, state, number DESC);

ALTER TABLE issues ADD COLUMN milestone_id uuid REFERENCES milestones(id) ON DELETE SET NULL;
CREATE INDEX issues_milestone_idx ON issues (milestone_id) WHERE milestone_id IS NOT NULL;

CREATE TABLE issue_labels (
    issue_id uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    label_id uuid NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (issue_id, label_id)
);
CREATE INDEX issue_labels_label_idx ON issue_labels (label_id, issue_id);

-- Assignment requires repository read access at assignment time. The API
-- re-checks that the repository still exists and the account is active.
CREATE TABLE issue_assignees (
    issue_id uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id),
    assigned_by uuid NOT NULL REFERENCES users(id),
    assigned_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (issue_id, user_id)
);
CREATE INDEX issue_assignees_user_idx ON issue_assignees (user_id, issue_id);
