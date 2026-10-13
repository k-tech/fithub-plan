package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"fithub.local/fithub/internal/authorization"
	"fithub.local/fithub/internal/store"
	"github.com/go-chi/chi/v5"
)

func levelValue(level string) int {
	switch level {
	case "read":
		return 1
	case "write":
		return 2
	case "admin":
		return 3
	default:
		return 0
	}
}

func (s *Server) canRepo(ctx context.Context, p *principal, repo store.Repository, level string) (bool, error) {
	if p == nil {
		return authorization.CanRepository(ctx, s.Store, nil, nil, repo, level)
	}
	return authorization.CanRepository(ctx, s.Store, &p.user, p.token, repo, level)
}

func (s *Server) repos(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	userID := ""
	if p != nil {
		userID = p.user.ID
	}
	rows, err := s.Store.ListRepos(r.Context(), userID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load repositories")
		return
	}
	result := make([]map[string]any, 0, len(rows))
	for _, repo := range rows {
		allowed, err := s.canRepo(r.Context(), p, repo, "read")
		if err != nil {
			fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to check repository access")
			return
		}
		if allowed {
			result = append(result, repoView(repo))
		}
	}
	respond(w, http.StatusOK, map[string]any{"repos": result})
}

func repoView(repo store.Repository) map[string]any {
	return map[string]any{
		"id": repo.ID, "name": repo.Slug, "slug": repo.Slug,
		"team":       map[string]any{"id": repo.TeamID, "slug": repo.TeamSlug, "name": repo.TeamName},
		"visibility": repo.Visibility, "defaultBranch": "main", "updatedAt": repo.CreatedAt,
	}
}

