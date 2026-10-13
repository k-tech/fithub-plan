package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

func issueFixture(t *testing.T) (*Store, User, Repository, Repository) {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	author, err := s.UpsertDevUser(ctx, "issues@example.test", "Issue Author")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserRole(ctx, author.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, "issues-team", "Issues Team")
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.RequestJoin(ctx, author.ID, team.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewJoin(ctx, request.ID, author.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	repoA, err := s.CreateRepo(ctx, team.ID, "one", "private", author.ID)
	if err != nil {
		t.Fatal(err)
	}
	repoB, err := s.CreateRepo(ctx, team.ID, "two", "private", author.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range []Repository{repoA, repoB} {
		if err := s.SetRepoState(ctx, repo.ID, "active"); err != nil {
			t.Fatal(err)
		}
	}
	return s, author, repoA, repoB
}

func TestIssuesConcurrentNumberingPaginationAndState(t *testing.T) {
	s, author, repoA, repoB := issueFixture(t)
	ctx := context.Background()
	const count = 16
	numbers := make(chan int64, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for n := 0; n < count; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			issue, err := s.CreateIssue(ctx, repoA.ID, author.ID, fmt.Sprintf("Concurrent %d", n), "A body")
			if err != nil {
				errs <- err
				return
			}
			if issue.AuthorID != author.ID || issue.AuthorName != author.Name || issue.Body != "A body" {
				errs <- fmt.Errorf("issue author/body mismatch: %+v", issue)
				return
			}
			numbers <- issue.Number
		}(n)
	}
	wg.Wait()
	close(numbers)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var got []int64
	for number := range numbers {
		got = append(got, number)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != count {
		t.Fatalf("created %d issues; want %d", len(got), count)
	}
	for i, number := range got {
		if number != int64(i+1) {
			t.Fatalf("number sequence %v", got)
		}
	}
	other, err := s.CreateIssue(ctx, repoB.ID, author.ID, "Independent number", "")
	if err != nil || other.Number != 1 {
		t.Fatalf("second repository numbering: %+v, %v", other, err)
	}
	firstPage, more, err := s.ListIssues(ctx, repoA.ID, IssueFilter{State: "open"}, 1, 5)
	if err != nil || !more || len(firstPage) != 5 || firstPage[0].Number != 16 || firstPage[4].Number != 12 {
		t.Fatalf("first page: %+v, more=%v, err=%v", firstPage, more, err)
	}
	lastPage, more, err := s.ListIssues(ctx, repoA.ID, IssueFilter{State: "all"}, 4, 5)
	if err != nil || more || len(lastPage) != 1 || lastPage[0].Number != 1 {
		t.Fatalf("last page: %+v, more=%v, err=%v", lastPage, more, err)
	}
	closed, err := s.SetIssueState(ctx, repoA.ID, 1, "closed")
	if err != nil || closed.State != "closed" || closed.ClosedAt == nil {
		t.Fatalf("close issue: %+v, %v", closed, err)
	}
	again, err := s.SetIssueState(ctx, repoA.ID, 1, "closed")
	if err != nil || !again.UpdatedAt.Equal(closed.UpdatedAt) || !again.ClosedAt.Equal(*closed.ClosedAt) {
		t.Fatalf("idempotent close: %+v, %v", again, err)
	}
	closedPage, more, err := s.ListIssues(ctx, repoA.ID, IssueFilter{State: "closed"}, 1, 5)
	if err != nil || more || len(closedPage) != 1 || closedPage[0].Number != 1 {
		t.Fatalf("closed page: %+v, more=%v, err=%v", closedPage, more, err)
	}
	reopened, err := s.SetIssueState(ctx, repoA.ID, 1, "open")
	if err != nil || reopened.State != "open" || reopened.ClosedAt != nil {
		t.Fatalf("reopen issue: %+v, %v", reopened, err)
	}
	againClosed, err := s.SetIssueState(ctx, repoA.ID, 1, "closed")
	if err != nil || againClosed.ClosedAt == nil || !againClosed.ClosedAt.After(*closed.ClosedAt) {
		t.Fatalf("re-close issue: %+v, %v", againClosed, err)
	}
	found, err := s.GetIssue(ctx, repoA.ID, 1)
	if err != nil || found.State != "closed" || found.Number != 1 {
		t.Fatalf("get issue: %+v, %v", found, err)
	}
	if _, err := s.GetIssue(ctx, repoB.ID, 16); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-repo lookup: %v", err)
	}
}

func TestIssuesValidationAndFailedCreateRollsBackNumber(t *testing.T) {
	s, author, repo, _ := issueFixture(t)
	ctx := context.Background()
	for _, input := range []struct{ title, body string }{
		{" \t ", ""},
		{strings.Repeat("x", 201), ""},
		{"Valid", strings.Repeat("x", 65537)},
		{"NUL\x00title", ""},
		{"Valid", "NUL\x00body"},
	} {
		if _, err := s.CreateIssue(ctx, repo.ID, author.ID, input.title, input.body); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid title/body accepted: %v", err)
		}
	}
	if _, err := s.CreateIssue(ctx, repo.ID, "00000000-0000-0000-0000-000000000001", "Valid", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid author accepted: %v", err)
	}
	first, err := s.CreateIssue(ctx, repo.ID, author.ID, "  Valid  ", "")
	if err != nil || first.Number != 1 || first.Title != "Valid" {
		t.Fatalf("failed create consumed a number: %+v, %v", first, err)
	}
	if _, _, err := s.ListIssues(ctx, repo.ID, IssueFilter{State: "invalid"}, 1, 20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid filter accepted: %v", err)
	}
	if _, err := s.SetIssueState(ctx, repo.ID, 1, "invalid"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid state accepted: %v", err)
	}
	if _, err := s.SetIssueState(ctx, repo.ID, 2, "closed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing issue update: %v", err)
	}
}
