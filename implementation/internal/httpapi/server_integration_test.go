package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLocalOnboardingAndRealGit(t *testing.T) {
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
	schema := fmt.Sprintf("fithub_http_test_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop schema: %v", err)
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
	gitRoot := filepath.Join(t.TempDir(), "repos")
	git, err := gitservice.New(gitRoot)
	if err != nil {
		t.Fatal(err)
	}
	installTestHook(t, git, schema)
	api := New(data, git, Config{DevMode: true, DevAdminEmail: "admin@example.test"})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	api.Config.PublicURL = srv.URL

	admin := newBrowser(t)
	employee := newBrowser(t)
	reader := newBrowser(t)
	postJSON(t, admin, srv.URL+"/api/v1/dev/login", map[string]any{"email": "admin@example.test", "name": "Admin"}, http.StatusOK)
	team := postJSON(t, admin, srv.URL+"/api/v1/teams", map[string]any{"slug": "engineering", "name": "Engineering"}, http.StatusCreated)["team"].(map[string]any)
	teamID := team["id"].(string)
	postJSON(t, employee, srv.URL+"/api/v1/dev/login", map[string]any{"email": "alice@example.test", "name": "Alice"}, http.StatusOK)
	if teams := getJSON(t, employee, srv.URL+"/api/v1/me", http.StatusOK)["teams"].([]any); len(teams) != 0 {
		t.Fatalf("new employee unexpectedly belongs to teams: %v", teams)
	}
	application := postJSON(t, employee, srv.URL+"/api/v1/teams/"+teamID+"/join-requests", map[string]any{}, http.StatusCreated)["request"].(map[string]any)
	if application["status"] != "pending" {
		t.Fatalf("expected pending application: %v", application)
	}
	preApproval := postJSON(t, employee, srv.URL+"/api/v1/repos", map[string]any{"teamId": teamID, "slug": "early", "visibility": "internal"}, http.StatusConflict)
	if preApproval["error"] == nil {
		t.Fatal("unapproved employee created a repository")
	}
	postJSON(t, admin, srv.URL+"/api/v1/admin/join-requests/"+application["id"].(string)+"/decision", map[string]any{"decision": "approved"}, http.StatusOK)
	if teams := getJSON(t, employee, srv.URL+"/api/v1/me", http.StatusOK)["teams"].([]any); len(teams) != 1 {
		t.Fatalf("approved employee lacks team: %v", teams)
	}
	repoResponse := postJSON(t, employee, srv.URL+"/api/v1/repos", map[string]any{
		"teamId": teamID, "slug": "service", "visibility": "internal", "grantTeamWrite": true,
	}, http.StatusCreated)["repo"].(map[string]any)
	if repoResponse["canWrite"] != true {
		t.Fatalf("creator should have write access: %v", repoResponse)
	}
	cloneURL := repoResponse["cloneUrl"].(string)
	if cloneURL != srv.URL+"/engineering/service.git" {
		t.Fatalf("unexpected clone URL %s", cloneURL)
	}
	postJSON(t, reader, srv.URL+"/api/v1/dev/login", map[string]any{"email": "bob@example.test", "name": "Bob"}, http.StatusOK)
	getJSON(t, reader, srv.URL+"/api/v1/repos/engineering/service", http.StatusNotFound)
	getJSON(t, reader, srv.URL+"/api/v1/repos/engineering/service/pulls", http.StatusNotFound)
	secondApplication := postJSON(t, reader, srv.URL+"/api/v1/teams/"+teamID+"/join-requests", map[string]any{}, http.StatusCreated)["request"].(map[string]any)
	postJSON(t, admin, srv.URL+"/api/v1/admin/join-requests/"+secondApplication["id"].(string)+"/decision", map[string]any{"decision": "approved"}, http.StatusOK)
	readerDetail := getJSON(t, reader, srv.URL+"/api/v1/repos/engineering/service", http.StatusOK)["repo"].(map[string]any)
	if readerDetail["canWrite"] != true {
		t.Fatalf("team write grant should allow member writes: %v", readerDetail)
	}
	postJSON(t, employee, srv.URL+"/api/v1/repos", map[string]any{
		"teamId": teamID, "slug": "vault", "visibility": "private",
	}, http.StatusCreated)
	getJSON(t, reader, srv.URL+"/api/v1/repos/engineering/vault", http.StatusNotFound)
	if repos := getJSON(t, reader, srv.URL+"/api/v1/repos", http.StatusOK)["repos"].([]any); len(repos) != 1 {
		t.Fatalf("private repository leaked into listing: %v", repos)
	}
	publicRepo := postJSON(t, employee, srv.URL+"/api/v1/repos", map[string]any{
		"teamId": teamID, "slug": "samples", "visibility": "public",
	}, http.StatusCreated)["repo"].(map[string]any)
	publicDetail := getJSON(t, newBrowser(t), srv.URL+"/api/v1/repos/engineering/samples", http.StatusOK)["repo"].(map[string]any)
	if publicDetail["canWrite"] != false {
		t.Fatalf("anonymous public access must be read-only: %v", publicDetail)
	}
	getJSON(t, newBrowser(t), srv.URL+"/api/v1/repos/engineering/samples/pulls", http.StatusOK)
	postJSON(t, newBrowser(t), srv.URL+"/api/v1/repos/engineering/samples/pulls", map[string]any{
		"title": "Anonymous PR", "headRef": "feature/anonymous",
	}, http.StatusUnauthorized)
	runGit(t, "", "clone", publicRepo["cloneUrl"].(string), filepath.Join(t.TempDir(), "public-clone"))

	writeToken := postJSON(t, employee, srv.URL+"/api/v1/tokens", map[string]any{"scope": "repository", "repoId": repoResponse["id"], "level": "write"}, http.StatusCreated)
	secret := writeToken["credential"].(string)
	local := filepath.Join(t.TempDir(), "source")
	runGit(t, "", "init", "--initial-branch=main", local)
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("# FitHub demo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, local, "add", "README.md")
	runGit(t, local, "-c", "user.name=Alice", "-c", "user.email=alice@example.test", "commit", "-m", "Initial commit")
	gitHTTP(t, local, secret, false, "push", cloneURL, "main")
	runGit(t, local, "switch", "-c", "feature/first-pr")
	if err := os.WriteFile(filepath.Join(local, "feature.txt"), []byte("feature\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("# FitHub demo\nfeature note\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, local, "add", "feature.txt", "README.md")
	runGit(t, local, "-c", "user.name=Alice", "-c", "user.email=alice@example.test", "commit", "-m", "Feature commit")
	gitHTTP(t, local, secret, false, "push", cloneURL, "feature/first-pr")
	issue := postJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/issues", map[string]any{
		"title": "Track the first feature", "body": "Verify the PR merge path",
	}, http.StatusCreated)["issue"].(map[string]any)
	if issue["number"] != float64(1) {
		t.Fatalf("unexpected first work item number: %v", issue)
	}
	comment := postJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/issues/1/comments", map[string]any{
		"body": "Ready for review",
	}, http.StatusCreated)["comment"].(map[string]any)
	if comment["body"] != "Ready for review" {
		t.Fatalf("issue comment: %v", comment)
	}
	if got := getJSON(t, reader, srv.URL+"/api/v1/repos/engineering/service/issues/1/comments", http.StatusOK)["comments"].([]any); len(got) != 1 {
		t.Fatalf("Issue comment not visible to reader: %v", got)
	}
	pull := postJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls", map[string]any{
		"title": "Add first feature", "body": "A small feature branch\nFixes #1", "headRef": "feature/first-pr", "baseRef": "main",
	}, http.StatusCreated)["pull"].(map[string]any)
	if pull["number"] != float64(2) {
		t.Fatalf("Issue and PR numbers should share one sequence: %v", pull)
	}
	pullDetail := getJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls/2", http.StatusOK)["pull"].(map[string]any)
	if pullDetail["mergeability"] != "clean" {
		t.Fatalf("expected a clean feature branch: %v", pullDetail)
	}
	preMergeDiff := getJSON(t, reader, srv.URL+"/api/v1/repos/engineering/service/pulls/2/diff", http.StatusOK)
	if !strings.Contains(preMergeDiff["patch"].(string), "feature.txt") {
		t.Fatalf("PR diff omitted added file: %v", preMergeDiff)
	}
	if links := pullDetail["issues"].([]any); len(links) != 1 || links[0].(map[string]any)["closesOnMerge"] != true {
		t.Fatalf("PR Issue link: %v", links)
	}
	postJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls/2/reviews", map[string]any{
		"state": "approved", "expectedBaseSha": pullDetail["currentBaseSha"], "expectedHeadSha": pullDetail["currentHeadSha"],
	}, http.StatusForbidden)
	postJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls/2/merge", map[string]any{
		"expectedHeadSha": strings.Repeat("0", 40),
	}, http.StatusConflict)
	merged := postJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls/2/merge", map[string]any{
		"expectedHeadSha": pullDetail["currentHeadSha"],
	}, http.StatusOK)["pull"].(map[string]any)
	if merged["state"] != "merged" || merged["mergedSha"] == nil {
		t.Fatalf("merge did not record the result: %v", merged)
	}
	postMergeDiff := getJSON(t, reader, srv.URL+"/api/v1/repos/engineering/service/pulls/2/diff", http.StatusOK)
	if !strings.Contains(postMergeDiff["patch"].(string), "feature.txt") {
		t.Fatalf("merged PR lost its original diff: %v", postMergeDiff)
	}
	closedIssue := getJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/issues/1", http.StatusOK)["issue"].(map[string]any)
	if closedIssue["state"] != "closed" || closedIssue["closedByPullNumber"] != float64(2) {
		t.Fatalf("successful PR did not close linked Issue: %v", closedIssue)
	}
	postJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls/2/merge", map[string]any{
		"expectedHeadSha": pullDetail["currentHeadSha"],
	}, http.StatusConflict)
	detail := getJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service", http.StatusOK)["repo"].(map[string]any)
	if detail["empty"] != false || !strings.Contains(detail["readme"].(string), "FitHub demo") {
		t.Fatalf("repository browse did not reflect push: %v", detail)
	}
	if files := detail["files"].([]any); len(files) != 2 {
		t.Fatalf("merged feature is absent from main: %v", files)
	}
	runGit(t, local, "switch", "main") // still points to the pre-merge base
	runGit(t, local, "switch", "-c", "feature/conflict")
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("# FitHub demo\nconflicting note\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, local, "add", "README.md")
	runGit(t, local, "-c", "user.name=Alice", "-c", "user.email=alice@example.test", "commit", "-m", "Conflicting README change")
	gitHTTP(t, local, secret, false, "push", cloneURL, "feature/conflict")
	conflicting := postJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls", map[string]any{
		"title": "Conflicting README", "headRef": "feature/conflict", "baseRef": "main",
	}, http.StatusCreated)["pull"].(map[string]any)
	if conflicting["number"] != float64(3) {
		t.Fatalf("unexpected conflict PR number: %v", conflicting)
	}
	conflictDetail := getJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls/3", http.StatusOK)["pull"].(map[string]any)
	if conflictDetail["mergeability"] != "conflicting" {
		t.Fatalf("conflict was not reported: %v", conflictDetail)
	}
	postJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls/3/merge", map[string]any{
		"expectedHeadSha": conflictDetail["currentHeadSha"],
	}, http.StatusConflict)
	unchanged := getJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service", http.StatusOK)["repo"].(map[string]any)
	if !strings.Contains(unchanged["readme"].(string), "feature note") {
		t.Fatalf("conflicting PR changed main: %v", unchanged)
	}
	runGit(t, local, "switch", "feature/first-pr")
	runGit(t, local, "switch", "-c", "feature/recovery")
	if err := os.WriteFile(filepath.Join(local, "recovered.txt"), []byte("recovered\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, local, "add", "recovered.txt")
	runGit(t, local, "-c", "user.name=Alice", "-c", "user.email=alice@example.test", "commit", "-m", "Recovery fixture")
	gitHTTP(t, local, secret, false, "push", cloneURL, "feature/recovery")
	postJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls", map[string]any{
		"title": "Recover merge metadata", "headRef": "feature/recovery",
	}, http.StatusCreated)
	recoveryPR := getJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls/4", http.StatusOK)["pull"].(map[string]any)
	postJSON(t, reader, srv.URL+"/api/v1/repos/engineering/service/pulls/4/reviews", map[string]any{
		"state": "approved", "expectedBaseSha": recoveryPR["currentBaseSha"], "expectedHeadSha": recoveryPR["currentHeadSha"],
	}, http.StatusCreated)
	recoveryCandidate, err := git.PrepareMerge(ctx, repoResponse["id"].(string), "refs/heads/main", "refs/heads/feature/recovery", recoveryPR["currentHeadSha"].(string))
	if err != nil {
		t.Fatal(err)
	}
	actorID := employeeID(t, employee, srv.URL)
	recoveryAttempt, err := data.StartPullMerge(ctx, repoResponse["id"].(string), 4, actorID,
		recoveryCandidate.BaseSHA, recoveryCandidate.HeadSHA, recoveryCandidate.MergedSHA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := git.ApplyMerge(ctx, repoResponse["id"].(string), recoveryCandidate); err != nil {
		t.Fatal(err)
	}
	if err := api.ReconcilePendingMerges(ctx); err != nil {
		t.Fatal(err)
	}
	recovered := getJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls/4", http.StatusOK)["pull"].(map[string]any)
	if recovered["state"] != "merged" || recovered["mergedSha"] != recoveryCandidate.MergedSHA {
		t.Fatalf("successful Git update was not reconciled: %v", recovered)
	}
	if attempt, err := data.GetPullMergeAttempt(ctx, recoveryAttempt.ID); err != nil || attempt.Status != "succeeded" {
		t.Fatalf("recovery attempt state: %+v, %v", attempt, err)
	}
	runGit(t, local, "switch", "feature/first-pr")
	runGit(t, local, "switch", "-c", "feature/abandoned")
	if err := os.WriteFile(filepath.Join(local, "abandoned.txt"), []byte("not merged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, local, "add", "abandoned.txt")
	runGit(t, local, "-c", "user.name=Alice", "-c", "user.email=alice@example.test", "commit", "-m", "Abandoned fixture")
	gitHTTP(t, local, secret, false, "push", cloneURL, "feature/abandoned")
	postJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls", map[string]any{
		"title": "Abandoned merge attempt", "headRef": "feature/abandoned",
	}, http.StatusCreated)
	abandonedPR := getJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls/5", http.StatusOK)["pull"].(map[string]any)
	postJSON(t, reader, srv.URL+"/api/v1/repos/engineering/service/pulls/5/reviews", map[string]any{
		"state": "approved", "expectedBaseSha": abandonedPR["currentBaseSha"], "expectedHeadSha": abandonedPR["currentHeadSha"],
	}, http.StatusCreated)
	abandonedCandidate, err := git.PrepareMerge(ctx, repoResponse["id"].(string), "refs/heads/main", "refs/heads/feature/abandoned", abandonedPR["currentHeadSha"].(string))
	if err != nil {
		t.Fatal(err)
	}
	abandonedAttempt, err := data.StartPullMerge(ctx, repoResponse["id"].(string), 5, actorID,
		abandonedCandidate.BaseSHA, abandonedCandidate.HeadSHA, abandonedCandidate.MergedSHA)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.ReconcilePendingMerges(ctx); err != nil {
		t.Fatal(err)
	}
	abandoned := getJSON(t, employee, srv.URL+"/api/v1/repos/engineering/service/pulls/5", http.StatusOK)["pull"].(map[string]any)
	if abandoned["state"] != "open" {
		t.Fatalf("unapplied candidate was marked merged: %v", abandoned)
	}
	if attempt, err := data.GetPullMergeAttempt(ctx, abandonedAttempt.ID); err != nil || attempt.Status != "failed" {
		t.Fatalf("abandoned attempt state: %+v, %v", attempt, err)
	}

	readToken := postJSON(t, reader, srv.URL+"/api/v1/tokens", map[string]any{"scope": "repository", "repoId": repoResponse["id"], "level": "read"}, http.StatusCreated)["credential"].(string)
	readOnlyPRBody, err := json.Marshal(map[string]any{"title": "Read token PR", "headRef": "feature/conflict"})
	if err != nil {
		t.Fatal(err)
	}
	readOnlyPRRequest, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/repos/engineering/service/pulls", bytes.NewReader(readOnlyPRBody))
	if err != nil {
		t.Fatal(err)
	}
	readOnlyPRRequest.Header.Set("Content-Type", "application/json")
	readOnlyPRRequest.Header.Set("Authorization", "Bearer "+readToken)
	requestJSON(t, newBrowser(t), readOnlyPRRequest, http.StatusForbidden)
	clone := filepath.Join(t.TempDir(), "clone")
	gitHTTP(t, "", readToken, false, "clone", cloneURL, clone)
	gitHTTP(t, local, readToken, true, "push", cloneURL, "feature/first-pr:refs/heads/unauthorized")
	if err := data.RevokeToken(ctx, employeeID(t, employee, srv.URL), writeToken["token"].(map[string]any)["id"].(string)); err != nil {
		t.Fatal(err)
	}
	gitHTTP(t, "", secret, true, "clone", cloneURL, filepath.Join(t.TempDir(), "revoked"))
}