func (s *Server) createRepo(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if p.token != nil && (p.token.Scope != "user" || levelValue(p.token.Level) < levelValue("write")) {
		fail(w, http.StatusForbidden, "WRITE_REQUIRED", "A user write token or browser session is required")
		return
	}
	var body struct {
		TeamID         string `json:"teamId"`
		Slug           string `json:"slug"`
		Visibility     string `json:"visibility"`
		GrantTeamWrite bool   `json:"grantTeamWrite"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Visibility == "" {
		body.Visibility = "internal"
	}
	if !validSlug(body.Slug) || (body.Visibility != "public" && body.Visibility != "internal" && body.Visibility != "private") {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid repository slug or visibility")
		return
	}
	row, err := s.Store.CreateRepo(r.Context(), body.TeamID, body.Slug, body.Visibility, p.user.ID)
	if err != nil {
		fail(w, http.StatusConflict, "REPO_CREATE_FAILED", "Membership and a unique repository name are required")
		return
	}
	if body.GrantTeamWrite {
		if err := s.Store.GrantRepoToTeam(r.Context(), row.ID, row.TeamID, "write"); err != nil {
			_ = s.Store.SetRepoState(r.Context(), row.ID, "failed")
			fail(w, http.StatusInternalServerError, "GRANT_FAILED", "Team write permission could not be created")
			return
		}
	}
	if err := s.Git.InitBare(r.Context(), row.ID); err != nil {
		_ = s.Store.SetRepoState(r.Context(), row.ID, "failed")
		fail(w, http.StatusInternalServerError, "GIT_INIT_FAILED", "Repository storage could not be initialized")
		return
	}
	if err := s.Store.SetRepoState(r.Context(), row.ID, "active"); err != nil {
		fail(w, http.StatusInternalServerError, "REPO_ACTIVATION_FAILED", "Repository was initialized but activation failed")
		return
	}
	row.State = "active"
	view := repoView(row)
	view["cloneUrl"] = s.cloneURL(row.TeamSlug, row.Slug)
	view["sshUrl"] = s.sshCloneURL(row.TeamSlug, row.Slug)
	view["canWrite"] = true
	respond(w, http.StatusCreated, map[string]any{"repo": view})
}

func (s *Server) repo(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	row, err := s.Store.GetRepoBySlug(r.Context(), chi.URLParam(r, "team"), chi.URLParam(r, "repo"))
	if err != nil {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Repository not found")
		return
	}
	allowed, err := s.canRepo(r.Context(), p, row, "read")
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to check repository access")
		return
	}
	if !allowed {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Repository not found")
		return
	}
	view := repoView(row)
	view["cloneUrl"] = s.cloneURL(row.TeamSlug, row.Slug)
	view["sshUrl"] = s.sshCloneURL(row.TeamSlug, row.Slug)
	canWrite, err := s.canRepo(r.Context(), p, row, "write")
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to check repository access")
		return
	}
	view["canWrite"] = canWrite
	canAdmin, err := s.canRepo(r.Context(), p, row, "admin")
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to check repository access")
		return
	}
	view["canAdmin"] = canAdmin
	path, err := s.Git.Path(row.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Repository storage is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, err := gitOutput(ctx, path, 256, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		view["empty"] = true
		view["files"] = []string{}
		respond(w, http.StatusOK, map[string]any{"repo": view})
		return
	}
	view["empty"] = false
	if files, err := gitOutput(ctx, path, 1<<20, "ls-tree", "-z", "--name-only", "HEAD"); err == nil {
		names := make([]string, 0)
		for _, name := range bytes.Split(files, []byte{0}) {
			if len(name) > 0 {
				names = append(names, string(name))
			}
		}
		view["files"] = names
	}
	for _, candidate := range []string{"README.md", "readme.md", "README.MD"} {
		if data, err := gitOutput(ctx, path, 1<<20, "show", "HEAD:"+candidate); err == nil {
			view["readme"] = string(data)
			break
		}
	}
	respond(w, http.StatusOK, map[string]any{"repo": view})
}

func (s *Server) sshCloneURL(team, repo string) string {
	if s.Config.SSHBaseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/%s.git", strings.TrimRight(s.Config.SSHBaseURL, "/"), team, repo)
}

type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		return 0, errors.New("git output limit exceeded")
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

func gitOutput(ctx context.Context, repoPath string, max int, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"--git-dir=" + repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Env = []string{
		"PATH=/usr/bin:/bin:/opt/homebrew/bin",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_NO_REPLACE_OBJECTS=1",
	}
	var out cappedBuffer
	out.max = max
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if !p.session {
		fail(w, http.StatusForbidden, "SESSION_REQUIRED", "Create tokens from the browser session")
		return
	}
	var body struct {
		Scope  string `json:"scope"`
		RepoID string `json:"repoId"`
		Level  string `json:"level"`
	}
	if !decode(w, r, &body) {
		return
	}
	if (body.Scope != "user" && body.Scope != "repository") || levelValue(body.Level) == 0 || (body.Scope == "repository" && body.RepoID == "") || (body.Scope == "user" && body.RepoID != "") {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid token scope or level")
		return
	}
	// Current user rights are checked on every use. Repository tokens also
	// require an existing grant when created, preventing accidental dead tokens.
	if body.Scope == "repository" {
		rows, err := s.Store.ListRepos(r.Context(), p.user.ID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to check repository")
			return
		}
		found := false
		for _, repo := range rows {
			if repo.ID == body.RepoID {
				found, err = s.canRepo(r.Context(), p, repo, "read")
				if err != nil {
					fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to check repository")
					return
				}
				break
			}
		}
		if !found {
			fail(w, http.StatusNotFound, "NOT_FOUND", "Repository not found")
			return
		}
	}
	secretPart, err := randomString(32)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to create token")
		return
	}
	secret := "fh_" + secretPart
	hash := sha256.Sum256([]byte(secret))
	tok, err := s.Store.CreateToken(r.Context(), p.user.ID, body.Scope, body.RepoID, body.Level, secret[:12], hex.EncodeToString(hash[:]), time.Now().Add(30*24*time.Hour))
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to create token")
		return
	}
	respond(w, http.StatusCreated, map[string]any{"token": tok, "credential": secret})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if !p.session {
		fail(w, http.StatusForbidden, "SESSION_REQUIRED", "Manage tokens from the browser session")
		return
	}
	rows, err := s.Store.ListTokens(r.Context(), p.user.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load tokens")
		return
	}
	respond(w, http.StatusOK, map[string]any{"tokens": rows})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if !p.session {
		fail(w, http.StatusForbidden, "SESSION_REQUIRED", "Manage tokens from the browser session")
		return
	}
	if err := s.Store.RevokeToken(r.Context(), p.user.ID, chi.URLParam(r, "tokenID")); err != nil {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Token not found")
		return
	}
	respond(w, http.StatusOK, map[string]any{"ok": true})
}
