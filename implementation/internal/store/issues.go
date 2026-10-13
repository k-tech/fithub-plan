package store

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

// Issue numbers are unique within one repository. Authorization is handled by
// the API's shared repository policy before calling these storage methods.
type Issue struct {
	ID                 string
	RepoID             string
	Number             int64
	Title              string
	Body               string
	State              string
	AuthorID           string
	AuthorName         string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ClosedAt           *time.Time
	ClosedByPullNumber *int64
	MilestoneID        *string
	MilestoneNumber    *int64
	MilestoneTitle     *string
	Labels             []Label
	Assignees          []Assignee
}

const issueColumns = `i.id::text, i.repo_id::text, i.number, i.title, i.body,
    i.state, i.author_id::text, u.name, i.created_at, i.updated_at, i.closed_at,
    (SELECT p.number FROM pull_requests p WHERE p.id=i.closed_by_pull_id),
    i.milestone_id::text,
    (SELECT m.number FROM milestones m WHERE m.id=i.milestone_id),
    (SELECT m.title FROM milestones m WHERE m.id=i.milestone_id)`

func scanIssue(row scanner) (Issue, error) {
	var i Issue
	err := row.Scan(&i.ID, &i.RepoID, &i.Number, &i.Title, &i.Body,
		&i.State, &i.AuthorID, &i.AuthorName, &i.CreatedAt, &i.UpdatedAt, &i.ClosedAt,
		&i.ClosedByPullNumber, &i.MilestoneID, &i.MilestoneNumber, &i.MilestoneTitle)
	i.Labels = make([]Label, 0)
	i.Assignees = make([]Assignee, 0)
	return i, dbErr(err)
}

// attachIssueMetadata batch-loads labels and assignees for the returned Issues
// so a page costs two extra queries regardless of its size.
func attachIssueMetadata(ctx context.Context, db queryer, issues []Issue) ([]Issue, error) {
	if len(issues) == 0 {
		return issues, nil
	}
	ids := make([]string, 0, len(issues))
	index := make(map[string]int, len(issues))
	for position, issue := range issues {
		ids = append(ids, issue.ID)
		index[issue.ID] = position
	}
	labelRows, err := db.Query(ctx, `SELECT il.issue_id::text, `+labelColumns+`
        FROM issue_labels il JOIN labels l ON l.id=il.label_id
        WHERE il.issue_id = ANY($1) ORDER BY lower(l.name)`, ids)
	if err != nil {
		return nil, dbErr(err)
	}
	for labelRows.Next() {
		var issueID string
		var label Label
		if err := labelRows.Scan(&issueID, &label.ID, &label.RepoID, &label.Name, &label.Color, &label.Description); err != nil {
			labelRows.Close()
			return nil, dbErr(err)
		}
		if position, ok := index[issueID]; ok {
			issues[position].Labels = append(issues[position].Labels, label)
		}
	}
	labelRows.Close()
	if err := labelRows.Err(); err != nil {
		return nil, dbErr(err)
	}
	assigneeRows, err := db.Query(ctx, `SELECT a.issue_id::text, `+assigneeColumns+`
        FROM issue_assignees a JOIN users u ON u.id=a.user_id
        WHERE a.issue_id = ANY($1) ORDER BY lower(u.name),u.id`, ids)
	if err != nil {
		return nil, dbErr(err)
	}
	defer assigneeRows.Close()
	for assigneeRows.Next() {
		var issueID string
		var assignee Assignee
		if err := assigneeRows.Scan(&issueID, &assignee.UserID, &assignee.Name, &assignee.Email); err != nil {
			return nil, dbErr(err)
		}
		if position, ok := index[issueID]; ok {
			issues[position].Assignees = append(issues[position].Assignees, assignee)
		}
	}
	return issues, dbErr(assigneeRows.Err())
}

func validIssueText(title, body string) bool {
	return title != "" && utf8.ValidString(title) && utf8.ValidString(body) &&
		!strings.ContainsRune(title, 0) && !strings.ContainsRune(body, 0) &&
		utf8.RuneCountInString(title) <= 200 && len(body) <= 65536
}

