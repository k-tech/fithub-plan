package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"fithub.local/fithub/internal/store"
)

func (s *Server) listPullReviews(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	number, ok := pullNumber(w, r)
	if !ok {
		return
	}
	if _, err := s.Store.GetPullRequest(r.Context(), repo.ID, number); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, http.StatusNotFound, "NOT_FOUND", "Pull request not found")
		} else {
			fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load pull request")
		}
		return
	}
	page, perPage, ok := commentPagination(w, r)
	if !ok {
		return
	}
	reviews, hasMore, err := s.Store.ListPullReviews(r.Context(), repo.ID, number, page, perPage)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load reviews")
		return
	}
	respond(w, http.StatusOK, map[string]any{"reviews": reviews, "page": page,
		"perPage": perPage, "hasMore": hasMore})
}

func (s *Server) createPullReview(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	number, ok := pullNumber(w, r)
	if !ok {
		return
	}
	pull, err := s.Store.GetPullRequest(r.Context(), repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Pull request not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load pull request")
		return
	}
	if pull.AuthorID == p.user.ID {
		fail(w, http.StatusForbidden, "SELF_REVIEW_DENIED", "Authors cannot approve or block their own pull request")
		return
	}
	if pull.State != "open" {
		fail(w, http.StatusConflict, "PR_NOT_OPEN", "Only open pull requests can be reviewed")
		return
	}
	var body struct {
		State           string `json:"state"`
		Body            string `json:"body"`
		ExpectedBaseSHA string `json:"expectedBaseSha"`
		ExpectedHeadSHA string `json:"expectedHeadSha"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.State != "approved" && body.State != "changes_requested" {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Review state must be approved or changes_requested")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	inspection, err := s.Git.InspectMerge(ctx, repo.ID, "refs/heads/"+pull.BaseRef, pull.HeadRef)
	if err != nil {
		writePullGitError(w, err)
		return
	}
	if body.ExpectedBaseSHA != inspection.BaseSHA || body.ExpectedHeadSHA != inspection.HeadSHA {
		fail(w, http.StatusConflict, "STALE_REVIEW", "Branches changed; refresh the PR before reviewing")
		return
	}
	// Permissions are checked again after Git inspection so a removed grant or
	// revoked token cannot leave a new approval on a PR.
	fresh, err := s.apiPrincipal(r)
	if err != nil || fresh == nil {
		fail(w, http.StatusForbidden, "WRITE_REQUIRED", "Authorization expired before review")
		return
	}
	allowed, err := s.canRepo(ctx, fresh, repo, "write")
	if err != nil || !allowed {
		fail(w, http.StatusForbidden, "WRITE_REQUIRED", "Write permission removed before review")
		return
	}
	review, err := s.Store.CreatePullReview(ctx, repo.ID, number, p.user.ID, body.State, body.Body,
		inspection.BaseSHA, inspection.HeadSHA)
	switch {
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "SELF_REVIEW_DENIED", "Authors cannot review their own PR")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "PR_BUSY", "Pull request changed or is being merged")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid review text or revision")
	case err != nil:
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to save review")
	default:
		// A review notifies the other participants; the reviewer never notifies
		// themselves. The key binds the notice to the review and the revision it
		// judged, so a re-review of the same revision stays one entry and a review
		// of a new revision is a new entry.
		s.notifyPullEvent(ctx, repo.ID, number, p.user.ID, store.NotifyPullReview, pull.Title,
			"pull_review:"+review.ID+":"+review.HeadSHA)
		respond(w, http.StatusCreated, map[string]any{"review": review})
	}
}
