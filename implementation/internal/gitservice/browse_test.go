package gitservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func browseFixture(t *testing.T) (*Service, string, string, context.Context) {
	t.Helper()
	svc, repo, work, ctx := mergeFixture(t)
	gitCommand(t, ctx, work, "checkout", "-b", "browse")
	if err := os.MkdirAll(filepath.Join(work, "docs", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/guide.md", []byte("# Guide\n\nReadable text.\n"))
	write("docs/nested/example.txt", []byte("nested file\n"))
	write("binary.bin", []byte{0, 1, 2, 3})
	write("large.txt", []byte(strings.Repeat("x", maxBrowseBlobBytes+1)))
	if err := os.Symlink("../outside-secret", filepath.Join(work, "docs", "secret-link")); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, ctx, work, "add", "--all")
	gitCommand(t, ctx, work, "commit", "-m", "add browsable content")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/browse")
	return svc, repo, work, ctx
}

func TestBrowseTreeAndTextBlob(t *testing.T) {
	svc, _, _, ctx := browseFixture(t)
	tree, err := svc.BrowseTree(ctx, testID, "browse", "")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Path != "" || tree.Ref != "browse" || len(tree.Entries) < 4 {
		t.Fatalf("unexpected root tree: %+v", tree)
	}
	var docs, symlink bool
	for _, entry := range tree.Entries {
		if entry.Name == "docs" && entry.Type == "directory" {
			docs = true
		}
	}
	if !docs {
		t.Fatal("docs directory missing")
	}
	docsTree, err := svc.BrowseTree(ctx, testID, "browse", "docs")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range docsTree.Entries {
		if entry.Name == "secret-link" && entry.Type == "symlink" {
			symlink = true
		}
	}
	if !symlink {
		t.Fatalf("symlink was not identified explicitly: %+v", docsTree.Entries)
	}
	blob, err := svc.BrowseBlob(ctx, testID, "browse", "docs/guide.md")
	if err != nil {
		t.Fatal(err)
	}
	if blob.Binary || blob.TooLarge || blob.Text != "# Guide\n\nReadable text.\n" || blob.Size != int64(len(blob.Text)) {
		t.Fatalf("unexpected text blob: %+v", blob)
	}
}

func TestBrowseRefusesUnsafePathsSymlinksAndNonText(t *testing.T) {
	svc, _, _, ctx := browseFixture(t)
	for _, path := range []string{"../outside-secret", "docs/../../outside-secret", "/etc/passwd", "docs//guide.md", "docs/./guide.md", "docs\\guide.md"} {
		if _, err := svc.BrowseBlob(ctx, testID, "browse", path); !errors.Is(err, ErrBrowseInvalid) {
			t.Errorf("BrowseBlob(%q) err=%v, want invalid", path, err)
		}
	}
	if _, err := svc.BrowseBlob(ctx, testID, "browse", "docs/secret-link"); !errors.Is(err, ErrBrowseInvalid) {
		t.Fatalf("symlink blob: %v", err)
	}
	if _, err := svc.BrowseTree(ctx, testID, "browse", "docs/secret-link"); !errors.Is(err, ErrBrowseNotFound) {
		t.Fatalf("symlink traversal: %v", err)
	}
	binary, err := svc.BrowseBlob(ctx, testID, "browse", "binary.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !binary.Binary || binary.Text != "" {
		t.Fatalf("binary content should not be returned: %+v", binary)
	}
	large, err := svc.BrowseBlob(ctx, testID, "browse", "large.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !large.TooLarge || large.Text != "" {
		t.Fatalf("large content should not be returned: %+v", large)
	}
	if _, err := svc.BrowseTree(ctx, testID, "--upload-pack=oops", ""); !errors.Is(err, ErrBrowseInvalid) {
		t.Fatalf("invalid branch option: %v", err)
	}
	if _, err := svc.BrowseTree(ctx, testID, "missing", ""); !errors.Is(err, ErrBrowseNotFound) {
		t.Fatalf("missing branch: %v", err)
	}
}

func TestBrowseTreeTruncatesLargeDirectories(t *testing.T) {
	svc, repo, work, ctx := mergeFixture(t)
	gitCommand(t, ctx, work, "checkout", "-b", "wide")
	for i := 0; i < maxBrowseEntries+1; i++ {
		name := filepath.Join(work, fmt.Sprintf("entry-%04d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitCommand(t, ctx, work, "add", "--all")
	gitCommand(t, ctx, work, "commit", "-m", "wide tree")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/wide")
	result, err := svc.BrowseTree(ctx, testID, "wide", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != maxBrowseEntries || !result.Truncated {
		t.Fatalf("entries=%d truncated=%v", len(result.Entries), result.Truncated)
	}
}

func TestBrowseHonorsTimeoutAndUnsafeConfig(t *testing.T) {
	svc, repo, _, ctx := browseFixture(t)
	gitCommand(t, ctx, repo, "config", "filter.bad.clean", "touch /tmp/not-run")
	if _, err := svc.BrowseTree(ctx, testID, "browse", ""); !errors.Is(err, ErrBrowseUnsafe) {
		t.Fatalf("unsafe config: %v", err)
	}
	deadline, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := svc.BrowseTree(deadline, testID, "browse", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired context: %v", err)
	}
}

// A tag has to be browsable for the tags list to be useful, and a branch must
// still win when it shares a name with a tag: a moving branch silently shadowed
// by a static tag would be very confusing.
func TestBrowseTreeResolvesTagsAndPrefersBranches(t *testing.T) {
	svc, repo, work, ctx := browseFixture(t)
	gitCommand(t, ctx, work, "tag", "release-1")
	gitCommand(t, ctx, work, "push", repo, "refs/tags/release-1")
	tagged, err := svc.BrowseTree(ctx, testID, "release-1", "")
	if err != nil {
		t.Fatalf("browsing a tag: %v", err)
	}
	if len(tagged.Entries) == 0 {
		t.Fatal("tagged tree was empty")
	}
	tagCommit := strings.TrimSpace(gitCommand(t, ctx, work, "rev-parse", "refs/tags/release-1^{commit}"))
	if tagged.CommitSHA != tagCommit {
		t.Fatalf("tag resolved to %q, want %q", tagged.CommitSHA, tagCommit)
	}

	// Now give the tag's name to a branch that is ahead of it.
	gitCommand(t, ctx, work, "checkout", "-b", "release-1")
	if err := os.WriteFile(filepath.Join(work, "ahead.txt"), []byte("ahead\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, ctx, work, "add", "ahead.txt")
	gitCommand(t, ctx, work, "commit", "-m", "branch is ahead of the tag")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/release-1")
	branchHead := strings.TrimSpace(gitCommand(t, ctx, work, "rev-parse", "refs/heads/release-1"))

	preferred, err := svc.BrowseTree(ctx, testID, "release-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if preferred.CommitSHA != branchHead {
		t.Fatalf("a branch sharing a tag's name must win: got %q, want %q", preferred.CommitSHA, branchHead)
	}
	// A name that exists as neither is still a clean not-found.
	if _, err := svc.BrowseTree(ctx, testID, "release-2", ""); !errors.Is(err, ErrBrowseNotFound) {
		t.Fatalf("unknown ref: %v", err)
	}
}

// Each entry in a directory listing reports the commit that last changed it, so
// a reader can tell a maintained file from an abandoned one without opening it.
func TestBrowseTreeAttachesLastCommitPerEntry(t *testing.T) {
	svc, repo, work, ctx := mergeFixture(t)
	gitCommand(t, ctx, work, "checkout", "-b", "last-commit")
	if err := os.MkdirAll(filepath.Join(work, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("top.txt", "top\n")
	write("docs/guide.md", "first\n")
	gitCommand(t, ctx, work, "add", "--all")
	gitCommand(t, ctx, work, "commit", "-m", "add initial files")
	write("docs/guide.md", "second\n")
	gitCommand(t, ctx, work, "add", "--all")
	gitCommand(t, ctx, work, "commit", "-m", "update the guide")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/last-commit")

	tree, err := svc.BrowseTree(ctx, testID, "last-commit", "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]BrowseEntry{}
	for _, entry := range tree.Entries {
		byName[entry.Name] = entry
	}
	// A file changed in the newer commit reports that commit.
	guide, ok := byName["docs"]
	if !ok {
		t.Fatalf("docs entry missing: %+v", tree.Entries)
	}
	if guide.LastCommit == nil || guide.LastCommit.Subject != "update the guide" {
		t.Fatalf("docs last commit: %+v", guide.LastCommit)
	}
	if guide.LastCommit.AuthorName == "" || guide.LastCommit.CommittedAt == "" || !validSHA(guide.LastCommit.SHA) {
		t.Fatalf("docs last commit is incomplete: %+v", guide.LastCommit)
	}
	// A file untouched by the newer commit still reports the older one.
	top, ok := byName["top.txt"]
	if !ok {
		t.Fatalf("top.txt entry missing")
	}
	if top.LastCommit == nil || top.LastCommit.Subject != "add initial files" {
		t.Fatalf("top.txt last commit: %+v", top.LastCommit)
	}
	// Both entries must not collapse onto the same commit.
	if top.LastCommit.SHA == guide.LastCommit.SHA {
		t.Fatalf("every entry reported the same commit %q", top.LastCommit.SHA)
	}
}

// Non-ASCII file names must still be matched against history: Git escapes them
// by default, which would silently blank out the column for those files.
func TestBrowseTreeLastCommitHandlesNonASCIIPaths(t *testing.T) {
	svc, repo, work, ctx := mergeFixture(t)
	gitCommand(t, ctx, work, "checkout", "-b", "unicode")
	if err := os.WriteFile(filepath.Join(work, "说明.md"), []byte("内容\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, ctx, work, "add", "--all")
	gitCommand(t, ctx, work, "commit", "-m", "add a Chinese named file")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/unicode")

	tree, err := svc.BrowseTree(ctx, testID, "unicode", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range tree.Entries {
		if entry.Name != "说明.md" {
			continue
		}
		if entry.LastCommit == nil {
			t.Fatalf("non-ASCII entry lost its last commit: %+v", entry)
		}
		if entry.LastCommit.Subject != "add a Chinese named file" {
			t.Fatalf("non-ASCII last commit: %+v", entry.LastCommit)
		}
		return
	}
	t.Fatal("非ASCII 条目未出现在列表中")
}
