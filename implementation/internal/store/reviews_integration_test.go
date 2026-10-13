package store

import (
	"context"
	"testing"
)

func TestPullReviewsDoNotGateMerge(t *testing.T) {
	s, author, repo, _ := issueFixture(t)
	ctx := context.Background()
	pull, err := s.CreatePullRequest(ctx, repo.ID, author.ID, "Merge without review", "", "main", "feature/review", testBaseSHA, testHeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := s.StartPullMerge(ctx, repo.ID, pull.Number, author.ID, testBaseSHA, testHeadSHA, testCandidateSHA)
	if err != nil {
		t.Fatalf("merge without a review: %v", err)
	}
	if _, err := s.FinishPullMerge(ctx, attempt.ID, "failed", "test cleanup"); err != nil {
		t.Fatal(err)
	}

	reviewer, err := s.UpsertDevUser(ctx, "reviewer@example.test", "Reviewer")
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
	if _, err := s.CreatePullReview(ctx, repo.ID, pull.Number, reviewer.ID, "changes_requested", "Please revise", testBaseSHA, testHeadSHA); err != nil {
		t.Fatal(err)
	}
	attempt, err = s.StartPullMerge(ctx, repo.ID, pull.Number, author.ID, testBaseSHA, testHeadSHA, testCandidateSHA)
	if err != nil {
		t.Fatalf("merge despite optional change request: %v", err)
	}
	if _, err := s.FinishPullMerge(ctx, attempt.ID, "failed", "test cleanup"); err != nil {
		t.Fatal(err)
	}
}
