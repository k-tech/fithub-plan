package store

import (
	"context"
	"errors"
	"testing"
)

// reviewCommentFixture creates a pull request plus a second account with write
// access, which is what an inline comment needs.
func reviewCommentFixture(t *testing.T) (*Store, User, User, Repository, PullRequest) {
	t.Helper()
	s, author, repo, _ := issueFixture(t)
	ctx := context.Background()
	reviewer, err := s.UpsertDevUser(ctx, "commenter@example.test", "Commenter")
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.RequestJoin(ctx, reviewer.ID, repo.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewJoin(ctx, request.ID, author.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantRepoToUser(ctx, repo.ID, reviewer.ID, "write"); err != nil {
		t.Fatal(err)
	}
	pull, err := s.CreatePullRequest(ctx, repo.ID, author.ID, "Inline review", "", "main", "feature/inline", testBaseSHA, testHeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	return s, author, reviewer, repo, pull
}

func TestCreatePullReviewCommentStoresAnchor(t *testing.T) {
	s, _, reviewer, repo, pull := reviewCommentFixture(t)
	ctx := context.Background()
	comment, err := s.CreatePullReviewComment(ctx, repo.ID, pull.Number, reviewer.ID,
		"", "internal/a.go", "new", "please rename this", testBaseSHA, testHeadSHA, 12)
	if err != nil {
		t.Fatal(err)
	}
	if comment.Path != "internal/a.go" || comment.Line != 12 || comment.Side != "new" || comment.ReplyToID != nil {
		t.Fatalf("unexpected comment: %+v", comment)
	}
	if comment.AuthorName != "Commenter" {
		t.Fatalf("author name not joined: %q", comment.AuthorName)
	}
	// A reply in the same file is accepted and keeps its parent.
	reply, err := s.CreatePullReviewComment(ctx, repo.ID, pull.Number, reviewer.ID,
		comment.ID, "internal/a.go", "new", "agreed", testBaseSHA, testHeadSHA, 12)
	if err != nil {
		t.Fatal(err)
	}
	if reply.ReplyToID == nil || *reply.ReplyToID != comment.ID {
		t.Fatalf("reply parent not stored: %+v", reply.ReplyToID)
	}
	// A reply pointing at a different file is refused so a thread cannot be
	// attached to an unrelated diff line.
	if _, err := s.CreatePullReviewComment(ctx, repo.ID, pull.Number, reviewer.ID,
		comment.ID, "other/file.go", "new", "off topic", testBaseSHA, testHeadSHA, 3); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-file reply: %v", err)
	}
}

func TestCreatePullReviewCommentValidatesAnchorFields(t *testing.T) {
	s, _, reviewer, repo, pull := reviewCommentFixture(t)
	ctx := context.Background()
	bad := []struct {
		name string
		path string
		side string
		line int
		body string
	}{
		{"empty body", "a.go", "new", 1, "   "},
		{"absolute path", "/etc/passwd", "new", 1, "x"},
		{"traversal", "../../etc/passwd", "new", 1, "x"},
		{"backslash", `internal\a.go`, "new", 1, "x"},
		{"unknown side", "a.go", "middle", 1, "x"},
		{"zero line", "a.go", "new", 0, "x"},
		{"negative line", "a.go", "new", -5, "x"},
	}
	for _, tc := range bad {
		if _, err := s.CreatePullReviewComment(ctx, repo.ID, pull.Number, reviewer.ID,
			"", tc.path, tc.side, tc.body, testBaseSHA, testHeadSHA, tc.line); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", tc.name, err)
		}
	}
}

func TestListPullReviewCommentsFlagsOutdated(t *testing.T) {
	s, _, reviewer, repo, pull := reviewCommentFixture(t)
	ctx := context.Background()
	if _, err := s.CreatePullReviewComment(ctx, repo.ID, pull.Number, reviewer.ID,
		"", "internal/a.go", "new", "old revision", testBaseSHA, testHeadSHA, 4); err != nil {
		t.Fatal(err)
	}
	current, err := s.ListPullReviewComments(ctx, repo.ID, pull.Number, testBaseSHA, testHeadSHA, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Comments) != 1 || current.Comments[0].Outdated || current.TotalOutdate != 0 {
		t.Fatalf("current revision must not be outdated: %+v", current)
	}
	newHead := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	moved, err := s.ListPullReviewComments(ctx, repo.ID, pull.Number, testBaseSHA, newHead, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved.Comments) != 1 || !moved.Comments[0].Outdated || moved.TotalOutdate != 1 {
		t.Fatalf("comment must be outdated after new commits: %+v", moved.Comments)
	}
	if moved.CurrentHead != newHead {
		t.Fatalf("current head not reported: %q", moved.CurrentHead)
	}
}

func TestCreatePullReviewCommentRequiresOpenPullRequest(t *testing.T) {
	s, author, reviewer, repo, pull := reviewCommentFixture(t)
	ctx := context.Background()
	if _, err := s.ClosePullRequest(ctx, repo.ID, pull.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePullReviewComment(ctx, repo.ID, pull.Number, reviewer.ID,
		"", "internal/a.go", "new", "too late", testBaseSHA, testHeadSHA, 4); !errors.Is(err, ErrConflict) {
		t.Fatalf("closed pull request accepted comment: %v", err)
	}
	if _, err := s.CreatePullReviewComment(ctx, repo.ID, 999, author.ID,
		"", "internal/a.go", "new", "missing", testBaseSHA, testHeadSHA, 4); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing pull request: %v", err)
	}
}
