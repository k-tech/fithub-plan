package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/store"
	"github.com/go-chi/chi/v5"
)

const sessionCookie = "fithub_session"

type Config struct {
	DevMode       bool
	DevAdminEmail string
	PublicURL     string
	AppURL        string
	Google        *GoogleOIDC
	SSHBaseURL    string
}

type Server struct {
	Store  *store.Store
	Git    *gitservice.Service
	Config Config
}

func New(data *store.Store, git *gitservice.Service, cfg Config) *Server {
	return &Server{Store: data, Git: git, Config: cfg}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.securityHeaders)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/meta", s.meta)
		r.Get("/auth/google/start", s.googleStart)
		r.Get("/auth/google/callback", s.googleCallback)
		r.Post("/auth/logout", s.logout)
		r.Post("/dev/login", s.devLogin)
		r.Post("/dev/logout", s.devLogout)
		r.Get("/me", s.me)
		// The inbox is always the caller's own; a token cannot widen it and there
		// is no route that reads another account's notifications.
		r.Get("/notifications", s.listNotifications)
		r.Post("/notifications/read", s.markNotificationsRead)
		r.Get("/teams", s.teams)
		r.Post("/teams", s.createTeam)
		r.Post("/teams/{teamID}/join-requests", s.requestJoin)
		r.Get("/me/join-requests", s.myJoinRequests)
		r.Post("/me/join-requests/{requestID}/cancel", s.cancelJoin)
		r.Get("/admin/join-requests", s.pendingJoinRequests)
		r.Post("/admin/join-requests/{requestID}/decision", s.reviewJoin)
		r.Get("/repos", s.repos)
		r.Post("/repos", s.createRepo)
		r.Get("/repos/{team}/{repo}", s.repo)
		r.Get("/repos/{team}/{repo}/collaborators", s.listCollaborators)
		r.Post("/repos/{team}/{repo}/collaborators", s.setCollaborator)
		r.Delete("/repos/{team}/{repo}/collaborators/{userID}", s.deleteCollaborator)
		r.Get("/repos/{team}/{repo}/tree", s.browseTree)
		r.Get("/repos/{team}/{repo}/blob", s.browseBlob)
		r.Get("/repos/{team}/{repo}/raw", s.downloadRaw)
		r.Get("/repos/{team}/{repo}/archive", s.downloadArchive)
		r.Patch("/repos/{team}/{repo}/default-branch", s.setDefaultBranch)
		r.Get("/repos/{team}/{repo}/branches", s.branches)
		r.Get("/repos/{team}/{repo}/tags", s.tags)
		r.Get("/repos/{team}/{repo}/commits", s.commits)
		r.Post("/repos/{team}/{repo}/commits", s.createTextCommit)
		r.Get("/repos/{team}/{repo}/commits/{sha}", s.commitDetail)
		r.Get("/repos/{team}/{repo}/issues", s.listIssues)
		r.Post("/repos/{team}/{repo}/issues", s.createIssue)
		r.Get("/repos/{team}/{repo}/issues/{number}", s.issue)
		r.Patch("/repos/{team}/{repo}/issues/{number}", s.updateIssueState)
		r.Put("/repos/{team}/{repo}/issues/{number}/labels", s.setIssueLabels)
		r.Put("/repos/{team}/{repo}/issues/{number}/assignees", s.setIssueAssignees)
		r.Put("/repos/{team}/{repo}/issues/{number}/milestone", s.setIssueMilestone)
		r.Get("/repos/{team}/{repo}/assignable-users", s.assignableUsers)
		r.Get("/repos/{team}/{repo}/labels", s.listLabels)
		r.Post("/repos/{team}/{repo}/labels", s.createLabel)
		r.Patch("/repos/{team}/{repo}/labels/{labelID}", s.updateLabel)
		r.Delete("/repos/{team}/{repo}/labels/{labelID}", s.deleteLabel)
		r.Get("/repos/{team}/{repo}/milestones", s.listMilestones)
		r.Post("/repos/{team}/{repo}/milestones", s.createMilestone)
		r.Patch("/repos/{team}/{repo}/milestones/{milestoneNumber}", s.updateMilestone)
		r.Delete("/repos/{team}/{repo}/milestones/{milestoneNumber}", s.deleteMilestone)
		r.Get("/repos/{team}/{repo}/issues/{number}/comments", s.listIssueComments)
		r.Post("/repos/{team}/{repo}/issues/{number}/comments", s.createIssueComment)
		r.Get("/repos/{team}/{repo}/pulls", s.listPulls)
		r.Post("/repos/{team}/{repo}/pulls", s.createPull)
		r.Get("/repos/{team}/{repo}/pulls/{number}", s.pull)
		r.Get("/repos/{team}/{repo}/pulls/{number}/diff", s.pullDiff)
		r.Get("/repos/{team}/{repo}/pulls/{number}/comments", s.listPullReviewComments)
		r.Post("/repos/{team}/{repo}/pulls/{number}/comments", s.createPullReviewComment)
		r.Get("/repos/{team}/{repo}/pulls/{number}/reviews", s.listPullReviews)
		r.Post("/repos/{team}/{repo}/pulls/{number}/reviews", s.createPullReview)
		r.Patch("/repos/{team}/{repo}/pulls/{number}", s.closePull)
		r.Post("/repos/{team}/{repo}/pulls/{number}/merge", s.mergePull)
		r.Post("/tokens", s.createToken)
		// Repository CI: read paths use the same repository policy as everything
		// else, so logs and runs are not exposed beyond a readable repository.
		r.Get("/repos/{team}/{repo}/runs", s.listCIRuns)
		r.Get("/repos/{team}/{repo}/runs/{number}", s.getCIRun)
		r.Get("/repos/{team}/{repo}/runs/{number}/logs", s.ciRunLogs)
		r.Post("/repos/{team}/{repo}/runs/demo", s.dispatchCIRun)
		r.Post("/repos/{team}/{repo}/runs/{number}/cancel", s.cancelCIRun)
		r.Get("/repos/{team}/{repo}/deploy-key", s.getRepoDeployKey)
		r.Put("/repos/{team}/{repo}/deploy-key", s.putRepoDeployKey)
		r.Delete("/repos/{team}/{repo}/deploy-key", s.deleteRepoDeployKey)
		// Runner protocol. These endpoints authenticate a registered runner and,
		// for job operations, the attempt's own job token.
		r.Post("/ci/jobs/claim", s.claimCIJob)
		r.Post("/ci/jobs/start", s.startCIJob)
		r.Post("/ci/jobs/heartbeat", s.heartbeatCIJob)
		r.Post("/ci/jobs/complete", s.completeCIJob)
		r.Post("/ci/jobs/log", s.appendCIJobLog)
		r.Get("/ci/jobs/archive", s.ciJobArchive)
		// Runner administration is limited to a platform administrator.
		r.Post("/admin/ci/runners", s.registerCIRunner)
		r.Get("/admin/ci/runners", s.listCIRunners)
		r.Delete("/admin/ci/runners/{runnerID}", s.revokeCIRunner)
		r.Get("/tokens", s.listTokens)
		r.Delete("/tokens/{tokenID}", s.revokeToken)
		r.Get("/ssh-keys", s.listSSHKeys)
		r.Post("/ssh-keys", s.createSSHKey)
		r.Delete("/ssh-keys/{keyID}", s.deleteSSHKey)
	})
	r.HandleFunc("/*", s.gitHTTP)
	return r
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !sameOrigin(r, s.Config.DevMode, s.Config.PublicURL) {
			fail(w, http.StatusForbidden, "ORIGIN_DENIED", "Request origin is not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request, devMode bool, publicURL string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if !devMode {
		public, err := url.Parse(publicURL)
		return err == nil && u.Scheme == "https" && strings.EqualFold(u.Host, public.Host)
	}
	if u.Scheme == "http" && strings.EqualFold(u.Host, r.Host) {
		return true
	}
	// Vite's development proxy can use a different localhost port. It is only
	// accepted for a request that is itself addressed to a loopback host.
	return u.Scheme == "http" && loopbackHostname(u.Hostname()) && loopbackHostname(hostname(r.Host))
}

