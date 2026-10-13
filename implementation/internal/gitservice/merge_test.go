package gitservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testBaseRef is the branch these merge tests protect and update. Merge calls
// take it explicitly now that a repository may protect any branch.
const testBaseRef = "refs/heads/main"

func mergeFixture(t *testing.T) (*Service, string, string, context.Context) {
	t.Helper()
	requireGitHTTPBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	svc, err := New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.InitBare(ctx, testID); err != nil {
		t.Fatal(err)
	}
	repo, err := svc.Path(testID)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work")
	gitCommand(t, ctx, t.TempDir(), "init", "--initial-branch=main", work)
	gitCommand(t, ctx, work, "config", "user.name", "Test User")
	gitCommand(t, ctx, work, "config", "user.email", "tester@example.test")
	if err := os.WriteFile(filepath.Join(work, "shared.txt"), []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, ctx, work, "add", "shared.txt")
	gitCommand(t, ctx, work, "commit", "-m", "initial")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/main")
	return svc, repo, work, ctx
}

func commitFile(t *testing.T, ctx context.Context, work, name, value, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, name), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, ctx, work, "add", name)
	gitCommand(t, ctx, work, "commit", "-m", message)
	return gitCommand(t, ctx, work, "rev-parse", "HEAD")
}

func TestPrepareAndApplyCleanMerge(t *testing.T) {
	svc, repo, work, ctx := mergeFixture(t)
	base := gitCommand(t, ctx, repo, "rev-parse", testBaseRef)
	gitCommand(t, ctx, work, "checkout", "-b", "feature")
	head := commitFile(t, ctx, work, "feature.txt", "new line\n", "feature")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/feature")

	inspection, err := svc.InspectMerge(ctx, testID, testBaseRef, "refs/heads/feature")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.BaseSHA != base || inspection.HeadSHA != head || inspection.Conflicted || inspection.FilesChanged != 1 || inspection.Additions != 1 || inspection.Deletions != 0 {
		t.Fatalf("unexpected inspection: %+v", inspection)
	}
	if _, err := svc.PrepareMerge(ctx, testID, testBaseRef, "refs/heads/feature", base); !errors.Is(err, ErrHeadStale) {
		t.Fatalf("wrong expected head: got %v, want ErrHeadStale", err)
	}
	candidate, err := svc.PrepareMerge(ctx, testID, testBaseRef, "refs/heads/feature", head)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.BaseSHA != base || candidate.HeadSHA != head || candidate.HeadRef != "refs/heads/feature" || !validSHA(candidate.MergedSHA) {
		t.Fatalf("unexpected candidate: %+v", candidate)
	}
	if got := gitCommand(t, ctx, repo, "rev-parse", testBaseRef); got != base {
		t.Fatalf("PrepareMerge moved main to %s", got)
	}
	result, err := svc.ApplyMerge(ctx, testID, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if result.MergedSHA != candidate.MergedSHA || gitCommand(t, ctx, repo, "rev-parse", testBaseRef) != result.MergedSHA {
		t.Fatalf("main did not advance to candidate: %+v", result)
	}
	parents := gitCommand(t, ctx, repo, "rev-list", "--parents", "-n", "1", result.MergedSHA)
	if parents != result.MergedSHA+" "+base+" "+head {
		t.Fatalf("merge parents: %s", parents)
	}
	if got := gitCommand(t, ctx, repo, "show", result.MergedSHA+":feature.txt"); got != "new line" {
		t.Fatalf("merged tree missing feature file: %q", got)
	}
	if _, err := svc.ApplyMerge(ctx, testID, candidate); !errors.Is(err, ErrBaseStale) {
		t.Fatalf("re-applying candidate: got %v, want ErrBaseStale", err)
	}
}

func TestConflictLeavesMainUntouched(t *testing.T) {
	svc, repo, work, ctx := mergeFixture(t)
	initial := gitCommand(t, ctx, repo, "rev-parse", testBaseRef)
	gitCommand(t, ctx, work, "checkout", "-b", "feature")
	head := commitFile(t, ctx, work, "shared.txt", "feature\n", "feature edits shared file")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/feature")
	gitCommand(t, ctx, work, "checkout", "main")
	newBase := commitFile(t, ctx, work, "shared.txt", "main\n", "main edits shared file")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/base-advance")
	gitCommand(t, ctx, repo, "update-ref", testBaseRef, newBase, initial)

	inspection, err := svc.InspectMerge(ctx, testID, testBaseRef, "refs/heads/feature")
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Conflicted || inspection.BaseSHA != newBase || inspection.HeadSHA != head {
		t.Fatalf("unexpected conflict inspection: %+v", inspection)
	}
	if _, err := svc.PrepareMerge(ctx, testID, testBaseRef, "refs/heads/feature", head); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("PrepareMerge conflict: got %v, want ErrMergeConflict", err)
	}
	if got := gitCommand(t, ctx, repo, "rev-parse", testBaseRef); got != newBase {
		t.Fatalf("conflicted merge moved main: %s", got)
	}
}

