package httpapi

import (
	"errors"
	"net/http"

	"fithub.local/fithub/internal/store"
	"github.com/go-chi/chi/v5"
)

// requireRepoAdmin resolves the repository and demands repository admin access.
func (s *Server) requireRepoAdmin(w http.ResponseWriter, r *http.Request) (store.Repository, *principal, bool) {
	p := s.requireUser(w, r)
	if p == nil {
		return store.Repository{}, nil, false
	}
	repo, err := s.Store.GetRepoBySlug(r.Context(), chi.URLParam(r, "team"), chi.URLParam(r, "repo"))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Repository not found")
		return store.Repository{}, nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load repository")
		return store.Repository{}, nil, false
	}
	allowed, err := s.canRepo(r.Context(), p, repo, "admin")
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to check repository access")
		return store.Repository{}, nil, false
	}
	if !allowed {
		canRead, err := s.canRepo(r.Context(), p, repo, "read")
		if err != nil {
			fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to check repository access")
			return store.Repository{}, nil, false
		}
		if canRead {
			fail(w, http.StatusForbidden, "ADMIN_REQUIRED", "Repository admin access is required")
		} else {
			fail(w, http.StatusNotFound, "NOT_FOUND", "Repository not found")
		}
		return store.Repository{}, nil, false
	}
	return repo, p, true
}

// PATCH /api/v1/repos/{team}/{repo}/default-branch
func (s *Server) setDefaultBranch(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.requireRepoAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Branch string `json:"branch"`
	}
	if !decode(w, r, &body) {
		return
	}
	path, err := s.Git.Path(repo.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Repository storage is unavailable")
		return
	}
	branches, _, err := listBranchNames(r.Context(), path)
	if err != nil {
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Unable to read repository branches")
		return
	}
	found := false
	for _, branch := range branches {
		if branch == body.Branch {
			found = true
			break
		}
	}
	if !found {
		fail(w, http.StatusBadRequest, "UNKNOWN_BRANCH", "That branch does not exist in this repository")
		return
	}
	if err := s.Store.SetDefaultBranch(r.Context(), repo.ID, body.Branch); err != nil {
		if errors.Is(err, store.ErrInvalid) {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid branch name")
			return
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to update the default branch")
		return
	}
	respond(w, http.StatusOK, map[string]any{"defaultBranch": body.Branch})
}