func loopbackHostname(host string) bool {
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
}

func hostname(hostport string) string {
	u, err := url.Parse("http://" + hostport)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (s *Server) meta(w http.ResponseWriter, _ *http.Request) {
	respond(w, http.StatusOK, map[string]any{"devAuth": s.Config.DevMode, "googleAuth": s.Config.Google != nil, "sshAuth": s.Config.SSHBaseURL != ""})
}

type principal struct {
	user    store.User
	session bool
	token   *store.Token
}

func (s *Server) browserPrincipal(r *http.Request) (*principal, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, nil
	}
	u, err := s.Store.LookupBrowserSession(r.Context(), hashSecret(cookie.Value))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &principal{user: u, session: true}, nil
}

func (s *Server) tokenPrincipal(ctx context.Context, secret string) (*principal, error) {
	if !strings.HasPrefix(secret, "fh_") || len(secret) < 35 {
		return nil, nil
	}
	h := sha256.Sum256([]byte(secret))
	tok, err := s.Store.LookupToken(ctx, hex.EncodeToString(h[:]))
	if err != nil {
		return nil, nil
	}
	if tok.RevokedAt != nil || time.Now().After(tok.ExpiresAt) {
		return nil, nil
	}
	u, err := s.Store.GetUser(ctx, tok.UserID)
	if err != nil || u.Status != "active" {
		return nil, nil
	}
	return &principal{user: u, token: &tok}, nil
}

