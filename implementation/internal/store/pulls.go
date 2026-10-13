package store

import (
	"context"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"
)

// PullRequest uses the same per-repository number sequence as Issue.
// Repository read/write authorization is enforced by the API policy layer.
type PullRequest struct {
	ID                string     `json:"id"`
	RepoID            string     `json:"repo_id"`
	Number            int64      `json:"number"`
	Title             string     `json:"title"`
	Body              string     `json:"body"`
	AuthorID          string     `json:"author_id"`
	AuthorName        string     `json:"author_name"`
	BaseRef           string     `json:"base_ref"`
	HeadRef           string     `json:"head_ref"`
	State             string     `json:"state"`
	BaseSHAAtCreation string     `json:"base_sha_at_creation"`
	HeadSHAAtCreation string     `json:"head_sha_at_creation"`
	MergedSHA         *string    `json:"merged_sha,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	ClosedAt          *time.Time `json:"closed_at,omitempty"`
	MergedAt          *time.Time `json:"merged_at,omitempty"`
}

type MergeAttempt struct {
	ID              string     `json:"id"`
	PullID          string     `json:"pull_id"`
	RepoID          string     `json:"repo_id"`
	Number          int64      `json:"number"`
	ActorID         string     `json:"actor_id"`
	ExpectedBaseSHA string     `json:"expected_base_sha"`
	ExpectedHeadSHA string     `json:"expected_head_sha"`
	CandidateSHA    string     `json:"candidate_sha"`
	Status          string     `json:"status"`
	ErrorMessage    string     `json:"error_message"`
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

const pullColumns = `p.id::text, p.repo_id::text, p.number, p.title, p.body,
    p.author_id::text, u.name, p.base_ref, p.head_ref, p.state,
    p.base_sha_at_creation, p.head_sha_at_creation, p.merged_sha,
    p.created_at, p.updated_at, p.closed_at, p.merged_at`

const mergeAttemptColumns = `a.id::text, a.pull_id::text, p.repo_id::text, p.number,
    a.actor_id::text, a.expected_base_sha, a.expected_head_sha, a.candidate_sha,
    a.status, a.error_message, a.started_at, a.completed_at`

func scanPull(row scanner) (PullRequest, error) {
	var p PullRequest
	err := row.Scan(&p.ID, &p.RepoID, &p.Number, &p.Title, &p.Body,
		&p.AuthorID, &p.AuthorName, &p.BaseRef, &p.HeadRef, &p.State,
		&p.BaseSHAAtCreation, &p.HeadSHAAtCreation, &p.MergedSHA,
		&p.CreatedAt, &p.UpdatedAt, &p.ClosedAt, &p.MergedAt)
	return p, dbErr(err)
}

func scanMergeAttempt(row scanner) (MergeAttempt, error) {
	var a MergeAttempt
	err := row.Scan(&a.ID, &a.PullID, &a.RepoID, &a.Number,
		&a.ActorID, &a.ExpectedBaseSHA, &a.ExpectedHeadSHA, &a.CandidateSHA,
		&a.Status, &a.ErrorMessage, &a.StartedAt, &a.CompletedAt)
	return a, dbErr(err)
}

func validGitSHA(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	if strings.ToLower(sha) != sha {
		return false
	}
	_, err := hex.DecodeString(sha)
	return err == nil
}

// validPullRef accepts a branch name, with or without the refs/heads/ prefix.
// It refuses control characters, path traversal, and names that Git would read
// as an option. Protection and equality with the base are checked separately.
func validPullRef(ref string) bool {
	if ref == "" || len(ref) > 255 || !utf8.ValidString(ref) || strings.ContainsRune(ref, 0) {
		return false
	}
	name := strings.TrimPrefix(ref, "refs/heads/")
	if name == "" || strings.Contains(name, "refs/") || strings.Contains(name, "..") ||
		strings.Contains(name, "\\") || strings.Contains(name, "@{") ||
		strings.HasPrefix(name, "-") || strings.HasSuffix(name, "/") ||
		name == "." || name == ".." {
		return false
	}
	return true
}

// CreatePullRequest increments issue_counter in the same transaction as the
// insert. A failed insert rolls the increment back, and Issue/PR creates
// serialize on that repository row under concurrency.
func (s *Store) CreatePullRequest(ctx context.Context, repoID, authorID, title, body, baseRef, headRef, baseSHA, headSHA string) (PullRequest, error) {
	title = strings.TrimSpace(title)
	if baseRef == "" {
		baseRef = "main"
	}
	// The base branch is no longer fixed to main: a repository may configure a
	// different default branch. It must still be a plain branch name, and the
	// storage layer refuses a base equal to the head.
	if !validIssueText(title, body) || !validPullRef(baseRef) || !validPullRef(headRef) ||
		baseRef == headRef || !validGitSHA(baseSHA) || !validGitSHA(headSHA) {
		return PullRequest{}, invalid("pull request fields")
	}
	refs, err := ParseIssueReferences(body)
	if err != nil {
		return PullRequest{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return PullRequest{}, err
	}
	defer tx.Rollback(ctx)
	var number int64
	err = tx.QueryRow(ctx, `UPDATE repositories SET issue_counter=issue_counter+1
        WHERE id=$1 AND state='active' RETURNING issue_counter`, repoID).Scan(&number)
	if err != nil {
		return PullRequest{}, dbErr(err)
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO pull_requests
        (repo_id,number,title,body,author_id,base_ref,head_ref,base_sha_at_creation,head_sha_at_creation)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text`,
		repoID, number, title, body, authorID, baseRef, headRef, baseSHA, headSHA).Scan(&id)
	if err != nil {
		return PullRequest{}, dbErr(err)
	}
	if err := insertPullIssueLinks(ctx, tx, id, repoID, refs); err != nil {
		return PullRequest{}, err
	}
	p, err := scanPull(tx.QueryRow(ctx, `SELECT `+pullColumns+`
        FROM pull_requests p JOIN users u ON u.id=p.author_id WHERE p.id=$1`, id))
	if err != nil {
		return PullRequest{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PullRequest{}, dbErr(err)
	}
	return p, nil
}

