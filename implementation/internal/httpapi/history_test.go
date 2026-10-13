package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHistoryReadsRealGitObjects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	service, err := gitservice.New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	const id = "77777777-7777-4777-8777-777777777777"
	if err := service.InitBare(ctx, id); err != nil {
		t.Fatal(err)
	}
	path, err := service.Path(id)
	if err != nil {
		t.Fatal(err)
	}
	branches, truncated, err := readHistoryBranches(ctx, path)
	if err != nil || truncated || len(branches) != 0 {
		t.Fatalf("empty branches: %v, %v, %v", branches, truncated, err)
	}
	commits, more, err := readHistoryCommits(ctx, path, "main", 20, 0)
	if err != nil || more || len(commits) != 0 {
		t.Fatalf("empty main history: %v, %v, %v", commits, more, err)
	}

	work := filepath.Join(t.TempDir(), "work")
	runGit(t, "", "init", "--initial-branch=main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "README.md")
	runGit(t, work, "-c", "user.name=Alice", "-c", "user.email=alice@example.test", "commit", "-m", "Initial subject", "-m", "Longer body")
	first := localGitSHA(t, work)
	runGit(t, work, "mv", "README.md", "GUIDE.md")
	runGit(t, work, "-c", "user.name=Alice", "-c", "user.email=alice@example.test", "commit", "-m", "Rename guide")
	second := localGitSHA(t, work)
	runGit(t, work, "remote", "add", "origin", path)
	runGit(t, work, "push", "origin", "main")
	runGit(t, work, "switch", "-c", "feature/history")
	if err := os.WriteFile(filepath.Join(work, "note.txt"), []byte("feature\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "note.txt")
	runGit(t, work, "-c", "user.name=Alice", "-c", "user.email=alice@example.test", "commit", "-m", "Feature work")
	runGit(t, work, "push", "origin", "feature/history")

	branches, truncated, err = readHistoryBranches(ctx, path)
	if err != nil || truncated || len(branches) != 2 {
		t.Fatalf("branches: %v, %v, %v", branches, truncated, err)
	}
	if branches[0].Name != "feature/history" || branches[1].Name != "main" || !branches[1].IsDefault || branches[1].SHA != second {
		t.Fatalf("unexpected branch refs: %+v", branches)
	}
	commits, more, err = readHistoryCommits(ctx, path, "main", 1, 0)
	if err != nil || !more || len(commits) != 1 || commits[0].SHA != second || commits[0].Subject != "Rename guide" {
		t.Fatalf("first page: %+v, more=%v, err=%v", commits, more, err)
	}
	commits, more, err = readHistoryCommits(ctx, path, "main", 1, 1)
	if err != nil || more || len(commits) != 1 || commits[0].SHA != first {
		t.Fatalf("second page: %+v, more=%v, err=%v", commits, more, err)
	}
	commits, more, err = readHistoryCommits(ctx, path, "feature/history", 20, 0)
	if err != nil || more || len(commits) != 3 || commits[0].Subject != "Feature work" {
		t.Fatalf("feature history: %+v, more=%v, err=%v", commits, more, err)
	}

	commit, message, files, stats, truncated, err := readHistoryCommit(ctx, path, first)
	if err != nil || truncated || commit.SHA != first || !strings.Contains(message, "Longer body") || stats.FilesChanged != 1 || stats.Additions != 1 || len(files) != 1 || files[0].Status != "added" || files[0].Path != "README.md" {
		t.Fatalf("root commit: %+v, %q, %+v, %+v, %v, %v", commit, message, files, stats, truncated, err)
	}
	commit, _, files, stats, truncated, err = readHistoryCommit(ctx, path, second)
	if err != nil || truncated || commit.SHA != second || stats.FilesChanged != 1 || len(files) != 1 || files[0].Status != "renamed" || files[0].PreviousPath != "README.md" || files[0].Path != "GUIDE.md" {
		t.Fatalf("rename commit: %+v, %+v, %+v, %v, %v", commit, files, stats, truncated, err)
	}
	tooLarge, err := gitOutput(ctx, path, 1, "log", "--format=%H", "-1", second)
	if err == nil {
		t.Fatalf("Git output cap did not stop a larger result: %q", tooLarge)
	}
	w := httptest.NewRecorder()
	writeHistoryError(w, err)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("output cap response: %d, error: %v", w.Code, err)
	}
}

func TestHistoryRejectsInvalidRefsAndMissingObjects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	service, err := gitservice.New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	const id = "88888888-8888-4888-8888-888888888888"
	if err := service.InitBare(ctx, id); err != nil {
		t.Fatal(err)
	}
	path, err := service.Path(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"--help", "../main", "main^{commit}", "main..other", "feature\nsecret", "@{-1}", strings.Repeat("x", 201)} {
		if _, _, err := readHistoryCommits(ctx, path, ref, 20, 0); !errors.Is(err, errHistoryInvalid) {
			t.Errorf("ref %q: got %v, want invalid", ref, err)
		}
	}
	if _, _, err := readHistoryCommits(ctx, path, "missing", 20, 0); !errors.Is(err, errHistoryNotFound) {
		t.Fatalf("missing branch: %v", err)
	}
	if _, _, err := readHistoryCommits(ctx, path, "main", 51, 0); !errors.Is(err, errHistoryInvalid) {
		t.Fatalf("out-of-range page: %v", err)
	}
	if _, _, _, _, _, err := readHistoryCommit(ctx, path, "--help"); !errors.Is(err, errHistoryInvalid) {
		t.Fatalf("invalid SHA: %v", err)
	}
	if _, _, _, _, _, err := readHistoryCommit(ctx, path, strings.Repeat("0", 40)); !errors.Is(err, errHistoryNotFound) {
		t.Fatalf("missing object: %v", err)
	}
	if _, err := parseHistoryStatuses([]byte("R100\x00old\x00")); err == nil {
		t.Fatal("accepted incomplete rename status")
	}
}

