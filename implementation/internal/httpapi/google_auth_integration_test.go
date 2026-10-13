package httpapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/store"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

func TestGoogleLoginStateSessionAndDisabledAccount(t *testing.T) {
	dbURL := os.Getenv("FITHUB_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set FITHUB_TEST_DATABASE_URL to a disposable PostgreSQL instance")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := fmt.Sprintf("fithub_google_auth_test_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})
	dbConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	dbConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, dbConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	data := store.New(pool)
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	git, err := gitservice.New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.ParseForm() != nil {
			http.Error(w, "invalid exchange", http.StatusBadRequest)
			return
		}
		code := strings.SplitN(r.FormValue("code"), ":", 2)
		if len(code) != 2 || r.FormValue("code_verifier") == "" {
			http.Error(w, "missing code or PKCE verifier", http.StatusBadRequest)
			return
		}
		claims := googleTestClaims(code[1])
		switch code[0] {
		case "badnonce":
			claims["nonce"] = "wrong-nonce"
		case "active":
			claims["sub"] = "active-sub"
			claims["email"] = "active@example.com"
		case "conflict":
			claims["sub"] = "conflict-sub"
			claims["email"] = "existing@example.com"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "unused", "token_type": "Bearer", "expires_in": 3600,
			"id_token": signedIDToken(t, key, claims),
		})
	}))
	t.Cleanup(tokenServer.Close)
	g := &GoogleOIDC{
		config: oauth2.Config{
			ClientID: "test-client", ClientSecret: "test-secret",
			Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: tokenServer.URL, AuthStyle: oauth2.AuthStyleInParams},
			Scopes:   []string{oidc.ScopeOpenID, oidc.ScopeEmail, oidc.ScopeProfile},
		},
		verifier:     oidc.NewVerifier("https://accounts.google.com", &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: "test-client"}),
		hostedDomain: "example.com",
	}
	api := New(data, git, Config{Google: g})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	api.Config.PublicURL = srv.URL
	api.Config.AppURL = srv.URL
	g.config.RedirectURL = srv.URL + oidcCallbackPath
	client := newBrowser(t)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	start := func() (state, nonce string) {
		t.Helper()
		resp, err := client.Get(srv.URL + "/api/v1/auth/google/start")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("start status: %d", resp.StatusCode)
		}
		location, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if location.Host != "accounts.google.com" || location.Query().Get("code_challenge_method") != "S256" || location.Query().Get("code_challenge") == "" {
			t.Fatalf("authorization URL lacks Google host or PKCE S256: %s", location)
		}
		return location.Query().Get("state"), location.Query().Get("nonce")
	}
	callback := func(state, code, wantError string) {
		t.Helper()
		u := srv.URL + oidcCallbackPath + "?state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(code)
		resp, err := client.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("callback status: %d", resp.StatusCode)
		}
		location, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if got := location.Query().Get("auth_error"); got != wantError {
			t.Fatalf("callback error = %q, want %q", got, wantError)
		}
	}
	state, nonce := start()
	callback(strings.Repeat("x", len(state)), "active:"+nonce, "login_failed")
	state, nonce = start()
	callback(state, "badnonce:"+nonce, "login_failed")
	callback(state, "active:"+nonce, "login_failed") // consumed once
	getJSON(t, client, srv.URL+"/api/v1/me", http.StatusUnauthorized)

	disabled, err := data.UpsertGoogleUser(ctx, "stable-google-sub", "alice@example.com", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetUserStatus(ctx, disabled.ID, "suspended"); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateBrowserSession(ctx, disabled.ID, hashSecret("forbidden-session"), time.Now().Add(time.Hour)); err != store.ErrForbidden {
		t.Fatalf("disabled account obtained a session: %v", err)
	}
	state, nonce = start()
	callback(state, "valid:"+nonce, "account_disabled")
	getJSON(t, client, srv.URL+"/api/v1/me", http.StatusUnauthorized)

	if _, err := data.UpsertDevUser(ctx, "existing@example.com", "Existing"); err != nil {
		t.Fatal(err)
	}
	state, nonce = start()
	callback(state, "conflict:"+nonce, "account_conflict")
	getJSON(t, client, srv.URL+"/api/v1/me", http.StatusUnauthorized)

	state, nonce = start()
	callback(state, "active:"+nonce, "")
	me := getJSON(t, client, srv.URL+"/api/v1/me", http.StatusOK)
	if me["email"] != "active@example.com" || me["role"] != "employee" || len(me["teams"].([]any)) != 0 {
		t.Fatalf("new Google account should be teamless employee: %v", me)
	}
	// A fresh Server instance must still accept the database-backed session.
	fresh := New(data, git, Config{Google: g, PublicURL: srv.URL, AppURL: srv.URL})
	freshServer := httptest.NewServer(fresh.Handler())
	t.Cleanup(freshServer.Close)
	getJSON(t, client, freshServer.URL+"/api/v1/me", http.StatusOK)
	postJSON(t, client, freshServer.URL+"/api/v1/auth/logout", map[string]any{}, http.StatusOK)
	getJSON(t, client, srv.URL+"/api/v1/me", http.StatusUnauthorized)
	state, nonce = start()
	callback(state, "active:"+nonce, "")
	if err := data.SetUserStatus(ctx, me["id"].(string), "suspended"); err != nil {
		t.Fatal(err)
	}
	getJSON(t, client, srv.URL+"/api/v1/me", http.StatusUnauthorized)
	if err := data.SetUserStatus(ctx, me["id"].(string), "active"); err != nil {
		t.Fatal(err)
	}
	getJSON(t, client, srv.URL+"/api/v1/me", http.StatusUnauthorized) // suspension revoked the old session
}
