// Package gitservice stores bare Git repositories and serves Git smart HTTP.
// Authentication and authorization belong to the caller: Classify must be
// called before ServeHTTP so the caller can enforce the returned access level.
package gitservice

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxGitRequestBytes int64 = 1 << 30 // First-slice limit: 1 GiB per request.

var (
	ErrInvalidID       = errors.New("invalid repository ID")
	ErrAlreadyExists   = errors.New("repository already exists")
	ErrNotFound        = errors.New("repository not found")
	ErrUnsupportedPath = errors.New("unsupported Git smart HTTP path")
	ErrInvalidRequest  = errors.New("invalid Git smart HTTP request")
)

// Access describes the permission required for one smart HTTP request.
type Access uint8

const (
	Read Access = iota + 1
	Write
)

var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Service owns one private on-disk repository root. It does not authenticate
// users or decide whether an authenticated user may read or write a repository.
type Service struct {
	root    string
	git     string
	backend string
	// hook is the receive-hook installation applied to every new repository.
	// It is empty until SetHookInstallation is called, which is the case in
	// tests that only exercise storage and browsing.
	hook *HookInstallation
}

// SetHookInstallation configures the receive hook applied to repositories
// created afterwards. Without it a new repository has no receive hook, so the
// server must not be run without setting it in production.
func (s *Service) SetHookInstallation(installation HookInstallation) {
	s.hook = &installation
}

// New creates the root if needed and locates the native Git HTTP backend.
// The configured root must be private to the service account (mode 0700).
func New(root string) (*Service, error) {
	if root == "" {
		return nil, errors.New("empty repository root")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0700); err != nil {
		return nil, fmt.Errorf("create repository root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root symlinks: %w", err)
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil || !info.IsDir() {
		return nil, errors.New("repository root is not a directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("repository root must be private to the service account")
	}

	git, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("git executable is unavailable")
	}
	git, err = filepath.Abs(git)
	if err != nil {
		return nil, errors.New("cannot resolve git executable")
	}
	cmd := exec.Command(git, "--exec-path")
	cmd.Env = gitEnv(git)
	execPathBytes, err := cmd.Output()
	if err != nil {
		return nil, errors.New("cannot locate git helper programs")
	}
	execPath := strings.TrimSpace(string(execPathBytes))
	if !filepath.IsAbs(execPath) {
		return nil, errors.New("git helper path is not absolute")
	}
	backend := filepath.Join(execPath, "git-http-backend")
	backendInfo, err := os.Stat(backend)
	if err != nil || !backendInfo.Mode().IsRegular() || backendInfo.Mode().Perm()&0111 == 0 {
		return nil, errors.New("git-http-backend is unavailable")
	}
	return &Service{root: resolvedRoot, git: git, backend: backend}, nil
}

// InitBare initializes a repository in a temporary directory and publishes it
// under its immutable UUID only after Git has completed successfully.
func (s *Service) InitBare(ctx context.Context, id string) error {
	if !canonicalUUID.MatchString(id) {
		return ErrInvalidID
	}
	final := s.diskPath(id)
	if _, err := os.Lstat(final); err == nil {
		return ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check repository destination: %w", err)
	}
	tmp, err := os.MkdirTemp(s.root, ".init-"+id+"-")
	if err != nil {
		return fmt.Errorf("create temporary repository: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := s.runGit(ctx, tmp, "init", "--bare", "--initial-branch=main"); err != nil {
		return err
	}
	// The HTTP gateway performs write authorization. Explicitly enabling this
	// service lets the native backend advertise receive-pack without inventing
	// a fake REMOTE_USER identity for the CGI process.
	if err := s.runGit(ctx, tmp, "config", "--local", "http.receivepack", "true"); err != nil {
		return err
	}
	if err := s.runGit(ctx, tmp, "config", "--local", "http.getanyfile", "false"); err != nil {
		return err
	}
	if err := s.installHook(tmp, id); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		if _, checkErr := os.Lstat(final); checkErr == nil {
			return ErrAlreadyExists
		}
		return fmt.Errorf("publish repository: %w", err)
	}
	return nil
}

// InstallHook applies the configured receive hook to a repository.
func (s *Service) InstallHook(repositoryID string) error {
	path, err := s.Path(repositoryID)
	if err != nil {
		return err
	}
	return s.installHook(path, repositoryID)
}

// installHook applies the configured receive hook to a repository. Without a
// configured policy program it still installs the fixed ref-namespace rule, so
// an unconfigured deployment cannot accept an unwritable ref namespace.
func (s *Service) installHook(repo, repositoryID string) error {
	if s.hook == nil {
		return writeHook(repo, namespaceHookScript())
	}
	return InstallReceiveHook(repo, repositoryID, *s.hook)
}

// Path returns the absolute location of an existing bare repository for
// trusted, server-side Git commands. It never accepts a client path or slug.
func (s *Service) Path(id string) (string, error) {
	if !canonicalUUID.MatchString(id) {
		return "", ErrInvalidID
	}
	path := s.diskPath(id)
	for name, wantDir := range map[string]bool{"": true, "objects": true, "refs": true, "HEAD": false, "config": false} {
		entry := path
		if name != "" {
			entry = filepath.Join(path, name)
		}
		info, err := os.Lstat(entry)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", ErrNotFound
			}
			return "", fmt.Errorf("inspect repository: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != wantDir || (!wantDir && !info.Mode().IsRegular()) {
			return "", ErrNotFound
		}
	}
	return path, nil
}

