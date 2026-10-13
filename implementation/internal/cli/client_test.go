package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientBearerAndAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fh_secret" {
			t.Errorf("authorization header missing or wrong")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"code":"WRITE_REQUIRED","message":"read only"}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "fh_secret", "")
	_, _, err := c.Do(context.Background(), http.MethodGet, "/me", nil)
	api, ok := err.(*APIError)
	if !ok || api.Status != 403 || api.Code != "WRITE_REQUIRED" || api.Message != "read only" {
		t.Fatalf("unexpected API error: %#v", err)
	}
}
func TestDevLoginAndCredentialFilePermissions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/dev/login" {
			http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "session-secret", Path: "/"})
			_, _ = w.Write([]byte(`{"user":{"email":"dev@example.test"}}`))
			return
		}
		if r.Header.Get("Cookie") != sessionCookieName+"=session-secret" {
			t.Errorf("dev cookie was not sent")
		}
		_, _ = w.Write([]byte(`{"id":"u1"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", "")
	if _, err := c.Login(context.Background(), "dev@example.test", "Dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "fithub", "credentials.json")
	if err := SaveCredentials(p, Credentials{BaseURL: srv.URL, Cookie: c.Cookie}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("credential mode=%o", info.Mode().Perm())
	}
	got, err := LoadCredentials(p)
	if err != nil || got.Cookie != "session-secret" {
		t.Fatalf("credentials: %#v %v", got, err)
	}
}
func TestRunJSONRepoList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"repos":[{"slug":"demo"}]}`))
	}))
	defer srv.Close()
	old := os.Getenv("FITHUB_CONFIG")
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "credentials.json"))
	defer os.Setenv("FITHUB_CONFIG", old)
	var out, errOut strings.Builder
	code := Run(context.Background(), []string{"--server", srv.URL, "--json", "repo", "list"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out.String()), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed["repos"].([]any)) != 1 {
		t.Fatalf("result=%s", out.String())
	}
}

func TestCLIApproveUsesCurrentPRRevision(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/engineering/demo/pulls/3":
			_, _ = w.Write([]byte(`{"pull":{"currentBaseSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","currentHeadSha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/engineering/demo/pulls/3/reviews":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"review":{"state":"approved"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "credentials.json"))
	var out, errOut strings.Builder
	code := Run(context.Background(), []string{"--server", srv.URL, "pr", "approve", "engineering/demo", "3", "--body", "Looks good"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if posted["state"] != "approved" || posted["body"] != "Looks good" || posted["expectedBaseSha"] != strings.Repeat("a", 40) || posted["expectedHeadSha"] != strings.Repeat("b", 40) {
		t.Fatalf("posted=%v", posted)
	}
}

func TestCLITextCommitReadsLocalFile(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repos/engineering/demo/commits" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"commitSha":"cccccccccccccccccccccccccccccccccccccccc"}`))
	}))
	defer srv.Close()
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "credentials.json"))
	file := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(file, []byte(" first line\nsecond line  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	code := Run(context.Background(), []string{"--server", srv.URL, "repo", "commit", "engineering/demo", "--branch", "feature/docs", "--head", strings.Repeat("a", 40), "--path", "docs/note.txt", "--file", file, "--message", "Document it"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if posted["content"] != " first line\nsecond line  \n" || posted["branch"] != "feature/docs" {
		t.Fatalf("posted=%v", posted)
	}
}

// `fh run logs` must pass its flags through to the API without panicking. The
// flags follow the subcommand, repository, and run number, so an off-by-one in
// that offset silently reads past the end of the argument slice.
func TestCLIRunLogsParsesFlagsAfterRunNumber(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/engineering/demo/runs/7/logs" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"jobs":[{"jobId":"test","lines":["one\n"],"cursor":1}]}`))
	}))
	defer srv.Close()
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "credentials.json"))
	var out, errOut strings.Builder
	code := Run(context.Background(), []string{"--server", srv.URL, "run", "logs", "engineering/demo", "7", "--job", "test", "--after", "3"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(gotQuery, "after=3") || !strings.Contains(gotQuery, "job=test") {
		t.Fatalf("query=%q", gotQuery)
	}
}

// A run command with a missing or non-numeric run number must fail with a
// message instead of indexing an empty argument slice.
func TestCLIRunRejectsMissingArguments(t *testing.T) {
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "credentials.json"))
	for _, args := range [][]string{
		{"run", "logs", "engineering/demo"},
		{"run", "view", "engineering/demo", "seven"},
		{"run", "cancel", "engineering/demo"},
		{"run"},
	} {
		var out, errOut strings.Builder
		if code := Run(context.Background(), append([]string{"--server", "http://127.0.0.1:1"}, args...), strings.NewReader(""), &out, &errOut); code == 0 {
			t.Fatalf("args %v unexpectedly succeeded", args)
		}
		if strings.TrimSpace(errOut.String()) == "" {
			t.Fatalf("args %v produced no error message", args)
		}
	}
}