func (s *Server) apiPrincipal(r *http.Request) (*principal, error) {
	if p, err := s.browserPrincipal(r); err != nil || p != nil {
		return p, err
	}
	value := r.Header.Get("Authorization")
	if strings.HasPrefix(value, "Bearer ") {
		return s.tokenPrincipal(r.Context(), strings.TrimSpace(strings.TrimPrefix(value, "Bearer ")))
	}
	return nil, nil
}

func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) *principal {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return nil
	}
	if p == nil {
		fail(w, http.StatusUnauthorized, "AUTH_REQUIRED", "Sign in to continue")
		return nil
	}
	return p
}

func (s *Server) requirePlatformAdmin(w http.ResponseWriter, r *http.Request) *principal {
	p := s.requireUser(w, r)
	if p == nil {
		return nil
	}
	if !p.session || p.user.Role != "admin" {
		fail(w, http.StatusForbidden, "ADMIN_REQUIRED", "An administrator session is required")
		return nil
	}
	return p
}

func (s *Server) devLogin(w http.ResponseWriter, r *http.Request) {
	if !s.Config.DevMode || !loopbackHostname(hostname(r.Host)) || !loopbackHostname(hostname(r.RemoteAddr)) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Not found")
		return
	}
	var body struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	body.Name = strings.TrimSpace(body.Name)
	if !strings.Contains(body.Email, "@") || len(body.Email) > 254 || body.Name == "" || len(body.Name) > 120 {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Enter a valid name and email")
		return
	}
	u, err := s.Store.UpsertDevUser(r.Context(), body.Email, body.Name)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to create development account")
		return
	}
	if u.Status != "active" {
		fail(w, http.StatusForbidden, "ACCOUNT_DISABLED", "Account is disabled")
		return
	}
	if strings.EqualFold(body.Email, s.Config.DevAdminEmail) && u.Role != "admin" {
		if err := s.Store.SetUserRole(r.Context(), u.ID, "admin"); err != nil {
			fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to prepare development administrator")
			return
		}
		u.Role = "admin"
	}
	if err := s.createSession(r.Context(), w, u.ID); err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to create session")
		return
	}
	respond(w, http.StatusOK, map[string]any{"user": u})
}

func (s *Server) devLogout(w http.ResponseWriter, r *http.Request) {
	if !s.Config.DevMode || !loopbackHostname(hostname(r.Host)) || !loopbackHostname(hostname(r.RemoteAddr)) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Not found")
		return
	}
	s.logout(w, r)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	teams, err := s.Store.ListUserTeams(r.Context(), p.user.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	respond(w, http.StatusOK, map[string]any{"id": p.user.ID, "email": p.user.Email, "name": p.user.Name, "role": p.user.Role, "status": p.user.Status, "teams": teams})
}

func (s *Server) teams(w http.ResponseWriter, r *http.Request) {
	if s.requireUser(w, r) == nil {
		return
	}
	rows, err := s.Store.ListTeams(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load teams")
		return
	}
	respond(w, http.StatusOK, map[string]any{"teams": rows})
}

