package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
)

const (
	testBaseSHA      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHeadSHA      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testCandidateSHA = "cccccccccccccccccccccccccccccccccccccccc"
)

func approveTestPull(t *testing.T, s *Store, repo Repository, pull PullRequest) {
	t.Helper()
	ctx := context.Background()
	reviewer, err := s.UpsertDevUser(ctx, "reviewer@example.test", "Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.RequestJoin(ctx, reviewer.ID, repo.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewJoin(ctx, request.ID, repo.CreatedBy, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantRepoToUser(ctx, repo.ID, reviewer.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePullReview(ctx, repo.ID, pull.Number, reviewer.ID, "approved", "Looks good", testBaseSHA, testHeadSHA); err != nil {
		t.Fatal(err)
	}
}

func TestIssueAndPullShareConcurrentRepositoryNumbers(t *testing.T) {
	s, author, repoA, repoB := issueFixture(t)
	ctx := context.Background()
	const each = 12
	numbers := make(chan int64, each*2)
	errs := make(chan error, each*2)
	var wg sync.WaitGroup
	for n := 0; n < each; n++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			issue, err := s.CreateIssue(ctx, repoA.ID, author.ID, fmt.Sprintf("Issue %d", n), "")
			if err != nil {
				errs <- err
				return
			}
			numbers <- issue.Number
		}(n)
		go func(n int) {
			defer wg.Done()
			pull, err := s.CreatePullRequest(ctx, repoA.ID, author.ID,
				fmt.Sprintf("PR %d", n), "", "main", fmt.Sprintf("feature/pr-%d", n), testBaseSHA, testHeadSHA)
			if err != nil {
				errs <- err
				return
			}
			numbers <- pull.Number
		}(n)
	}
	wg.Wait()
	close(numbers)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	got := make([]int64, 0, each*2)
	for number := range numbers {
		got = append(got, number)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != each*2 {
		t.Fatalf("created %d items; want %d", len(got), each*2)
	}
	for i, number := range got {
		if number != int64(i+1) {
			t.Fatalf("shared sequence: %v", got)
		}
	}
	other, err := s.CreatePullRequest(ctx, repoB.ID, author.ID, "Other repo", "", "main", "feature/other", testBaseSHA, testHeadSHA)
	if err != nil || other.Number != 1 {
		t.Fatalf("other repo independent sequence: %+v, %v", other, err)
	}
	firstPage, more, err := s.ListPullRequests(ctx, repoA.ID, "open", 1, 5)
	if err != nil || len(firstPage) != 5 || !more {
		t.Fatalf("first PR page: %d, more=%v, err=%v", len(firstPage), more, err)
	}
	if firstPage[0].Number <= firstPage[1].Number {
		t.Fatalf("PR list order: %+v", firstPage)
	}
}

func TestPullCloseAndFailedCreateRollbackCounter(t *testing.T) {
	s, author, repo, _ := issueFixture(t)
	ctx := context.Background()
	issue, err := s.CreateIssue(ctx, repo.ID, author.ID, "Issue first", "")
	if err != nil || issue.Number != 1 {
		t.Fatalf("first issue: %+v, %v", issue, err)
	}
	pull, err := s.CreatePullRequest(ctx, repo.ID, author.ID, "  Feature  ", "Body", "main", "feature/one", testBaseSHA, testHeadSHA)
	if err != nil || pull.Number != 2 || pull.Title != "Feature" {
		t.Fatalf("first PR: %+v, %v", pull, err)
	}
	if _, err := s.CreatePullRequest(ctx, repo.ID, author.ID, "Duplicate", "", "main", "feature/one", testBaseSHA, testHeadSHA); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate open branch pair: %v", err)
	}
	next, err := s.CreateIssue(ctx, repo.ID, author.ID, "Issue after conflict", "")
	if err != nil || next.Number != 3 {
		t.Fatalf("counter rollback: %+v, %v", next, err)
	}
	if _, err := s.CreatePullRequest(ctx, repo.ID, author.ID, "Bad SHA", "", "main", "feature/bad", "invalid", testHeadSHA); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid SHA: %v", err)
	}
	closed, err := s.ClosePullRequest(ctx, repo.ID, pull.Number)
	if err != nil || closed.State != "closed" || closed.ClosedAt == nil {
		t.Fatalf("close: %+v, %v", closed, err)
	}
	again, err := s.ClosePullRequest(ctx, repo.ID, pull.Number)
	if err != nil || !again.UpdatedAt.Equal(closed.UpdatedAt) {
		t.Fatalf("idempotent close: %+v, %v", again, err)
	}
	if _, err := s.StartPullMerge(ctx, repo.ID, pull.Number, author.ID, testBaseSHA, testHeadSHA, testCandidateSHA); !errors.Is(err, ErrConflict) {
		t.Fatalf("merge closed PR: %v", err)
	}
	result, more, err := s.ListPullRequests(ctx, repo.ID, "closed", 0, 0)
	if err != nil || more || len(result) != 1 || result[0].Number != pull.Number {
		t.Fatalf("closed PR list: %+v, more=%v, err=%v", result, more, err)
	}
	if got, err := s.GetPullRequest(ctx, repo.ID, pull.Number); err != nil || got.State != "closed" {
		t.Fatalf("get PR: %+v, %v", got, err)
	}
}

