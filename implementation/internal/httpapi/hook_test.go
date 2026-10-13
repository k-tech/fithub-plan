package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
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

// installTestHook builds the real hook binary and points the Git service at a
// configuration file whose database URL selects the test schema. It is shared
// by every integration test that performs a real push.
func installTestHook(t *testing.T, git *gitservice.Service, schema string) {
	t.Helper()
	dbURL := os.Getenv("FITHUB_TEST_DATABASE_URL")
	hookURL, err := addSearchPath(dbURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "fithub-hook")
	build := exec.Command("go", "build", "-o", binary, "fithub.local/fithub/cmd/fithub-hook")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build hook: %v: %s", err, output)
	}
	configPath := filepath.Join(t.TempDir(), "hook.json")
	payload, err := json.Marshal(map[string]string{"databaseUrl": hookURL})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	git.SetHookInstallation(gitservice.HookInstallation{ConfigPath: configPath, ExecutablePath: binary})
}

// gitHookTestServer creates a server whose Git service installs the real
// database-backed receive hook. The hook gets a configuration file whose
// database URL carries the same search_path as the test, so the hook and API
// use the same test schema.
func gitHookTestServer(t *testing.T) (*store.Store, *httptest.Server) {
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
	schema := fmt.Sprintf("fithub_hook_http_test_%d", time.Now().UnixNano())
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

	// The hook runs as its own process, so it needs the schema in its own
	// connection URL rather than the pool runtime parameter.
	git, err := gitservice.New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	installTestHook(t, git, schema)
	api := New(data, git, Config{DevMode: true, DevAdminEmail: "admin@example.test"})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	api.Config.PublicURL = srv.URL
	return data, srv
}

// addSearchPath returns dbURL with an explicit search_path option so a separate
// process connects to the same schema the test uses. ConnString does not render
// runtime parameters, so the option is appended to the URL directly.
func addSearchPath(dbURL, schema string) (string, error) {
	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return "", err
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	separator := "?"
	if strings.Contains(dbURL, "?") {
		separator = "&"
	}
	return dbURL + separator + "options=" + url.QueryEscape("-c search_path="+schema), nil
}
