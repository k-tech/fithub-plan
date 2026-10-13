package store

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Issue references use a deliberately narrow, line-oriented syntax. This
// avoids treating examples in prose, quotes, or fenced code as merge commands.
// Supported lines: "Refs #12", "Fixes #12, #13", "Closes #12", and
// "Resolves #12" (case-insensitive). An unknown issue is rejected at create.
type IssueReference struct {
	Number        int64 `json:"number"`
	ClosesOnMerge bool  `json:"closesOnMerge"`
}

var referenceLine = regexp.MustCompile(`(?i)^(refs|references|fixes|closes|resolves)\s+((?:#[1-9][0-9]*\s*,\s*)*#[1-9][0-9]*)\s*[.!]?$`)
var referenceNumber = regexp.MustCompile(`#[1-9][0-9]*`)

func ParseIssueReferences(body string) ([]IssueReference, error) {
	refs := make([]IssueReference, 0)
	seen := make(map[int64]int)
	inFence := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(line, ">") || strings.HasPrefix(line, "-") {
			continue
		}
		match := referenceLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		closes := strings.EqualFold(match[1], "fixes") || strings.EqualFold(match[1], "closes") || strings.EqualFold(match[1], "resolves")
		for _, rawNumber := range referenceNumber.FindAllString(match[2], -1) {
			number, err := strconv.ParseInt(strings.TrimPrefix(rawNumber, "#"), 10, 64)
			if err != nil || number < 1 {
				return nil, invalid("issue reference number")
			}
			if index, ok := seen[number]; ok {
				refs[index].ClosesOnMerge = refs[index].ClosesOnMerge || closes
				continue
			}
			if len(refs) >= 100 {
				return nil, invalid("too many issue references")
			}
			seen[number] = len(refs)
			refs = append(refs, IssueReference{Number: number, ClosesOnMerge: closes})
		}
	}
	return refs, nil
}

func insertPullIssueLinks(ctx context.Context, tx pgx.Tx, pullID, repoID string, refs []IssueReference) error {
	for _, ref := range refs {
		result, err := tx.Exec(ctx, `INSERT INTO pull_issue_links (pull_id,issue_id,closes_on_merge)
			SELECT $1,i.id,$4 FROM issues i WHERE i.repo_id=$2 AND i.number=$3`,
			pullID, repoID, ref.Number, ref.ClosesOnMerge)
		if err != nil {
			return dbErr(err)
		}
		if result.RowsAffected() != 1 {
			return invalid("referenced issue does not exist in this repository")
		}
	}
	return nil
}

type PullIssueLink struct {
	Number        int64  `json:"number"`
	Title         string `json:"title"`
	State         string `json:"state"`
	ClosesOnMerge bool   `json:"closesOnMerge"`
}

func (s *Store) ListPullIssueLinks(ctx context.Context, repoID string, pullNumber int64) ([]PullIssueLink, error) {
	rows, err := s.Pool.Query(ctx, `SELECT i.number,i.title,i.state,l.closes_on_merge
		FROM pull_requests p JOIN pull_issue_links l ON l.pull_id=p.id
		JOIN issues i ON i.id=l.issue_id
		WHERE p.repo_id=$1 AND p.number=$2 ORDER BY i.number`, repoID, pullNumber)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	links := make([]PullIssueLink, 0)
	for rows.Next() {
		var link PullIssueLink
		if err := rows.Scan(&link.Number, &link.Title, &link.State, &link.ClosesOnMerge); err != nil {
			return nil, dbErr(err)
		}
		links = append(links, link)
	}
	return links, dbErr(rows.Err())
}

type IssueComment struct {
	ID         string    `json:"id"`
	IssueID    string    `json:"issueId"`
	AuthorID   string    `json:"authorId"`
	AuthorName string    `json:"authorName"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

const issueCommentColumns = `c.id::text,c.issue_id::text,c.author_id::text,u.name,c.body,c.created_at,c.updated_at`

func scanIssueComment(row scanner) (IssueComment, error) {
	var comment IssueComment
	err := row.Scan(&comment.ID, &comment.IssueID, &comment.AuthorID,
		&comment.AuthorName, &comment.Body, &comment.CreatedAt, &comment.UpdatedAt)
	return comment, dbErr(err)
}

func (s *Store) CreateIssueComment(ctx context.Context, issueID, authorID, body string) (IssueComment, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 65536 || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return IssueComment{}, invalid("comment body")
	}
	var id string
	err := s.Pool.QueryRow(ctx, `INSERT INTO issue_comments (issue_id,author_id,body)
		VALUES ($1,$2,$3) RETURNING id::text`, issueID, authorID, body).Scan(&id)
	if err != nil {
		return IssueComment{}, dbErr(err)
	}
	return scanIssueComment(s.Pool.QueryRow(ctx, `SELECT `+issueCommentColumns+`
		FROM issue_comments c JOIN users u ON u.id=c.author_id WHERE c.id=$1`, id))
}

func (s *Store) ListIssueComments(ctx context.Context, issueID string, page, perPage int) ([]IssueComment, bool, error) {
	if page < 1 || page > 1_000_000 || perPage < 1 || perPage > 100 {
		return nil, false, invalid("pagination")
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+issueCommentColumns+`
		FROM issue_comments c JOIN users u ON u.id=c.author_id
		WHERE c.issue_id=$1 ORDER BY c.created_at,c.id LIMIT $2 OFFSET $3`,
		issueID, perPage+1, (page-1)*perPage)
	if err != nil {
		return nil, false, dbErr(err)
	}
	defer rows.Close()
	comments := make([]IssueComment, 0, perPage)
	for rows.Next() {
		comment, err := scanIssueComment(rows)
		if err != nil {
			return nil, false, err
		}
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		return nil, false, dbErr(err)
	}
	hasMore := len(comments) > perPage
	if hasMore {
		comments = comments[:perPage]
	}
	return comments, hasMore, nil
}
