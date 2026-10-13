package store

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type PullReview struct {
	ID           string    `json:"id"`
	PullID       string    `json:"pullId"`
	ReviewerID   string    `json:"reviewerId"`
	ReviewerName string    `json:"reviewerName"`
	State        string    `json:"state"`
	Body         string    `json:"body"`
	BaseSHA      string    `json:"baseSha"`
	HeadSHA      string    `json:"headSha"`
	CreatedAt    time.Time `json:"createdAt"`
}

type ReviewSummary struct {
	Approvals        int `json:"approvals"`
	ChangesRequested int `json:"changesRequested"`
}

const reviewColumns = `v.id::text,v.pull_id::text,v.reviewer_id::text,u.name,
    v.state,v.body,v.base_sha,v.head_sha,v.created_at`

func scanReview(row scanner) (PullReview, error) {
	var review PullReview
	err := row.Scan(&review.ID, &review.PullID, &review.ReviewerID,
		&review.ReviewerName, &review.State, &review.Body,
		&review.BaseSHA, &review.HeadSHA, &review.CreatedAt)
	return review, dbErr(err)
}

// CreatePullReview serializes with StartPullMerge on the PR row. Once a merge
// attempt is pending, no new review can change its decision before Git CAS.
func (s *Store) CreatePullReview(ctx context.Context, repoID string, number int64, reviewerID, state, body, baseSHA, headSHA string) (PullReview, error) {
	body = strings.TrimSpace(body)
	if number < 1 || (state != "approved" && state != "changes_requested") ||
		!utf8.ValidString(body) || strings.ContainsRune(body, 0) || len(body) > 65536 ||
		!validGitSHA(baseSHA) || !validGitSHA(headSHA) {
		return PullReview{}, invalid("review fields")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return PullReview{}, err
	}
	defer tx.Rollback(ctx)
	var pullID, authorID, pullState string
	err = tx.QueryRow(ctx, `SELECT id::text,author_id::text,state FROM pull_requests
		WHERE repo_id=$1 AND number=$2 FOR UPDATE`, repoID, number).Scan(&pullID, &authorID, &pullState)
	if err != nil {
		return PullReview{}, dbErr(err)
	}
	if authorID == reviewerID {
		return PullReview{}, ErrForbidden
	}
	if pullState != "open" {
		return PullReview{}, ErrConflict
	}
	var active, pending bool
	err = tx.QueryRow(ctx, `SELECT status='active' FROM users WHERE id=$1`, reviewerID).Scan(&active)
	if err != nil {
		return PullReview{}, dbErr(err)
	}
	if !active {
		return PullReview{}, ErrForbidden
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pull_merge_attempts
		WHERE pull_id=$1 AND status='pending')`, pullID).Scan(&pending)
	if err != nil {
		return PullReview{}, dbErr(err)
	}
	if pending {
		return PullReview{}, ErrConflict
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO pull_reviews
		(pull_id,reviewer_id,state,body,base_sha,head_sha,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,clock_timestamp()) RETURNING id::text`,
		pullID, reviewerID, state, body, baseSHA, headSHA).Scan(&id)
	if err != nil {
		return PullReview{}, dbErr(err)
	}
	review, err := scanReview(tx.QueryRow(ctx, `SELECT `+reviewColumns+`
		FROM pull_reviews v JOIN users u ON u.id=v.reviewer_id WHERE v.id=$1`, id))
	if err != nil {
		return PullReview{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PullReview{}, dbErr(err)
	}
	return review, nil
}

func (s *Store) ListPullReviews(ctx context.Context, repoID string, number int64, page, perPage int) ([]PullReview, bool, error) {
	if number < 1 || page < 1 || page > 1_000_000 || perPage < 1 || perPage > 100 {
		return nil, false, invalid("pagination")
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+reviewColumns+`
		FROM pull_requests p JOIN pull_reviews v ON v.pull_id=p.id
		JOIN users u ON u.id=v.reviewer_id
		WHERE p.repo_id=$1 AND p.number=$2
		ORDER BY v.created_at,v.id LIMIT $3 OFFSET $4`,
		repoID, number, perPage+1, (page-1)*perPage)
	if err != nil {
		return nil, false, dbErr(err)
	}
	defer rows.Close()
	reviews := make([]PullReview, 0, perPage)
	for rows.Next() {
		review, err := scanReview(rows)
		if err != nil {
			return nil, false, err
		}
		reviews = append(reviews, review)
	}
	if err := rows.Err(); err != nil {
		return nil, false, dbErr(err)
	}
	hasMore := len(reviews) > perPage
	if hasMore {
		reviews = reviews[:perPage]
	}
	return reviews, hasMore, nil
}

// The newest decision of each reviewer counts only for the exact base/head
// under test. Suspended or no-longer-authorized reviewers are excluded.
const reviewSummarySQL = `WITH latest AS (
    SELECT DISTINCT ON (v.reviewer_id) v.reviewer_id,v.state,v.base_sha,v.head_sha
    FROM pull_reviews v WHERE v.pull_id=$1
    ORDER BY v.reviewer_id,v.created_at DESC,v.id DESC
)
SELECT count(*) FILTER (WHERE l.state='approved')::int,
       count(*) FILTER (WHERE l.state='changes_requested')::int
FROM latest l JOIN users u ON u.id=l.reviewer_id
WHERE u.status='active' AND l.reviewer_id<>$5
    AND l.base_sha=$2 AND l.head_sha=$3
    AND EXISTS (SELECT 1 FROM team_members tm_any WHERE tm_any.user_id=l.reviewer_id)
    AND EXISTS (
        SELECT 1 FROM repositories repo WHERE repo.id=$4 AND repo.state='active'
        AND (
            repo.created_by=l.reviewer_id
            OR EXISTS (SELECT 1 FROM repo_grants g WHERE g.repo_id=repo.id
                AND g.user_id=l.reviewer_id AND g.role IN ('write','admin'))
            OR EXISTS (SELECT 1 FROM repo_grants g JOIN team_members tm ON tm.team_id=g.team_id
                WHERE g.repo_id=repo.id AND tm.user_id=l.reviewer_id AND g.role IN ('write','admin'))
        )
    )`

// reviewSummary reports current review feedback for one revision. Reviews are
// informational and never gate a merge.
func reviewSummary(ctx context.Context, tx pgx.Tx, pullID, repoID, authorID, baseSHA, headSHA string) (ReviewSummary, error) {
	var result ReviewSummary
	err := tx.QueryRow(ctx, reviewSummarySQL, pullID, baseSHA, headSHA, repoID, authorID).
		Scan(&result.Approvals, &result.ChangesRequested)
	return result, dbErr(err)
}

// PullReviewSummary reports current review feedback for one revision.
func (s *Store) PullReviewSummary(ctx context.Context, repoID string, number int64, baseSHA, headSHA string) (ReviewSummary, error) {
	if number < 1 || !validGitSHA(baseSHA) || !validGitSHA(headSHA) {
		return ReviewSummary{}, invalid("review summary fields")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ReviewSummary{}, err
	}
	defer tx.Rollback(ctx)
	var pullID, authorID string
	err = tx.QueryRow(ctx, `SELECT id::text,author_id::text FROM pull_requests
		WHERE repo_id=$1 AND number=$2`, repoID, number).Scan(&pullID, &authorID)
	if err != nil {
		return ReviewSummary{}, dbErr(err)
	}
	return reviewSummary(ctx, tx, pullID, repoID, authorID, baseSHA, headSHA)
}
