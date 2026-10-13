package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"fithub.local/fithub/internal/store"
	"github.com/go-chi/chi/v5"
)

// Issue metadata endpoints share the repository policy in issueRepo. Reading
// labels needs repository read; changing any metadata needs repository write.

func labelView(label store.Label) map[string]any {
	return map[string]any{
		"id": label.ID, "repoId": label.RepoID, "name": label.Name,
		"color": label.Color, "description": label.Description,
	}
}

func milestoneView(milestone store.Milestone) map[string]any {
	return map[string]any{
		"id": milestone.ID, "repoId": milestone.RepoID, "number": milestone.Number,
		"title": milestone.Title, "description": milestone.Description,
		"state": milestone.State, "dueOn": milestone.DueOn,
		"createdAt": milestone.CreatedAt, "updatedAt": milestone.UpdatedAt,
		"closedAt":   milestone.ClosedAt,
		"openIssues": milestone.OpenIssues, "closedIssues": milestone.ClosedIssues,
	}
}

func assigneeView(assignee store.Assignee) map[string]any {
	return map[string]any{"userId": assignee.UserID, "name": assignee.Name, "email": assignee.Email}
}

func (s *Server) listLabels(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	labels, err := s.Store.ListLabels(r.Context(), repo.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load labels")
		return
	}
	views := make([]map[string]any, 0, len(labels))
	for _, label := range labels {
		views = append(views, labelView(label))
	}
	respond(w, http.StatusOK, map[string]any{"labels": views})
}

func (s *Server) createLabel(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	var body struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if !decode(w, r, &body) {
		return
	}
	label, err := s.Store.CreateLabel(r.Context(), repo.ID, body.Name, body.Color, body.Description)
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Label name must be 1–50 characters, color must be #RRGGBB, and description at most 200 characters")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		fail(w, http.StatusConflict, "LABEL_EXISTS", "This repository already has a label with that name")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to create label")
		return
	}
	respond(w, http.StatusCreated, map[string]any{"label": labelView(label)})
}

func (s *Server) updateLabel(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	var body struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if !decode(w, r, &body) {
		return
	}
	label, err := s.Store.UpdateLabel(r.Context(), repo.ID, chi.URLParam(r, "labelID"), body.Name, body.Color, body.Description)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Label not found")
		return
	}
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Label name must be 1–50 characters, color must be #RRGGBB, and description at most 200 characters")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		fail(w, http.StatusConflict, "LABEL_EXISTS", "This repository already has a label with that name")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to update label")
		return
	}
	respond(w, http.StatusOK, map[string]any{"label": labelView(label)})
}

func (s *Server) deleteLabel(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	err := s.Store.DeleteLabel(r.Context(), repo.ID, chi.URLParam(r, "labelID"))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Label not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to delete label")
		return
	}
	respond(w, http.StatusOK, map[string]any{"deleted": true})
}

// issueMetadataRepo resolves the repository and the Issue before a metadata
// mutation so a missing Issue is a clear 404 rather than a silent no-op.
func (s *Server) issueMetadataRepo(w http.ResponseWriter, r *http.Request, p *principal, level string) (store.Repository, store.Issue, bool) {
	repo, ok := s.issueRepo(w, r, p, level)
	if !ok {
		return store.Repository{}, store.Issue{}, false
	}
	number, ok := issueNumber(w, r)
	if !ok {
		return store.Repository{}, store.Issue{}, false
	}
	issue, err := s.Store.GetIssue(r.Context(), repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Issue not found")
		return store.Repository{}, store.Issue{}, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load issue")
		return store.Repository{}, store.Issue{}, false
	}
	return repo, issue, true
}

func (s *Server) setIssueLabels(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, _, ok := s.issueMetadataRepo(w, r, p, "write")
	if !ok {
		return
	}
	number, _ := issueNumber(w, r)
	var body struct {
		LabelIDs []string `json:"labelIds"`
	}
	if !decode(w, r, &body) {
		return
	}
	labels, err := s.Store.SetIssueLabels(r.Context(), repo.ID, number, body.LabelIDs)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Issue not found")
		return
	}
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Every label must belong to this repository")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to update labels")
		return
	}
	views := make([]map[string]any, 0, len(labels))
	for _, label := range labels {
		views = append(views, labelView(label))
	}
	respond(w, http.StatusOK, map[string]any{"labels": views})
}

