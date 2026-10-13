package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"fithub.local/fithub/internal/store"
	"github.com/go-chi/chi/v5"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validUUID(value string) bool { return uuidPattern.MatchString(value) }

func issueView(issue store.Issue) map[string]any {
	labels := make([]map[string]any, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, map[string]any{
			"id": label.ID, "name": label.Name, "color": label.Color, "description": label.Description,
		})
	}
	assignees := make([]map[string]any, 0, len(issue.Assignees))
	for _, assignee := range issue.Assignees {
		assignees = append(assignees, map[string]any{
			"userId": assignee.UserID, "name": assignee.Name, "email": assignee.Email,
		})
	}
	return map[string]any{
		"id": issue.ID, "repoId": issue.RepoID, "number": issue.Number,
		"title": issue.Title, "body": issue.Body, "state": issue.State,
		"authorId": issue.AuthorID, "authorName": issue.AuthorName,
		"createdAt": issue.CreatedAt, "updatedAt": issue.UpdatedAt, "closedAt": issue.ClosedAt,
		"closedByPullNumber": issue.ClosedByPullNumber,
		"labels":             labels, "assignees": assignees,
		"milestoneNumber": issue.MilestoneNumber, "milestoneTitle": issue.MilestoneTitle,
	}
}

// issueRepo uses the same authorization policy as Git HTTP and repository
// browsing. A repository the caller cannot read is intentionally a 404.
func (s *Server) issueRepo(w http.ResponseWriter, r *http.Request, p *principal, level string) (store.Repository, bool) {
	repo, err := s.Store.GetRepoBySlug(r.Context(), chi.URLParam(r, "team"), chi.URLParam(r, "repo"))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Repository not found")
		return store.Repository{}, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load repository")
		return store.Repository{}, false
	}
	allowed, err := s.canRepo(r.Context(), p, repo, level)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to check repository access")
		return store.Repository{}, false
	}
	if !allowed {
		if level != "read" {
			canRead, err := s.canRepo(r.Context(), p, repo, "read")
			if err != nil {
				fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to check repository access")
				return store.Repository{}, false
			}
			if canRead {
				if level == "admin" {
					fail(w, http.StatusForbidden, "ADMIN_REQUIRED", "Repository admin access is required")
				} else {
					fail(w, http.StatusForbidden, "WRITE_REQUIRED", "Repository write access is required")
				}
				return store.Repository{}, false
			}
		}
		fail(w, http.StatusNotFound, "NOT_FOUND", "Repository not found")
		return store.Repository{}, false
	}
	return repo, true
}

func issueNumber(w http.ResponseWriter, r *http.Request) (int64, bool) {
	n, err := strconv.ParseInt(chi.URLParam(r, "number"), 10, 64)
	if err != nil || n < 1 {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Issue number must be positive")
		return 0, false
	}
	return n, true
}

// issueFilterValues reads a repeatable filter parameter. Both forms are accepted
// because the user interface sends one value per selection while a bookmark or a
// script is easier to write with a comma-separated list.
func issueFilterValues(w http.ResponseWriter, q url.Values, key, label string) ([]string, bool) {
	values := make([]string, 0, len(q[key]))
	for _, raw := range q[key] {
		for _, part := range strings.Split(raw, ",") {
			candidate := strings.TrimSpace(part)
			if candidate == "" {
				continue
			}
			if !validUUID(candidate) {
				fail(w, http.StatusBadRequest, "INVALID_INPUT", label+" must be a UUID")
				return nil, false
			}
			values = append(values, candidate)
		}
	}
	return values, true
}

func issuePagination(w http.ResponseWriter, r *http.Request) (store.IssueFilter, int, int, bool) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "State must be open, closed, or all")
		return store.IssueFilter{}, 0, 0, false
	}
	filter := store.IssueFilter{State: state}
	if values, ok := issueFilterValues(w, q, "labelId", "Label ID"); !ok {
		return store.IssueFilter{}, 0, 0, false
	} else {
		filter.LabelIDs = values
	}
	if values, ok := issueFilterValues(w, q, "assignee", "Assignee"); !ok {
		return store.IssueFilter{}, 0, 0, false
	} else {
		filter.AssigneeIDs = values
	}
	if raw := q.Get("milestone"); raw != "" {
		number, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || number < 1 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Milestone must be a positive number")
			return store.IssueFilter{}, 0, 0, false
		}
		filter.MilestoneNumber = &number
	}
	page, perPage := 1, 20
	if raw := q.Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 1_000_000 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Page must be between 1 and 1000000")
			return store.IssueFilter{}, 0, 0, false
		}
	}
	if raw := q.Get("perPage"); raw != "" {
		var err error
		perPage, err = strconv.Atoi(raw)
		if err != nil || perPage < 1 || perPage > 100 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Per-page limit must be between 1 and 100")
			return store.IssueFilter{}, 0, 0, false
		}
	}
	return filter, page, perPage, true
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	filter, page, perPage, ok := issuePagination(w, r)
	if !ok {
		return
	}
	rows, hasMore, err := s.Store.ListIssues(r.Context(), repo.ID, filter, page, perPage)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load issues")
		return
	}
	issues := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, issueView(row))
	}
	respond(w, http.StatusOK, map[string]any{"issues": issues, "page": page, "perPage": perPage, "hasMore": hasMore})
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if !decode(w, r, &body) {
		return
	}
	issue, err := s.Store.CreateIssue(r.Context(), repo.ID, p.user.ID, body.Title, body.Body)
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Title must be 1–200 characters and body at most 64 KiB")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Repository not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to create issue")
		return
	}
	respond(w, http.StatusCreated, map[string]any{"issue": issueView(issue)})
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request) {
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
	row, err := s.Store.GetIssue(r.Context(), repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Issue not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load issue")
		return
	}
	respond(w, http.StatusOK, map[string]any{"issue": issueView(row)})
}

func (s *Server) updateIssueState(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		State string `json:"state"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.State != "open" && body.State != "closed" {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "State must be open or closed")
		return
	}
	row, err := s.Store.SetIssueState(r.Context(), repo.ID, number, body.State)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Issue not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to update issue")
		return
	}
	respond(w, http.StatusOK, map[string]any{"issue": issueView(row)})
}
