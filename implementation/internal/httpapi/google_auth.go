package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fithub.local/fithub/internal/store"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	oidcStateCookie  = "fithub_oidc_state"
	oidcCallbackPath = "/api/v1/auth/google/callback"
	sessionLifetime  = 12 * time.Hour
)

// GoogleOIDC uses Google's discovery document and JWKS-backed ID token
// verifier. It never stores Google access or refresh tokens.
type GoogleOIDC struct {
	config       oauth2.Config
	verifier     *oidc.IDTokenVerifier
	hostedDomain string
}

func NewGoogleOIDC(ctx context.Context, clientID, clientSecret, callbackURL, hostedDomain string) (*GoogleOIDC, error) {
	if clientID == "" || clientSecret == "" || hostedDomain == "" {
		return nil, errors.New("Google client ID, secret, and hosted domain are required")
	}
	callback, err := url.Parse(callbackURL)
	if err != nil || callback.Host == "" || callback.Path != oidcCallbackPath || callback.RawQuery != "" || callback.Fragment != "" || (callback.Scheme != "https" && !(callback.Scheme == "http" && loopbackHostname(callback.Hostname()))) {
		return nil, errors.New("Google callback must be an HTTPS URL (or localhost HTTP) at the exact callback path")
	}
	provider, err := oidc.NewProvider(ctx, "https://accounts.google.com")
	if err != nil {
		return nil, fmt.Errorf("Google OIDC discovery: %w", err)
	}
	if provider.Endpoint().AuthURL == "" || provider.Endpoint().TokenURL == "" {
		return nil, errors.New("Google OIDC discovery lacks required endpoints")
	}
	return &GoogleOIDC{
		config: oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret, RedirectURL: callbackURL,
			Endpoint: provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, oidc.ScopeEmail, oidc.ScopeProfile},
		},
		verifier:     provider.Verifier(&oidc.Config{ClientID: clientID}),
		hostedDomain: strings.ToLower(strings.TrimSpace(hostedDomain)),
	}, nil
}

func (g *GoogleOIDC) authCodeURL(state, verifier, nonce string) string {
	return g.config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("hd", g.hostedDomain))
}

type googleIdentity struct {
	subject string
	email   string
	name    string
}

func (g *GoogleOIDC) exchangeAndVerify(ctx context.Context, code, codeVerifier, expectedNonce string) (googleIdentity, error) {
	token, err := g.config.Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return googleIdentity{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return googleIdentity{}, errors.New("Google did not return an ID token")
	}
	return g.verifyIDToken(ctx, raw, expectedNonce)
}