func newBrowser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 15 * time.Second}
}

func postJSON(t *testing.T, client *http.Client, endpoint string, body any, status int) map[string]any {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return requestJSON(t, client, req, status)
}

func getJSON(t *testing.T, client *http.Client, endpoint string, status int) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	return requestJSON(t, client, req, status)
}

func requestJSON(t *testing.T, client *http.Client, req *http.Request, status int) map[string]any {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: got %d, want %d: %s", req.Method, req.URL.Path, resp.StatusCode, status, body)
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func employeeID(t *testing.T, client *http.Client, base string) string {
	t.Helper()
	return getJSON(t, client, base+"/api/v1/me", http.StatusOK)["id"].(string)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, output)
	}
}

// gitHTTPFailure asserts that Git fails. When reason is not empty, the
// server-side rejection text must also appear, proving the expected rule
// decided it rather than an unrelated transport error.
func gitHTTPFailure(t *testing.T, dir, secret, reason string, args ...string) {
	t.Helper()
	auth := base64.StdEncoding.EncodeToString([]byte("fithub:" + secret))
	allArgs := append([]string{"-c", "http.extraHeader=Authorization: Basic " + auth}, args...)
	cmd := exec.Command("git", allArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("git %s unexpectedly succeeded", args[0])
	}
	if reason != "" && !strings.Contains(string(output), reason) {
		t.Fatalf("git %s: output %q does not contain %q", args[0], strings.ReplaceAll(string(output), secret, "[redacted]"), reason)
	}
}

func gitHTTP(t *testing.T, dir, secret string, expectFailure bool, args ...string) {
	t.Helper()
	auth := base64.StdEncoding.EncodeToString([]byte("fithub:" + secret))
	allArgs := append([]string{"-c", "http.extraHeader=Authorization: Basic " + auth}, args...)
	cmd := exec.Command("git", allArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if expectFailure && err == nil {
		t.Fatalf("git %s unexpectedly succeeded", args[0])
	}
	if !expectFailure && err != nil {
		t.Fatalf("git %s failed: %v: %s", args[0], err, strings.ReplaceAll(string(output), secret, "[redacted]"))
	}
}
