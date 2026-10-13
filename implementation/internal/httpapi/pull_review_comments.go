package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fithub.local/fithub/internal/store"
)

// reviewCommentView renders one inline comment. The author's anchors are
// exposed so the UI can place the thread on the diff, and `outdated` tells it
// whether the comment still refers to the current head revision.
func reviewCommentView(comment store.PullReviewComment) map[string]any {
	return map[string]any{
		"id": comment.ID, "pullId": comment.PullID,
		"authorId": comment.AuthorID, "authorName": comment.AuthorName,
		"replyToId": comment.ReplyToID, "path": comment.Path, "line": comment.Line,
		"side": comment.Side, "body": comment.Body,
		"baseSha": comment.BaseSHA, "headSha": comment.HeadSHA,
		"createdAt": comment.CreatedAt, "updatedAt": comment.UpdatedAt,
		"outdated": comment.Outdated,
	}
}

// listPullReviewComments returns the inline comments of a pull request together
// with the revision they are compared against.
func (s *Server) listPullReviewComments(w http.ResponseWriter, r *http.Request) {
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
	page, perPage := 1, 100
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 1_000_000 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid page")
			return
		}
		page = parsed
	}
	if raw := r.URL.Query().Get("perPage"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 200 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid page size")
			return
		}
		perPage = parsed
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
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	revisions, err := s.resolvePullRevisions(ctx, repo, pull)
	if err != nil {
		// A closed-but-unmerged or unavailable revision must not hide comments
		// entirely; fall back to the PR's recorded revision so the page works.
		revisions = pullRevisions{BaseSHA: pull.BaseSHAAtCreation, HeadSHA: pull.HeadSHAAtCreation}
	}
	result, err := s.Store.ListPullReviewComments(r.Context(), repo.ID, number, revisions.BaseSHA, revisions.HeadSHA, page, perPage)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load review comments")
		return
	}
	views := make([]map[string]any, 0, len(result.Comments))
	for _, comment := range result.Comments {
		views = append(views, reviewCommentView(comment))
	}
	respond(w, http.StatusOK, map[string]any{
		"comments": views, "hasMore": result.HasMore,
		"baseSha": result.CurrentBase, "headSha": result.CurrentHead,
		"outdatedCount": result.TotalOutdate,
	})
}

// createPullReviewComment stores one inline comment or one reply in a thread.
// Commenting requires write access, matching the rule for submitting a review,
// so readers cannot leave review notes.
func (s *Server) createPullReviewComment(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Side      string `json:"side"`
		Body      string `json:"body"`
		ReplyToID string `json:"replyToId"`
		BaseSHA   string `json:"baseSha"`
		HeadSHA   string `json:"headSha"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Side == "" {
		body.Side = "new"
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
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	// The client may echo the revision it displayed. When it does, it must be
	// the revision the server would resolve now; otherwise the comment could be
	// anchored to a stale or forged revision. Without an echo the server uses
	// its own current revision, which is the revision the diff endpoint shows.
	revisions, revErr := s.resolvePullRevisions(ctx, repo, pull)
	headSHA, baseSHA := body.HeadSHA, body.BaseSHA
	if revErr == nil {
		if headSHA != "" && headSHA != revisions.HeadSHA {
			fail(w, http.StatusConflict, "STALE_REVISION", "The pull request changed; reload the diff before commenting")
			return
		}
		headSHA, baseSHA = revisions.HeadSHA, revisions.BaseSHA
	} else {
		if !validReviewSHA(headSHA) || !validReviewSHA(baseSHA) {
			writePullRevisionsError(w, revErr)
			return
		}
	}
	if body.ReplyToID == "" {
		// A new thread must anchor to a line that exists in the current diff.
		// Checking here prevents store-level anchors that no reviewer could see.
		if body.Line < 1 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Line must be a positive number")
			return
		}
		patch, patchErr := s.currentPullPatch(ctx, repo, revisions)
		if patchErr != nil {
			writePullRevisionsError(w, patchErr)
			return
		}
		if !diffLineExists(patch, body.Path, body.Line, body.Side) {
			fail(w, http.StatusBadRequest, "INVALID_ANCHOR", "That line is not part of the current diff")
			return
		}
	} else if body.Line < 1 {
		// Replies inherit the parent's anchor, so only a positive line is kept.
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Line must be a positive number")
		return
	}
	comment, err := s.Store.CreatePullReviewComment(r.Context(), repo.ID, number, p.user.ID,
		body.ReplyToID, body.Path, body.Side, body.Body, baseSHA, headSHA, body.Line)
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Comment must be 1–65536 UTF-8 bytes with a valid file path and line")
		return
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "PR_CLOSED", "This pull request is closed")
		return
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "NOT_FOUND", "The comment being replied to was not found")
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to save review comment")
		return
	}
	comment.Outdated = false
	// The event key includes the comment ID, so a reply notifies once even if the
	// request is retried.
	s.notifyPullEvent(r.Context(), repo.ID, number, p.user.ID, store.NotifyPullComment,
		pull.Title, "pull_comment:"+comment.ID)
	respond(w, http.StatusCreated, map[string]any{"comment": reviewCommentView(comment)})
}

// currentPullPatch returns the bounded patch used for anchor validation.
func (s *Server) currentPullPatch(ctx context.Context, repo store.Repository, revisions pullRevisions) (string, error) {
	if revisions.MergeBaseSHA == "" || revisions.HeadSHA == "" {
		return "", errPullGitUnavailable
	}
	path, err := s.Git.Path(repo.ID)
	if err != nil {
		return "", errPullGitUnavailable
	}
	patch, err := gitOutput(ctx, path, maxPullPatchBytes,
		"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-color",
		"--unified=3", revisions.MergeBaseSHA, revisions.HeadSHA, "--")
	if err != nil {
		if ctx.Err() != nil {
			return "", errPullGitTimeout
		}
		if strings.Contains(err.Error(), "git output limit exceeded") {
			return "", errPullDiffTooLarge
		}
		return "", errPullGitUnavailable
	}
	return string(patch), nil
}

var (
	errPullDiffTooLarge = errors.New("pull diff too large")
)

func validReviewSHA(value string) bool {
	return objectIDPattern.MatchString(strings.TrimSpace(value))
}
