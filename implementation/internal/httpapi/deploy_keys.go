package httpapi

import (
	"errors"
	"net/http"

	"fithub.local/fithub/internal/store"
)

func (s *Server) getRepoDeployKey(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.requireRepoAdmin(w, r)
	if !ok {
		return
	}
	info, err := s.Store.RepoDeployKeyInfo(r.Context(), repo.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load deployment key")
		return
	}
	respond(w, http.StatusOK, map[string]any{"key": info})
}

func (s *Server) putRepoDeployKey(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.requireRepoAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Name       string `json:"name"`
		PrivateKey string `json:"privateKey"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.Store.SetRepoDeployKey(r.Context(), repo.ID, body.Name, body.PrivateKey); errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Provide a name and a complete SSH private key")
		return
	} else if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to save deployment key")
		return
	}
	respond(w, http.StatusOK, map[string]any{"key": map[string]any{"name": body.Name, "configured": true}})
}

func (s *Server) deleteRepoDeployKey(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.requireRepoAdmin(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteRepoDeployKey(r.Context(), repo.ID); err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to remove deployment key")
		return
	}
	respond(w, http.StatusOK, map[string]any{"ok": true})
}
