package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func issueAPIServer(t *testing.T) (*store.Store, *httptest.Server) {
	t.Helper()
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
	schema := fmt.Sprintf("fithub_issue_http_test_%d", time.Now().UnixNano())
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
	api := New(data, git, Config{DevMode: true, DevAdminEmail: "admin@example.test"})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	api.Config.PublicURL = srv.URL
	return data, srv
}

func patchJSON(t *testing.T, client *http.Client, endpoint string, body any, status int) map[string]any {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPatch, endpoint, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return requestJSON(t, client, req, status)
}

func bearerRequest(t *testing.T, client *http.Client, method, endpoint, credential string, body any, status int) map[string]any {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return requestJSON(t, client, req, status)
}

func TestIssueAPIRepositoryPermissionsAndTokens(t *testing.T) {
	data, srv := issueAPIServer(t)
	admin := newBrowser(t)
	owner := newBrowser(t)
	reader := newBrowser(t)
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
	repo := postJSON(t, owner, srv.URL+"/api/v1/repos", map[string]any{"teamId": teamID, "slug": "private", "visibility": "private"}, http.StatusCreated)["repo"].(map[string]any)
	base := srv.URL + "/api/v1/repos/engineering/private/issues"
	getJSON(t, reader, base, http.StatusNotFound)
	getJSON(t, newBrowser(t), base, http.StatusNotFound)
	created := postJSON(t, owner, base, map[string]any{"title": "  Fix login  ", "body": "Reproduce first"}, http.StatusCreated)["issue"].(map[string]any)
	if created["number"] != float64(1) || created["title"] != "Fix login" || created["authorName"] != "Owner" || created["state"] != "open" {
		t.Fatalf("Issue response contract: %v", created)
	}
	if err := data.GrantRepoToUser(context.Background(), repo["id"].(string), employeeID(t, reader, srv.URL), "read"); err != nil {
		t.Fatal(err)
	}
	listed := getJSON(t, reader, base, http.StatusOK)
	if issues := listed["issues"].([]any); len(issues) != 1 || listed["hasMore"] != false {
		t.Fatalf("reader list: %v", listed)
	}
	postJSON(t, reader, base, map[string]any{"title": "Forbidden"}, http.StatusForbidden)
	patchJSON(t, reader, base+"/1", map[string]any{"state": "closed"}, http.StatusForbidden)
	readToken := postJSON(t, reader, srv.URL+"/api/v1/tokens", map[string]any{"scope": "repository", "repoId": repo["id"], "level": "read"}, http.StatusCreated)["credential"].(string)
	bearerRequest(t, http.DefaultClient, http.MethodGet, base, readToken, nil, http.StatusOK)
	bearerRequest(t, http.DefaultClient, http.MethodPost, base, readToken, map[string]any{"title": "Forbidden"}, http.StatusForbidden)
	writeToken := postJSON(t, owner, srv.URL+"/api/v1/tokens", map[string]any{"scope": "repository", "repoId": repo["id"], "level": "write"}, http.StatusCreated)["credential"].(string)
	second := bearerRequest(t, http.DefaultClient, http.MethodPost, base, writeToken, map[string]any{"title": "Token issue", "body": "Automated"}, http.StatusCreated)["issue"].(map[string]any)
	if second["number"] != float64(2) {
		t.Fatalf("second issue number: %v", second)
	}
	closed := patchJSON(t, owner, base+"/1", map[string]any{"state": "closed"}, http.StatusOK)["issue"].(map[string]any)
	if closed["closedAt"] == nil || closed["state"] != "closed" {
		t.Fatalf("closed response: %v", closed)
	}
	if issues := getJSON(t, reader, base+"?state=closed", http.StatusOK)["issues"].([]any); len(issues) != 1 {
		t.Fatalf("closed filter: %v", issues)
	}
	if issues := getJSON(t, reader, base+"?state=open", http.StatusOK)["issues"].([]any); len(issues) != 1 || issues[0].(map[string]any)["number"] != float64(2) {
		t.Fatalf("open filter: %v", issues)
	}
	if issues := getJSON(t, reader, base+"?state=all&perPage=1&page=1", http.StatusOK)["issues"].([]any); len(issues) != 1 || issues[0].(map[string]any)["number"] != float64(2) {
		t.Fatalf("descending pagination: %v", issues)
	}
	getJSON(t, reader, base+"/1", http.StatusOK)
	getJSON(t, reader, base+"/bad", http.StatusBadRequest)
	getJSON(t, reader, base+"?state=invalid", http.StatusBadRequest)
	postJSON(t, owner, base, map[string]any{"title": " "}, http.StatusBadRequest)
	publicRepo := postJSON(t, owner, srv.URL+"/api/v1/repos", map[string]any{"teamId": teamID, "slug": "public", "visibility": "public"}, http.StatusCreated)["repo"].(map[string]any)
	if publicRepo["id"] == "" {
		t.Fatal("missing public repository ID")
	}
	publicBase := srv.URL + "/api/v1/repos/engineering/public/issues"
	postJSON(t, owner, publicBase, map[string]any{"title": "Public issue"}, http.StatusCreated)
	getJSON(t, newBrowser(t), publicBase, http.StatusOK)
	getJSON(t, newBrowser(t), publicBase+"/1", http.StatusOK)
	postJSON(t, newBrowser(t), publicBase, map[string]any{"title": "Anonymous"}, http.StatusUnauthorized)
}
