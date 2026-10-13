package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"fithub.local/fithub/internal/sshtransport"
	"fithub.local/fithub/internal/store"
	"github.com/go-chi/chi/v5"
)

func (s *Server) listSSHKeys(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if !p.session {
		fail(w, http.StatusForbidden, "SESSION_REQUIRED", "Manage SSH keys from a browser session")
		return
	}
	keys, err := s.Store.ListSSHKeys(r.Context(), p.user.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load SSH keys")
		return
	}
	respond(w, http.StatusOK, map[string]any{"keys": keys})
}

func (s *Server) createSSHKey(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if !p.session {
		fail(w, http.StatusForbidden, "SESSION_REQUIRED", "Manage SSH keys from a browser session")
		return
	}
	var body struct {
		Name      string `json:"name"`
		PublicKey string `json:"publicKey"`
	}
	if !decode(w, r, &body) {
		return
	}
	parsed, err := sshtransport.ParsePublicKey(body.PublicKey)
	if err != nil {
		fail(w, http.StatusBadRequest, "INVALID_SSH_KEY", "Provide one supported SSH public key without options")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 100 {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Enter an SSH key name of at most 100 characters")
		return
	}
	key, err := s.Store.CreateSSHKey(r.Context(), p.user.ID, name, parsed.Type, parsed.PublicKey, parsed.Fingerprint)
	if errors.Is(err, store.ErrConflict) {
		fail(w, http.StatusConflict, "SSH_KEY_EXISTS", "This SSH key is already registered")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to register SSH key")
		return
	}
	respond(w, http.StatusCreated, map[string]any{"key": key})
}

func (s *Server) deleteSSHKey(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if !p.session {
		fail(w, http.StatusForbidden, "SESSION_REQUIRED", "Manage SSH keys from a browser session")
		return
	}
	if err := s.Store.DeleteSSHKey(r.Context(), p.user.ID, chi.URLParam(r, "keyID")); err != nil {
		fail(w, http.StatusNotFound, "NOT_FOUND", "SSH key not found")
		return
	}
	respond(w, http.StatusOK, map[string]any{"ok": true})
}
