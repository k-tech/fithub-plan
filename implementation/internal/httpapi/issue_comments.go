package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"fithub.local/fithub/internal/store"
)

func commentPagination(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	page, perPage := 1, 20
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1_000_000 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid comment page")
			return 0, 0, false
		}
		page = parsed
	}
	if raw := r.URL.Query().Get("perPage"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid comment page size")
			return 0, 0, false
		}
		perPage = parsed
	}
	return page, perPage, true
}

func (s *Server) listIssueComments(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	issue, err := s.Store.GetIssue(r.Context(), repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Issue not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load issue")
		return
	}
	page, perPage, ok := commentPagination(w, r)
	if !ok {
		return
	}
	comments, hasMore, err := s.Store.ListIssueComments(r.Context(), issue.ID, page, perPage)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load comments")
		return
	}
	respond(w, http.StatusOK, map[string]any{"comments": comments, "page": page,
		"perPage": perPage, "hasMore": hasMore})
}

func (s *Server) createIssueComment(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	issue, err := s.Store.GetIssue(r.Context(), repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Issue not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load issue")
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &body) {
		return
	}
	comment, err := s.Store.CreateIssueComment(r.Context(), issue.ID, p.user.ID, body.Body)
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Comment must be 1–65536 UTF-8 bytes")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to add comment")
		return
	}
	eventKey := "issue_comment:" + comment.ID
	s.notifyIssueEvent(r.Context(), repo.ID, number, p.user.ID, store.NotifyIssueComment, issue.Title, eventKey)
	respond(w, http.StatusCreated, map[string]any{"comment": comment})
}
