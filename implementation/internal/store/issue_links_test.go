package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseIssueReferences(t *testing.T) {
	refs, err := ParseIssueReferences("" +
		"This change fixes an example, not an Issue.\n" +
		"Refs #1\n" +
		"Fixes #1, #2.\n" +
		"> Closes #3\n" +
		"```text\nResolves #4\n```\n" +
		"- Closes #5\n" +
		"references #6!\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []IssueReference{{1, true}, {2, true}, {6, false}}
	if len(refs) != len(want) {
		t.Fatalf("refs=%+v, want %+v", refs, want)
	}
	for i := range refs {
		if refs[i] != want[i] {
			t.Fatalf("refs=%+v, want %+v", refs, want)
		}
	}
	if _, err := ParseIssueReferences("Fixes #999999999999999999999999999999"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflowing Issue number: %v", err)
	}
	var tooMany strings.Builder
	for n := 1; n <= 101; n++ {
		fmt.Fprintf(&tooMany, "Refs #%d\n", n)
	}
	if _, err := ParseIssueReferences(tooMany.String()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too many references: %v", err)
	}
}

func TestIssueCommentsAndMergeClosure(t *testing.T) {
	s, author, repo, _ := issueFixture(t)
	ctx := context.Background()
	reference, err := s.CreateIssue(ctx, repo.ID, author.ID, "Context", "")
	if err != nil {
		t.Fatal(err)
	}
	fix, err := s.CreateIssue(ctx, repo.ID, author.ID, "Fix me", "")
	if err != nil {
		t.Fatal(err)
	}
	comment, err := s.CreateIssueComment(ctx, fix.ID, author.ID, "  Reproduced  ")
	if err != nil || comment.Body != "Reproduced" || comment.AuthorName != author.Name {
		t.Fatalf("comment: %+v, %v", comment, err)
	}
	comments, more, err := s.ListIssueComments(ctx, fix.ID, 1, 20)
	if err != nil || more || len(comments) != 1 || comments[0].ID != comment.ID {
		t.Fatalf("comments: %+v more=%v err=%v", comments, more, err)
	}
	if _, err := s.CreateIssueComment(ctx, fix.ID, author.ID, "  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty comment: %v", err)
	}
	if _, err := s.CreatePullRequest(ctx, repo.ID, author.ID, "Bad reference", "Fixes #999", "main", "feature/missing", testBaseSHA, testHeadSHA); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing Issue reference: %v", err)
	}
	pull, err := s.CreatePullRequest(ctx, repo.ID, author.ID, "Fix", "Refs #1\nFixes #2", "main", "feature/fix", testBaseSHA, testHeadSHA)
	if err != nil || pull.Number != 3 {
		t.Fatalf("PR creation/counter rollback: %+v, %v", pull, err)
	}
	approveTestPull(t, s, repo, pull)
	links, err := s.ListPullIssueLinks(ctx, repo.ID, pull.Number)
	if err != nil || len(links) != 2 || links[0].Number != reference.Number || links[0].ClosesOnMerge ||
		links[1].Number != fix.Number || !links[1].ClosesOnMerge {
		t.Fatalf("links: %+v, %v", links, err)
	}
	failed, err := s.StartPullMerge(ctx, repo.ID, pull.Number, author.ID, testBaseSHA, testHeadSHA, testCandidateSHA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishPullMerge(ctx, failed.ID, "failed", "conflict"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetIssue(ctx, repo.ID, fix.Number); err != nil || got.State != "open" {
		t.Fatalf("failed merge closed Issue: %+v, %v", got, err)
	}
	success, err := s.StartPullMerge(ctx, repo.ID, pull.Number, author.ID, testBaseSHA, testHeadSHA, testCandidateSHA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishPullMerge(ctx, success.ID, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetIssue(ctx, repo.ID, reference.Number); err != nil || got.State != "open" {
		t.Fatalf("reference-only Issue closed: %+v, %v", got, err)
	}
	closed, err := s.GetIssue(ctx, repo.ID, fix.Number)
	if err != nil || closed.State != "closed" || closed.ClosedByPullNumber == nil || *closed.ClosedByPullNumber != pull.Number {
		t.Fatalf("Issue not closed by successful PR: %+v, %v", closed, err)
	}
	if _, err := s.FinishPullMerge(ctx, success.ID, "succeeded", ""); err != nil {
		t.Fatalf("idempotent finish: %v", err)
	}
	reopened, err := s.SetIssueState(ctx, repo.ID, fix.Number, "open")
	if err != nil || reopened.ClosedByPullNumber != nil || reopened.ClosedAt != nil {
		t.Fatalf("reopen metadata: %+v, %v", reopened, err)
	}
}
