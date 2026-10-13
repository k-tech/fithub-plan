package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitializeAndToolsList(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"test","version":"1"}}}` + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	var output, diag bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(input), &output, &diag); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two responses, got %d", len(lines))
	}
	var init response
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil {
		t.Fatal(err)
	}
	result := init.Result.(map[string]any)
	if result["protocolVersion"] != protocolVersion {
		t.Fatalf("version %v", result["protocolVersion"])
	}
	var listing response
	if err := json.Unmarshal([]byte(lines[1]), &listing); err != nil {
		t.Fatal(err)
	}
	tools := listing.Result.(map[string]any)["tools"].([]any)
	names := make(map[string]bool, len(tools))
	for _, raw := range tools {
		names[raw.(map[string]any)["name"].(string)] = true
	}
	for _, expected := range []string{
		"auth_status", "repo_list", "repo_view", "repo_branches", "commit_changes",
		"issue_list", "issue_create", "issue_view",
		"label_list", "label_create", "label_delete",
		"issue_set_labels", "issue_set_assignees", "issue_set_milestone",
		"milestone_list", "milestone_create", "milestone_update", "milestone_delete",
		"pr_list", "pr_create", "pr_view", "pr_diff", "pr_reviews",
		"pr_review_comments_list", "pr_review_comment_create", "pr_review", "pr_merge",
		"run_list", "run_view", "run_logs", "run_dispatch", "run_cancel",
		"notify_list", "notify_read",
	} {
		if !names[expected] {
			t.Fatalf("missing tool %q", expected)
		}
	}
	if len(tools) != len(names) {
		t.Fatalf("duplicate tool names: %d tools, %d unique", len(tools), len(names))
	}
	first := tools[0].(map[string]any)
	if first["name"] != "auth_status" {
		t.Fatalf("first tool=%v", first)
	}
}
func TestCommitChangesToolPreservesTextAndExpectedSHA(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repos/engineering/demo/commits" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"commitSha":"cccccccccccccccccccccccccccccccccccccccc"}`))
	}))
	defer srv.Close()
	t.Setenv("FITHUB_URL", srv.URL)
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	content := " line one\nline two  \n"
	_, err := callTool(context.Background(), "commit_changes", map[string]any{"team": "engineering", "repo": "demo", "branch": "feature/docs", "expectedHeadSha": strings.Repeat("a", 40), "path": "docs/note.txt", "content": content, "message": "Add note"})
	if err != nil {
		t.Fatal(err)
	}
	if posted["content"] != content || posted["expectedHeadSha"] != strings.Repeat("a", 40) {
		t.Fatalf("posted=%v", posted)
	}
}
func TestIssueListToolForwardsFilters(t *testing.T) {
	var requested string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/repos/engineering/demo/issues" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requested = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"issues":[]}`))
	}))
	defer srv.Close()
	t.Setenv("FITHUB_URL", srv.URL)
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	_, err := callTool(context.Background(), "issue_list", map[string]any{
		"team": "engineering", "repo": "demo", "state": "all",
		"labelId": "11111111-1111-1111-1111-111111111111", "milestone": "3",
	})
	if err != nil {
		t.Fatal(err)
	}
	values, err := url.ParseQuery(requested)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("state") != "all" || values.Get("milestone") != "3" ||
		values.Get("labelId") != "11111111-1111-1111-1111-111111111111" || values.Has("assignee") {
		t.Fatalf("query=%q", requested)
	}
}

