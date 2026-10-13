package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func putJSON(t *testing.T, client *http.Client, endpoint string, body any, status int) map[string]any {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, endpoint, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return requestJSON(t, client, req, status)
}

// issueMetadataFixture creates an admin, an owner, a reader, and an outsider with
// an internal repository. The owner and reader are Team members; the outsider is
// a registered account that never joined a Team.
func issueMetadataFixture(t *testing.T) (owner, reader, outsider *http.Client, base string) {
	t.Helper()
	_, srv := issueAPIServer(t)
	admin, owner, reader, outsider := newBrowser(t), newBrowser(t), newBrowser(t), newBrowser(t)
	postJSON(t, admin, srv.URL+"/api/v1/dev/login", map[string]any{"email": "admin@example.test", "name": "Admin"}, http.StatusOK)
	team := postJSON(t, admin, srv.URL+"/api/v1/teams", map[string]any{"slug": "engineering", "name": "Engineering"}, http.StatusCreated)["team"].(map[string]any)
	teamID := team["id"].(string)
	for _, account := range []struct {
		client *http.Client
		email  string
		name   string
	}{
		{owner, "owner@example.test", "Owner"},
		{reader, "reader@example.test", "Reader"},
	} {
		postJSON(t, account.client, srv.URL+"/api/v1/dev/login", map[string]any{"email": account.email, "name": account.name}, http.StatusOK)
		request := postJSON(t, account.client, srv.URL+"/api/v1/teams/"+teamID+"/join-requests", map[string]any{}, http.StatusCreated)["request"].(map[string]any)
		postJSON(t, admin, srv.URL+"/api/v1/admin/join-requests/"+request["id"].(string)+"/decision", map[string]any{"decision": "approved"}, http.StatusOK)
	}
	postJSON(t, outsider, srv.URL+"/api/v1/dev/login", map[string]any{"email": "outsider@example.test", "name": "Outsider"}, http.StatusOK)
	postJSON(t, owner, srv.URL+"/api/v1/repos", map[string]any{"teamId": teamID, "slug": "metadata", "visibility": "internal"}, http.StatusCreated)
	return owner, reader, outsider, srv.URL + "/api/v1/repos/engineering/metadata"
}

func TestLabelAPIRequiresWriteAndScopesToRepository(t *testing.T) {
	owner, reader, _, base := issueMetadataFixture(t)
	labels := base + "/labels"
	created := postJSON(t, owner, labels, map[string]any{"name": "Bug", "color": "#FF0000", "description": "Broken"}, http.StatusCreated)["label"].(map[string]any)
	if created["name"] != "Bug" || created["color"] != "ff0000" {
		t.Fatalf("label response: %v", created)
	}
	postJSON(t, owner, labels, map[string]any{"name": "BUG", "color": "00ff00"}, http.StatusConflict)
	postJSON(t, owner, labels, map[string]any{"name": "Bad", "color": "nope"}, http.StatusBadRequest)
	postJSON(t, reader, labels, map[string]any{"name": "Reader", "color": "00ff00"}, http.StatusForbidden)
	if listed := getJSON(t, reader, labels, http.StatusOK)["labels"].([]any); len(listed) != 1 {
		t.Fatalf("reader label list: %v", listed)
	}
	labelID := created["id"].(string)
	updated := requestJSON(t, owner, newJSONRequest(t, http.MethodPatch, labels+"/"+labelID, map[string]any{"name": "Defect", "color": "0000ff", "description": ""}), http.StatusOK)["label"].(map[string]any)
	if updated["name"] != "Defect" {
		t.Fatalf("label update: %v", updated)
	}
	requestJSON(t, reader, newJSONRequest(t, http.MethodPatch, labels+"/"+labelID, map[string]any{"name": "Nope", "color": "0000ff"}), http.StatusForbidden)
	deleteJSON(t, reader, labels+"/"+labelID, http.StatusForbidden)
	deleteJSON(t, owner, labels+"/"+labelID, http.StatusOK)
	getJSON(t, owner, labels, http.StatusOK)
}

