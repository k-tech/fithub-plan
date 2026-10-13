package httpapi

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoWriteFixture creates an owner and a writer in one team with an internal
// repository, returning their clients and a repository-scoped write token.
func repoWriteFixture(t *testing.T) (owner, writer *http.Client, writerToken, writerID, base string) {
	t.Helper()
	_, srv := gitHookTestServer(t)
	admin, owner, writer := newBrowser(t), newBrowser(t), newBrowser(t)
	postJSON(t, admin, srv.URL+"/api/v1/dev/login", map[string]any{"email": "admin@example.test", "name": "Admin"}, http.StatusOK)
	team := postJSON(t, admin, srv.URL+"/api/v1/teams", map[string]any{"slug": "guard", "name": "Guard"}, http.StatusCreated)["team"].(map[string]any)
	teamID := team["id"].(string)
	for _, account := range []struct {
		client *http.Client
		email  string
		name   string
	}{
		{owner, "owner@example.test", "Owner"},
		{writer, "writer@example.test", "Writer"},
	} {
		postJSON(t, account.client, srv.URL+"/api/v1/dev/login", map[string]any{"email": account.email, "name": account.name}, http.StatusOK)
		request := postJSON(t, account.client, srv.URL+"/api/v1/teams/"+teamID+"/join-requests", map[string]any{}, http.StatusCreated)["request"].(map[string]any)
		postJSON(t, admin, srv.URL+"/api/v1/admin/join-requests/"+request["id"].(string)+"/decision", map[string]any{"decision": "approved"}, http.StatusOK)
	}
	repo := postJSON(t, owner, srv.URL+"/api/v1/repos", map[string]any{"teamId": teamID, "slug": "demo", "visibility": "internal"}, http.StatusCreated)["repo"].(map[string]any)
	postJSON(t, owner, srv.URL+"/api/v1/repos/guard/demo/collaborators", map[string]any{"email": "writer@example.test", "role": "write"}, http.StatusOK)
	token := postJSON(t, writer, srv.URL+"/api/v1/tokens", map[string]any{"scope": "repository", "repoId": repo["id"], "level": "write"}, http.StatusCreated)["credential"].(string)
	return owner, writer, token, employeeID(t, writer, srv.URL), srv.URL + "/api/v1/repos/guard/demo"
}

func TestWriterCanPushDirectlyToDefaultBranch(t *testing.T) {
	owner, _, writerToken, _, base := repoWriteFixture(t)
	repository := getJSON(t, owner, base, http.StatusOK)["repo"].(map[string]any)
	cloneURL := repository["cloneUrl"].(string)
	local := t.TempDir()
	gitInitIfNeeded(t, local)
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, local, "add", "README.md")
	runGit(t, local, "-c", "user.name=Writer", "-c", "user.email=writer@example.test", "commit", "-m", "Initial")
	gitHTTP(t, local, writerToken, false, "push", cloneURL, "main")

	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("updated directly\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, local, "add", "README.md")
	runGit(t, local, "-c", "user.name=Writer", "-c", "user.email=writer@example.test", "commit", "-m", "Update main directly")
	gitHTTP(t, local, writerToken, false, "push", cloneURL, "main")
	shaCmd := exec.Command("git", "rev-parse", "HEAD")
	shaCmd.Dir = local
	sha, err := shaCmd.Output()
	if err != nil {
		t.Fatalf("read expected HEAD: %v", err)
	}
	expectedSHA := strings.TrimSpace(string(sha))
	branches := getJSON(t, owner, base+"/branches", http.StatusOK)["branches"].([]any)
	if len(branches) != 1 || branches[0].(map[string]any)["name"] != "main" || branches[0].(map[string]any)["sha"] != expectedSHA {
		t.Fatalf("default branch was not updated to %s: %v", expectedSHA, branches)
	}
}