func (s *Service) diskPath(id string) string { return filepath.Join(s.root, id+".git") }

func (s *Service) runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, s.git, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(s.git)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git repository initialization failed: %w", err)
	}
	return nil
}

// Classify recognizes only the three smart HTTP endpoints. Its result must be
// used by the calling gateway for read/write authorization before ServeHTTP.
func Classify(r *http.Request, pathInfo string) (Access, error) {
	if r == nil || r.URL == nil {
		return 0, ErrInvalidRequest
	}
	protocol := r.Header.Values("Git-Protocol")
	if len(protocol) > 1 || (len(protocol) == 1 && protocol[0] != "version=2" && protocol[0] != "version=1" && protocol[0] != "version=0") {
		return 0, ErrInvalidRequest
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, ErrInvalidRequest
	}
	switch pathInfo {
	case "/info/refs":
		if r.Method != http.MethodGet || len(query) != 1 {
			return 0, ErrInvalidRequest
		}
		service := query["service"]
		if len(service) != 1 {
			return 0, ErrInvalidRequest
		}
		switch service[0] {
		case "git-upload-pack":
			return Read, nil
		case "git-receive-pack":
			return Write, nil
		default:
			return 0, ErrInvalidRequest
		}
	case "/git-upload-pack", "/git-receive-pack":
		if r.Method != http.MethodPost || len(query) != 0 || r.Header.Get("Content-Type") != "application/x-"+strings.TrimPrefix(pathInfo, "/")+"-request" {
			return 0, ErrInvalidRequest
		}
		if pathInfo == "/git-upload-pack" {
			return Read, nil
		}
		return Write, nil
	default:
		return 0, ErrUnsupportedPath
	}
}

// Actor identifies the authenticated account behind a Git request so the
// receive hook can re-check permissions against the current database state
// instead of trusting a decision made when the request started.
type Actor struct {
	// UserID is the authenticated account UUID. It is required for writes.
	UserID string
	// Kind is one of token, session, or ssh and is recorded in the audit trail.
	Kind string
}

// HookEnvironment renders the hook identity variables. The values come from
// the server-side principal, never from anything the client sent.
func (a Actor) HookEnvironment() []string {
	if a.UserID == "" {
		return nil
	}
	kind := a.Kind
	if kind == "" {
		kind = "token"
	}
	return []string{"FITHUB_USER_ID=" + a.UserID, "FITHUB_ACTOR_KIND=" + kind}
}

