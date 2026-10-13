package store

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

// PullReviewComment is one inline review comment on a pull request diff. The
// anchor is a path plus a line number on the chosen side, together with the
// exact base/head revisions the author was looking at.
type PullReviewComment struct {
	ID         string    `json:"id"`
	PullID     string    `json:"pullId"`
	AuthorID   string    `json:"authorId"`
	AuthorName string    `json:"authorName"`
	ReplyToID  *string   `json:"replyToId"`
	Path       string    `json:"path"`
	Line       int       `json:"line"`
	Side       string    `json:"side"`
	Body       string    `json:"body"`
	BaseSHA    string    `json:"baseSha"`
	HeadSHA    string    `json:"headSha"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	// Outdated is computed per request, not stored: true when the comment was
	// written against a revision that is no longer the pull request head.
	Outdated bool `json:"outdated"`
}

const reviewCommentColumns = `c.id::text,c.pull_id::text,c.author_id::text,u.name,
    c.reply_to_id::text,c.path,c.line,c.side,c.body,c.base_sha,c.head_sha,
    c.created_at,c.updated_at`

func scanReviewComment(row scanner) (PullReviewComment, error) {
	var comment PullReviewComment
	err := row.Scan(&comment.ID, &comment.PullID, &comment.AuthorID,
		&comment.AuthorName, &comment.ReplyToID, &comment.Path, &comment.Line,
		&comment.Side, &comment.Body, &comment.BaseSHA, &comment.HeadSHA,
		&comment.CreatedAt, &comment.UpdatedAt)
	return comment, dbErr(err)
}

// validCommentPath rejects absolute paths, parent traversal, control
// characters, and backslashes. Comment paths must match the diff's relative
// repository paths exactly, so anything Git would not print is refused.
func validCommentPath(path string) bool {
	if path == "" || len(path) > 4096 || !utf8.ValidString(path) ||
		strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") ||
		strings.Contains(path, "\\") || strings.Contains(path, "..") {
		return false
	}
	for _, char := range path {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validReviewSide(side string) bool {
	return side == "new" || side == "old"
}

// CreatePullReviewComment stores one inline comment or reply. Commenting
// requires write access, which the caller authorizes; this function only
// enforces data integrity and pull-request state.
func (s *Store) CreatePullReviewComment(ctx context.Context, repoID string, number int64, authorID, replyToID, path, side, body, baseSHA, headSHA string, line int) (PullReviewComment, error) {
	body = strings.TrimSpace(body)
	if number < 1 || !validCommentPath(path) || !validReviewSide(side) ||
		line < 1 || line > 10_000_000 ||
		!utf8.ValidString(body) || strings.ContainsRune(body, 0) || len(body) > 65536 ||
		!validGitSHA(baseSHA) || !validGitSHA(headSHA) {
		return PullReviewComment{}, invalid("review comment fields")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return PullReviewComment{}, err
	}
	defer tx.Rollback(ctx)
	var pullID, pullState string
	err = tx.QueryRow(ctx, `SELECT id::text,state FROM pull_requests
		WHERE repo_id=$1 AND number=$2 FOR SHARE`, repoID, number).Scan(&pullID, &pullState)
	if err != nil {
		return PullReviewComment{}, dbErr(err)
	}
	if pullState != "open" {
		return PullReviewComment{}, ErrConflict
	}
	// A reply must belong to the same pull request and the same file; otherwise
	// a thread could be attached to an unrelated diff line.
	if replyToID != "" {
		var parentPath string
		err = tx.QueryRow(ctx, `SELECT path FROM pull_review_comments
			WHERE id=$1 AND pull_id=$2`, replyToID, pullID).Scan(&parentPath)
		if err != nil {
			return PullReviewComment{}, dbErr(err)
		}
		if parentPath != path {
			return PullReviewComment{}, invalid("reply path")
		}
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO pull_review_comments
		(pull_id,author_id,reply_to_id,path,line,side,body,base_sha,head_sha,created_at,updated_at)
		VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9,clock_timestamp(),clock_timestamp())
		RETURNING id::text`,
		pullID, authorID, replyToID, path, line, side, body, baseSHA, headSHA).Scan(&id)
	if err != nil {
		return PullReviewComment{}, dbErr(err)
	}
	comment, err := scanReviewComment(tx.QueryRow(ctx, `SELECT `+reviewCommentColumns+`
		FROM pull_review_comments c JOIN users u ON u.id=c.author_id WHERE c.id=$1`, id))
	if err != nil {
		return PullReviewComment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PullReviewComment{}, dbErr(err)
	}
	return comment, nil
}

// ReviewCommentPage is one page of inline comments. Each comment carries the
// head revision it targets, and the caller compares it with the current head to
// flag outdated threads.
type ReviewCommentPage struct {
	Comments     []PullReviewComment
	HasMore      bool
	CurrentHead  string
	CurrentBase  string
	TotalOutdate int
}

// ListPullReviewComments returns inline comments ordered by file, line, and
// creation time so the UI can group a thread without extra sorting. The current
// head and base revisions come from the caller, which resolves them through the
// same path the diff view uses.
func (s *Store) ListPullReviewComments(ctx context.Context, repoID string, number int64, currentBase, currentHead string, page, perPage int) (ReviewCommentPage, error) {
	if number < 1 || page < 1 || page > 1_000_000 || perPage < 1 || perPage > 200 {
		return ReviewCommentPage{}, invalid("pagination")
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+reviewCommentColumns+`
		FROM pull_requests p JOIN pull_review_comments c ON c.pull_id=p.id
		JOIN users u ON u.id=c.author_id
		WHERE p.repo_id=$1 AND p.number=$2
		ORDER BY c.path, c.line, c.created_at, c.id
		LIMIT $3 OFFSET $4`,
		repoID, number, perPage+1, (page-1)*perPage)
	if err != nil {
		return ReviewCommentPage{}, dbErr(err)
	}
	defer rows.Close()
	comments := make([]PullReviewComment, 0, perPage)
	outdated := 0
	for rows.Next() {
		comment, err := scanReviewComment(rows)
		if err != nil {
			return ReviewCommentPage{}, err
		}
		comment.Outdated = comment.HeadSHA != currentHead
		if comment.Outdated {
			outdated++
		}
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		return ReviewCommentPage{}, dbErr(err)
	}
	hasMore := len(comments) > perPage
	if hasMore {
		comments = comments[:perPage]
	}
	return ReviewCommentPage{Comments: comments, HasMore: hasMore, CurrentHead: currentHead, CurrentBase: currentBase, TotalOutdate: outdated}, nil
}
