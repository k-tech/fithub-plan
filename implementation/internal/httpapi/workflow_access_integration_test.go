package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const workflowFile = "version: 1\non:\n  pull_request: {}\njobs:\n  test:\n    runs-on: linux-x64\n    steps:\n      - builtin: checkout\n      - run: go test ./...\n"

// workflowRepoFixture creates an internal repository whose main branch already
// has one commit, pushing as the writer so the first push is the ordinary
// empty-repository case.
func workflowRepoFixture(t *testing.T) (owner, writer *http.Client, writerToken, base, cloneURL string) {
	t.Helper()
	owner, writer, writerToken, _, base = repoWriteFixture(t)
	repository := getJSON(t, owner, base, http.StatusOK)["repo"].(map[string]any)
	cloneURL = repository["cloneUrl"].(string)

	local := filepath.Join(t.TempDir(), "work")
	gitInitIfNeeded(t, local)
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, local, "add", "README.md")
	runGit(t, local, "-c", "user.name=Writer", "-c", "user.email=writer@example.test", "commit", "-m", "Initial")
	gitHTTP(t, local, writerToken, false, "push", cloneURL, "main")
	return owner, writer, writerToken, base, cloneURL
}

// commitWorkflowFile adds a workflow definition to the given branch in a fresh
// clone and returns the local path. Committing locally always works; only the
// push is authorized.
func commitWorkflowFile(t *testing.T, cloneURL, token, branch string) string {
	t.Helper()
	local := filepath.Join(t.TempDir(), "workflow")
	// The clone itself needs credentials, otherwise Git prompts and fails before
	// the push under test ever runs.
	gitHTTP(t, "", token, false, "clone", "--quiet", cloneURL, local)
	runGit(t, local, "switch", "-c", branch)
	if err := os.MkdirAll(filepath.Join(local, ".fithub", "workflows"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, ".fithub", "workflows", "ci.yml"), []byte(workflowFile), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, local, "add", ".fithub/workflows/ci.yml")
	runGit(t, local, "-c", "user.name=Writer", "-c", "user.email=writer@example.test", "commit", "-m", "Add workflow")
	return local
}

// A writer must not be able to publish a workflow definition: the definition
// decides which checks protect the default branch and which jobs a pull request
// must satisfy.
func TestPushRejectsWorkflowFileFromNonAdmin(t *testing.T) {
	_, _, writerToken, _, cloneURL := workflowRepoFixture(t)
	local := commitWorkflowFile(t, cloneURL, writerToken, "feature/workflow")
	gitHTTPFailure(t, local, writerToken, "may only be changed by a repository administrator", "push", cloneURL, "feature/workflow")
}

// The same content pushed by a repository administrator is accepted, so the rule
// restricts the account rather than blocking the feature outright.
func TestPushAcceptsWorkflowFileFromAdmin(t *testing.T) {
	owner, _, _, base, cloneURL := workflowRepoFixture(t)
	adminToken := createUserToken(t, owner, base, "admin")
	local := commitWorkflowFile(t, cloneURL, adminToken, "feature/workflow-admin")
	gitHTTP(t, local, adminToken, false, "push", cloneURL, "feature/workflow-admin")

	// The definition is now readable from the branch, proving the push landed.
	blob := getJSON(t, owner, base+"/blob?ref=feature/workflow-admin&path=.fithub/workflows/ci.yml", http.StatusOK)
	if blob["text"] == nil || blob["text"] == "" {
		t.Fatalf("workflow file was not stored: %v", blob)
	}
}

// A pull request that changes a workflow file may only be merged by an
// administrator; this separate workflow permission remains in place.
func TestMergeRejectsWorkflowChangeFromNonAdmin(t *testing.T) {
	owner, writer, writerToken, base, cloneURL := workflowRepoFixture(t)
	local := commitWorkflowFile(t, cloneURL, writerToken, "feature/workflow-pr")
	gitHTTPFailure(t, local, writerToken, "may only be changed by a repository administrator", "push", cloneURL, "feature/workflow-pr")

	// An administrator publishes the workflow on a branch, then opens a PR for
	// it. The workflow-file permission remains separate from ordinary merging.
	adminToken := createUserToken(t, owner, base, "admin")
	gitHTTP(t, local, adminToken, false, "push", cloneURL, "feature/workflow-pr")
	pull := postJSON(t, owner, base+"/pulls", map[string]any{
		"title": "Add workflow", "body": "", "baseRef": "main", "headRef": "feature/workflow-pr",
	}, http.StatusCreated)["pull"].(map[string]any)

	// A normal writer still cannot merge a workflow change; the error names the
	// workflow permission rule rather than a generic merge failure.
	response := requestJSON(t, writer, newJSONRequest(t, http.MethodPost, base+"/pulls/1/merge",
		map[string]any{"expectedHeadSha": pull["currentHeadSha"]}), http.StatusForbidden)
	detail, _ := response["error"].(map[string]any)
	if detail["code"] != "WORKFLOW_ADMIN_REQUIRED" {
		t.Fatalf("expected WORKFLOW_ADMIN_REQUIRED, got %v", response)
	}
}

// createUserToken issues a user-scope token for a browser client. The token
// endpoint lives at the API root, so it is resolved against the repository base
// URL rather than appended to it.
func createUserToken(t *testing.T, client *http.Client, base, level string) string {
	t.Helper()
	root := base[:strings.Index(base, "/api/v1")]
	return postJSON(t, client, root+"/api/v1/tokens",
		map[string]any{"scope": "user", "level": level}, http.StatusCreated)["credential"].(string)
}

// Ordinary PRs can be merged by a writer without an approval or required check.
func TestMergeOfOrdinaryChangeNeedsNoReview(t *testing.T) {
	owner, writer, writerToken, base, cloneURL := workflowRepoFixture(t)

	local := filepath.Join(t.TempDir(), "plain")
	gitHTTP(t, "", writerToken, false, "clone", "--quiet", cloneURL, local)
	runGit(t, local, "switch", "-c", "feature/plain")
	if err := os.WriteFile(filepath.Join(local, "notes.txt"), []byte("notes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, local, "add", "notes.txt")
	runGit(t, local, "-c", "user.name=Writer", "-c", "user.email=writer@example.test", "commit", "-m", "Add notes")
	gitHTTP(t, local, writerToken, false, "push", cloneURL, "feature/plain")
	postJSON(t, owner, base+"/pulls", map[string]any{
		"title": "Add notes", "body": "", "baseRef": "main", "headRef": "feature/plain",
	}, http.StatusCreated)

	// The current head is still checked for staleness, but approval is optional.
	detail := getJSON(t, owner, base+"/pulls/1", http.StatusOK)["pull"].(map[string]any)
	postJSON(t, writer, base+"/pulls/1/merge", map[string]any{"expectedHeadSha": detail["currentHeadSha"]}, http.StatusOK)
}
