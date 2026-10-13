package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"fithub.local/fithub/internal/gitservice"
)

// createTextCommit is intentionally narrow: one regular UTF-8 file on an
// already-existing development branch. Git's native plumbing writes the blob,
// tree, and commit, then atomically compares the branch tip before publishing.
func (s *Server) createTextCommit(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	var body struct {
		Branch          string `json:"branch"`
		ExpectedHeadSHA string `json:"expectedHeadSha"`
		Path            string `json:"path"`
		Content         string `json:"content"`
		Message         string `json:"message"`
	}
	if !decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	candidate, err := s.Git.PrepareTextCommit(ctx, repo.ID, body.Branch, body.ExpectedHeadSHA,
		body.Path, body.Content, body.Message, p.user.Name, p.user.Email)
	if err != nil {
		writeTextCommitError(w, err)
		return
	}
	// Recheck the live account, token, and grants after object preparation and
	// immediately before the ref CAS. These objects remain unreachable on deny.
	fresh, err := s.apiPrincipal(r)
	if err != nil || fresh == nil || fresh.user.ID != p.user.ID {
		fail(w, http.StatusForbidden, "WRITE_REQUIRED", "Authorization expired before commit")
		return
	}
	allowed, err := s.canRepo(ctx, fresh, repo, "write")
	if err != nil || !allowed {
		fail(w, http.StatusForbidden, "WRITE_REQUIRED", "Write permission removed before commit")
		return
	}
	if err := s.Git.ApplyTextCommit(ctx, repo.ID, candidate); err != nil {
		writeTextCommitError(w, err)
		return
	}
	respond(w, http.StatusCreated, map[string]any{
		"commitSha": candidate.CommitSHA,
		"branch":    body.Branch,
		"path":      body.Path,
	})
}

func writeTextCommitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gitservice.ErrTextCommitInvalid):
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Use an existing branch, a valid expected SHA, one text file up to 64 KiB, and a short one-line message")
	case errors.Is(err, gitservice.ErrTextCommitUnsafe):
		fail(w, http.StatusBadRequest, "UNSAFE_PATH", "Cannot edit a symlink, submodule, Git metadata, or workflow configuration")
	case errors.Is(err, gitservice.ErrHeadMissing):
		fail(w, http.StatusNotFound, "BRANCH_NOT_FOUND", "Branch not found")
	case errors.Is(err, gitservice.ErrHeadStale):
		fail(w, http.StatusConflict, "STALE_BRANCH", "Branch changed; refresh its head SHA before committing")
	case errors.Is(err, gitservice.ErrAlreadyMerged):
		fail(w, http.StatusConflict, "NO_CHANGE", "The submitted file content does not change this branch")
	case errors.Is(err, context.DeadlineExceeded):
		fail(w, http.StatusGatewayTimeout, "GIT_TIMEOUT", "Git commit timed out")
	default:
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Unable to create text commit")
	}
}
