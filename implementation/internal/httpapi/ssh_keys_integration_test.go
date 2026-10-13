package httpapi

import (
	"context"
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
	"fithub.local/fithub/internal/sshtransport"
	"fithub.local/fithub/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSSHKeyAPI(t *testing.T) {
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
	schema := fmt.Sprintf("fithub_ssh_api_test_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})
	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
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
	owner := newBrowser(t)
	other := newBrowser(t)
	postJSON(t, owner, srv.URL+"/api/v1/dev/login", map[string]any{"email": "owner@example.test", "name": "Owner"}, http.StatusOK)
	postJSON(t, other, srv.URL+"/api/v1/dev/login", map[string]any{"email": "other@example.test", "name": "Other"}, http.StatusOK)
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "untrusted-comment", "-f", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	public, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sshtransport.ParsePublicKey(string(public))
	if err != nil {
		t.Fatal(err)
	}
	created := postJSON(t, owner, srv.URL+"/api/v1/ssh-keys", map[string]any{"name": "Laptop", "publicKey": string(public)}, http.StatusCreated)["key"].(map[string]any)
	if created["fingerprint"] != parsed.Fingerprint || strings.Contains(created["publicKey"].(string), "untrusted-comment") {
		t.Fatalf("SSH key not canonicalized: %v", created)
	}
	postJSON(t, owner, srv.URL+"/api/v1/ssh-keys", map[string]any{"name": "Duplicate", "publicKey": string(public)}, http.StatusConflict)
	postJSON(t, owner, srv.URL+"/api/v1/ssh-keys", map[string]any{"name": "Options", "publicKey": `command="/bin/sh" ` + string(public)}, http.StatusBadRequest)
	list := getJSON(t, owner, srv.URL+"/api/v1/ssh-keys", http.StatusOK)["keys"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected one key: %v", list)
	}
	keyID := created["id"].(string)
	deleteKey := func(client *http.Client, status int) {
		t.Helper()
		req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/ssh-keys/"+keyID, nil)
		if err != nil {
			t.Fatal(err)
		}
		requestJSON(t, client, req, status)
	}
	deleteKey(other, http.StatusNotFound)
	token := postJSON(t, owner, srv.URL+"/api/v1/tokens", map[string]any{"scope": "user", "level": "admin"}, http.StatusCreated)["credential"].(string)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/ssh-keys", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	requestJSON(t, newBrowser(t), req, http.StatusForbidden)
	deleteKey(owner, http.StatusOK)
	if keys := getJSON(t, owner, srv.URL+"/api/v1/ssh-keys", http.StatusOK)["keys"].([]any); len(keys) != 0 {
		t.Fatalf("deleted SSH key remains listed: %v", keys)
	}
}