// ListPullRequests returns newest numbers first. A zero page/perPage means
// page 1 / 20 items; limits above 100 are rejected.
func (s *Store) ListPullRequests(ctx context.Context, repoID, state string, page, perPage int) ([]PullRequest, bool, error) {
	if state == "" {
		state = "open"
	}
	if page == 0 {
		page = 1
	}
	if perPage == 0 {
		perPage = 20
	}
	if state != "open" && state != "closed" && state != "merged" && state != "all" {
		return nil, false, invalid("state")
	}
	if page < 1 || page > 1_000_000 || perPage < 1 || perPage > 100 {
		return nil, false, invalid("pagination")
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+pullColumns+`
        FROM pull_requests p JOIN users u ON u.id=p.author_id
        WHERE p.repo_id=$1 AND ($2='all' OR p.state=$2)
        ORDER BY p.number DESC LIMIT $3 OFFSET $4`, repoID, state, perPage+1, (page-1)*perPage)
	if err != nil {
		return nil, false, dbErr(err)
	}
	defer rows.Close()
	pulls := make([]PullRequest, 0, perPage)
	for rows.Next() {
		p, err := scanPull(rows)
		if err != nil {
			return nil, false, err
		}
		pulls = append(pulls, p)
	}
	if err := rows.Err(); err != nil {
		return nil, false, dbErr(err)
	}
	hasMore := len(pulls) > perPage
	if hasMore {
		pulls = pulls[:perPage]
	}
	return pulls, hasMore, nil
}

func (s *Store) GetPullRequest(ctx context.Context, repoID string, number int64) (PullRequest, error) {
	if number < 1 {
		return PullRequest{}, invalid("number")
	}
	return scanPull(s.Pool.QueryRow(ctx, `SELECT `+pullColumns+`
        FROM pull_requests p JOIN users u ON u.id=p.author_id
        WHERE p.repo_id=$1 AND p.number=$2`, repoID, number))
}

