package store

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Labels, milestones, and assignees are repository-scoped Issue metadata.
// The API applies its shared repository policy before calling these methods.

var labelColorPattern = regexp.MustCompile(`^[0-9a-f]{6}$`)

type Label struct {
	ID          string `json:"id"`
	RepoID      string `json:"repoId"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

const labelColumns = `l.id::text,l.repo_id::text,l.name,l.color,l.description`

func scanLabel(row scanner) (Label, error) {
	var l Label
	err := row.Scan(&l.ID, &l.RepoID, &l.Name, &l.Color, &l.Description)
	return l, dbErr(err)
}

// NormalizeLabelColor accepts "#RRGGBB" or "RRGGBB" and returns lowercase hex.
func NormalizeLabelColor(raw string) (string, bool) {
	color := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
	return color, labelColorPattern.MatchString(color)
}

func validLabelName(name string) bool {
	return utf8.ValidString(name) && !strings.ContainsRune(name, 0) &&
		utf8.RuneCountInString(name) <= 50
}

func (s *Store) ListLabels(ctx context.Context, repoID string) ([]Label, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+labelColumns+`
        FROM labels l WHERE l.repo_id=$1 ORDER BY lower(l.name)`, repoID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	labels := make([]Label, 0)
	for rows.Next() {
		label, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, dbErr(rows.Err())
}

// CreateLabel rejects a duplicate name regardless of case, including a label
// of another repository, because the unique index is per repository.
func (s *Store) CreateLabel(ctx context.Context, repoID, name, color, description string) (Label, error) {
	name = strings.TrimSpace(name)
	color, ok := NormalizeLabelColor(color)
	description = strings.TrimSpace(description)
	if !validLabelName(name) || !ok || utf8.RuneCountInString(description) > 200 {
		return Label{}, invalid("label name, color, or description")
	}
	var id string
	err := s.Pool.QueryRow(ctx, `INSERT INTO labels (repo_id,name,color,description)
        VALUES ($1,$2,$3,$4) RETURNING id::text`, repoID, name, color, description).Scan(&id)
	if err != nil {
		return Label{}, dbErr(err)
	}
	return scanLabel(s.Pool.QueryRow(ctx, `SELECT `+labelColumns+` FROM labels l WHERE l.id=$1`, id))
}

// UpdateLabel allows renaming, recoloring, and editing the description. A
// duplicate name is a conflict; the label stays unchanged.
func (s *Store) UpdateLabel(ctx context.Context, repoID, labelID, name, color, description string) (Label, error) {
	name = strings.TrimSpace(name)
	color, ok := NormalizeLabelColor(color)
	description = strings.TrimSpace(description)
	if !validLabelName(name) || !ok || utf8.RuneCountInString(description) > 200 {
		return Label{}, invalid("label name, color, or description")
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE labels SET name=$3,color=$4,description=$5
        WHERE id=$1 AND repo_id=$2`, labelID, repoID, name, color, description)
	if err != nil {
		return Label{}, dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return Label{}, ErrNotFound
	}
	return scanLabel(s.Pool.QueryRow(ctx, `SELECT `+labelColumns+` FROM labels l WHERE l.id=$1`, labelID))
}

