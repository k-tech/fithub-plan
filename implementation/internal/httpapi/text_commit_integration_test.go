package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextCommitAPIRequiresWriteAndAllowsDefaultBranch(t *testing.T) {
	_, srv := gitHookTestServer(t)
	admin, owner, other := newBrowser(t), newBrowser(t), newBrowser(t)
	postJSON(t, admin, srv.URL+"/api/v1/dev/login", map[string]any{"email": "admin@example.test", "name": "Admin"}, http.StatusOK)
	team := postJSON(t, admin, srv.URL+"/api/v1/teams", map[string]any{"slug": "text-api", "name": "Text API"}, http.StatusCreated)["team"].(map[string]any)
	postJSON(t, owner, srv.URL+"/api/v1/dev/login", map[string]any{"email": "author@example.test", "name": "Author"}, http.StatusOK)
	request := postJSON(t, owner, srv.URL+"/api/v1/teams/"+team["id"].(string)+"/join-requests", map[string]any{}, http.StatusCreated)["request"].(map[string]any)
	postJSON(t, admin, srv.URL+"/api/v1/admin/join-requests/"+request["id"].(string)+"/decision", map[string]any{"decision": "approved"}, http.StatusOK)
	postJSON(t, other, srv.URL+"/api/v1/dev/login", map[string]any{"email": "other@example.test", "name": "Other"}, http.StatusOK)
	repository := postJSON(t, owner, srv.URL+"/api/v1/repos", map[string]any{"teamId": team["id"], "slug": "demo", "visibility": "public"}, http.StatusCreated)["repo"].(map[string]any)
	secret := postJSON(t, owner, srv.URL+"/api/v1/tokens", map[string]any{"scope": "repository", "repoId": repository["id"], "level": "write"}, http.StatusCreated)["credential"].(string)
	work := filepath.Join(t.TempDir(), "work")
	runGit(t, "", "init", "--initial-branch=main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("Initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "README.md")
	runGit(t, work, "-c", "user.name=Author", "-c", "user.email=author@example.test", "commit", "-m", "Initial")
	cloneURL := repository["cloneUrl"].(string)
	gitHTTP(t, work, secret, false, "push", cloneURL, "main")
	runGit(t, work, "switch", "-c", "feature/notes")
	gitHTTP(t, work, secret, false, "push", cloneURL, "feature/notes")
	base := srv.URL + "/api/v1/repos/text-api/demo"
	mainBefore := getJSON(t, owner, base+"/branches", http.StatusOK)["branches"].([]any)[0].(map[string]any)["sha"].(string)
	body := map[string]any{"branch": "feature/notes", "expectedHeadSha": mainBefore, "path": "docs/note.txt", "content": "Added by MCP\n", "message": "Add note"}
	postJSON(t, other, base+"/commits", body, http.StatusForbidden)
	created := postJSON(t, owner, base+"/commits", body, http.StatusCreated)
	commitSHA := created["commitSha"].(string)
	if len(commitSHA) != 40 && len(commitSHA) != 64 {
		t.Fatalf("commit SHA=%q", commitSHA)
	}
	getJSON(t, owner, base+"/blob?ref=feature%2Fnotes&path=docs%2Fnote.txt", http.StatusOK)
	postJSON(t, owner, base+"/commits", body, http.StatusConflict)
	body["branch"] = "main"
	mainCommit := postJSON(t, owner, base+"/commits", body, http.StatusCreated)["commitSha"].(string)
	branches := getJSON(t, owner, base+"/branches", http.StatusOK)["branches"].([]any)
	for _, raw := range branches {
		branch := raw.(map[string]any)
		if branch["name"] == "main" && branch["sha"] != mainCommit {
			t.Fatalf("main was not updated: %v", branch)
		}
		if branch["name"] == "feature/notes" && branch["sha"] != commitSHA {
			t.Fatalf("feature not updated: %v", branch)
		}
	}
	if !strings.Contains(getJSON(t, owner, base+"/blob?ref=feature%2Fnotes&path=docs%2Fnote.txt", http.StatusOK)["text"].(string), "MCP") {
		t.Fatal("new text file not browsable")
	}
}
