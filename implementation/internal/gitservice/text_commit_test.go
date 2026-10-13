package gitservice

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextCommitUsesNativeGitAndCAS(t *testing.T) {
	svc, repo, work, ctx := mergeFixture(t)
	gitCommand(t, ctx, work, "checkout", "-b", "feature/api-edit")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/feature/api-edit")
	head := gitCommand(t, ctx, repo, "rev-parse", "refs/heads/feature/api-edit")
	main := gitCommand(t, ctx, repo, "rev-parse", testBaseRef)
	candidate, err := svc.PrepareTextCommit(ctx, testID, "feature/api-edit", head, "docs/note.txt", "hello from MCP\n", "Document API edit", "Alice", "alice@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if gitCommand(t, ctx, repo, "rev-parse", "refs/heads/feature/api-edit") != head {
		t.Fatal("preparation moved branch")
	}
	if err := svc.ApplyTextCommit(ctx, testID, candidate); err != nil {
		t.Fatal(err)
	}
	if got := gitCommand(t, ctx, repo, "show", candidate.CommitSHA+":docs/note.txt"); got != "hello from MCP" {
		t.Fatalf("file content=%q", got)
	}
	if gitCommand(t, ctx, repo, "rev-parse", testBaseRef) != main {
		t.Fatal("text commit on feature branch moved main")
	}
	if got := gitCommand(t, ctx, repo, "show", "-s", "--format=%an <%ae>", candidate.CommitSHA); got != "Alice <alice@example.test>" {
		t.Fatalf("author=%q", got)
	}
	if err := svc.ApplyTextCommit(ctx, testID, candidate); !errors.Is(err, ErrHeadStale) {
		t.Fatalf("repeat commit=%v", err)
	}
	if _, err := svc.PrepareTextCommit(ctx, testID, "feature/api-edit", head, "docs/note.txt", "again", "Stale", "Alice", "alice@example.test"); !errors.Is(err, ErrHeadStale) {
		t.Fatalf("stale head=%v", err)
	}
	mainCandidate, err := svc.PrepareTextCommit(ctx, testID, "main", main, "bypass.txt", "allowed", "Update main", "Alice", "alice@example.test")
	if err != nil {
		t.Fatalf("default branch commit: %v", err)
	}
	if err := svc.ApplyTextCommit(ctx, testID, mainCandidate); err != nil {
		t.Fatal(err)
	}
	if gitCommand(t, ctx, repo, "rev-parse", testBaseRef) != mainCandidate.CommitSHA {
		t.Fatal("text commit did not update default branch")
	}
	if got := gitCommand(t, ctx, repo, "show", mainCandidate.CommitSHA+":bypass.txt"); got != "allowed" {
		t.Fatalf("default branch file content=%q", got)
	}
}

func TestTextCommitRejectsUnsafePaths(t *testing.T) {
	svc, repo, work, ctx := mergeFixture(t)
	gitCommand(t, ctx, work, "checkout", "-b", "feature/safe")
	if err := os.Symlink("shared.txt", filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "docs", "keep.txt"), []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, ctx, work, "add", "link", "docs/keep.txt")
	gitCommand(t, ctx, work, "commit", "-m", "symlink")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/feature/safe")
	head := gitCommand(t, ctx, repo, "rev-parse", "refs/heads/feature/safe")
	for _, path := range []string{"link", "link/child.txt", "docs", ".fithub/workflows/test.yml", ".git/config", "nested/.GIT/config", ".gitmodules", "../escape", "docs/../escape"} {
		_, err := svc.PrepareTextCommit(ctx, testID, "feature/safe", head, path, "bad", "Bad edit", "Alice", "alice@example.test")
		if !errors.Is(err, ErrTextCommitInvalid) && !errors.Is(err, ErrTextCommitUnsafe) {
			t.Errorf("path=%q err=%v", path, err)
		}
	}
	if _, err := svc.PrepareTextCommit(ctx, testID, "feature/safe", head, "safe.txt", strings.Repeat("x", 64<<10+1), "too large", "Alice", "alice@example.test"); !errors.Is(err, ErrTextCommitInvalid) {
		t.Fatalf("oversized content=%v", err)
	}
	if gitCommand(t, ctx, repo, "rev-parse", "refs/heads/feature/safe") != head {
		t.Fatal("rejected edit moved branch")
	}
}