// CreateIssue allocates the next number by updating the repository row inside
// the same transaction as the insert. Rollback restores the counter; concurrent
// writers serialize on that row and cannot create duplicate numbers.
func (s *Store) CreateIssue(ctx context.Context, repoID, authorID, title, body string) (Issue, error) {
	title = strings.TrimSpace(title)
	if !validIssueText(title, body) {
		return Issue{}, invalid("title or body")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Issue{}, err
	}
	defer tx.Rollback(ctx)
	var number int64
	err = tx.QueryRow(ctx, `UPDATE repositories SET issue_counter=issue_counter+1
        WHERE id=$1 AND state='active' RETURNING issue_counter`, repoID).Scan(&number)
	if err != nil {
		return Issue{}, dbErr(err)
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO issues (repo_id, number, title, body, author_id)
        VALUES ($1,$2,$3,$4,$5) RETURNING id::text`, repoID, number, title, body, authorID).Scan(&id)
	if err != nil {
		return Issue{}, dbErr(err)
	}
	issue, err := scanIssue(tx.QueryRow(ctx, `SELECT `+issueColumns+`
        FROM issues i JOIN users u ON u.id=i.author_id WHERE i.id=$1`, id))
	if err != nil {
		return Issue{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Issue{}, dbErr(err)
	}
	return issue, nil
}

// IssueFilter narrows one Issue page. Zero values mean no restriction.
type IssueFilter struct {
	State string
	// LabelIDs and AssigneeIDs require an Issue to match every value, not just
	// one of them. Selecting "bug" and "urgent" therefore narrows the list rather
	// than widening it, which is what makes the filter usable when several
	// labels are in play.
	LabelIDs        []string
	AssigneeIDs     []string
	MilestoneNumber *int64
}

// nilIfEmpty turns "no filter" into SQL NULL, so the query can skip the clause
// without a second statement. An empty slice would compare as an empty array and
// still match everything, but NULL makes that intent explicit.
func nilIfEmpty(values []string) any {
	if len(values) == 0 {
		return nil
	}
	return values
}

// ListIssues returns the requested page in newest-number-first order. The
// extra row determines whether another page exists without a separate count.
func (s *Store) ListIssues(ctx context.Context, repoID string, filter IssueFilter, page, perPage int) ([]Issue, bool, error) {
	state := filter.State
	if state != "open" && state != "closed" && state != "all" {
		return nil, false, invalid("state")
	}
	if page < 1 || page > 1_000_000 || perPage < 1 || perPage > 100 {
		return nil, false, invalid("pagination")
	}
	if filter.MilestoneNumber != nil && *filter.MilestoneNumber < 1 {
		return nil, false, invalid("milestone")
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+issueColumns+`
        FROM issues i JOIN users u ON u.id=i.author_id
        WHERE i.repo_id=$1 AND ($2='all' OR i.state=$2)
          AND ($3::text[] IS NULL OR (SELECT COUNT(DISTINCT il.label_id::text)
                FROM issue_labels il
                WHERE il.issue_id=i.id AND il.label_id::text = ANY($3::text[])) = cardinality($3::text[]))
          AND ($4::text[] IS NULL OR (SELECT COUNT(DISTINCT ia.user_id::text)
                FROM issue_assignees ia
                WHERE ia.issue_id=i.id AND ia.user_id::text = ANY($4::text[])) = cardinality($4::text[]))
          AND ($5::bigint IS NULL OR i.milestone_id=(
                SELECT m.id FROM milestones m WHERE m.repo_id=$1 AND m.number=$5))
        ORDER BY i.number DESC LIMIT $6 OFFSET $7`,
		repoID, state, nilIfEmpty(filter.LabelIDs), nilIfEmpty(filter.AssigneeIDs), filter.MilestoneNumber, perPage+1, (page-1)*perPage)
	if err != nil {
		return nil, false, dbErr(err)
	}
	defer rows.Close()
	issues := make([]Issue, 0, perPage)
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, false, err
		}
		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, false, dbErr(err)
	}
	hasMore := len(issues) > perPage
	if hasMore {
		issues = issues[:perPage]
	}
	if issues, err = attachIssueMetadata(ctx, s.Pool, issues); err != nil {
		return nil, false, err
	}
	return issues, hasMore, nil
}

// GetIssue returns one Issue with its labels and assignees loaded.
func (s *Store) GetIssue(ctx context.Context, repoID string, number int64) (Issue, error) {
	if number < 1 {
		return Issue{}, invalid("number")
	}
	issue, err := scanIssue(s.Pool.QueryRow(ctx, `SELECT `+issueColumns+`
        FROM issues i JOIN users u ON u.id=i.author_id
        WHERE i.repo_id=$1 AND i.number=$2`, repoID, number))
	if err != nil {
		return Issue{}, err
	}
	loaded, err := attachIssueMetadata(ctx, s.Pool, []Issue{issue})
	if err != nil {
		return Issue{}, err
	}
	return loaded[0], nil
}

// SetIssueState is idempotent: an unchanged state preserves updated_at and
// closed_at. Closing after a reopen records a new closed_at timestamp.
func (s *Store) SetIssueState(ctx context.Context, repoID string, number int64, state string) (Issue, error) {
	if number < 1 || (state != "open" && state != "closed") {
		return Issue{}, invalid("number or state")
	}
	return scanIssue(s.Pool.QueryRow(ctx, `WITH changed AS (
            UPDATE issues SET
                updated_at=CASE WHEN state<>$3 THEN clock_timestamp() ELSE updated_at END,
                closed_at=CASE WHEN $3='closed' THEN COALESCE(closed_at,clock_timestamp()) ELSE NULL END,
                closed_by_pull_id=CASE WHEN state<>$3 THEN NULL ELSE closed_by_pull_id END,
                state=$3
            WHERE repo_id=$1 AND number=$2
            RETURNING *
        ) SELECT `+issueColumns+`
        FROM changed i JOIN users u ON u.id=i.author_id`, repoID, number, state))
}
