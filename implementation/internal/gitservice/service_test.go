package gitservice

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const testID = "01a0d254-a825-7661-acb3-25df09b59cc4"

// testUserID stands in for an authenticated account; the receive hook would
// re-check this identity against the database in a real push.
const testUserID = "8b1f5c2a-1c2b-4a5e-9f3d-2f0a7c9d4e11"

func requireGitHTTPBackend(t *testing.T) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git executable unavailable")
	}
	output, err := exec.Command(git, "--exec-path").Output()
	if err != nil {
		t.Skip("cannot locate Git helper directory")
	}
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(output)), "git-http-backend")); err != nil {
		t.Skip("git-http-backend unavailable")
	}
}

func TestSmartHTTPRejectsKnownOversizedRequest(t *testing.T) {
	requireGitHTTPBackend(t)
	svc, err := New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.InitBare(context.Background(), testID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/git-receive-pack", bytes.NewReader(nil))
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	req.ContentLength = maxGitRequestBytes + 1
	w := httptest.NewRecorder()
	svc.ServeHTTP(w, req, testID, "/git-receive-pack", Actor{UserID: testUserID, Kind: "token"})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status: got %d, want 413", w.Code)
	}
}

func gitCommand(t *testing.T, ctx context.Context, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitCommandFails(t *testing.T, ctx context.Context, dir, expectedMessage string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), expectedMessage) {
		t.Fatalf("git %v: error=%v output=%s; want rejection containing %q", args, err, output, expectedMessage)
	}
}

func TestSmartHTTPCloneFetchPush(t *testing.T) {
	requireGitHTTPBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	svc, err := New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.InitBare(ctx, testID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Path(testID); err != nil {
		t.Fatal(err)
	}
	if err := svc.InitBare(ctx, testID); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second initialization: got %v, want ErrAlreadyExists", err)
	}

	var mu sync.Mutex
	var sawChunkedPush bool
	var sawRead, sawWrite bool
	prefix := "/r/" + testID + ".git"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix+"/") {
			http.NotFound(w, r)
			return
		}
		pathInfo := strings.TrimPrefix(r.URL.Path, prefix)
		access, err := Classify(r, pathInfo)
		if err != nil {
			http.Error(w, "invalid Git request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		if access == Read {
			sawRead = true
		} else if access == Write {
			sawWrite = true
		}
		if pathInfo == "/git-receive-pack" && r.ContentLength < 0 {
			sawChunkedPush = true
		}
		mu.Unlock()
		svc.ServeHTTP(w, r, testID, pathInfo, Actor{UserID: testUserID, Kind: "token"})
	}))
	defer server.Close()

	workspace := t.TempDir()
	local := filepath.Join(workspace, "local")
	clone := filepath.Join(workspace, "clone")
	gitCommand(t, ctx, workspace, "init", "--initial-branch=main", local)
	gitCommand(t, ctx, local, "config", "user.email", "tester@example.test")
	gitCommand(t, ctx, local, "config", "user.name", "Test User")
	data := make([]byte, 2<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "large.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, ctx, local, "add", "large.bin")
	gitCommand(t, ctx, local, "commit", "-m", "large initial object")
	gitCommand(t, ctx, local, "remote", "add", "origin", server.URL+prefix)
	gitCommand(t, ctx, local, "-c", "http.postBuffer=1024", "push", "-u", "origin", "main")
	gitCommand(t, ctx, workspace, "clone", server.URL+prefix, clone)
	cloned, err := os.ReadFile(filepath.Join(clone, "large.bin"))
	if err != nil || !bytes.Equal(cloned, data) {
		t.Fatalf("clone did not preserve large object: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("second commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, ctx, local, "add", "README.md")
	gitCommand(t, ctx, local, "commit", "-m", "second commit")
	// Protected-branch refusal is decided by the database-backed policy in the
	// API integration tests. Every repository still carries the fixed
	// namespace rule, so an unwritable ref must be refused here.
	gitCommand(t, ctx, local, "push", "origin", "HEAD:refs/heads/feature/test")
	gitCommandFails(t, ctx, local, "ref namespace is not writable", "push", "origin", "HEAD:refs/pull/1/head")
	gitCommand(t, ctx, local, "tag", "v1")
	gitCommand(t, ctx, local, "push", "origin", "refs/tags/v1")
	gitCommand(t, ctx, clone, "fetch", "origin")
	localHead := gitCommand(t, ctx, local, "rev-parse", "HEAD")
	fetchedHead := gitCommand(t, ctx, clone, "rev-parse", "refs/remotes/origin/feature/test")
	if localHead != fetchedHead {
		t.Fatalf("fetch got %s, want %s", fetchedHead, localHead)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawRead || !sawWrite || !sawChunkedPush {
		t.Fatalf("missing HTTP paths: read=%t write=%t chunked push=%t", sawRead, sawWrite, sawChunkedPush)
	}
}

func TestRejectInvalidIDsAndRoutes(t *testing.T) {
	requireGitHTTPBackend(t)
	svc, err := New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", "UPPERCASE", testID + "/..", strings.ToUpper(testID)} {
		if err := svc.InitBare(context.Background(), id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("InitBare(%q) = %v", id, err)
		}
		if _, err := svc.Path(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Path(%q) = %v", id, err)
		}
	}
	if err := svc.InitBare(context.Background(), testID); err != nil {
		t.Fatal(err)
	}
	requests := []struct {
		method, path, query, contentType string
		want                             error
	}{
		{http.MethodGet, "/info/refs", "service=git-upload-pack", "", nil},
		{http.MethodGet, "/info/refs", "service=git-receive-pack", "", nil},
		{http.MethodGet, "/info/refs", "service=git-upload-pack&service=git-receive-pack", "", ErrInvalidRequest},
		{http.MethodGet, "/info/refs", "service=git-upload-pack&extra=1", "", ErrInvalidRequest},
		{http.MethodPost, "/git-receive-pack", "", "application/x-git-receive-pack-request", nil},
		{http.MethodPost, "/git-receive-pack", "", "text/plain", ErrInvalidRequest},
		{http.MethodGet, "/git-upload-pack", "", "", ErrInvalidRequest},
		{http.MethodGet, "/HEAD", "", "", ErrUnsupportedPath},
		{http.MethodGet, "/objects/aa/bb", "", "", ErrUnsupportedPath},
		{http.MethodGet, "/../info/refs", "service=git-upload-pack", "", ErrUnsupportedPath},
	}
	for _, tc := range requests {
		t.Run(fmt.Sprintf("%s %s?%s", tc.method, tc.path, tc.query), func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://example.test"+tc.path+"?"+tc.query, nil)
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			_, err := Classify(r, tc.path)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Classify error = %v, want %v", err, tc.want)
			}
		})
	}

	outside := t.TempDir()
	linkID := "02a0d254-a825-7661-acb3-25df09b59cc4"
	if err := os.Symlink(outside, svc.diskPath(linkID)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := svc.Path(linkID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("symlink Path error = %v, want ErrNotFound", err)
	}
	if err := svc.InitBare(context.Background(), linkID); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("symlink InitBare error = %v, want ErrAlreadyExists", err)
	}

	badRequest := httptest.NewRequest(http.MethodGet, "http://example.test/info/refs?service=git-upload-pack", nil)
	response := httptest.NewRecorder()
	svc.ServeHTTP(response, badRequest, "../escape", "/info/refs", Actor{})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid ID response = %d", response.Code)
	}
	response = httptest.NewRecorder()
	svc.ServeHTTP(response, badRequest, testID, "/objects/info/packs", Actor{})
	if response.Code != http.StatusNotFound {
		t.Fatalf("dumb HTTP route response = %d", response.Code)
	}
}