func (s *Server) setIssueAssignees(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, issue, ok := s.issueMetadataRepo(w, r, p, "write")
	if !ok {
		return
	}
	number := issue.Number
	var body struct {
		UserIDs []string `json:"userIds"`
	}
	if !decode(w, r, &body) {
		return
	}
	assignees, added, err := s.Store.SetIssueAssignees(r.Context(), repo.ID, number, body.UserIDs, p.user.ID)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Issue not found")
		return
	}
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Assignees must be at most 20 accounts that are active and can read this repository")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to update assignees")
		return
	}
	// Only newly added assignees are notified, so re-saving the same set does not
	// notify everyone on the Issue again. The number is part of the key so two
	// separate additions of the same person to two different Issues stay distinct.
	for _, userID := range added {
		eventKey := "issue_assigned:" + userID + ":" + strconv.FormatInt(number, 10) + ":" + p.user.ID
		s.notify(r.Context(), repo.ID, p.user.ID, store.NotifyIssueAssigned, &number, nil, issue.Title, eventKey, []string{userID})
	}
	views := make([]map[string]any, 0, len(assignees))
	for _, assignee := range assignees {
		views = append(views, assigneeView(assignee))
	}
	respond(w, http.StatusOK, map[string]any{"assignees": views})
}

func (s *Server) setIssueMilestone(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, _, ok := s.issueMetadataRepo(w, r, p, "write")
	if !ok {
		return
	}
	number, _ := issueNumber(w, r)
	var body struct {
		MilestoneNumber *int64 `json:"milestoneNumber"`
	}
	if !decode(w, r, &body) {
		return
	}
	milestone, err := s.Store.SetIssueMilestone(r.Context(), repo.ID, number, body.MilestoneNumber)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Issue or milestone not found in this repository")
		return
	}
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Milestone number must be positive")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to update milestone")
		return
	}
	if milestone == nil {
		respond(w, http.StatusOK, map[string]any{"milestone": nil})
		return
	}
	respond(w, http.StatusOK, map[string]any{"milestone": milestoneView(*milestone)})
}

func (s *Server) listMilestones(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	milestones, err := s.Store.ListMilestones(r.Context(), repo.ID, state)
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "State must be open, closed, or all")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load milestones")
		return
	}
	views := make([]map[string]any, 0, len(milestones))
	for _, milestone := range milestones {
		views = append(views, milestoneView(milestone))
	}
	respond(w, http.StatusOK, map[string]any{"milestones": views})
}

func (s *Server) createMilestone(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		DueOn       string `json:"dueOn"`
	}
	if !decode(w, r, &body) {
		return
	}
	milestone, err := s.Store.CreateMilestone(r.Context(), repo.ID, body.Title, body.Description, body.DueOn)
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Title must be 1–200 characters, description at most 64 KiB, and due date an ISO date")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to create milestone")
		return
	}
	respond(w, http.StatusCreated, map[string]any{"milestone": milestoneView(milestone)})
}

func (s *Server) updateMilestone(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	number, err := strconv.ParseInt(chi.URLParam(r, "milestoneNumber"), 10, 64)
	if err != nil || number < 1 {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Milestone number must be positive")
		return
	}
	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		DueOn       string `json:"dueOn"`
		State       string `json:"state"`
	}
	if !decode(w, r, &body) {
		return
	}
	milestone, err := s.Store.UpdateMilestone(r.Context(), repo.ID, number, body.Title, body.Description, body.DueOn, body.State)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Milestone not found")
		return
	}
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Title, description, due date, or state is invalid")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to update milestone")
		return
	}
	respond(w, http.StatusOK, map[string]any{"milestone": milestoneView(milestone)})
}

func (s *Server) deleteMilestone(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	number, err := strconv.ParseInt(chi.URLParam(r, "milestoneNumber"), 10, 64)
	if err != nil || number < 1 {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Milestone number must be positive")
		return
	}
	err = s.Store.DeleteMilestone(r.Context(), repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Milestone not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to delete milestone")
		return
	}
	respond(w, http.StatusOK, map[string]any{"deleted": true})
}

// assignableUsers lists the accounts that may be assigned to an Issue in this
// repository. It returns exactly the accounts that already have read access, so
// assignment cannot become an account directory.
func (s *Server) assignableUsers(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 100 {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Search text is too long")
		return
	}
	users, err := s.Store.ListAssignableUsers(r.Context(), repo.ID, query)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load assignable users")
		return
	}
	views := make([]map[string]any, 0, len(users))
	for _, user := range users {
		views = append(views, assigneeView(user))
	}
	respond(w, http.StatusOK, map[string]any{"users": views})
}
