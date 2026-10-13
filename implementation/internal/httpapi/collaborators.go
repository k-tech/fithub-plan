package httpapi

import (
	"errors"
	"net/http"

	"fithub.local/fithub/internal/store"
	"github.com/go-chi/chi/v5"
)

func (s *Server) listCollaborators(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "admin")
	if !ok {
		return
	}
	items, err := s.Store.ListRepoCollaborators(r.Context(), repo.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load collaborators")
		return
	}
	respond(w, http.StatusOK, map[string]any{"collaborators": items})
}

func (s *Server) setCollaborator(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "admin")
	if !ok {
		return
	}
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decode(w, r, &body) {
		return
	}
	item, err := s.Store.SetRepoCollaborator(r.Context(), repo.ID, body.Email, body.Role)
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Enter a registered email and read, write, or admin role")
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "USER_NOT_FOUND", "Employee must register before being added")
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "EMPLOYEE_NOT_ELIGIBLE", "Employee must be active and belong to a Team; the creator remains admin")
	case err != nil:
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to update collaborator")
	default:
		respond(w, http.StatusOK, map[string]any{"collaborator": item})
	}
}

func (s *Server) deleteCollaborator(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "admin")
	if !ok {
		return
	}
	err := s.Store.DeleteRepoCollaborator(r.Context(), repo.ID, chi.URLParam(r, "userID"))
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid collaborator ID")
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "NOT_FOUND", "Collaborator not found")
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "CREATOR_REQUIRED", "The repository creator always remains an admin")
	case err != nil:
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to remove collaborator")
	default:
		respond(w, http.StatusOK, map[string]any{"ok": true})
	}
}
