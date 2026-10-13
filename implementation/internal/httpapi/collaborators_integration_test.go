package httpapi

import (
	"net/http"
	"testing"
)

func TestRepositoryCollaboratorManagement(t *testing.T) {
	_, srv := issueAPIServer(t)
	admin, owner, colleague, outsider := newBrowser(t), newBrowser(t), newBrowser(t), newBrowser(t)
	postJSON(t, admin, srv.URL+"/api/v1/dev/login", map[string]any{"email": "admin@example.test", "name": "Admin"}, http.StatusOK)
	team := postJSON(t, admin, srv.URL+"/api/v1/teams", map[string]any{"slug": "collab", "name": "Collab"}, http.StatusCreated)["team"].(map[string]any)
	teamID := team["id"].(string)
	for _, account := range []struct {
		client *http.Client
		email  string
		name   string
	}{
		{owner, "owner@example.test", "Owner"},
		{colleague, "colleague@example.test", "Colleague"},
	} {
		postJSON(t, account.client, srv.URL+"/api/v1/dev/login", map[string]any{"email": account.email, "name": account.name}, http.StatusOK)
		request := postJSON(t, account.client, srv.URL+"/api/v1/teams/"+teamID+"/join-requests", map[string]any{}, http.StatusCreated)["request"].(map[string]any)
		postJSON(t, admin, srv.URL+"/api/v1/admin/join-requests/"+request["id"].(string)+"/decision", map[string]any{"decision": "approved"}, http.StatusOK)
	}
	postJSON(t, outsider, srv.URL+"/api/v1/dev/login", map[string]any{"email": "outsider@example.test", "name": "Outsider"}, http.StatusOK)
	postJSON(t, owner, srv.URL+"/api/v1/repos", map[string]any{"teamId": teamID, "slug": "private", "visibility": "private"}, http.StatusCreated)
	base := srv.URL + "/api/v1/repos/collab/private/collaborators"
	postJSON(t, owner, base, map[string]any{"email": "unknown@example.test", "role": "write"}, http.StatusNotFound)
	postJSON(t, owner, base, map[string]any{"email": "outsider@example.test", "role": "write"}, http.StatusForbidden)
	added := postJSON(t, owner, base, map[string]any{"email": "colleague@example.test", "role": "write"}, http.StatusOK)["collaborator"].(map[string]any)
	if added["role"] != "write" || added["source"] != "direct" {
		t.Fatalf("collaborator response: %v", added)
	}
	if got := getJSON(t, colleague, srv.URL+"/api/v1/repos/collab/private", http.StatusOK)["repo"].(map[string]any); got["canWrite"] != true {
		t.Fatalf("write collaborator did not gain write access: %v", got)
	}
	getJSON(t, colleague, base, http.StatusForbidden)
	items := getJSON(t, owner, base, http.StatusOK)["collaborators"].([]any)
	if len(items) != 2 {
		t.Fatalf("creator and direct collaborator: %v", items)
	}
	postJSON(t, owner, base, map[string]any{"email": "colleague@example.test", "role": "read"}, http.StatusOK)
	postJSON(t, colleague, srv.URL+"/api/v1/repos/collab/private/issues", map[string]any{"title": "cannot write"}, http.StatusForbidden)
	deleteJSON(t, owner, base+"/"+added["userId"].(string), http.StatusOK)
	getJSON(t, colleague, srv.URL+"/api/v1/repos/collab/private", http.StatusNotFound)
	creator := items[0].(map[string]any)
	if creator["source"] != "creator" {
		creator = items[1].(map[string]any)
	}
	deleteJSON(t, owner, base+"/"+creator["userId"].(string), http.StatusForbidden)
	deleteJSON(t, owner, base+"/not-a-user-id", http.StatusBadRequest)
}

func deleteJSON(t *testing.T, client *http.Client, endpoint string, status int) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	return requestJSON(t, client, req, status)
}
