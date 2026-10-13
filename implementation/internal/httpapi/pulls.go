package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/store"
	"fithub.local/fithub/internal/workflow"
	"github.com/go-chi/chi/v5"
)

func pullView(pull store.PullRequest) map[string]any {
	return map[string]any{
		"id": pull.ID, "repoId": pull.RepoID, "number": pull.Number,
		"title": pull.Title, "body": pull.Body, "state": pull.State,
		"authorId": pull.AuthorID, "authorName": pull.AuthorName,
		"baseRef":           strings.TrimPrefix(pull.BaseRef, "refs/heads/"),
		"headRef":           strings.TrimPrefix(pull.HeadRef, "refs/heads/"),
		"baseShaAtCreation": pull.BaseSHAAtCreation,
		"headShaAtCreation": pull.HeadSHAAtCreation,
		"mergedSha":         pull.MergedSHA,
		"createdAt":         pull.CreatedAt, "updatedAt": pull.UpdatedAt, "mergedAt": pull.MergedAt,
	}
}

func pullPage(w http.ResponseWriter, r *http.Request) (string, int, int, bool) {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "merged" && state != "all" {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid PR state")
		return "", 0, 0, false
	}
	page, perPage := 1, 20
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1_000_000 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid page")
			return "", 0, 0, false
		}
		page = parsed
	}
	if raw := r.URL.Query().Get("perPage"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid page size")
			return "", 0, 0, false
		}
		perPage = parsed
	}
	return state, page, perPage, true
}

func pullNumber(w http.ResponseWriter, r *http.Request) (int64, bool) {
	number, err := strconv.ParseInt(chi.URLParam(r, "number"), 10, 64)
	if err != nil || number < 1 {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid PR number")
		return 0, false
	}
	return number, true
}

func (s *Server) listPulls(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	state, page, perPage, ok := pullPage(w, r)
	if !ok {
		return
	}
	rows, hasMore, err := s.Store.ListPullRequests(r.Context(), repo.ID, state, page, perPage)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load pull requests")
		return
	}
	views := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		views = append(views, pullView(row))
	}
	respond(w, http.StatusOK, map[string]any{"pulls": views, "page": page, "perPage": perPage, "hasMore": hasMore})
}

func (s *Server) createPull(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	var body struct {
		Title   string `json:"title"`
		Body    string `json:"body"`
		BaseRef string `json:"baseRef"`
		HeadRef string `json:"headRef"`
	}
	if !decode(w, r, &body) {
		return
	}
	// Pull requests target the repository's configured default branch. A client
	// may ask for a different existing branch only by naming it, and a branch
	// equal to the head is rejected below by the storage layer.
	baseBranch := repo.DefaultBranch
	if body.BaseRef != "" {
		baseBranch = body.BaseRef
	}
	if baseBranch == "" || strings.HasPrefix(baseBranch, "refs/") || strings.ContainsAny(baseBranch, " \t\r\n") {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Choose a valid target branch")
		return
	}
	if body.HeadRef == "" || body.HeadRef == baseBranch || strings.HasPrefix(body.HeadRef, "refs/") {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Choose an existing development branch")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	headRef := "refs/heads/" + body.HeadRef
	inspection, err := s.Git.InspectMerge(ctx, repo.ID, "refs/heads/"+baseBranch, headRef)
	if err != nil {
		writePullGitError(w, err)
		return
	}
	if inspection.BaseSHA == inspection.HeadSHA || inspection.FilesChanged == 0 {
		fail(w, http.StatusConflict, "EMPTY_PR", "The development branch has no changes to propose")
		return
	}
	pull, err := s.Store.CreatePullRequest(ctx, repo.ID, p.user.ID, body.Title, body.Body,
		baseBranch, headRef, inspection.BaseSHA, inspection.HeadSHA)
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid PR fields or referenced Issue; use Refs #N or Fixes #N on its own line")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		fail(w, http.StatusConflict, "PR_EXISTS", "An open PR already exists for this branch")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to create pull request")
		return
	}
	view := pullView(pull)
	links, err := s.Store.ListPullIssueLinks(r.Context(), repo.ID, pull.Number)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load linked Issues")
		return
	}
	view["issues"] = links
	respond(w, http.StatusCreated, map[string]any{"pull": view})
}

