package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inlineCommentFixture creates a repository with a real commit on a feature
// branch and an open pull request, so inline comments can be anchored to a
// genuine diff produced by Git.
func inlineCommentFixture(t *testing.T) (author, reviewer *http.Client, base, reviewToken string) {
	t.Helper()
	owner, writer, writerToken, _, base := repoWriteFixture(t)
	repository := getJSON(t, owner, base, http.StatusOK)["repo"].(map[string]any)
	cloneURL := repository["cloneUrl"].(string)

	local := filepath.Join(t.TempDir(), "work")
	gitInitIfNeeded(t, local)
	commitFile(t, local, "internal/a.go", "package a\n\nvar x = 1\n", "Initial")
	gitHTTP(t, local, writerToken, false, "push", cloneURL, "main")

	// A feature branch that adds and changes lines gives the diff real anchors.
	runGit(t, local, "switch", "-c", "feature/inline")
	commitFile(t, local, "internal/a.go",
		"package a\n\nimport \"fmt\"\n\nvar x = 1\n\nfunc f() { fmt.Println(x) }\n", "Add fmt usage")
	gitHTTP(t, local, writerToken, false, "push", cloneURL, "feature/inline")

	postJSON(t, owner, base+"/pulls", map[string]any{
		"title": "Add fmt", "body": "", "baseRef": "main", "headRef": "feature/inline",
	}, http.StatusCreated)
	return owner, writer, base, writerToken
}

// commitFile writes one file and commits it on the currently checked-out
// branch. The caller initializes and switches branches.
func commitFile(t *testing.T, repo, file, content, message string) {
	t.Helper()
	full := filepath.Join(repo, file)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", file)
	runGit(t, repo, "-c", "user.name=Writer", "-c", "user.email=writer@example.test", "commit", "-m", message)
}

// gitInitIfNeeded lets the fixture create the very first commit too.
func gitInitIfNeeded(t *testing.T, repo string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
		return
	}
	runGit(t, "", "init", "--initial-branch=main", repo)
}

func TestInlineReviewCommentRoundTrip(t *testing.T) {
	author, reviewer, base, _ := inlineCommentFixture(t)
	pull := getJSON(t, author, base+"/pulls/1", http.StatusOK)["pull"].(map[string]any)
	diff := getJSON(t, author, base+"/pulls/1/diff", http.StatusOK)
	patch := diff["patch"].(string)
	if !strings.Contains(patch, "internal/a.go") {
		t.Fatalf("diff missing file: %s", patch)
	}

	// Find a real added line in the diff so the anchor is legitimate.
	line := addedLineNumber(t, patch, "internal/a.go")
	created := postJSON(t, reviewer, base+"/pulls/1/comments", map[string]any{
		"path": "internal/a.go", "line": line, "side": "new", "body": "nit: keep imports grouped",
	}, http.StatusCreated)["comment"].(map[string]any)
	if created["path"] != "internal/a.go" || created["line"] != float64(line) || created["outdated"] != false {
		t.Fatalf("created comment: %v", created)
	}
	if created["headSha"] != diff["headSha"] {
		t.Fatalf("comment anchored to %v, diff head %v", created["headSha"], diff["headSha"])
	}

	// The thread is listed with the revision it was compared against.
	listed := getJSON(t, author, base+"/pulls/1/comments", http.StatusOK)
	if listed["headSha"] != diff["headSha"] || listed["outdatedCount"] != float64(0) {
		t.Fatalf("comment list: %v", listed)
	}
	comments := listed["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("expected one comment, got %d", len(comments))
	}

	// A reply in the same file is accepted.
	postJSON(t, author, base+"/pulls/1/comments", map[string]any{
		"path": "internal/a.go", "line": line, "side": "new", "body": "agreed",
		"replyToId": created["id"],
	}, http.StatusCreated)

	// An anchor that is not in the diff is refused, which is the point of
	// validating against Git rather than trusting the client.
	postJSON(t, reviewer, base+"/pulls/1/comments", map[string]any{
		"path": "internal/a.go", "line": line + 5000, "side": "new", "body": "nowhere",
	}, http.StatusBadRequest)
	postJSON(t, reviewer, base+"/pulls/1/comments", map[string]any{
		"path": "internal/missing.go", "line": 1, "side": "new", "body": "ghost file",
	}, http.StatusBadRequest)
	// An echoed stale revision is refused so a comment cannot claim to be
	// anchored to a revision the reviewer never saw.
	postJSON(t, reviewer, base+"/pulls/1/comments", map[string]any{
		"path": "internal/a.go", "line": line, "side": "new", "body": "stale",
		"headSha": strings.Repeat("a", 40),
	}, http.StatusConflict)
	_ = pull
}

func TestInlineReviewCommentRequiresWriteAccess(t *testing.T) {
	_, _, base, _ := inlineCommentFixture(t)
	reader := newBrowser(t)
	admin := newBrowser(t)
	postJSON(t, admin, base[:strings.Index(base, "/api/v1")]+"/api/v1/dev/login", map[string]any{"email": "admin@example.test", "name": "Admin"}, http.StatusOK)
	postJSON(t, reader, base[:strings.Index(base, "/api/v1")]+"/api/v1/dev/login", map[string]any{"email": "reader@example.test", "name": "Reader"}, http.StatusOK)

	// A signed-in account with no repository access cannot read or comment. The
	// read path answers 404 so repository existence is not disclosed to
	// non-members; the write path answers 404 for the same reason.
	getJSON(t, reader, base+"/pulls/1/comments", http.StatusNotFound)
	postJSON(t, reader, base+"/pulls/1/comments", map[string]any{
		"path": "internal/a.go", "line": 1, "side": "new", "body": "hello",
	}, http.StatusNotFound)
}

// addedLineNumber returns the new-side line number of the first added line of
// the named file in a unified diff.
func addedLineNumber(t *testing.T, patch, path string) int {
	t.Helper()
	var (
		current string
		inHunk  bool
		newLine int
	)
	for _, raw := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(raw, "diff --git "):
			current, inHunk = "", false
		case strings.HasPrefix(raw, "+++ "):
			current = parseDiffPath(strings.TrimPrefix(raw, "+++ "))
			inHunk = false
		case strings.HasPrefix(raw, "@@"):
			var ok bool
			_, newLine, ok = parseHunkHeader(raw)
			inHunk = ok
		case !inHunk || current != path:
			continue
		case strings.HasPrefix(raw, "+"):
			return newLine
		case strings.HasPrefix(raw, " "):
			newLine++
		}
	}
	t.Fatalf("no added line found for %s in patch:\n%s", path, patch)
	return 0
}
