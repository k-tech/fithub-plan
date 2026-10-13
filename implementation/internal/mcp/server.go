// Package mcp implements a bounded MCP stdio server over FitHub's existing REST API.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"fithub.local/fithub/internal/cli"
)

const protocolVersion = "2025-11-25"
const maxMessageBytes = 1 << 20

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func tools() []tool {
	mk := func(name, description string, fields, required []string) tool {
		props := map[string]any{}
		for _, field := range fields {
			props[field] = map[string]any{"type": "string"}
		}
		req := []any{}
		for _, x := range required {
			req = append(req, x)
		}
		return tool{Name: name, Description: description, InputSchema: map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}}
	}
	return []tool{
		mk("auth_status", "Get the authenticated FitHub account; uses FITHUB_TOKEN or the local CLI session.", nil, nil),
		mk("repo_list", "List repositories visible to the current account.", nil, nil),
		mk("repo_view", "View one repository.", []string{"team", "repo"}, []string{"team", "repo"}),
		mk("repo_branches", "List repository branches and their current head SHAs.", []string{"team", "repo"}, []string{"team", "repo"}),
		mk("commit_changes", "Commit one UTF-8 regular file on an existing branch using native Git. Requires write access and an expected head SHA; workflow paths are administrator-only.", []string{"team", "repo", "branch", "expectedHeadSha", "path", "content", "message"}, []string{"team", "repo", "branch", "expectedHeadSha", "path", "content", "message"}),
		mk("issue_list", "List repository issues; optional state=open|closed|all, labelId, assignee (user ID), and milestone (number).", []string{"team", "repo", "state", "labelId", "assignee", "milestone"}, []string{"team", "repo"}),
		mk("issue_create", "Create an issue. Requires repository write access.", []string{"team", "repo", "title", "body"}, []string{"team", "repo", "title"}),
		mk("issue_view", "View one issue.", []string{"team", "repo", "number"}, []string{"team", "repo", "number"}),
		mk("label_list", "List the labels defined in one repository.", []string{"team", "repo"}, []string{"team", "repo"}),
		mk("label_create", "Create a repository label. color is RRGGBB. Requires write access.", []string{"team", "repo", "name", "color", "description"}, []string{"team", "repo", "name", "color"}),
		mk("label_delete", "Delete a repository label. Requires write access.", []string{"team", "repo", "labelId"}, []string{"team", "repo", "labelId"}),
		mk("issue_set_labels", "Replace the label set of one issue with the given repository label IDs; an empty list clears all labels. Requires write access.", []string{"team", "repo", "number", "labelIds"}, []string{"team", "repo", "number", "labelIds"}),
		mk("issue_set_assignees", "Replace the assignees of one issue with the given user IDs; an empty list clears them. Every user must already have read access to the repository. Requires write access.", []string{"team", "repo", "number", "userIds"}, []string{"team", "repo", "number", "userIds"}),
		mk("issue_set_milestone", "Set or clear the milestone of one issue. Omit milestoneNumber or pass 0 to clear it. Requires write access.", []string{"team", "repo", "number", "milestoneNumber"}, []string{"team", "repo", "number"}),
		mk("milestone_list", "List repository milestones; optional state=open|closed|all.", []string{"team", "repo", "state"}, []string{"team", "repo"}),
		mk("milestone_create", "Create a repository milestone. dueOn is an ISO date such as 2026-06-30. Requires write access.", []string{"team", "repo", "title", "description", "dueOn"}, []string{"team", "repo", "title"}),
		mk("milestone_update", "Update a milestone by number. state must be open or closed. Requires write access.", []string{"team", "repo", "number", "title", "description", "dueOn", "state"}, []string{"team", "repo", "number", "title", "state"}),
		mk("milestone_delete", "Delete a milestone by number. Its issues are kept and detached. Requires write access.", []string{"team", "repo", "number"}, []string{"team", "repo", "number"}),
		mk("pr_list", "List pull requests; optional state=open|closed|merged|all.", []string{"team", "repo", "state"}, []string{"team", "repo"}),
		mk("pr_create", "Create a PR from an existing repository branch into main. Requires write access.", []string{"team", "repo", "head", "title", "body"}, []string{"team", "repo", "head", "title"}),
		mk("pr_view", "View one PR and its current mergeability.", []string{"team", "repo", "number"}, []string{"team", "repo", "number"}),
		mk("pr_diff", "Read the PR's current unified diff.", []string{"team", "repo", "number"}, []string{"team", "repo", "number"}),
		mk("pr_reviews", "List reviews on one PR.", []string{"team", "repo", "number"}, []string{"team", "repo", "number"}),
		mk("pr_review_comments_list", "List inline review comments on one PR, including each comment's file, line, side, and whether it is outdated.", []string{"team", "repo", "number"}, []string{"team", "repo", "number"}),
		mk("pr_review_comment_create", "Add an inline review comment to a line of a PR diff. line is the number on the chosen side; replyToId continues an existing thread. Requires repository write access.", []string{"team", "repo", "number", "path", "line", "side", "body", "replyToId"}, []string{"team", "repo", "number", "path", "line", "body"}),
		mk("pr_review", "Review a PR as a different employee with write access. state=approved or changes_requested; the server rejects author self-review and stale revisions.", []string{"team", "repo", "number", "state", "body"}, []string{"team", "repo", "number", "state"}),
		mk("pr_merge", "Merge an open clean PR using its current head SHA. Requires another employee's current approval and repository write access.", []string{"team", "repo", "number"}, []string{"team", "repo", "number"}),
		mk("run_list", "List CI runs for a repository; optional state=all|queued|running|succeeded|failed|cancelled|timed_out|infrastructure_failed and page. Each run reports its workflow, revision, event, and job count.", []string{"team", "repo", "state", "page"}, []string{"team", "repo"}),
		mk("run_view", "View one CI run and its jobs, including each job's state and attempt.", []string{"team", "repo", "number"}, []string{"team", "repo", "number"}),
		mk("run_logs", "Read a finished or running job's output. after is the cursor returned by the previous call; omit it for the whole log. jobId limits the read to one job.", []string{"team", "repo", "number", "after", "jobId"}, []string{"team", "repo", "number"}),
		mk("run_dispatch", "Queue a manual CI run on the default branch using the trusted workflow definitions. Requires write access.", []string{"team", "repo", "workflowPath"}, []string{"team", "repo"}),
		mk("run_cancel", "Cancel a queued or running CI run. Requires write access.", []string{"team", "repo", "number"}, []string{"team", "repo", "number"}),
		mk("notify_list", "List the authenticated account's own notifications; optional unread=true and page. Notifications for repositories the account can no longer read are omitted, so every entry is openable.", []string{"unread", "page"}, nil),
		mk("notify_read", "Mark notifications read. Pass a comma-separated notificationIds to mark specific entries, or omit it to mark the whole visible inbox.", []string{"notificationIds"}, []string{}),
	}
}