// ClosePullRequest is idempotent. It rejects merges already completed and
// pending merge attempts, so Git and database cannot race a close request.
func (s *Store) ClosePullRequest(ctx context.Context, repoID string, number int64) (PullRequest, error) {
	if number < 1 {
		return PullRequest{}, invalid("number")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return PullRequest{}, err
	}
	defer tx.Rollback(ctx)
	var id, state string
	err = tx.QueryRow(ctx, `SELECT id::text,state FROM pull_requests
        WHERE repo_id=$1 AND number=$2 FOR UPDATE`, repoID, number).Scan(&id, &state)
	if err != nil {
		return PullRequest{}, dbErr(err)
	}
	if state == "merged" {
		return PullRequest{}, ErrConflict
	}
	var pending bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pull_merge_attempts
        WHERE pull_id=$1 AND status='pending')`, id).Scan(&pending)
	if err != nil {
		return PullRequest{}, dbErr(err)
	}
	if pending {
		return PullRequest{}, ErrConflict
	}
	if state == "open" {
		_, err = tx.Exec(ctx, `UPDATE pull_requests SET state='closed',
            closed_at=clock_timestamp(), updated_at=clock_timestamp() WHERE id=$1`, id)
		if err != nil {
			return PullRequest{}, dbErr(err)
		}
	}
	p, err := scanPull(tx.QueryRow(ctx, `SELECT `+pullColumns+`
        FROM pull_requests p JOIN users u ON u.id=p.author_id WHERE p.id=$1`, id))
	if err != nil {
		return PullRequest{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PullRequest{}, dbErr(err)
	}
	return p, nil
}

// StartPullMerge persists a candidate commit before Git applies its
// compare-and-swap update to the target branch. A pending attempt must be
// reconciled against the actual ref after an uncertain response or process crash.
func (s *Store) StartPullMerge(ctx context.Context, repoID string, number int64, actorID, expectedBaseSHA, expectedHeadSHA, candidateSHA string) (MergeAttempt, error) {
	if number < 1 || !validGitSHA(expectedBaseSHA) || !validGitSHA(expectedHeadSHA) || !validGitSHA(candidateSHA) {
		return MergeAttempt{}, invalid("merge SHAs or number")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return MergeAttempt{}, err
	}
	defer tx.Rollback(ctx)
	var pullID, state string
	err = tx.QueryRow(ctx, `SELECT id::text,state FROM pull_requests
		WHERE repo_id=$1 AND number=$2 FOR UPDATE`, repoID, number).Scan(&pullID, &state)
	if err != nil {
		return MergeAttempt{}, dbErr(err)
	}
	if state != "open" {
		return MergeAttempt{}, ErrConflict
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO pull_merge_attempts
        (pull_id,actor_id,expected_base_sha,expected_head_sha,candidate_sha)
        VALUES ($1,$2,$3,$4,$5) RETURNING id::text`,
		pullID, actorID, expectedBaseSHA, expectedHeadSHA, candidateSHA).Scan(&id)
	if err != nil {
		return MergeAttempt{}, dbErr(err)
	}
	attempt, err := scanMergeAttempt(tx.QueryRow(ctx, `SELECT `+mergeAttemptColumns+`
        FROM pull_merge_attempts a JOIN pull_requests p ON p.id=a.pull_id WHERE a.id=$1`, id))
	if err != nil {
		return MergeAttempt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return MergeAttempt{}, dbErr(err)
	}
	return attempt, nil
}

