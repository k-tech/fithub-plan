package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"fithub.local/fithub/internal/store"
)

const maxPullPatchBytes = 2 << 20

type pullRevisions struct {
	BaseSHA      string
	HeadSHA      string
	MergeBaseSHA string
}

// resolvePullRevisions resolves the base/head pair for a pull request and their
// merge base from the server's own refs. Both the diff view and the inline
// comment view use it so a comment is always anchored to the revision the user
// was actually shown.
func (s *Server) resolvePullRevisions(ctx context.Context, repo store.Repository, pull store.PullRequest) (pullRevisions, error) {
	path, err := s.Git.Path(repo.ID)
	if err != nil {
		return pullRevisions{}, errPullGitUnavailable
	}
	var baseSHA, headSHA string
	if pull.State == "merged" && pull.MergedSHA != nil {
		// A merged PR's branch can move again, and main already contains its
		// original head. Use the published merge commit's two exact parents.
		first, firstErr := gitOutput(ctx, path, 128, "rev-parse", "--verify", *pull.MergedSHA+"^1")
		second, secondErr := gitOutput(ctx, path, 128, "rev-parse", "--verify", *pull.MergedSHA+"^2")
		if firstErr != nil || secondErr != nil {
			return pullRevisions{}, errPullGitUnavailable
		}
		baseSHA, headSHA = strings.TrimSpace(string(first)), strings.TrimSpace(string(second))
		if !objectIDPattern.MatchString(baseSHA) || !objectIDPattern.MatchString(headSHA) {
			return pullRevisions{}, errPullGitUnavailable
		}
	} else {
		inspection, inspectErr := s.Git.InspectMerge(ctx, repo.ID, "refs/heads/"+pull.BaseRef, pull.HeadRef)
		if inspectErr != nil {
			return pullRevisions{}, inspectErr
		}
		baseSHA, headSHA = inspection.BaseSHA, inspection.HeadSHA
	}
	mergeBaseOut, err := gitOutput(ctx, path, 128, "merge-base", baseSHA, headSHA)
	if err != nil {
		if ctx.Err() != nil {
			return pullRevisions{}, errPullGitTimeout
		}
		return pullRevisions{}, errPullNoMergeBase
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOut))
	if !objectIDPattern.MatchString(mergeBase) {
		return pullRevisions{}, errPullGitUnavailable
	}
	return pullRevisions{BaseSHA: baseSHA, HeadSHA: headSHA, MergeBaseSHA: mergeBase}, nil
}

var (
	errPullGitUnavailable = errors.New("pull git unavailable")
	errPullGitTimeout     = errors.New("pull git timeout")
	errPullNoMergeBase    = errors.New("pull has no merge base")
)

// pullDiff displays the current development branch against its merge base.
// The two object IDs are resolved from the server's own refs, never from a
// client-supplied revision or path. Git performs the actual diff calculation.
func (s *Server) pullDiff(w http.ResponseWriter, r *http.Request) {
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
		writePullRevisionsError(w, err)
		return
	}
	path, err := s.Git.Path(repo.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Repository storage is unavailable")
		return
	}
	patch, err := gitOutput(ctx, path, maxPullPatchBytes,
		"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-color",
		"--unified=3", revisions.MergeBaseSHA, revisions.HeadSHA, "--")
	if err != nil {
		switch {
		case ctx.Err() != nil:
			fail(w, http.StatusGatewayTimeout, "GIT_TIMEOUT", "Git diff timed out")
		case strings.Contains(err.Error(), "git output limit exceeded"):
			fail(w, http.StatusRequestEntityTooLarge, "DIFF_TOO_LARGE", "This PR diff exceeds the 2 MiB display limit")
		default:
			fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Unable to load PR diff")
		}
		return
	}
	// Render as plain text in React. Strip terminal control characters that
	// could alter a terminal/log viewer if the same API response is printed.
	clean := strings.Map(func(char rune) rune {
		if char == '\n' || char == '\t' || !unicode.IsControl(char) {
			return char
		}
		return -1
	}, strings.ToValidUTF8(string(patch), "\ufffd"))
	respond(w, http.StatusOK, map[string]any{
		"baseSha": revisions.BaseSHA, "headSha": revisions.HeadSHA,
		"mergeBaseSha": revisions.MergeBaseSHA, "patch": clean,
	})
}

// writePullRevisionsError maps a revision resolution failure to the same status
// the diff endpoint has always returned, so callers keep one contract.
func writePullRevisionsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPullGitTimeout):
		fail(w, http.StatusGatewayTimeout, "GIT_TIMEOUT", "Git diff timed out")
	case errors.Is(err, errPullNoMergeBase):
		fail(w, http.StatusConflict, "NO_MERGE_BASE", "These branches have no common ancestor")
	case errors.Is(err, errPullGitUnavailable):
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Unable to load PR revision")
	case errors.Is(err, errPullDiffTooLarge):
		fail(w, http.StatusRequestEntityTooLarge, "DIFF_TOO_LARGE", "This PR diff exceeds the 2 MiB display limit")
	default:
		writePullGitError(w, err)
	}
}