func (s *Store) DeleteLabel(ctx context.Context, repoID, labelID string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM labels WHERE id=$1 AND repo_id=$2`, labelID, repoID)
	if err != nil {
		return dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetIssueLabels replaces the whole label set of one Issue atomically. Label
// IDs that do not belong to the repository are rejected rather than ignored.
func (s *Store) SetIssueLabels(ctx context.Context, repoID string, number int64, labelIDs []string) ([]Label, error) {
	if number < 1 {
		return nil, invalid("number")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var issueID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM issues WHERE repo_id=$1 AND number=$2`, repoID, number).Scan(&issueID)
	if err != nil {
		return nil, dbErr(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM issue_labels WHERE issue_id=$1`, issueID); err != nil {
		return nil, dbErr(err)
	}
	for _, labelID := range labelIDs {
		tag, err := tx.Exec(ctx, `INSERT INTO issue_labels (issue_id,label_id)
            SELECT $1,l.id FROM labels l WHERE l.id=$2 AND l.repo_id=$3`, issueID, labelID, repoID)
		if err != nil {
			return nil, dbErr(err)
		}
		if tag.RowsAffected() != 1 {
			return nil, invalid("label does not belong to this repository")
		}
	}
	labels, err := listIssueLabels(ctx, tx, issueID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, dbErr(err)
	}
	return labels, nil
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func listIssueLabels(ctx context.Context, q queryer, issueID string) ([]Label, error) {
	rows, err := q.Query(ctx, `SELECT `+labelColumns+`
        FROM issue_labels il JOIN labels l ON l.id=il.label_id
        WHERE il.issue_id=$1 ORDER BY lower(l.name)`, issueID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	labels := make([]Label, 0)
	for rows.Next() {
		label, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, dbErr(rows.Err())
}

// Milestones use a repository-scoped counter. Allocation serializes on the
// repository row so concurrent creates cannot produce duplicate numbers.

type Milestone struct {
	ID           string     `json:"id"`
	RepoID       string     `json:"repoId"`
	Number       int64      `json:"number"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	State        string     `json:"state"`
	DueOn        *string    `json:"dueOn"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	ClosedAt     *time.Time `json:"closedAt"`
	OpenIssues   int64      `json:"openIssues"`
	ClosedIssues int64      `json:"closedIssues"`
}

const milestoneColumns = `m.id::text,m.repo_id::text,m.number,m.title,m.description,m.state,
    to_char(m.due_on,'YYYY-MM-DD'),m.created_at,m.updated_at,m.closed_at,
    (SELECT count(*) FROM issues i WHERE i.milestone_id=m.id AND i.state='open'),
    (SELECT count(*) FROM issues i WHERE i.milestone_id=m.id AND i.state='closed')`

func scanMilestone(row scanner) (Milestone, error) {
	var m Milestone
	err := row.Scan(&m.ID, &m.RepoID, &m.Number, &m.Title, &m.Description, &m.State,
		&m.DueOn, &m.CreatedAt, &m.UpdatedAt, &m.ClosedAt, &m.OpenIssues, &m.ClosedIssues)
	return m, dbErr(err)
}

// validDueDate reports whether raw is empty (no due date, hasDue=false) or a
// valid ISO calendar date (hasDue=true). ok=false means the value is invalid.
func validDueDate(raw string) (bool, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, true
	}
	_, err := time.Parse("2006-01-02", raw)
	return true, err == nil
}

func (s *Store) ListMilestones(ctx context.Context, repoID, state string) ([]Milestone, error) {
	if state == "" {
		state = "all"
	}
	if state != "open" && state != "closed" && state != "all" {
		return nil, invalid("state")
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+milestoneColumns+`
        FROM milestones m WHERE m.repo_id=$1 AND ($2='all' OR m.state=$2)
        ORDER BY m.number DESC`, repoID, state)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	milestones := make([]Milestone, 0)
	for rows.Next() {
		milestone, err := scanMilestone(rows)
		if err != nil {
			return nil, err
		}
		milestones = append(milestones, milestone)
	}
	return milestones, dbErr(rows.Err())
}

func (s *Store) CreateMilestone(ctx context.Context, repoID, title, description, dueOn string) (Milestone, error) {
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	hasDue, ok := validDueDate(dueOn)
	if !ok || title == "" || utf8.RuneCountInString(title) > 200 ||
		len(description) > 65536 || !utf8.ValidString(title) || !utf8.ValidString(description) ||
		strings.ContainsRune(title, 0) || strings.ContainsRune(description, 0) {
		return Milestone{}, invalid("milestone title, description, or due date")
	}
	var due *string
	if hasDue {
		due = &dueOn
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Milestone{}, err
	}
	defer tx.Rollback(ctx)
	var number int64
	err = tx.QueryRow(ctx, `UPDATE repositories SET milestone_counter=milestone_counter+1
        WHERE id=$1 AND state='active' RETURNING milestone_counter`, repoID).Scan(&number)
	if err != nil {
		return Milestone{}, dbErr(err)
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO milestones (repo_id,number,title,description,due_on)
        VALUES ($1,$2,$3,$4,$5::date) RETURNING id::text`, repoID, number, title, description, due).Scan(&id)
	if err != nil {
		return Milestone{}, dbErr(err)
	}
	milestone, err := scanMilestone(tx.QueryRow(ctx, `SELECT `+milestoneColumns+` FROM milestones m WHERE m.id=$1`, id))
	if err != nil {
		return Milestone{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Milestone{}, dbErr(err)
	}
	return milestone, nil
}

// UpdateMilestone edits title, description, due date, and state. Closing
// records closed_at once; reopening clears it.
func (s *Store) UpdateMilestone(ctx context.Context, repoID string, number int64, title, description, dueOn, state string) (Milestone, error) {
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	hasDue, ok := validDueDate(dueOn)
	if !ok || number < 1 || title == "" || utf8.RuneCountInString(title) > 200 ||
		len(description) > 65536 || (state != "open" && state != "closed") {
		return Milestone{}, invalid("milestone fields")
	}
	var due *string
	if hasDue {
		due = &dueOn
	}
	return scanMilestone(s.Pool.QueryRow(ctx, `WITH changed AS (
            UPDATE milestones SET title=$3,description=$4,due_on=$5::date,state=$6,
                closed_at=CASE WHEN $6='closed' THEN COALESCE(closed_at,clock_timestamp()) ELSE NULL END,
                updated_at=clock_timestamp()
            WHERE repo_id=$1 AND number=$2
            RETURNING *
        ) SELECT `+milestoneColumns+` FROM changed m`, repoID, number, title, description, due, state))
}

func (s *Store) DeleteMilestone(ctx context.Context, repoID string, number int64) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM milestones WHERE repo_id=$1 AND number=$2`, repoID, number)
	if err != nil {
		return dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetIssueMilestone assigns or clears the milestone of one Issue. A nil number
// clears it; a milestone from another repository is rejected.
func (s *Store) SetIssueMilestone(ctx context.Context, repoID string, issueNumber int64, milestoneNumber *int64) (*Milestone, error) {
	if issueNumber < 1 {
		return nil, invalid("number")
	}
	if milestoneNumber == nil {
		tag, err := s.Pool.Exec(ctx, `UPDATE issues SET milestone_id=NULL
            WHERE repo_id=$1 AND number=$2`, repoID, issueNumber)
		if err != nil {
			return nil, dbErr(err)
		}
		if tag.RowsAffected() == 0 {
			return nil, ErrNotFound
		}
		return nil, nil
	}
	if *milestoneNumber < 1 {
		return nil, invalid("milestone number")
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE issues SET milestone_id=m.id
        FROM milestones m
        WHERE issues.repo_id=$1 AND issues.number=$2 AND m.repo_id=$1 AND m.number=$3`,
		repoID, issueNumber, *milestoneNumber)
	if err != nil {
		return nil, dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	milestone, err := scanMilestone(s.Pool.QueryRow(ctx, `SELECT `+milestoneColumns+`
        FROM milestones m WHERE m.repo_id=$1 AND m.number=$2`, repoID, *milestoneNumber))
	if err != nil {
		return nil, err
	}
	return &milestone, nil
}

