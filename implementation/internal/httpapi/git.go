package httpapi

import (
	"net/http"
	"strings"

	"fithub.local/fithub/internal/gitservice"
)

func (s *Server) gitHTTP(w http.ResponseWriter, r *http.Request) {
	escaped := strings.ToLower(r.URL.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		http.NotFound(w, r)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) != 3 || !validSlug(parts[0]) || !strings.HasSuffix(parts[1], ".git") {
		http.NotFound(w, r)
		return
	}
	repoSlug := strings.TrimSuffix(parts[1], ".git")
	if !validSlug(repoSlug) {
		http.NotFound(w, r)
		return
	}
	pathInfo := "/" + parts[2]
	access, err := gitservice.Classify(r, pathInfo)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var p *principal
	if _, password, ok := r.BasicAuth(); ok {
		p, _ = s.tokenPrincipal(r.Context(), password)
	}
	row, err := s.Store.GetRepoBySlug(r.Context(), parts[0], repoSlug)
	if err != nil {
		if p == nil {
			gitChallenge(w)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	level := "read"
	if access == gitservice.Write {
		level = "write"
	}
	allowed, err := s.canRepo(r.Context(), p, row, level)
	if err != nil {
		http.Error(w, "Repository temporarily unavailable", http.StatusInternalServerError)
		return
	}
	if !allowed {
		if p == nil {
			gitChallenge(w)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	// A write must carry the authenticated account into the receive hook, which
	// re-checks the account and current repository write access at push time.
	actor := gitservice.Actor{Kind: "token"}
	if p != nil {
		actor.UserID = p.user.ID
		if p.session {
			actor.Kind = "session"
		}
	}
	s.Git.ServeHTTP(w, r, row.ID, pathInfo, actor)
}

func gitChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="FitHub Git"`)
	http.Error(w, "Git token required", http.StatusUnauthorized)
}