func (s *Server) pull(w http.ResponseWriter, r *http.Request) {
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
	row, err := s.Store.GetPullRequest(r.Context(), repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Pull request not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load pull request")
		return
	}
	view := pullView(row)
	links, err := s.Store.ListPullIssueLinks(r.Context(), repo.ID, number)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load linked Issues")
		return
	}
	view["issues"] = links
	if row.State == "open" {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		inspection, err := s.Git.InspectMerge(ctx, repo.ID, "refs/heads/"+row.BaseRef, row.HeadRef)
		switch {
		case err == nil:
			view["currentBaseSha"] = inspection.BaseSHA
			view["currentHeadSha"] = inspection.HeadSHA
			view["filesChanged"] = inspection.FilesChanged
			view["additions"] = inspection.Additions
			view["deletions"] = inspection.Deletions
			reviews, reviewErr := s.Store.PullReviewSummary(ctx, repo.ID, number, inspection.BaseSHA, inspection.HeadSHA)
			if reviewErr != nil {
				fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load review status")
				return
			}
			view["review"] = reviews
			if inspection.FilesChanged == 0 {
				view["mergeability"] = "empty"
			} else if inspection.Conflicted {
				view["mergeability"] = "conflicting"
			} else {
				view["mergeability"] = "clean"
			}
		case errors.Is(err, gitservice.ErrHeadMissing), errors.Is(err, gitservice.ErrMainMissing):
			view["mergeability"] = "missing_ref"
		default:
			view["mergeability"] = "error"
		}
	}
	respond(w, http.StatusOK, map[string]any{"pull": view})
}