func TestConcurrentInitializationPublishesOneRepository(t *testing.T) {
	requireGitHTTPBackend(t)
	svc, err := New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- svc.InitBare(context.Background(), testID) }()
	}
	first, second := <-results, <-results
	if !(first == nil && errors.Is(second, ErrAlreadyExists) || second == nil && errors.Is(first, ErrAlreadyExists)) {
		t.Fatalf("concurrent InitBare results = %v, %v", first, second)
	}
	if _, err := svc.Path(testID); err != nil {
		t.Fatal(err)
	}
}

func TestRejectMalformedCGIStatus(t *testing.T) {
	_, _, err := readCGIHeaders(bufio.NewReader(strings.NewReader("Status: \r\n\r\n")))
	if err == nil {
		t.Fatal("empty CGI status accepted")
	}
}

func TestBackendFailureDoesNotExposeItsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test backend script requires a Unix shell")
	}
	requireGitHTTPBackend(t)
	svc, err := New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.InitBare(context.Background(), testID); err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(t.TempDir(), "failed-backend")
	script := "#!/bin/sh\nprintf 'Status: 500 Internal Error\\r\\nContent-Type: text/plain\\r\\n\\r\\nserver-secret-path'\n"
	if err := os.WriteFile(backend, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	svc.backend = backend
	r := httptest.NewRequest(http.MethodGet, "http://example.test/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	svc.ServeHTTP(w, r, testID, "/info/refs", Actor{})
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "server-secret-path") {
		t.Fatalf("backend failure leaked output: status=%d body=%q", w.Code, w.Body.String())
	}
}