func TestStaleHeadAndBase(t *testing.T) {
	t.Run("head advances", func(t *testing.T) {
		svc, repo, work, ctx := mergeFixture(t)
		gitCommand(t, ctx, work, "checkout", "-b", "feature")
		head := commitFile(t, ctx, work, "feature.txt", "v1\n", "feature v1")
		gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/feature")
		candidate, err := svc.PrepareMerge(ctx, testID, testBaseRef, "refs/heads/feature", head)
		if err != nil {
			t.Fatal(err)
		}
		commitFile(t, ctx, work, "feature.txt", "v2\n", "feature v2")
		gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/feature")
		if _, err := svc.PrepareMerge(ctx, testID, testBaseRef, "refs/heads/feature", head); !errors.Is(err, ErrHeadStale) {
			t.Fatalf("PrepareMerge stale head: %v", err)
		}
		if _, err := svc.ApplyMerge(ctx, testID, candidate); !errors.Is(err, ErrHeadStale) {
			t.Fatalf("ApplyMerge stale head: %v", err)
		}
	})
	t.Run("main advances", func(t *testing.T) {
		svc, repo, work, ctx := mergeFixture(t)
		base := gitCommand(t, ctx, repo, "rev-parse", testBaseRef)
		gitCommand(t, ctx, work, "checkout", "-b", "feature")
		head := commitFile(t, ctx, work, "feature.txt", "feature\n", "feature")
		gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/feature")
		candidate, err := svc.PrepareMerge(ctx, testID, testBaseRef, "refs/heads/feature", head)
		if err != nil {
			t.Fatal(err)
		}
		gitCommand(t, ctx, work, "checkout", "main")
		newBase := commitFile(t, ctx, work, "base.txt", "base\n", "base advances")
		gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/base-advance")
		gitCommand(t, ctx, repo, "update-ref", testBaseRef, newBase, base)
		if _, err := svc.ApplyMerge(ctx, testID, candidate); !errors.Is(err, ErrBaseStale) {
			t.Fatalf("ApplyMerge stale base: %v", err)
		}
	})
}

func TestConcurrentCandidatesOnlyOneAdvancesMain(t *testing.T) {
	svc, repo, work, ctx := mergeFixture(t)
	base := gitCommand(t, ctx, repo, "rev-parse", testBaseRef)
	var candidates [2]MergeCandidate
	for i, branch := range []string{"feature-a", "feature-b"} {
		gitCommand(t, ctx, work, "checkout", "main")
		gitCommand(t, ctx, work, "checkout", "-b", branch)
		head := commitFile(t, ctx, work, branch+".txt", branch+"\n", branch)
		gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/"+branch)
		candidate, err := svc.PrepareMerge(ctx, testID, testBaseRef, "refs/heads/"+branch, head)
		if err != nil {
			t.Fatal(err)
		}
		candidates[i] = candidate
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		wg.Add(1)
		go func(candidate MergeCandidate) {
			defer wg.Done()
			<-start
			_, err := svc.ApplyMerge(ctx, testID, candidate)
			results <- err
		}(candidate)
	}
	close(start)
	wg.Wait()
	first, second := <-results, <-results
	if !(first == nil && errors.Is(second, ErrBaseStale) || second == nil && errors.Is(first, ErrBaseStale)) {
		t.Fatalf("concurrent apply results: %v, %v", first, second)
	}
	main := gitCommand(t, ctx, repo, "rev-parse", testBaseRef)
	if main == base || main != candidates[0].MergedSHA && main != candidates[1].MergedSHA {
		t.Fatalf("main points to unexpected commit %s", main)
	}
}