func (s *Server) closePull(w http.ResponseWriter, r *http.Request) {
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
		State string `json:"state"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.State != "closed" {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Only closing a PR is supported here")
		return
	}
	row, err := s.Store.ClosePullRequest(r.Context(), repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Pull request not found")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		fail(w, http.StatusConflict, "PR_BUSY", "Pull request is already merged or being merged")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to close pull request")
		return
	}
	respond(w, http.StatusOK, map[string]any{"pull": pullView(row)})
}

func writePullGitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gitservice.ErrInvalidRef), errors.Is(err, gitservice.ErrInvalidSHA):
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid branch or commit ID")
	case errors.Is(err, gitservice.ErrMainMissing), errors.Is(err, gitservice.ErrHeadMissing):
		fail(w, http.StatusConflict, "MISSING_REF", "The target or development branch no longer exists")
	case errors.Is(err, gitservice.ErrHeadStale), errors.Is(err, gitservice.ErrBaseStale):
		fail(w, http.StatusConflict, "STALE_PR", "A branch changed; refresh the PR before merging")
	case errors.Is(err, gitservice.ErrMergeConflict):
		fail(w, http.StatusConflict, "MERGE_CONFLICT", "Resolve conflicts on the development branch and push again")
	case errors.Is(err, gitservice.ErrAlreadyMerged):
		fail(w, http.StatusConflict, "EMPTY_PR", "This branch has no remaining changes to merge")
	case errors.Is(err, context.DeadlineExceeded):
		fail(w, http.StatusGatewayTimeout, "GIT_TIMEOUT", "Git merge operation timed out")
	default:
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Unable to inspect or merge this pull request")
	}
}

func (s *Server) mergePull(w http.ResponseWriter, r *http.Request) {
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
		ExpectedHeadSHA string `json:"expectedHeadSha"`
	}
	if !decode(w, r, &body) {
		return
	}
	row, err := s.Store.GetPullRequest(r.Context(), repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Pull request not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load pull request")
		return
	}
	if row.State != "open" {
		fail(w, http.StatusConflict, "PR_NOT_OPEN", "Only open pull requests can be merged")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// A pull request that changes a workflow definition is a change to the checks
	// it must satisfy, so merging it requires repository administrator access.
	// This is evaluated from the merge result's own trees, not from a
	// client-supplied file list.
	mergePaths, err := s.Git.MergeChangedPaths(ctx, repo.ID, "refs/heads/"+row.BaseRef, row.HeadRef)
	if err != nil {
		writePullGitError(w, err)
		return
	}
	if touchesWorkflowFiles(mergePaths) {
		isAdmin, adminErr := s.canRepo(ctx, p, repo, "admin")
		if adminErr != nil {
			fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to verify repository access")
			return
		}
		if !isAdmin {
			fail(w, http.StatusForbidden, "WORKFLOW_ADMIN_REQUIRED", "Workflow files under "+workflow.FilePrefix+" may only be changed by a repository administrator")
			return
		}
	}
	candidate, err := s.Git.PrepareMerge(ctx, repo.ID, "refs/heads/"+row.BaseRef, row.HeadRef, body.ExpectedHeadSHA)
	if err != nil {
		writePullGitError(w, err)
		return
	}
	attempt, err := s.Store.StartPullMerge(ctx, repo.ID, number, p.user.ID, candidate.BaseSHA, candidate.HeadSHA, candidate.MergedSHA)
	if errors.Is(err, store.ErrConflict) {
		fail(w, http.StatusConflict, "PR_BUSY", "This PR is already being merged or changed")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to record merge attempt")
		return
	}
	// Check fresh account, token, and repository grants immediately before the
	// ref update. The development-only hook does not yet perform a DB check.
	fresh, err := s.apiPrincipal(r)
	if err != nil || fresh == nil {
		_, _ = s.Store.FinishPullMerge(ctx, attempt.ID, "failed", "Authorization expired")
		fail(w, http.StatusForbidden, "WRITE_REQUIRED", "Authorization expired before merge")
		return
	}
	allowed, err := s.canRepo(ctx, fresh, repo, "write")
	if err != nil || !allowed {
		_, _ = s.Store.FinishPullMerge(ctx, attempt.ID, "failed", "Write permission removed")
		fail(w, http.StatusForbidden, "WRITE_REQUIRED", "Write permission removed before merge")
		return
	}
	_, applyErr := s.Git.ApplyMerge(ctx, repo.ID, candidate)
	// Git can update the ref and lose its reply (for example if the request is
	// canceled at that instant). Resolve uncertain outcomes from the actual ref
	// before recording the database result. Use a fresh context so a client
	// disconnect cannot leave a known successful merge pending indefinitely.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	reached, inspectErr := s.candidateReachedMain(finishCtx, repo.ID, candidate.MergedSHA)
	if inspectErr != nil {
		fail(w, http.StatusInternalServerError, "MERGE_RECONCILIATION_REQUIRED", "Merge result is uncertain; metadata will be reconciled on restart")
		return
	}
	result, reason := "failed", "Git did not update main"
	if reached {
		result, reason = "succeeded", ""
	} else if applyErr != nil {
		reason = boundedHistoryText([]byte(applyErr.Error()), 1900)
	}
	merged, err := s.Store.FinishPullMerge(finishCtx, attempt.ID, result, reason)
	if err != nil {
		fail(w, http.StatusInternalServerError, "MERGE_RECONCILIATION_REQUIRED", "Merge metadata will be reconciled on restart")
		return
	}
	if !reached {
		if applyErr != nil {
			writePullGitError(w, applyErr)
		} else {
			fail(w, http.StatusConflict, "STALE_PR", "Main changed before the merge could be completed")
		}
		return
	}
	// A server-side merge writes the ref directly, so no receive hook runs and the
	// pushed commit would otherwise never start CI. Queue the same trigger a push
	// would have produced, for the commit that actually landed on the base branch.
	s.queuePushTrigger(finishCtx, repo, "refs/heads/"+row.BaseRef, candidate.BaseSHA, candidate.MergedSHA, p.user.ID)
	// A merge is a one-time event per pull request, so the pull request itself is
	// the event key: a retried merge cannot produce a second notice.
	s.notifyPullEvent(finishCtx, repo.ID, row.Number, p.user.ID, store.NotifyPullMerged,
		row.Title, "pull_merged:"+row.ID)
	respond(w, http.StatusOK, map[string]any{"pull": pullView(merged)})
}

// queuePushTrigger records a push trigger for a ref this server wrote itself. A
// failure is logged but never fails the caller: the ref update already happened,
// and CI can be re-dispatched manually.
func (s *Server) queuePushTrigger(ctx context.Context, repo store.Repository, ref, oldSHA, newSHA, actorID string) {
	if !gitservice.ValidSHA(newSHA) {
		return
	}
	event := store.CITriggerEvent{RepoID: repo.ID, Event: "push", Ref: ref, NewSHA: newSHA, ActorID: &actorID}
	if gitservice.ValidSHA(oldSHA) {
		old := oldSHA
		event.OldSHA = &old
	}
	if _, _, err := s.Store.RecordTriggerEvent(ctx, event); err != nil {
		return
	}
}

func (s *Server) candidateReachedMain(ctx context.Context, repoID, candidateSHA string) (bool, error) {
	path, err := s.Git.Path(repoID)
	if err != nil {
		return false, err
	}
	typeOutput, err := gitOutput(ctx, path, 32, "cat-file", "-t", candidateSHA)
	if err != nil {
		// Leave the attempt pending when the candidate cannot be inspected;
		// losing Git storage access must never be interpreted as a failed merge.
		return false, err
	}
	if strings.TrimSpace(string(typeOutput)) != "commit" {
		return false, errors.New("merge candidate is not a Git commit")
	}
	out, err := gitOutput(ctx, path, 128, "merge-base", candidateSHA, "refs/heads/main")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == candidateSHA, nil
}

// ReconcilePendingMerges runs before the local development server accepts
// requests. A pending candidate reachable from main means Git committed before
// a process crash; otherwise the attempt had not completed its ref update.
func (s *Server) ReconcilePendingMerges(ctx context.Context) error {
	for {
		attempts, err := s.Store.ListPendingPullMerges(ctx, 100)
		if err != nil {
			return err
		}
		if len(attempts) == 0 {
			return nil
		}
		for _, attempt := range attempts {
			reached, err := s.candidateReachedMain(ctx, attempt.RepoID, attempt.CandidateSHA)
			if err != nil {
				return err
			}
			result, reason := "failed", "Merge did not update main before restart"
			if reached {
				result, reason = "succeeded", ""
			}
			if _, err := s.Store.FinishPullMerge(ctx, attempt.ID, result, reason); err != nil {
				return err
			}
		}
	}
}

// touchesWorkflowFiles reports whether a change touches a workflow definition.
// It uses the workflow package's path rule, so the admin-only merge rule and the
// workflow loader always agree about which files matter.
func touchesWorkflowFiles(paths []string) bool {
	for _, changed := range paths {
		if workflow.IsWorkflowPath(changed) {
			return true
		}
	}
	return false
}