func TestPullMergeAttemptRecoveryAndIdempotency(t *testing.T) {
	s, author, repo, _ := issueFixture(t)
	ctx := context.Background()
	pull, err := s.CreatePullRequest(ctx, repo.ID, author.ID, "Merge me", "", "main", "feature/merge", testBaseSHA, testHeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.StartPullMerge(ctx, repo.ID, pull.Number, author.ID, testBaseSHA, testHeadSHA, testCandidateSHA)
	if err != nil || first.Status != "pending" || first.CandidateSHA != testCandidateSHA {
		t.Fatalf("start merge: %+v, %v", first, err)
	}
	if _, err := s.StartPullMerge(ctx, repo.ID, pull.Number, author.ID, testBaseSHA, testHeadSHA, testCandidateSHA); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate pending attempt: %v", err)
	}
	if _, err := s.ClosePullRequest(ctx, repo.ID, pull.Number); !errors.Is(err, ErrConflict) {
		t.Fatalf("close with pending merge: %v", err)
	}
	pending, err := s.GetPendingPullMerge(ctx, repo.ID, pull.Number)
	if err != nil || pending.ID != first.ID {
		t.Fatalf("get pending: %+v, %v", pending, err)
	}
	listed, err := s.ListPendingPullMerges(ctx, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != first.ID {
		t.Fatalf("list pending: %+v, %v", listed, err)
	}
	stillOpen, err := s.FinishPullMerge(ctx, first.ID, "failed", "base moved")
	if err != nil || stillOpen.State != "open" {
		t.Fatalf("failed merge: %+v, %v", stillOpen, err)
	}
	failed, err := s.GetPullMergeAttempt(ctx, first.ID)
	if err != nil || failed.Status != "failed" || failed.ErrorMessage != "base moved" || failed.CompletedAt == nil {
		t.Fatalf("failed attempt: %+v, %v", failed, err)
	}
	second, err := s.StartPullMerge(ctx, repo.ID, pull.Number, author.ID, testBaseSHA, testHeadSHA, testCandidateSHA)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := s.FinishPullMerge(ctx, second.ID, "succeeded", "")
	if err != nil || merged.State != "merged" || merged.MergedSHA == nil || *merged.MergedSHA != testCandidateSHA || merged.MergedAt == nil {
		t.Fatalf("merge result: %+v, %v", merged, err)
	}
	again, err := s.FinishPullMerge(ctx, second.ID, "succeeded", "")
	if err != nil || !again.UpdatedAt.Equal(merged.UpdatedAt) {
		t.Fatalf("idempotent success: %+v, %v", again, err)
	}
	if _, err := s.FinishPullMerge(ctx, first.ID, "succeeded", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("change failed result to success: %v", err)
	}
	if _, err := s.GetPendingPullMerge(ctx, repo.ID, pull.Number); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pending after merge: %v", err)
	}
	if _, err := s.ClosePullRequest(ctx, repo.ID, pull.Number); !errors.Is(err, ErrConflict) {
		t.Fatalf("close merged PR: %v", err)
	}
	if rows, more, err := s.ListPullRequests(ctx, repo.ID, "merged", 1, 20); err != nil || more || len(rows) != 1 {
		t.Fatalf("merged list: %+v, more=%v, err=%v", rows, more, err)
	}
}