func TestMergeRejectsInvalidRefsAndCandidate(t *testing.T) {
	svc, repo, work, ctx := mergeFixture(t)
	for _, ref := range []string{"main", testBaseRef, "refs/pull/1/head", "refs/heads/../bad", "refs/heads/--upload-pack=evil"} {
		if _, err := svc.InspectMerge(ctx, testID, testBaseRef, ref); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("InspectMerge(%q) = %v, want ErrInvalidRef", ref, err)
		}
	}
	if _, err := svc.InspectMerge(ctx, testID, testBaseRef, "refs/heads/missing"); !errors.Is(err, ErrHeadMissing) {
		t.Fatalf("missing head: %v", err)
	}
	gitCommand(t, ctx, work, "checkout", "-b", "feature")
	head := commitFile(t, ctx, work, "feature.txt", "feature\n", "feature")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/feature")
	if _, err := svc.PrepareMerge(ctx, testID, testBaseRef, "refs/heads/feature", "-c core.hooksPath=/tmp/evil"); !errors.Is(err, ErrInvalidSHA) {
		t.Fatalf("invalid expected SHA: %v", err)
	}
	candidate, err := svc.PrepareMerge(ctx, testID, testBaseRef, "refs/heads/feature", head)
	if err != nil {
		t.Fatal(err)
	}
	forged := candidate
	forged.MergedSHA = head // A normal commit is not the prepared two-parent merge.
	if _, err := svc.ApplyMerge(ctx, testID, forged); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("forged candidate: %v", err)
	}
	if gitCommand(t, ctx, repo, "rev-parse", testBaseRef) != candidate.BaseSHA {
		t.Fatal("forged candidate moved main")
	}
	gitCommand(t, ctx, repo, "update-ref", "-d", testBaseRef)
	if _, err := svc.InspectMerge(ctx, testID, testBaseRef, "refs/heads/feature"); !errors.Is(err, ErrMainMissing) {
		t.Fatalf("missing main: %v", err)
	}
}

func TestMergeRejectsExternalFilterConfiguration(t *testing.T) {
	svc, repo, work, ctx := mergeFixture(t)
	gitCommand(t, ctx, work, "checkout", "-b", "feature")
	commitFile(t, ctx, work, "feature.txt", "feature\n", "feature")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/feature")
	gitCommand(t, ctx, repo, "config", "--local", "filter.pwn.process", "/tmp/not-a-real-filter")
	if _, err := svc.InspectMerge(ctx, testID, testBaseRef, "refs/heads/feature"); !errors.Is(err, ErrUnsafeGitConfig) {
		t.Fatalf("unsafe filter config: %v", err)
	}
	if strings.Contains(gitCommand(t, ctx, repo, "config", "--local", "--get", "filter.pwn.process"), "not-a-real-filter") == false {
		t.Fatal("filter config fixture missing")
	}
}

func TestMergeGitBoundsUntrustedOutput(t *testing.T) {
	svc, repo, _, ctx := mergeFixture(t)
	large := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(large, []byte(strings.Repeat("x", maxMergeOutputBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	sha := gitCommand(t, ctx, repo, "hash-object", "-w", large)
	if output, err := svc.mergeGit(ctx, repo, nil, false, "cat-file", "-p", sha); !errors.Is(err, ErrGitOperation) || len(output) != 0 {
		t.Fatalf("large Git output bypassed bound: len=%d err=%v", len(output), err)
	}
}