func client() (*cli.Client, error) {
	cred, err := cli.LoadCredentials(cli.ConfigPath())
	if err != nil {
		return nil, err
	}
	base := os.Getenv("FITHUB_URL")
	if base == "" {
		base = cred.BaseURL
	}
	return cli.NewClient(base, os.Getenv("FITHUB_TOKEN"), cred.Cookie), nil
}

// Serve runs the newline-delimited JSON-RPC stdio transport. It writes protocol
// messages only to stdout; diagnostics go to stderr.
func Serve(ctx context.Context, r io.Reader, w, diagnostics io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), maxMessageBytes)
	enc := json.NewEncoder(w)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			if e := enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "Parse error"}}); e != nil {
				return e
			}
			continue
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			if len(req.ID) > 0 {
				if e := enc.Encode(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "Invalid Request"}}); e != nil {
					return e
				}
			}
			continue
		}
		result, rpcErr := dispatch(ctx, req)
		if len(req.ID) == 0 {
			if rpcErr != nil && diagnostics != nil {
				fmt.Fprintln(diagnostics, rpcErr.Message)
			}
			continue
		}
		resp := response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP stdio: %w", err)
	}
	return nil
}

func dispatch(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string         `json:"protocolVersion"`
			ClientInfo      map[string]any `json:"clientInfo"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "Invalid initialize params"}
		}
		version := protocolVersion
		switch p.ProtocolVersion {
		case "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05":
			version = p.ProtocolVersion
		}
		return map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "fithub", "version": "0.1.0"}, "instructions": "FitHub MCP calls the REST API. commit_changes writes one small text file on an existing development branch using an expected SHA; full Git transport and CI execution are not MCP tools."}, nil
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools()}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "Invalid tools/call params"}
		}
		v, err := callTool(ctx, p.Name, p.Arguments)
		if err != nil {
			b, _ := json.Marshal(map[string]any{"error": err.Error()})
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "isError": true}, nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, &rpcError{-32603, "Unable to encode tool result"}
		}
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "structuredContent": v, "isError": false}, nil
	default:
		if strings.HasPrefix(req.Method, "notifications/") {
			return nil, nil
		}
		return nil, &rpcError{-32601, "Method not found"}
	}
}

func arg(a map[string]any, key string, required bool) (string, error) {
	v, ok := a[key]
	s, _ := v.(string)
	if (!ok || strings.TrimSpace(s) == "") && required {
		return "", fmt.Errorf("missing argument %q", key)
	}
	return strings.TrimSpace(s), nil
}
func repoArgs(a map[string]any) (string, string, error) {
	team, e := arg(a, "team", true)
	if e != nil {
		return "", "", e
	}
	repo, e := arg(a, "repo", true)
	return team, repo, e
}
func callTool(ctx context.Context, name string, a map[string]any) (any, error) {
	c, err := client()
	if err != nil {
		return nil, err
	}
	var method, path string
	var body any
	switch name {
	case "auth_status":
		method = http.MethodGet
		path = "/me"
	case "notify_list":
		// No repository argument exists: an inbox is always the caller's own.
		query := url.Values{}
		for _, key := range []string{"unread", "page"} {
			value, e := arg(a, key, false)
			if e != nil {
				return nil, e
			}
			if value != "" {
				query.Set(key, value)
			}
		}
		path = "/notifications"
		if encoded := query.Encode(); encoded != "" {
			path += "?" + encoded
		}
	case "notify_read":
		method = http.MethodPost
		path = "/notifications/read"
		ids := []string{}
		if _, present := a["notificationIds"]; present {
			parsed, listErr := stringList(a, "notificationIds")
			if listErr != nil {
				return nil, listErr
			}
			ids = parsed
		}
		body = map[string]any{"ids": ids}
	case "repo_list":
		method = http.MethodGet
		path = "/repos"
	case "repo_view", "repo_branches", "commit_changes", "issue_list", "issue_create", "issue_view", "label_list", "label_create", "label_delete", "issue_set_labels", "issue_set_assignees", "issue_set_milestone", "milestone_list", "milestone_create", "milestone_update", "milestone_delete", "pr_list", "pr_create", "pr_view", "pr_diff", "pr_reviews", "pr_review_comments_list", "pr_review_comment_create", "pr_review", "pr_merge", "run_list", "run_view", "run_logs", "run_dispatch", "run_cancel":
		team, repo, e := repoArgs(a)
		if e != nil {
			return nil, e
		}
		path = cli.RepoPath(team, repo)
		method = http.MethodGet
		switch name {
		case "repo_view":
		case "repo_branches":
			path += "/branches"
		case "commit_changes":
			branch, e := arg(a, "branch", true)
			if e != nil {
				return nil, e
			}
			head, e := arg(a, "expectedHeadSha", true)
			if e != nil {
				return nil, e
			}
			filePath, e := arg(a, "path", true)
			if e != nil {
				return nil, e
			}
			content, ok := a["content"].(string)
			if !ok {
				return nil, errors.New("missing string argument \"content\"")
			}
			message, e := arg(a, "message", true)
			if e != nil {
				return nil, e
			}
			method = http.MethodPost
			path += "/commits"
			body = map[string]string{"branch": branch, "expectedHeadSha": head, "path": filePath, "content": content, "message": message}
		case "issue_list":
			state, e := arg(a, "state", false)
			if e != nil {
				return nil, e
			}
			query := url.Values{}
			if state != "" {
				query.Set("state", state)
			}
			for _, key := range []string{"labelId", "assignee", "milestone"} {
				value, e := arg(a, key, false)
				if e != nil {
					return nil, e
				}
				if value != "" {
					query.Set(key, value)
				}
			}
			if encoded := query.Encode(); encoded != "" {
				path += "/issues?" + encoded
			} else {
				path += "/issues"
			}
		case "label_list":
			path += "/labels"
		case "label_create":
			name, e := arg(a, "name", true)
			if e != nil {
				return nil, e
			}
			color, e := arg(a, "color", true)
			if e != nil {
				return nil, e
			}
			description, _ := arg(a, "description", false)
			method = http.MethodPost
			path += "/labels"
			body = map[string]string{"name": name, "color": color, "description": description}
		case "label_delete":
			labelID, e := arg(a, "labelId", true)
			if e != nil {
				return nil, e
			}
			method = http.MethodDelete
			path += "/labels/" + url.PathEscape(labelID)
		case "issue_set_labels":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			labelIDs, e := stringList(a, "labelIds")
			if e != nil {
				return nil, e
			}
			method = http.MethodPut
			path += "/issues/" + num + "/labels"
			body = map[string][]string{"labelIds": labelIDs}
		case "issue_set_assignees":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			userIDs, e := stringList(a, "userIds")
			if e != nil {
				return nil, e
			}
			method = http.MethodPut
			path += "/issues/" + num + "/assignees"
			body = map[string][]string{"userIds": userIDs}
		case "issue_set_milestone":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			payload := map[string]any{"milestoneNumber": nil}
			if raw, ok := a["milestoneNumber"]; ok {
				number, ok := raw.(float64)
				if !ok || number != float64(int64(number)) || number < 0 {
					return nil, errors.New("milestoneNumber must be a non-negative integer")
				}
				if number > 0 {
					payload["milestoneNumber"] = int64(number)
				}
			}
			method = http.MethodPut
			path += "/issues/" + num + "/milestone"
			body = payload
		case "milestone_list":
			state, e := arg(a, "state", false)
			if e != nil {
				return nil, e
			}
			if state != "" {
				path += "/milestones?state=" + url.QueryEscape(state)
			} else {
				path += "/milestones"
			}
		case "milestone_create":
			title, e := arg(a, "title", true)
			if e != nil {
				return nil, e
			}
			description, _ := arg(a, "description", false)
			dueOn, _ := arg(a, "dueOn", false)
			method = http.MethodPost
			path += "/milestones"
			body = map[string]string{"title": title, "description": description, "dueOn": dueOn}
		case "milestone_update":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			title, e := arg(a, "title", true)
			if e != nil {
				return nil, e
			}
			state, e := arg(a, "state", true)
			if e != nil {
				return nil, e
			}
			description, _ := arg(a, "description", false)
			dueOn, _ := arg(a, "dueOn", false)
			method = http.MethodPatch
			path += "/milestones/" + num
			body = map[string]string{"title": title, "description": description, "dueOn": dueOn, "state": state}
		case "milestone_delete":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			method = http.MethodDelete
			path += "/milestones/" + num
		case "issue_create":
			title, e := arg(a, "title", true)
			if e != nil {
				return nil, e
			}
			bodyText, _ := arg(a, "body", false)
			method = http.MethodPost
			path += "/issues"
			body = map[string]string{"title": title, "body": bodyText}
		case "issue_view":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			path += "/issues/" + num
		case "pr_list":
			state, e := arg(a, "state", false)
			if e != nil {
				return nil, e
			}
			if state != "" {
				path += "/pulls?state=" + url.QueryEscape(state)
			} else {
				path += "/pulls"
			}
		case "pr_create":
			head, e := arg(a, "head", true)
			if e != nil {
				return nil, e
			}
			title, e := arg(a, "title", true)
			if e != nil {
				return nil, e
			}
			bodyText, _ := arg(a, "body", false)
			method = http.MethodPost
			path += "/pulls"
			body = map[string]string{"headRef": head, "title": title, "body": bodyText}
		case "pr_view":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			path += "/pulls/" + num
		case "pr_diff", "pr_reviews", "pr_review_comments_list":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			path += "/pulls/" + num
			switch name {
			case "pr_diff":
				path += "/diff"
			case "pr_reviews":
				path += "/reviews"
			default:
				path += "/comments"
			}
		case "pr_review_comment_create":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			file, e := arg(a, "path", true)
			if e != nil {
				return nil, e
			}
			bodyText, e := arg(a, "body", true)
			if e != nil {
				return nil, e
			}
			lineValue, ok := a["line"].(float64)
			if !ok || lineValue != float64(int(lineValue)) || lineValue < 1 {
				return nil, errors.New("line must be a positive integer")
			}
			side := "new"
			if raw, ok := a["side"].(string); ok && raw != "" {
				side = raw
			}
			if side != "new" && side != "old" {
				return nil, errors.New("side must be new or old")
			}
			payload := map[string]any{"path": file, "line": int(lineValue), "side": side, "body": bodyText}
			if replyTo, ok := a["replyToId"].(string); ok && replyTo != "" {
				payload["replyToId"] = replyTo
			}
			method = http.MethodPost
			path += "/pulls/" + num + "/comments"
			body = payload
		case "pr_review":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			state, e := arg(a, "state", true)
			if e != nil {
				return nil, e
			}
			if state != "approved" && state != "changes_requested" {
				return nil, errors.New("state must be approved or changes_requested")
			}
			bodyText, _ := arg(a, "body", false)
			if state == "changes_requested" && bodyText == "" {
				return nil, errors.New("body is required when requesting changes")
			}
			base := path + "/pulls/" + num
			detail, _, e := c.Do(ctx, http.MethodGet, base, nil)
			if e != nil {
				return nil, e
			}
			pv, ok := detail["pull"].(map[string]any)
			if !ok {
				return nil, errors.New("invalid pull request response")
			}
			baseSHA, _ := pv["currentBaseSha"].(string)
			headSHA, _ := pv["currentHeadSha"].(string)
			if baseSHA == "" || headSHA == "" {
				return nil, errors.New("pull request has no current base/head revision")
			}
			path = base + "/reviews"
			method = http.MethodPost
			body = map[string]string{"state": state, "body": bodyText, "expectedBaseSha": baseSHA, "expectedHeadSha": headSHA}
		case "pr_merge":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			base := path + "/pulls/" + num
			detail, _, e := c.Do(ctx, http.MethodGet, base, nil)
			if e != nil {
				return nil, e
			}
			pv, ok := detail["pull"].(map[string]any)
			if !ok {
				return nil, errors.New("invalid pull request response")
			}
			sha, _ := pv["currentHeadSha"].(string)
			if sha == "" {
				return nil, errors.New("pull request has no current mergeable head SHA")
			}
			path = base + "/merge"
			method = http.MethodPost
			body = map[string]string{"expectedHeadSha": sha}
		case "run_list":
			// page and state are optional; both default to "everything".
			query := url.Values{}
			for _, key := range []string{"state", "page"} {
				value, e := arg(a, key, false)
				if e != nil {
					return nil, e
				}
				if value != "" {
					query.Set(key, value)
				}
			}
			path += "/runs"
			if encoded := query.Encode(); encoded != "" {
				path += "?" + encoded
			}
		case "run_view":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			path += "/runs/" + num
		case "run_logs":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			query := url.Values{}
			for _, key := range []string{"after", "jobId"} {
				value, e := arg(a, key, false)
				if e != nil {
					return nil, e
				}
				if value != "" {
					query.Set(key, value)
				}
			}
			path += "/runs/" + num + "/logs"
			if encoded := query.Encode(); encoded != "" {
				path += "?" + encoded
			}
		case "run_dispatch":
			method = http.MethodPost
			path += "/runs/demo"
			workflowPath, e := arg(a, "workflowPath", false)
			if e != nil {
				return nil, e
			}
			body = map[string]string{}
			if workflowPath != "" {
				body.(map[string]string)["workflowPath"] = workflowPath
			}
		case "run_cancel":
			num, e := positiveNumber(a)
			if e != nil {
				return nil, e
			}
			method = http.MethodPost
			path += "/runs/" + num + "/cancel"
		}
	default:
		return nil, fmt.Errorf("unknown FitHub tool %q", name)
	}
	out, _, err := c.Do(ctx, method, path, body)
	return out, err
}

// stringList accepts an array of strings, or a single comma-separated string so
// simple MCP clients can clear labels and assignees with an empty value.
func stringList(a map[string]any, key string) ([]string, error) {
	raw, ok := a[key]
	if !ok || raw == nil {
		return nil, errors.New("missing argument " + strconv.Quote(key))
	}
	values := make([]string, 0)
	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New(key + " must be an array of strings")
			}
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				values = append(values, trimmed)
			}
		}
	case string:
		for _, part := range strings.Split(typed, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				values = append(values, trimmed)
			}
		}
	default:
		return nil, errors.New(key + " must be an array of strings")
	}
	if len(values) > 50 {
		return nil, errors.New(key + " has too many entries")
	}
	return values, nil
}

func positiveNumber(a map[string]any) (string, error) {
	s, e := arg(a, "number", true)
	if e != nil {
		return "", e
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return "", errors.New("number must be a positive integer")
	}
	return strconv.Itoa(n), nil
}