func TestMilestoneAPIAndIssueAssignment(t *testing.T) {
	owner, reader, outsider, base := issueMetadataFixture(t)
	milestones := base + "/milestones"
	first := postJSON(t, owner, milestones, map[string]any{"title": "v1.0", "description": "release", "dueOn": "2026-06-30"}, http.StatusCreated)["milestone"].(map[string]any)
	if first["number"] != float64(1) || first["state"] != "open" || first["dueOn"] != "2026-06-30" {
		t.Fatalf("milestone response: %v", first)
	}
	postJSON(t, owner, milestones, map[string]any{"title": "Bad", "dueOn": "tomorrow"}, http.StatusBadRequest)
	postJSON(t, reader, milestones, map[string]any{"title": "Forbidden"}, http.StatusForbidden)
	if listed := getJSON(t, reader, milestones+"?state=open", http.StatusOK)["milestones"].([]any); len(listed) != 1 {
		t.Fatalf("milestone list: %v", listed)
	}
	getJSON(t, reader, milestones+"?state=bogus", http.StatusBadRequest)
	closed := requestJSON(t, owner, newJSONRequest(t, http.MethodPatch, milestones+"/1", map[string]any{"title": "v1.0", "description": "release", "dueOn": "2026-06-30", "state": "closed"}), http.StatusOK)["milestone"].(map[string]any)
	if closed["closedAt"] == nil {
		t.Fatalf("milestone close: %v", closed)
	}
	requestJSON(t, reader, newJSONRequest(t, http.MethodPatch, milestones+"/1", map[string]any{"title": "x", "state": "open"}), http.StatusForbidden)

	issue := postJSON(t, owner, base+"/issues", map[string]any{"title": "Ship it"}, http.StatusCreated)["issue"].(map[string]any)
	if _, ok := issue["labels"].([]any); !ok {
		t.Fatalf("issue must expose an empty label list: %v", issue)
	}
	assigned := putJSON(t, owner, base+"/issues/1/milestone", map[string]any{"milestoneNumber": 1}, http.StatusOK)["milestone"].(map[string]any)
	if assigned["number"] != float64(1) {
		t.Fatalf("issue milestone: %v", assigned)
	}
	putJSON(t, reader, base+"/issues/1/milestone", map[string]any{"milestoneNumber": 1}, http.StatusForbidden)
	if cleared := putJSON(t, owner, base+"/issues/1/milestone", map[string]any{"milestoneNumber": nil}, http.StatusOK); cleared["milestone"] != nil {
		t.Fatalf("milestone clear: %v", cleared)
	}
	putJSON(t, owner, base+"/issues/1/milestone", map[string]any{"milestoneNumber": 99}, http.StatusNotFound)

	label := postJSON(t, owner, base+"/labels", map[string]any{"name": "Urgent", "color": "ff0000"}, http.StatusCreated)["label"].(map[string]any)
	applied := putJSON(t, owner, base+"/issues/1/labels", map[string]any{"labelIds": []string{label["id"].(string)}}, http.StatusOK)["labels"].([]any)
	if len(applied) != 1 {
		t.Fatalf("issue labels: %v", applied)
	}
	putJSON(t, owner, base+"/issues/1/labels", map[string]any{"labelIds": []string{"11111111-1111-1111-1111-111111111111"}}, http.StatusBadRequest)
	putJSON(t, reader, base+"/issues/1/labels", map[string]any{"labelIds": []string{}}, http.StatusForbidden)

	// Both Team members have read access and are assignable; the outsider who
	// never joined a Team is not, and cannot even see the repository.
	users := getJSON(t, owner, base+"/assignable-users", http.StatusOK)["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("assignable users: %v", users)
	}
	ownerID := employeeID(t, owner, srvBase(base))
	assignable := map[string]bool{}
	for _, entry := range users {
		assignable[entry.(map[string]any)["userId"].(string)] = true
	}
	if !assignable[ownerID] {
		t.Fatalf("owner missing from assignable users: %v", users)
	}
	getJSON(t, outsider, base+"/assignable-users", http.StatusNotFound)
	assignees := putJSON(t, owner, base+"/issues/1/assignees", map[string]any{"userIds": []string{ownerID}}, http.StatusOK)["assignees"].([]any)
	if len(assignees) != 1 {
		t.Fatalf("assignees: %v", assignees)
	}
	putJSON(t, owner, base+"/issues/1/assignees", map[string]any{"userIds": []string{"11111111-1111-1111-1111-111111111111"}}, http.StatusBadRequest)
	putJSON(t, reader, base+"/issues/1/assignees", map[string]any{"userIds": []string{}}, http.StatusForbidden)

	// Filters are honoured on the list endpoint.
	filtered := getJSON(t, owner, base+"/issues?state=all&labelId="+label["id"].(string), http.StatusOK)["issues"].([]any)
	if len(filtered) != 1 {
		t.Fatalf("label filter: %v", filtered)
	}
	if list := getJSON(t, owner, base+"/issues?state=all&milestone=5", http.StatusOK)["issues"].([]any); len(list) != 0 {
		t.Fatalf("milestone filter: %v", list)
	}
	getJSON(t, owner, base+"/issues?labelId=not-a-uuid", http.StatusBadRequest)
	getJSON(t, owner, base+"/issues?milestone=zero", http.StatusBadRequest)

	// Deleting the milestone keeps the Issue.
	requestJSON(t, owner, deleteRequest(t, milestones+"/1"), http.StatusOK)
	loaded := getJSON(t, owner, base+"/issues/1", http.StatusOK)["issue"].(map[string]any)
	if loaded["milestoneNumber"] != nil || loaded["state"] != "open" {
		t.Fatalf("issue after milestone delete: %v", loaded)
	}
}