func TestHistoryAPIVisibility(t *testing.T) {
	dbURL := os.Getenv("FITHUB_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set FITHUB_TEST_DATABASE_URL to a disposable PostgreSQL instance")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := fmt.Sprintf("fithub_history_test_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	data := store.New(pool)
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	git, err := gitservice.New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := data.UpsertDevUser(ctx, "admin@example.test", "Admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetUserRole(ctx, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	owner, err := data.UpsertDevUser(ctx, "owner@example.test", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := data.UpsertDevUser(ctx, "outsider@example.test", "Outsider")
	if err != nil {
		t.Fatal(err)
	}
	team, err := data.CreateTeam(ctx, "engineering", "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	join, err := data.RequestJoin(ctx, owner.ID, team.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.ReviewJoin(ctx, join.ID, admin.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct{ name, visibility string }{{"private", "private"}, {"public", "public"}} {
		repo, err := data.CreateRepo(ctx, team.ID, spec.name, spec.visibility, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := git.InitBare(ctx, repo.ID); err != nil {
			t.Fatal(err)
		}
		if err := data.SetRepoState(ctx, repo.ID, "active"); err != nil {
			t.Fatal(err)
		}
	}
	api := New(data, git, Config{DevMode: true})
	if err := data.CreateBrowserSession(ctx, owner.ID, hashSecret("owner-session"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateBrowserSession(ctx, outsider.ID, hashSecret("outsider-session"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Get("/api/v1/repos/{team}/{repo}/branches", api.branches)
	router.Get("/api/v1/repos/{team}/{repo}/commits", api.commits)
	router.Get("/api/v1/repos/{team}/{repo}/commits/{sha}", api.commitDetail)
	getStatus := func(url, sessionValue, bearer string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if sessionValue != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionValue})
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	base := "/api/v1/repos/engineering/"
	if got := getStatus(base+"private/branches", "", ""); got != http.StatusNotFound {
		t.Fatalf("anonymous private branches: %d", got)
	}
	if got := getStatus(base+"private/commits", "outsider-session", ""); got != http.StatusNotFound {
		t.Fatalf("outsider private history: %d", got)
	}
	if got := getStatus(base+"private/commits/"+strings.Repeat("a", 40), "outsider-session", ""); got != http.StatusNotFound {
		t.Fatalf("outsider private commit: %d", got)
	}
	if got := getStatus(base+"private/branches", "owner-session", ""); got != http.StatusOK {
		t.Fatalf("owner private branches: %d", got)
	}
	if got := getStatus(base+"private/commits", "owner-session", ""); got != http.StatusOK {
		t.Fatalf("owner empty private history: %d", got)
	}
	if got := getStatus(base+"public/branches", "", ""); got != http.StatusOK {
		t.Fatalf("anonymous public branches: %d", got)
	}
	if got := getStatus(base+"private/commits?ref=--help", "owner-session", ""); got != http.StatusBadRequest {
		t.Fatalf("invalid branch: %d", got)
	}
	if got := getStatus(base+"private/commits?limit=51", "owner-session", ""); got != http.StatusBadRequest {
		t.Fatalf("invalid page: %d", got)
	}
	if got := getStatus(base+"private/commits/not-a-sha", "owner-session", ""); got != http.StatusBadRequest {
		t.Fatalf("invalid SHA: %d", got)
	}

	// A repository-scoped token is never a general public-repository token.
	privateRepo, err := data.GetRepoBySlug(ctx, "engineering", "private")
	if err != nil {
		t.Fatal(err)
	}
	secret := "fh_" + strings.Repeat("x", 44)
	hash := sha256.Sum256([]byte(secret))
	if _, err := data.CreateToken(ctx, owner.ID, "repository", privateRepo.ID, "read", secret[:12], hex.EncodeToString(hash[:]), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := getStatus(base+"private/branches", "", secret); got != http.StatusOK {
		t.Fatalf("scoped token private read: %d", got)
	}
	if got := getStatus(base+"public/branches", "", secret); got != http.StatusNotFound {
		t.Fatalf("scoped token crossed repositories: %d", got)
	}
}

func localGitSHA(t *testing.T, dir string) string {
	t.Helper()
	cmd := execGit(t, dir, "rev-parse", "HEAD")
	return strings.TrimSpace(string(cmd))
}

func execGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The branch list carries enough commit context to tell a live branch from a
// stale one, and tags distinguish an annotated release from a bare pointer.
func TestHistoryReadsTagsAndBranchCommitInfo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	service, err := gitservice.New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	const id = "88888888-8888-4888-8888-888888888888"
	if err := service.InitBare(ctx, id); err != nil {
		t.Fatal(err)
	}
	path, err := service.Path(id)
	if err != nil {
		t.Fatal(err)
	}
	tags, truncated, err := readHistoryTags(ctx, path)
	if err != nil || truncated || len(tags) != 0 {
		t.Fatalf("empty tags: %v, %v, %v", tags, truncated, err)
	}

	work := filepath.Join(t.TempDir(), "work")
	runGit(t, "", "init", "--initial-branch=main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "README.md")
	runGit(t, work, "-c", "user.name=Alice", "-c", "user.email=alice@example.test", "commit", "-m", "Initial subject")
	head := localGitSHA(t, work)
	runGit(t, work, "remote", "add", "origin", path)
	runGit(t, work, "push", "origin", "main")

	// A branch must report its last commit, not just its name.
	branches, _, err := readHistoryBranches(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || branches[0].Subject != "Initial subject" || branches[0].AuthorName != "Alice" || branches[0].CommittedAt == "" {
		t.Fatalf("branch commit context: %+v", branches)
	}

	runGit(t, work, "tag", "v0.1.0")
	runGit(t, work, "-c", "user.name=Alice", "-c", "user.email=alice@example.test", "tag", "-a", "v1.0.0", "-m", "First release")
	runGit(t, work, "push", "origin", "--tags")

	tags, truncated, err = readHistoryTags(ctx, path)
	if err != nil || truncated || len(tags) != 2 {
		t.Fatalf("tags: %+v, truncated=%v, err=%v", tags, truncated, err)
	}
	byName := map[string]historyTag{}
	for _, tag := range tags {
		byName[tag.Name] = tag
	}
	annotated, ok := byName["v1.0.0"]
	if !ok {
		t.Fatalf("annotated tag missing: %+v", tags)
	}
	if !annotated.Annotated || annotated.TagSHA == "" || annotated.TagSHA == annotated.SHA {
		t.Fatalf("annotated tag object ids: %+v", annotated)
	}
	// The browsable revision is the commit the tag wraps, never the tag object.
	if annotated.SHA != head {
		t.Fatalf("annotated tag points at %q, want the commit %q", annotated.SHA, head)
	}
	if annotated.Subject != "First release" {
		t.Fatalf("annotated tag subject: %q", annotated.Subject)
	}
	lightweight, ok := byName["v0.1.0"]
	if !ok {
		t.Fatalf("lightweight tag missing: %+v", tags)
	}
	if lightweight.Annotated || lightweight.TagSHA != "" || lightweight.SHA != head {
		t.Fatalf("lightweight tag: %+v", lightweight)
	}
	if lightweight.Subject != "Initial subject" {
		t.Fatalf("lightweight tag falls back to the commit subject: %q", lightweight.Subject)
	}
}