func TestSetLabelsAndMilestoneTools(t *testing.T) {
	posted := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		posted[r.URL.Path] = body
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/repos/engineering/demo/issues/4/labels":
			_, _ = w.Write([]byte(`{"labels":[]}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/repos/engineering/demo/issues/4/milestone":
			_, _ = w.Write([]byte(`{"milestone":null}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("FITHUB_URL", srv.URL)
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	if _, err := callTool(context.Background(), "issue_set_labels", map[string]any{
		"team": "engineering", "repo": "demo", "number": "4",
		"labelIds": []any{"aaaaaaaa-1111-1111-1111-111111111111", "bbbbbbbb-2222-2222-2222-222222222222"},
	}); err != nil {
		t.Fatal(err)
	}
	labels, ok := posted["/api/v1/repos/engineering/demo/issues/4/labels"]["labelIds"].([]any)
	if !ok || len(labels) != 2 {
		t.Fatalf("label payload=%v", posted)
	}
	if _, err := callTool(context.Background(), "issue_set_milestone", map[string]any{
		"team": "engineering", "repo": "demo", "number": "4", "milestoneNumber": float64(0),
	}); err != nil {
		t.Fatal(err)
	}
	if number, present := posted["/api/v1/repos/engineering/demo/issues/4/milestone"]["milestoneNumber"]; !present || number != nil {
		t.Fatalf("milestone payload=%v", posted)
	}
	if _, err := callTool(context.Background(), "issue_set_milestone", map[string]any{
		"team": "engineering", "repo": "demo", "number": "4", "milestoneNumber": "two",
	}); err == nil {
		t.Fatal("non-numeric milestone number accepted")
	}
}

func TestInlineCommentToolPostsVerifiedAnchor(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/engineering/demo/pulls/9/comments":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"comment":{"id":"c1"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/engineering/demo/pulls/9/comments":
			_, _ = w.Write([]byte(`{"comments":[]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("FITHUB_URL", srv.URL)
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	args := map[string]any{
		"team": "engineering", "repo": "demo", "number": "9",
		"path": "internal/a.go", "line": float64(12), "side": "old", "body": "rename this",
	}
	if _, err := callTool(context.Background(), "pr_review_comment_create", args); err != nil {
		t.Fatal(err)
	}
	if posted["path"] != "internal/a.go" || posted["line"] != float64(12) || posted["side"] != "old" || posted["body"] != "rename this" {
		t.Fatalf("posted=%v", posted)
	}
	if _, err := callTool(context.Background(), "pr_review_comments_list", map[string]any{"team": "engineering", "repo": "demo", "number": "9"}); err != nil {
		t.Fatal(err)
	}
	// A missing line must fail before any request is made, because the server
	// would otherwise reject an unbounded integer.
	bad := map[string]any{"team": "engineering", "repo": "demo", "number": "9", "path": "a.go", "body": "x", "line": "three"}
	if _, err := callTool(context.Background(), "pr_review_comment_create", bad); err == nil {
		t.Fatal("non-numeric line accepted")
	}
}

func TestReviewToolUsesCurrentRevisions(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/engineering/demo/pulls/7":
			_, _ = w.Write([]byte(`{"pull":{"currentBaseSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","currentHeadSha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/engineering/demo/pulls/7/reviews":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"review":{"state":"approved"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("FITHUB_URL", srv.URL)
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	_, err := callTool(context.Background(), "pr_review", map[string]any{"team": "engineering", "repo": "demo", "number": "7", "state": "approved"})
	if err != nil {
		t.Fatal(err)
	}
	if posted["state"] != "approved" || posted["expectedBaseSha"] != strings.Repeat("a", 40) || posted["expectedHeadSha"] != strings.Repeat("b", 40) {
		t.Fatalf("posted=%v", posted)
	}
}
func TestMCPToolCallUsesFitHubAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/engineering/demo/issues" || r.URL.Query().Get("state") != "closed" {
			t.Errorf("unexpected request %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"issues":[]}`))
	}))
	defer srv.Close()
	oldURL, oldConfig := os.Getenv("FITHUB_URL"), os.Getenv("FITHUB_CONFIG")
	t.Setenv("FITHUB_URL", srv.URL)
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	defer os.Setenv("FITHUB_URL", oldURL)
	defer os.Setenv("FITHUB_CONFIG", oldConfig)
	result, err := callTool(context.Background(), "issue_list", map[string]any{"team": "engineering", "repo": "demo", "state": "closed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(map[string]any)["issues"]; !ok {
		t.Fatalf("result=%v", result)
	}
}
func TestUnknownToolIsToolError(t *testing.T) {
	result, err := callTool(context.Background(), "git_commit", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown FitHub tool") {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

// The CI tools must reach the documented endpoints with the caller's arguments
// intact. Each case is one tool call against a stub server.
func TestCIToolsMapToEndpoints(t *testing.T) {
	cases := []struct {
		name     string
		args     map[string]any
		wantPath string
		wantBody string
	}{
		{"run_list", map[string]any{"team": "engineering", "repo": "demo", "state": "failed", "page": "2"}, "/api/v1/repos/engineering/demo/runs?page=2&state=failed", ""},
		{"run_view", map[string]any{"team": "engineering", "repo": "demo", "number": "7"}, "/api/v1/repos/engineering/demo/runs/7", ""},
		{"run_logs", map[string]any{"team": "engineering", "repo": "demo", "number": "7", "after": "12", "jobId": "test"}, "/api/v1/repos/engineering/demo/runs/7/logs?after=12&jobId=test", ""},
		{"run_dispatch", map[string]any{"team": "engineering", "repo": "demo"}, "/api/v1/repos/engineering/demo/runs/demo", "{}"},
		{"run_dispatch", map[string]any{"team": "engineering", "repo": "demo", "workflowPath": ".fithub/workflows/ci.yml"}, "/api/v1/repos/engineering/demo/runs/demo", `{"workflowPath":".fithub/workflows/ci.yml"}`},
		{"run_cancel", map[string]any{"team": "engineering", "repo": "demo", "number": "7"}, "/api/v1/repos/engineering/demo/runs/7/cancel", ""},
		// The inbox tools take no team or repository: an inbox is always the
		// caller's own, so there is no argument that could widen it.
		{"notify_list", map[string]any{}, "/api/v1/notifications", ""},
		{"notify_list", map[string]any{"unread": "true", "page": "2"}, "/api/v1/notifications?page=2&unread=true", ""},
		{"notify_read", map[string]any{}, "/api/v1/notifications/read", `{"ids":[]}`},
		{"notify_read", map[string]any{"notificationIds": "a,b"}, "/api/v1/notifications/read", `{"ids":["a","b"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name+" "+tc.wantPath, func(t *testing.T) {
			var gotPath, gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				if r.URL.RawQuery != "" {
					gotPath += "?" + r.URL.RawQuery
				}
				if r.Body != nil {
					raw, _ := io.ReadAll(r.Body)
					gotBody = string(raw)
				}
				_, _ = w.Write([]byte(`{"runs":[]}`))
			}))
			defer srv.Close()
			t.Setenv("FITHUB_URL", srv.URL)
			t.Setenv("FITHUB_TOKEN", "test-token")
			t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "credentials.json"))
			if _, err := callTool(context.Background(), tc.name, tc.args); err != nil {
				t.Fatalf("callTool: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Fatalf("path=%q want %q", gotPath, tc.wantPath)
			}
			if tc.wantBody != "" && gotBody != tc.wantBody {
				t.Fatalf("body=%q want %q", gotBody, tc.wantBody)
			}
		})
	}
}

// A missing run number must be rejected before any request is made.
func TestCIToolsRejectMissingArguments(t *testing.T) {
	t.Setenv("FITHUB_URL", "http://127.0.0.1:1")
	t.Setenv("FITHUB_TOKEN", "test-token")
	t.Setenv("FITHUB_CONFIG", filepath.Join(t.TempDir(), "credentials.json"))
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"run_view", map[string]any{"team": "engineering", "repo": "demo"}},
		{"run_logs", map[string]any{"team": "engineering", "repo": "demo"}},
		{"run_cancel", map[string]any{"team": "engineering", "repo": "demo", "number": "0"}},
	} {
		if _, err := callTool(context.Background(), tc.name, tc.args); err == nil {
			t.Fatalf("%s accepted missing arguments", tc.name)
		}
	}
}