// ServeHTTP forwards a previously authorized request to git-http-backend.
// pathInfo must be the suffix after the public repository URL, beginning with
// a slash. This method validates it again to prevent accidental bypasses.
// actor is required for receive-pack requests so the receive hook can decide
// with the current account and repository state.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request, id, pathInfo string, actor Actor) {
	access, err := Classify(r, pathInfo)
	if err != nil {
		if errors.Is(err, ErrUnsupportedPath) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "invalid Git request", http.StatusBadRequest)
		}
		return
	}
	if access == Write && actor.UserID == "" {
		http.Error(w, "write requests require an authenticated account", http.StatusForbidden)
		return
	}
	path, err := s.Path(id)
	if err != nil {
		if errors.Is(err, ErrInvalidID) {
			http.Error(w, "invalid repository ID", http.StatusBadRequest)
		} else if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "repository unavailable", http.StatusInternalServerError)
		}
		return
	}

	input := r.Body
	contentLength := r.ContentLength
	if contentLength > maxGitRequestBytes {
		http.Error(w, "Git request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	if r.Method == http.MethodPost && contentLength < 0 {
		// CGI requires CONTENT_LENGTH. Spool unknown-length (e.g. chunked)
		// requests to a bounded private disk file, never to an unbounded buffer.
		spool, err := os.CreateTemp(s.root, ".git-request-*")
		if err != nil {
			http.Error(w, "Git request unavailable", http.StatusInternalServerError)
			return
		}
		defer func() { spool.Close(); os.Remove(spool.Name()) }()
		contentLength, err = io.Copy(spool, io.LimitReader(r.Body, maxGitRequestBytes+1))
		if err != nil {
			http.Error(w, "invalid Git request body", http.StatusBadRequest)
			return
		}
		if contentLength > maxGitRequestBytes {
			http.Error(w, "Git request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "Git request unavailable", http.StatusInternalServerError)
			return
		}
		input = spool
	}
	backendCtx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(backendCtx, s.backend)
	cmd.Env = append(gitEnv(s.git),
		"GIT_PROJECT_ROOT="+s.root,
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO=/"+filepath.Base(path)+pathInfo,
		"QUERY_STRING="+r.URL.RawQuery,
		"REQUEST_METHOD="+r.Method,
		"CONTENT_TYPE="+r.Header.Get("Content-Type"),
		"CONTENT_LENGTH="+strconv.FormatInt(contentLength, 10),
	)
	cmd.Env = append(cmd.Env, actor.HookEnvironment()...)
	if protocol := r.Header.Get("Git-Protocol"); protocol != "" {
		cmd.Env = append(cmd.Env, "GIT_PROTOCOL="+protocol)
	}
	cmd.Stdin = input
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "Git backend unavailable", http.StatusBadGateway)
		return
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, "Git backend unavailable", http.StatusBadGateway)
		return
	}
	reader := bufio.NewReader(stdout)
	status, headers, err := readCGIHeaders(reader)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		http.Error(w, "Git backend failed", http.StatusBadGateway)
		return
	}
	if status < 200 || status >= 300 {
		_, _ = io.Copy(io.Discard, reader)
		_ = cmd.Wait()
		http.Error(w, "Git backend rejected request", status)
		return
	}
	for key, values := range headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(status)
	if _, err := io.Copy(w, reader); err != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait() // The HTTP response may already be streaming; never expose stderr.
}

func readCGIHeaders(reader *bufio.Reader) (int, http.Header, error) {
	status := http.StatusOK
	headers := make(http.Header)
	read := 0
	for {
		lineBytes, err := reader.ReadSlice('\n')
		read += len(lineBytes)
		if err != nil || read > 32*1024 {
			return 0, nil, errors.New("invalid CGI headers")
		}
		line := string(lineBytes)
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			return status, headers, nil
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || key == "" || strings.ContainsAny(key, " \t\r\n") {
			return 0, nil, errors.New("invalid CGI header")
		}
		value = strings.TrimSpace(value)
		if strings.EqualFold(key, "Status") {
			parts := strings.Fields(value)
			if len(parts) == 0 {
				return 0, nil, errors.New("invalid CGI status")
			}
			code, err := strconv.Atoi(parts[0])
			if err != nil || code < 100 || code > 599 {
				return 0, nil, errors.New("invalid CGI status")
			}
			status = code
			continue
		}
		switch http.CanonicalHeaderKey(key) {
		case "Content-Type", "Cache-Control", "Pragma", "Expires":
			headers.Add(key, value)
		}
	}
}

func gitEnv(git string) []string {
	return []string{
		"PATH=" + filepath.Dir(git) + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
}