// FinishPullMerge should be called with succeeded only after Git confirms
// main points at the candidate SHA. Repeating the same outcome is idempotent.
func (s *Store) FinishPullMerge(ctx context.Context, attemptID, result, errorMessage string) (PullRequest, error) {
	if result != "succeeded" && result != "failed" {
		return PullRequest{}, invalid("result")
	}
	if result == "succeeded" {
		errorMessage = ""
	}
	if len(errorMessage) > 2000 || !utf8.ValidString(errorMessage) || strings.ContainsRune(errorMessage, 0) {
		return PullRequest{}, invalid("error message")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return PullRequest{}, err
	}
	defer tx.Rollback(ctx)
	var pullID string
	err = tx.QueryRow(ctx, `SELECT pull_id::text FROM pull_merge_attempts WHERE id=$1`, attemptID).Scan(&pullID)
	if err != nil {
		return PullRequest{}, dbErr(err)
	}
	var pullState string
	err = tx.QueryRow(ctx, `SELECT state FROM pull_requests WHERE id=$1 FOR UPDATE`, pullID).Scan(&pullState)
	if err != nil {
		return PullRequest{}, dbErr(err)
	}
	var attemptState, candidateSHA string
	err = tx.QueryRow(ctx, `SELECT status,candidate_sha FROM pull_merge_attempts
        WHERE id=$1 FOR UPDATE`, attemptID).Scan(&attemptState, &candidateSHA)
	if err != nil {
		return PullRequest{}, dbErr(err)
	}
	if attemptState != "pending" && attemptState != result {
		return PullRequest{}, ErrConflict
	}
	if attemptState == "pending" {
		if pullState != "open" {
			return PullRequest{}, ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE pull_merge_attempts SET status=$2,
            error_message=$3, completed_at=clock_timestamp() WHERE id=$1`, attemptID, result, errorMessage)
		if err != nil {
			return PullRequest{}, dbErr(err)
		}
		if result == "succeeded" {
			_, err = tx.Exec(ctx, `UPDATE pull_requests SET state='merged',
                merged_sha=$2, merged_at=clock_timestamp(), updated_at=clock_timestamp()
                WHERE id=$1`, pullID, candidateSHA)
			if err != nil {
				return PullRequest{}, dbErr(err)
			}
			// Git has already committed main. Keep the PR state and automatic
			// Issue closures in the same database transaction, including startup
			// reconciliation after a crash between the ref update and response.
			_, err = tx.Exec(ctx, `UPDATE issues SET state='closed',
				closed_at=clock_timestamp(), updated_at=clock_timestamp(),
				closed_by_pull_id=$1
				WHERE id IN (SELECT issue_id FROM pull_issue_links
					WHERE pull_id=$1 AND closes_on_merge) AND state='open'`, pullID)
			if err != nil {
				return PullRequest{}, dbErr(err)
			}
		}
	}
	p, err := scanPull(tx.QueryRow(ctx, `SELECT `+pullColumns+`
        FROM pull_requests p JOIN users u ON u.id=p.author_id WHERE p.id=$1`, pullID))
	if err != nil {
		return PullRequest{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PullRequest{}, dbErr(err)
	}
	return p, nil
}

func (s *Store) GetPullMergeAttempt(ctx context.Context, attemptID string) (MergeAttempt, error) {
	return scanMergeAttempt(s.Pool.QueryRow(ctx, `SELECT `+mergeAttemptColumns+`
        FROM pull_merge_attempts a JOIN pull_requests p ON p.id=a.pull_id WHERE a.id=$1`, attemptID))
}

func (s *Store) GetPendingPullMerge(ctx context.Context, repoID string, number int64) (MergeAttempt, error) {
	if number < 1 {
		return MergeAttempt{}, invalid("number")
	}
	return scanMergeAttempt(s.Pool.QueryRow(ctx, `SELECT `+mergeAttemptColumns+`
        FROM pull_merge_attempts a JOIN pull_requests p ON p.id=a.pull_id
        WHERE p.repo_id=$1 AND p.number=$2 AND a.status='pending'`, repoID, number))
}

// ListPendingPullMerges supports bounded startup reconciliation with Git refs.
func (s *Store) ListPendingPullMerges(ctx context.Context, limit int) ([]MergeAttempt, error) {
	if limit < 1 || limit > 1000 {
		return nil, invalid("limit")
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+mergeAttemptColumns+`
        FROM pull_merge_attempts a JOIN pull_requests p ON p.id=a.pull_id
        WHERE a.status='pending' ORDER BY a.started_at,a.id LIMIT $1`, limit)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	attempts := make([]MergeAttempt, 0)
	for rows.Next() {
		a, err := scanMergeAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, a)
	}
	return attempts, dbErr(rows.Err())
}