// Assignee carries the account fields a repository page needs.

type Assignee struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

const assigneeColumns = `u.id::text,u.name,u.email`

func scanAssignee(row scanner) (Assignee, error) {
	var a Assignee
	err := row.Scan(&a.UserID, &a.Name, &a.Email)
	return a, dbErr(err)
}

// ListAssignees returns the current assignees of one Issue, ordered by name.
func (s *Store) ListAssignees(ctx context.Context, issueID string) ([]Assignee, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+assigneeColumns+`
        FROM issue_assignees a JOIN users u ON u.id=a.user_id
        WHERE a.issue_id=$1 ORDER BY lower(u.name),u.id`, issueID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	assignees := make([]Assignee, 0)
	for rows.Next() {
		assignee, err := scanAssignee(rows)
		if err != nil {
			return nil, err
		}
		assignees = append(assignees, assignee)
	}
	return assignees, dbErr(rows.Err())
}

// repoReadAccess is the shared repository read policy for an active account,
// expressed over the aliases users u and repositories r. It mirrors
// authorization.CanRepository's visibility, creator, direct-grant, and
// team-grant rules so assignment matches what the account can actually open.
const repoReadAccess = `(r.visibility='public'
    OR (r.visibility='internal' AND EXISTS (SELECT 1 FROM team_members tm WHERE tm.user_id=u.id))
    OR r.created_by=u.id
    OR EXISTS (SELECT 1 FROM repo_grants g WHERE g.repo_id=r.id
        AND (g.user_id=u.id
             OR EXISTS (SELECT 1 FROM team_members tm2
                    WHERE tm2.team_id=g.team_id AND tm2.user_id=u.id))))`