// srvBase derives the server root from a repository API URL.
func srvBase(repoURL string) string {
	return repoURL[:strings.Index(repoURL, "/api/v1/")]
}

func newJSONRequest(t *testing.T, method, endpoint string, body any) *http.Request {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func deleteRequest(t *testing.T, endpoint string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestIssueMetadataRespectsReadOnlyToken(t *testing.T) {
	_, srv := issueAPIServer(t)
	admin, owner, srvURL := newBrowser(t), newBrowser(t), srv.URL
	postJSON(t, admin, srvURL+"/api/v1/dev/login", map[string]any{"email": "admin@example.test", "name": "Admin"}, http.StatusOK)
	team := postJSON(t, admin, srvURL+"/api/v1/teams", map[string]any{"slug": "engineering", "name": "Engineering"}, http.StatusCreated)["team"].(map[string]any)
	postJSON(t, owner, srvURL+"/api/v1/dev/login", map[string]any{"email": "owner@example.test", "name": "Owner"}, http.StatusOK)
	request := postJSON(t, owner, srvURL+"/api/v1/teams/"+team["id"].(string)+"/join-requests", map[string]any{}, http.StatusCreated)["request"].(map[string]any)
	postJSON(t, admin, srvURL+"/api/v1/admin/join-requests/"+request["id"].(string)+"/decision", map[string]any{"decision": "approved"}, http.StatusOK)
	repo := postJSON(t, owner, srvURL+"/api/v1/repos", map[string]any{"teamId": team["id"], "slug": "tokens", "visibility": "internal"}, http.StatusCreated)["repo"].(map[string]any)
	base := srvURL + "/api/v1/repos/engineering/tokens"
	postJSON(t, owner, base+"/issues", map[string]any{"title": "Token target"}, http.StatusCreated)
	readToken := postJSON(t, owner, srvURL+"/api/v1/tokens", map[string]any{"scope": "repository", "repoId": repo["id"], "level": "read"}, http.StatusCreated)["credential"].(string)
	// Reading metadata through a read token is allowed; changing it is not.
	bearerRequest(t, http.DefaultClient, http.MethodGet, base+"/labels", readToken, nil, http.StatusOK)
	bearerRequest(t, http.DefaultClient, http.MethodPost, base+"/labels", readToken, map[string]any{"name": "X", "color": "000000"}, http.StatusForbidden)
	bearerRequest(t, http.DefaultClient, http.MethodPut, base+"/issues/1/labels", readToken, map[string]any{"labelIds": []string{}}, http.StatusForbidden)
	bearerRequest(t, http.DefaultClient, http.MethodGet, base+"/assignable-users", readToken, nil, http.StatusOK)
}
