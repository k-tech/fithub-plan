package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const sessionCookieName = "fithub_session"

// Client calls the documented FitHub REST API. A bearer token from FITHUB_TOKEN
// takes precedence; local dev login uses the server's short-lived session cookie.
type Client struct {
	BaseURL string
	Token   string
	Cookie  string
	HTTP    *http.Client
}

type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("FitHub API (%d): %s", e.Status, e.Message)
	}
	return fmt.Sprintf("FitHub API returned HTTP %d", e.Status)
}

func NewClient(base, token, cookie string) *Client {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	return &Client{BaseURL: base, Token: token, Cookie: cookie, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) Do(ctx context.Context, method, path string, body any) (map[string]any, http.Header, error) {
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		input = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/v1"+path, input)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	} else if c.Cookie != "" {
		req.Header.Set("Cookie", sessionCookieName+"="+c.Cookie)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.Header, err
	}
	var out map[string]any
	if len(data) > 0 {
		if err = json.Unmarshal(data, &out); err != nil {
			return nil, resp.Header, fmt.Errorf("invalid FitHub response: %w", err)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode}
		if e, ok := out["error"].(map[string]any); ok {
			apiErr.Code, _ = e["code"].(string)
			apiErr.Message, _ = e["message"].(string)
		}
		return nil, resp.Header, apiErr
	}
	return out, resp.Header, nil
}

func (c *Client) Login(ctx context.Context, email, name string) (map[string]any, error) {
	out, h, err := c.Do(ctx, http.MethodPost, "/dev/login", map[string]string{"email": email, "name": name})
	if err != nil {
		return nil, err
	}
	for _, raw := range h.Values("Set-Cookie") {
		if strings.HasPrefix(raw, sessionCookieName+"=") {
			c.Cookie = strings.SplitN(strings.TrimPrefix(raw, sessionCookieName+"="), ";", 2)[0]
			break
		}
	}
	if c.Cookie == "" {
		return nil, errors.New("server did not issue a FitHub dev session cookie")
	}
	return out, nil
}
func (c *Client) Logout(ctx context.Context) error {
	_, _, err := c.Do(ctx, http.MethodPost, "/auth/logout", map[string]any{})
	c.Cookie = ""
	return err
}
func (c *Client) Me(ctx context.Context) (map[string]any, error) {
	out, _, err := c.Do(ctx, http.MethodGet, "/me", nil)
	return out, err
}

type Credentials struct {
	BaseURL string `json:"baseUrl,omitempty"`
	Cookie  string `json:"cookie,omitempty"`
}

func ConfigPath() string {
	if p := os.Getenv("FITHUB_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return filepath.Join(".", ".fithub", "credentials.json")
	}
	return filepath.Join(dir, "fithub", "credentials.json")
}
func LoadCredentials(path string) (Credentials, error) {
	var c Credentials
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}
func SaveCredentials(path string, c Credentials) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err = os.Chmod(tmp, 0600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
func RepoPath(team, repo string) string {
	return "/repos/" + url.PathEscape(team) + "/" + url.PathEscape(repo)
}