func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) {
	if s.requirePlatformAdmin(w, r) == nil {
		return
	}
	var body struct{ Slug, Name string }
	if !decode(w, r, &body) {
		return
	}
	body.Slug = strings.TrimSpace(body.Slug)
	body.Name = strings.TrimSpace(body.Name)
	if !validSlug(body.Slug) || body.Name == "" || len(body.Name) > 120 {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid team name or slug")
		return
	}
	row, err := s.Store.CreateTeam(r.Context(), body.Slug, body.Name)
	if err != nil {
		fail(w, http.StatusConflict, "TEAM_CREATE_FAILED", "Team slug may already be in use")
		return
	}
	respond(w, http.StatusCreated, map[string]any{"team": row})
}

func (s *Server) requestJoin(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if !p.session {
		fail(w, http.StatusForbidden, "SESSION_REQUIRED", "Apply for a team in the browser")
		return
	}
	row, err := s.Store.RequestJoin(r.Context(), p.user.ID, chi.URLParam(r, "teamID"))
	if err != nil {
		fail(w, http.StatusConflict, "JOIN_REQUEST_FAILED", "Unable to create team request")
		return
	}
	respond(w, http.StatusCreated, map[string]any{"request": joinRequestView(row)})
}

func (s *Server) myJoinRequests(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	rows, err := s.Store.ListMyRequests(r.Context(), p.user.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load requests")
		return
	}
	views := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		views = append(views, joinRequestView(row))
	}
	respond(w, http.StatusOK, map[string]any{"requests": views})
}

func (s *Server) cancelJoin(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if !p.session {
		fail(w, http.StatusForbidden, "SESSION_REQUIRED", "Manage team applications in the browser")
		return
	}
	row, err := s.Store.CancelJoin(r.Context(), p.user.ID, chi.URLParam(r, "requestID"))
	if err != nil {
		fail(w, http.StatusConflict, "CANCEL_FAILED", "Unable to cancel request")
		return
	}
	respond(w, http.StatusOK, map[string]any{"request": joinRequestView(row)})
}

func (s *Server) pendingJoinRequests(w http.ResponseWriter, r *http.Request) {
	if s.requirePlatformAdmin(w, r) == nil {
		return
	}
	rows, err := s.Store.ListPendingRequests(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load requests")
		return
	}
	views := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		views = append(views, joinRequestView(row))
	}
	respond(w, http.StatusOK, map[string]any{"requests": views})
}

func (s *Server) reviewJoin(w http.ResponseWriter, r *http.Request) {
	p := s.requirePlatformAdmin(w, r)
	if p == nil {
		return
	}
	var body struct{ Decision string }
	if !decode(w, r, &body) {
		return
	}
	if body.Decision != "approved" && body.Decision != "rejected" {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Decision must be approved or rejected")
		return
	}
	row, err := s.Store.ReviewJoin(r.Context(), chi.URLParam(r, "requestID"), p.user.ID, body.Decision)
	if err != nil {
		fail(w, http.StatusConflict, "REVIEW_FAILED", "Unable to review request")
		return
	}
	respond(w, http.StatusOK, map[string]any{"request": joinRequestView(row)})
}

func joinRequestView(row store.JoinRequest) map[string]any {
	return map[string]any{
		"id": row.ID, "status": row.Status, "createdAt": row.CreatedAt,
		"teamId":     row.TeamID,
		"team":       map[string]any{"id": row.TeamID, "slug": row.TeamSlug, "name": row.TeamName},
		"user":       map[string]any{"id": row.UserID, "name": row.UserName, "email": row.UserEmail},
		"reviewedAt": row.ReviewedAt,
	}
}

func validSlug(s string) bool {
	if len(s) < 2 || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "JSON_REQUIRED", "Use application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		fail(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON body")
		return false
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "INVALID_JSON", "Only one JSON value is allowed")
		return false
	}
	return true
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (s *Server) cloneURL(team, repo string) string {
	return fmt.Sprintf("%s/%s/%s.git", strings.TrimRight(s.Config.PublicURL, "/"), team, repo)
}