// ListAssignableUsers returns the active accounts that may be assigned to an
// Issue in this repository: exactly those with read access. An empty query
// lists every eligible account, so assignment cannot become a directory of
// unrelated employees.
func (s *Store) ListAssignableUsers(ctx context.Context, repoID, query string) ([]Assignee, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	rows, err := s.Pool.Query(ctx, `SELECT u.id::text,u.name,u.email
        FROM users u JOIN repositories r ON r.id=$1
        WHERE u.status='active' AND `+repoReadAccess+`
          AND ($2='' OR lower(u.name) LIKE '%'||$2||'%' OR lower(u.email) LIKE '%'||$2||'%')
        ORDER BY lower(u.name),u.id LIMIT 50`, repoID, query)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	users := make([]Assignee, 0)
	for rows.Next() {
		user, err := scanAssignee(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, dbErr(rows.Err())
}

// SetIssueAssignees replaces the assignee set. Every account must be active and
// have read access to this repository; the caller has already checked write
// access for the requester.
func (s *Store) SetIssueAssignees(ctx context.Context, repoID string, number int64, userIDs []string, assignedBy string) ([]Assignee, []string, error) {
	if number < 1 || len(userIDs) > 20 {
		return nil, nil, invalid("assignees")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	var issueID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM issues WHERE repo_id=$1 AND number=$2`, repoID, number).Scan(&issueID)
	if err != nil {
		return nil, nil, dbErr(err)
	}
	// The previous assignee set is read before it is replaced so the caller can
	// tell an addition from a no-op: notifying someone who was already assigned
	// would grow the inbox with every save of an unrelated field.
	previous := map[string]bool{}
	previousRows, err := tx.Query(ctx, `SELECT user_id::text FROM issue_assignees WHERE issue_id=$1`, issueID)
	if err != nil {
		return nil, nil, dbErr(err)
	}
	for previousRows.Next() {
		var userID string
		if err := previousRows.Scan(&userID); err != nil {
			previousRows.Close()
			return nil, nil, dbErr(err)
		}
		previous[userID] = true
	}
	previousRows.Close()
	if err := previousRows.Err(); err != nil {
		return nil, nil, dbErr(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM issue_assignees WHERE issue_id=$1`, issueID); err != nil {
		return nil, nil, dbErr(err)
	}
	for _, userID := range userIDs {
		tag, err := tx.Exec(ctx, `INSERT INTO issue_assignees (issue_id,user_id,assigned_by)
            SELECT $1,u.id,$3 FROM users u JOIN repositories r ON r.id=$4
            WHERE u.id=$2 AND u.status='active' AND `+repoReadAccess,
			issueID, userID, assignedBy, repoID)
		if err != nil {
			return nil, nil, dbErr(err)
		}
		if tag.RowsAffected() != 1 {
			return nil, nil, invalid("assignee is not an active employee with read access to this repository")
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+assigneeColumns+`
        FROM issue_assignees a JOIN users u ON u.id=a.user_id
        WHERE a.issue_id=$1 ORDER BY lower(u.name),u.id`, issueID)
	if err != nil {
		return nil, nil, dbErr(err)
	}
	assignees := make([]Assignee, 0)
	added := []string{}
	for rows.Next() {
		assignee, err := scanAssignee(rows)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		assignees = append(assignees, assignee)
		if !previous[assignee.UserID] {
			added = append(added, assignee.UserID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, dbErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, dbErr(err)
	}
	return assignees, added, nil
}
