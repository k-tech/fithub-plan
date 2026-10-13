package httpapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

func signedIDToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func googleTestClaims(nonce string) map[string]any {
	return map[string]any{
		"iss": "https://accounts.google.com", "aud": "test-client", "sub": "stable-google-sub",
		"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		"nonce": nonce, "email": "alice@example.com", "email_verified": true,
		"hd": "example.com", "name": "Alice",
	}
}

func TestGoogleIDTokenValidation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g := &GoogleOIDC{
		verifier:     oidc.NewVerifier("https://accounts.google.com", &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: "test-client"}),
		hostedDomain: "example.com",
	}
	goodNonce := strings.Repeat("n", 43)
	good := googleTestClaims(goodNonce)
	identity, err := g.verifyIDToken(context.Background(), signedIDToken(t, key, good), goodNonce)
	if err != nil || identity.subject != "stable-google-sub" || identity.email != "alice@example.com" {
		t.Fatalf("valid token: %+v, %v", identity, err)
	}
	cases := []struct {
		name   string
		change func(map[string]any)
		nonce  string
	}{
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://attacker.test" }, goodNonce},
		{"wrong audience", func(c map[string]any) { c["aud"] = "other-client" }, goodNonce},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }, goodNonce},
		{"wrong nonce", func(c map[string]any) {}, "another-nonce"},
		{"unverified email", func(c map[string]any) { c["email_verified"] = false }, goodNonce},
		{"missing hosted domain", func(c map[string]any) { delete(c, "hd") }, goodNonce},
		{"wrong hosted domain", func(c map[string]any) { c["hd"] = "example.net" }, goodNonce},
		{"missing subject", func(c map[string]any) { delete(c, "sub") }, goodNonce},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := googleTestClaims(goodNonce)
			tc.change(claims)
			if _, err := g.verifyIDToken(context.Background(), signedIDToken(t, key, claims), tc.nonce); err == nil {
				t.Fatal("invalid ID token was accepted")
			}
		})
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.verifyIDToken(context.Background(), signedIDToken(t, otherKey, good), goodNonce); err == nil {
		t.Fatal("ID token signed by an unknown key was accepted")
	}
}

func TestProductionSessionCookieFlags(t *testing.T) {
	s := &Server{Config: Config{PublicURL: "https://git.example.com"}}
	w := httptest.NewRecorder()
	s.setSessionCookie(w, "secret")
	cookies := (&http.Response{Header: w.Header()}).Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" {
		t.Fatalf("insecure production session cookie: %+v", cookies)
	}
}