func (g *GoogleOIDC) verifyIDToken(ctx context.Context, raw, expectedNonce string) (googleIdentity, error) {
	// go-oidc checks the JWKS signature, exact canonical issuer, audience,
	// expiry, and supported signing algorithm. The nonce and domain are
	// application-specific checks below.
	idToken, err := g.verifier.Verify(ctx, raw)
	if err != nil {
		return googleIdentity{}, fmt.Errorf("verify ID token: %w", err)
	}
	if expectedNonce == "" || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(expectedNonce)) != 1 {
		return googleIdentity{}, errors.New("ID token nonce mismatch")
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		HostedDomain  string `json:"hd"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return googleIdentity{}, fmt.Errorf("decode ID token claims: %w", err)
	}
	if !claims.EmailVerified || strings.TrimSpace(claims.Email) == "" {
		return googleIdentity{}, errors.New("Google email is unverified or missing")
	}
	if !strings.EqualFold(claims.HostedDomain, g.hostedDomain) || g.hostedDomain == "" {
		return googleIdentity{}, errors.New("Google hosted domain is not allowed")
	}
	if idToken.Subject == "" || len(idToken.Subject) > 255 {
		return googleIdentity{}, errors.New("Google subject is missing or invalid")
	}
	name := strings.TrimSpace(claims.Name)
	if name == "" {
		name = strings.SplitN(claims.Email, "@", 2)[0]
	}
	return googleIdentity{subject: idToken.Subject, email: claims.Email, name: name}, nil
}

func hashSecret(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

func (s *Server) cookieSecure() bool {
	u, err := url.Parse(s.Config.PublicURL)
	return err == nil && u.Scheme == "https"
}

func (s *Server) setSessionCookie(w http.ResponseWriter, secret string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: secret, Path: "/", HttpOnly: true,
		Secure: s.cookieSecure(), SameSite: http.SameSiteStrictMode,
		MaxAge: int(sessionLifetime.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: s.cookieSecure(), SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
}

func (s *Server) createSession(ctx context.Context, w http.ResponseWriter, userID string) error {
	secret, err := randomString(32)
	if err != nil {
		return err
	}
	if err := s.Store.CreateBrowserSession(ctx, userID, hashSecret(secret), time.Now().Add(sessionLifetime)); err != nil {
		return err
	}
	s.setSessionCookie(w, secret)
	return nil
}

func (s *Server) googleStart(w http.ResponseWriter, r *http.Request) {
	if s.Config.Google == nil {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Not found")
		return
	}
	state, err := randomString(32)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to start Google sign-in")
		return
	}
	nonce, err := randomString(32)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to start Google sign-in")
		return
	}
	verifier := oauth2.GenerateVerifier()
	if err := s.Store.CreateOIDCLoginAttempt(r.Context(), hashSecret(state), verifier, nonce, time.Now().Add(10*time.Minute)); err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to start Google sign-in")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: state, Path: oidcCallbackPath,
		HttpOnly: true, Secure: s.cookieSecure(), SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
	http.Redirect(w, r, s.Config.Google.authCodeURL(state, verifier, nonce), http.StatusFound)
}

func (s *Server) googleCallback(w http.ResponseWriter, r *http.Request) {
	if s.Config.Google == nil {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Not found")
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Path: oidcCallbackPath, Value: "", HttpOnly: true, Secure: s.cookieSecure(), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	stateCookie, err := r.Cookie(oidcStateCookie)
	state := r.URL.Query().Get("state")
	if err != nil || len(state) < 32 || subtle.ConstantTimeCompare([]byte(state), []byte(stateCookie.Value)) != 1 {
		s.googleFailure(w, r, "login_failed")
		return
	}
	verifier, nonce, err := s.Store.ConsumeOIDCLoginAttempt(r.Context(), hashSecret(state))
	if err != nil || r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		s.googleFailure(w, r, "login_failed")
		return
	}
	identity, err := s.Config.Google.exchangeAndVerify(r.Context(), r.URL.Query().Get("code"), verifier, nonce)
	if err != nil {
		s.googleFailure(w, r, "login_failed")
		return
	}
	u, err := s.Store.UpsertGoogleUser(r.Context(), identity.subject, identity.email, identity.name)
	if errors.Is(err, store.ErrConflict) {
		s.googleFailure(w, r, "account_conflict")
		return
	}
	if err != nil {
		s.googleFailure(w, r, "login_failed")
		return
	}
	if u.Status != "active" {
		s.googleFailure(w, r, "account_disabled")
		return
	}
	// Rotate any existing browser session when changing account.
	if old, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Store.RevokeBrowserSession(r.Context(), hashSecret(old.Value)); err != nil {
			s.googleFailure(w, r, "login_failed")
			return
		}
	}
	if err := s.createSession(r.Context(), w, u.ID); err != nil {
		s.googleFailure(w, r, "login_failed")
		return
	}
	http.Redirect(w, r, s.appURL(), http.StatusSeeOther)
}

func (s *Server) appURL() string {
	if s.Config.AppURL != "" {
		return s.Config.AppURL
	}
	return s.Config.PublicURL
}

func (s *Server) googleFailure(w http.ResponseWriter, r *http.Request, code string) {
	base, err := url.Parse(s.appURL())
	if err != nil || base.Host == "" {
		fail(w, http.StatusUnauthorized, "GOOGLE_LOGIN_FAILED", "Google sign-in failed")
		return
	}
	q := base.Query()
	q.Set("auth_error", code)
	base.RawQuery = q.Encode()
	http.Redirect(w, r, base.String(), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.Store.RevokeBrowserSession(r.Context(), hashSecret(c.Value)); err != nil {
			fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to sign out")
			return
		}
	}
	s.clearSessionCookie(w)
	respond(w, http.StatusOK, map[string]any{"ok": true})
}
