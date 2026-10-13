package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"fithub.local/fithub/internal/gitservice"
)

func (s *Server) browseTree(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "main"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	result, err := s.Git.BrowseTree(ctx, repo.ID, ref, r.URL.Query().Get("path"))
	if err != nil {
		writeBrowseError(w, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (s *Server) browseBlob(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "main"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	result, err := s.Git.BrowseBlob(ctx, repo.ID, ref, r.URL.Query().Get("path"))
	if err != nil {
		writeBrowseError(w, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func writeBrowseError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gitservice.ErrBrowseInvalid):
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid branch or repository path")
	case errors.Is(err, gitservice.ErrBrowseNotFound):
		fail(w, http.StatusNotFound, "NOT_FOUND", "Branch or repository path not found")
	case errors.Is(err, gitservice.ErrBrowseTooLarge):
		fail(w, http.StatusRequestEntityTooLarge, "BROWSE_TOO_LARGE", "Directory listing exceeds the current limit")
	case errors.Is(err, gitservice.ErrDownloadTooLarge):
		fail(w, http.StatusRequestEntityTooLarge, "DOWNLOAD_TOO_LARGE", "This file is too large to download here; clone the repository instead")
	case errors.Is(err, context.DeadlineExceeded):
		fail(w, http.StatusGatewayTimeout, "GIT_TIMEOUT", "Repository browse timed out")
	case errors.Is(err, gitservice.ErrBrowseUnsafe):
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Repository configuration is unsafe to browse")
	default:
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Unable to browse repository content")
	}
}
