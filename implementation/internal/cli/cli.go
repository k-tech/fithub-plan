package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Options struct {
	Server string
	JSON   bool
	Config string
	Token  string
}

func parseGlobals(args []string) (Options, []string, error) {
	o := Options{Server: os.Getenv("FITHUB_URL"), Config: ConfigPath(), Token: os.Getenv("FITHUB_TOKEN")}
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			o.JSON = true
		case "--server", "-s":
			if i+1 >= len(args) {
				return o, nil, errors.New("--server needs a URL")
			}
			i++
			o.Server = args[i]
		case "--config":
			if i+1 >= len(args) {
				return o, nil, errors.New("--config needs a path")
			}
			i++
			o.Config = args[i]
		case "--help", "-h":
			rest = append(rest, "help")
		default:
			rest = append(rest, args[i])
		}
	}
	return o, rest, nil
}
func readClient(o Options) (*Client, Credentials, error) {
	cred, err := LoadCredentials(o.Config)
	if err != nil {
		return nil, cred, fmt.Errorf("read credentials: %w", err)
	}
	base := o.Server
	if base == "" {
		base = cred.BaseURL
	}
	return NewClient(base, o.Token, cred.Cookie), cred, nil
}

// Run implements the fh command. It is exported so protocol adapters can share the
// exact same HTTP client and semantics without shelling out to the executable.
func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	o, rest, err := parseGlobals(args)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	if len(rest) == 0 || rest[0] == "help" {
		printHelp(out)
		return 0
	}
	client, cred, err := readClient(o)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	cmd := rest[0]
	args = rest[1:]
	var value any
	switch cmd {
	case "auth":
		value, err = auth(ctx, args, client, cred, o, in, out)
	case "repo":
		value, err = repo(ctx, args, client)
	case "label":
		value, err = label(ctx, args, client)
	case "run":
		value, err = ciRun(ctx, args, client)
	case "notify":
		value, err = notify(ctx, args, client)
	case "milestone":
		value, err = milestone(ctx, args, client)
	case "issue":
		value, err = issue(ctx, args, client)
	case "pr":
		value, err = pull(ctx, args, client)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if value == nil {
		return 0
	}
	if o.JSON {
		b, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			fmt.Fprintln(errOut, e)
			return 1
		}
		fmt.Fprintln(out, string(b))
		return 0
	}
	printValue(out, value)
	return 0
}

func auth(ctx context.Context, args []string, c *Client, cred Credentials, o Options, in io.Reader, out io.Writer) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: fh auth status|login|logout")
	}
	switch args[0] {
	case "status":
		return c.Me(ctx)
	case "login":
		if c.Token != "" {
			return nil, errors.New("FITHUB_TOKEN is already set; unset it to use interactive dev login")
		}
		fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		email := fs.String("email", "", "account email")
		name := fs.String("name", "", "display name")
		if err := fs.Parse(args[1:]); err != nil {
			return nil, err
		}
		if *email == "" {
			return nil, errors.New("auth login currently supports local dev mode; pass --email and --name")
		}
		if *name == "" {
			*name = *email
		}
		meta, _, err := c.Do(ctx, http.MethodGet, "/meta", nil)
		if err != nil {
			return nil, err
		}
		if meta["devAuth"] != true {
			return nil, errors.New("fh auth login supports the local development account picker only; use the Web app for Google sign-in or set FITHUB_TOKEN")
		}
		result, err := c.Login(ctx, *email, *name)
		if err != nil {
			return nil, err
		}
		cred.BaseURL = c.BaseURL
		cred.Cookie = c.Cookie
		if err = SaveCredentials(o.Config, cred); err != nil {
			return nil, err
		}
		return result, nil
	case "logout":
		var err error
		if c.Cookie != "" {
			err = c.Logout(ctx)
		}
		cred.Cookie = ""
		if o.Server != "" {
			cred.BaseURL = o.Server
		}
		if saveErr := SaveCredentials(o.Config, cred); saveErr != nil {
			return nil, saveErr
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "note": func() string {
			if c.Token != "" {
				return "FITHUB_TOKEN environment variable remains active"
			}
			return "logged out"
		}()}, nil
	default:
		return nil, fmt.Errorf("unknown auth command %q", args[0])
	}
}

func repo(ctx context.Context, args []string, c *Client) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: fh repo list | view|branches|commit TEAM/REPO")
	}
	switch args[0] {
	case "list":
		o, _, e := c.Do(ctx, http.MethodGet, "/repos", nil)
		return o, e
	case "view":
		if len(args) < 2 {
			return nil, errors.New("usage: fh repo view TEAM/REPO")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		o, _, e := c.Do(ctx, http.MethodGet, RepoPath(team, r), nil)
		return o, e
	case "branches":
		if len(args) < 2 {
			return nil, errors.New("usage: fh repo branches TEAM/REPO")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		o, _, e := c.Do(ctx, http.MethodGet, RepoPath(team, r)+"/branches", nil)
		return o, e
	case "commit":
		if len(args) < 2 {
			return nil, errors.New("usage: fh repo commit TEAM/REPO --branch BRANCH --head SHA --path REPO_PATH --file LOCAL_FILE --message MESSAGE")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("repo commit", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		branch := fs.String("branch", "", "existing development branch")
		head := fs.String("head", "", "expected branch head SHA")
		path := fs.String("path", "", "repository file path")
		file := fs.String("file", "", "local UTF-8 source file")
		message := fs.String("message", "", "one-line commit message")
		if err := fs.Parse(args[2:]); err != nil || len(fs.Args()) != 0 {
			return nil, errors.New("invalid repo commit arguments")
		}
		if *branch == "" || *head == "" || *path == "" || *file == "" || *message == "" {
			return nil, errors.New("--branch, --head, --path, --file, and --message are required")
		}
		info, err := os.Stat(*file)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 64<<10 {
			return nil, errors.New("source must be a regular text file no larger than 64 KiB")
		}
		content, err := os.ReadFile(*file)
		if err != nil {
			return nil, err
		}
		o, _, e := c.Do(ctx, http.MethodPost, RepoPath(team, r)+"/commits", map[string]string{
			"branch": *branch, "expectedHeadSha": *head,
			"path": *path, "content": string(content), "message": *message,
		})
		return o, e
	default:
		return nil, fmt.Errorf("unknown repo command %q", args[0])
	}
}

// notify implements `fh notify`: the caller's own inbox. There is no way to read
// another account's notifications, so no account argument exists.
func notify(ctx context.Context, args []string, c *Client) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: fh notify list [--unread] [--page N] | read [--ids ID,ID]")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("notify list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		unread := fs.Bool("unread", false, "show only unread notifications")
		page := fs.Int("page", 1, "page number")
		if e := fs.Parse(args[1:]); e != nil {
			return nil, e
		}
		if *page < 1 {
			return nil, errors.New("--page must be at least 1")
		}
		query := url.Values{"page": {strconv.Itoa(*page)}}
		if *unread {
			query.Set("unread", "true")
		}
		o, _, e := c.Do(ctx, http.MethodGet, "/notifications?"+query.Encode(), nil)
		return o, e
	case "read":
		fs := flag.NewFlagSet("notify read", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		ids := fs.String("ids", "", "comma separated notification IDs; empty marks the whole inbox")
		if e := fs.Parse(args[1:]); e != nil {
			return nil, e
		}
		parsed := []string{}
		for _, item := range strings.Split(*ids, ",") {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				parsed = append(parsed, trimmed)
			}
		}
		o, _, e := c.Do(ctx, http.MethodPost, "/notifications/read", map[string]any{"ids": parsed})
		return o, e
	default:
		return nil, fmt.Errorf("unknown notify command %q", args[0])
	}
}

// ciRun implements `fh run`, the operator view of CI. It mirrors the web UI's
// read paths and adds the two write actions (dispatch and cancel) that an
// operator needs without opening a browser.
func ciRun(ctx context.Context, args []string, c *Client) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: fh run list|view|logs|cancel|dispatch TEAM/REPO")
	}
	switch args[0] {
	case "list":
		if len(args) < 2 {
			return nil, errors.New("usage: fh run list TEAM/REPO [--state all|queued|running|succeeded|failed|cancelled|timed_out|infrastructure_failed] [--page N]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("run list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		state := fs.String("state", "all", "filter by run state")
		page := fs.Int("page", 1, "page number")
		if e := fs.Parse(args[2:]); e != nil {
			return nil, e
		}
		if *page < 1 {
			return nil, errors.New("--page must be at least 1")
		}
		path := fmt.Sprintf("%s/runs?state=%s&page=%d", RepoPath(team, r), url.QueryEscape(*state), *page)
		o, _, e := c.Do(ctx, http.MethodGet, path, nil)
		return o, e
	case "view":
		team, r, number, err := runTarget(args[1:])
		if err != nil {
			return nil, err
		}
		o, _, e := c.Do(ctx, http.MethodGet, fmt.Sprintf("%s/runs/%d", RepoPath(team, r), number), nil)
		return o, e
	case "logs":
		team, r, number, err := runTarget(args[1:])
		if err != nil {
			return nil, err
		}
		fs := flag.NewFlagSet("run logs", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		job := fs.String("job", "", "show only one job by its job ID")
		after := fs.Int64("after", -1, "return chunks after this sequence number; -1 starts from the beginning")
		// args[0] is the subcommand, args[1:3] the repository and run number, so
		// flags start after index 3.
		if e := fs.Parse(args[3:]); e != nil {
			return nil, e
		}
		path := fmt.Sprintf("%s/runs/%d/logs?after=%d", RepoPath(team, r), number, *after)
		if *job != "" {
			path += "&job=" + url.QueryEscape(*job)
		}
		o, _, e := c.Do(ctx, http.MethodGet, path, nil)
		return o, e
	case "cancel":
		team, r, number, err := runTarget(args[1:])
		if err != nil {
			return nil, err
		}
		o, _, e := c.Do(ctx, http.MethodPost, fmt.Sprintf("%s/runs/%d/cancel", RepoPath(team, r), number), nil)
		return o, e
	case "dispatch":
		if len(args) < 2 {
			return nil, errors.New("usage: fh run dispatch TEAM/REPO [--workflow PATH]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("run dispatch", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		workflowPath := fs.String("workflow", "", "run only one workflow file instead of every matching workflow")
		if e := fs.Parse(args[2:]); e != nil {
			return nil, e
		}
		body := map[string]any{}
		if *workflowPath != "" {
			body["workflowPath"] = *workflowPath
		}
		o, _, e := c.Do(ctx, http.MethodPost, RepoPath(team, r)+"/runs/demo", body)
		return o, e
	default:
		return nil, fmt.Errorf("unknown run command %q", args[0])
	}
}

// runTarget parses TEAM/REPO NUMBER for a single-run command.
func runTarget(args []string) (string, string, int, error) {
	if len(args) < 2 {
		return "", "", 0, errors.New("usage: fh run view|logs|cancel TEAM/REPO NUMBER")
	}
	team, r, ok := splitRepo(args[0])
	if !ok {
		return "", "", 0, errors.New("repository must be TEAM/REPO")
	}
	number, err := strconv.Atoi(args[1])
	if err != nil || number < 1 {
		return "", "", 0, errors.New("run number must be a positive integer")
	}
	return team, r, number, nil
}

func label(ctx context.Context, args []string, c *Client) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: fh label list|create|update|delete TEAM/REPO")
	}
	switch args[0] {
	case "list":
		if len(args) < 2 {
			return nil, errors.New("usage: fh label list TEAM/REPO")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		o, _, e := c.Do(ctx, http.MethodGet, RepoPath(team, r)+"/labels", nil)
		return o, e
	case "create":
		if len(args) < 2 {
			return nil, errors.New("usage: fh label create TEAM/REPO --name NAME --color RRGGBB [--description TEXT]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("label create", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		name := fs.String("name", "", "label name")
		color := fs.String("color", "", "label color as RRGGBB")
		description := fs.String("description", "", "label description")
		if e := fs.Parse(args[2:]); e != nil {
			return nil, e
		}
		if *name == "" || *color == "" {
			return nil, errors.New("--name and --color are required")
		}
		o, _, e := c.Do(ctx, http.MethodPost, RepoPath(team, r)+"/labels",
			map[string]string{"name": *name, "color": *color, "description": *description})
		return o, e
	case "update":
		if len(args) < 3 {
			return nil, errors.New("usage: fh label update TEAM/REPO LABEL_ID --name NAME --color RRGGBB [--description TEXT]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("label update", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		name := fs.String("name", "", "label name")
		color := fs.String("color", "", "label color as RRGGBB")
		description := fs.String("description", "", "label description")
		if e := fs.Parse(args[3:]); e != nil {
			return nil, e
		}
		if *name == "" || *color == "" {
			return nil, errors.New("--name and --color are required")
		}
		o, _, e := c.Do(ctx, http.MethodPatch, RepoPath(team, r)+"/labels/"+args[2],
			map[string]string{"name": *name, "color": *color, "description": *description})
		return o, e
	case "delete":
		if len(args) < 3 {
			return nil, errors.New("usage: fh label delete TEAM/REPO LABEL_ID")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		o, _, e := c.Do(ctx, http.MethodDelete, RepoPath(team, r)+"/labels/"+args[2], nil)
		return o, e
	default:
		return nil, fmt.Errorf("unknown label command %q", args[0])
	}
}

func milestone(ctx context.Context, args []string, c *Client) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: fh milestone list|create|update|delete TEAM/REPO")
	}
	switch args[0] {
	case "list":
		if len(args) < 2 {
			return nil, errors.New("usage: fh milestone list TEAM/REPO [--state open|closed|all]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("milestone list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		state := fs.String("state", "open", "milestone state")
		if e := fs.Parse(args[2:]); e != nil {
			return nil, e
		}
		o, _, e := c.Do(ctx, http.MethodGet, RepoPath(team, r)+"/milestones?state="+*state, nil)
		return o, e
	case "create":
		if len(args) < 2 {
			return nil, errors.New("usage: fh milestone create TEAM/REPO --title TITLE [--description TEXT] [--due YYYY-MM-DD]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("milestone create", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		title := fs.String("title", "", "milestone title")
		description := fs.String("description", "", "milestone description")
		due := fs.String("due", "", "due date as YYYY-MM-DD")
		if e := fs.Parse(args[2:]); e != nil {
			return nil, e
		}
		if *title == "" {
			return nil, errors.New("--title is required")
		}
		o, _, e := c.Do(ctx, http.MethodPost, RepoPath(team, r)+"/milestones",
			map[string]string{"title": *title, "description": *description, "dueOn": *due})
		return o, e
	case "update":
		if len(args) < 3 {
			return nil, errors.New("usage: fh milestone update TEAM/REPO NUMBER --title TITLE [--description TEXT] [--due YYYY-MM-DD] [--state open|closed]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("milestone update", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		title := fs.String("title", "", "milestone title")
		description := fs.String("description", "", "milestone description")
		due := fs.String("due", "", "due date as YYYY-MM-DD")
		state := fs.String("state", "", "open or closed")
		if e := fs.Parse(args[3:]); e != nil {
			return nil, e
		}
		if *title == "" || (*state != "open" && *state != "closed") {
			return nil, errors.New("--title is required and --state must be open or closed")
		}
		o, _, e := c.Do(ctx, http.MethodPatch, RepoPath(team, r)+"/milestones/"+args[2],
			map[string]string{"title": *title, "description": *description, "dueOn": *due, "state": *state})
		return o, e
	case "delete":
		if len(args) < 3 {
			return nil, errors.New("usage: fh milestone delete TEAM/REPO NUMBER")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		o, _, e := c.Do(ctx, http.MethodDelete, RepoPath(team, r)+"/milestones/"+args[2], nil)
		return o, e
	default:
		return nil, fmt.Errorf("unknown milestone command %q", args[0])
	}
}

// csvList splits a comma-separated flag value, returning an empty slice for
// blank input so clearing labels or assignees is explicit.
func csvList(raw string) []string {
	items := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func issue(ctx context.Context, args []string, c *Client) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: fh issue list|create|view|labels|assign|milestone TEAM/REPO [NUMBER]")
	}
	switch args[0] {
	case "labels":
		if len(args) < 3 {
			return nil, errors.New("usage: fh issue labels TEAM/REPO NUMBER [--set LABEL_ID,LABEL_ID]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("issue labels", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		set := fs.String("set", "", "comma-separated label IDs; an empty value clears them")
		if e := fs.Parse(args[3:]); e != nil {
			return nil, e
		}
		o, _, e := c.Do(ctx, http.MethodPut, RepoPath(team, r)+"/issues/"+args[2]+"/labels",
			map[string][]string{"labelIds": csvList(*set)})
		return o, e
	case "assign":
		if len(args) < 3 {
			return nil, errors.New("usage: fh issue assign TEAM/REPO NUMBER [--users USER_ID,USER_ID]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("issue assign", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		users := fs.String("users", "", "comma-separated user IDs; an empty value clears them")
		if e := fs.Parse(args[3:]); e != nil {
			return nil, e
		}
		o, _, e := c.Do(ctx, http.MethodPut, RepoPath(team, r)+"/issues/"+args[2]+"/assignees",
			map[string][]string{"userIds": csvList(*users)})
		return o, e
	case "milestone":
		if len(args) < 4 {
			return nil, errors.New("usage: fh issue milestone TEAM/REPO NUMBER --number N (use 0 to clear)")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("issue milestone", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		number := fs.Int64("number", 0, "milestone number; 0 clears it")
		if e := fs.Parse(args[3:]); e != nil {
			return nil, e
		}
		payload := map[string]any{"milestoneNumber": nil}
		if *number > 0 {
			payload["milestoneNumber"] = *number
		}
		o, _, e := c.Do(ctx, http.MethodPut, RepoPath(team, r)+"/issues/"+args[2]+"/milestone", payload)
		return o, e
	case "list":
		if len(args) < 2 {
			return nil, errors.New("usage: fh issue list TEAM/REPO [--state open|closed|all]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("issue list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		state := fs.String("state", "open", "issue state")
		labelID := fs.String("label", "", "filter by label ID")
		assignee := fs.String("assignee", "", "filter by assignee user ID")
		milestoneNumber := fs.Int64("milestone", 0, "filter by milestone number")
		if e := fs.Parse(args[2:]); e != nil {
			return nil, e
		}
		query := url.Values{"state": []string{*state}}
		if *labelID != "" {
			query.Set("labelId", *labelID)
		}
		if *assignee != "" {
			query.Set("assignee", *assignee)
		}
		if *milestoneNumber > 0 {
			query.Set("milestone", strconv.FormatInt(*milestoneNumber, 10))
		}
		o, _, e := c.Do(ctx, http.MethodGet, RepoPath(team, r)+"/issues?"+query.Encode(), nil)
		return o, e
	case "create":
		if len(args) < 2 {
			return nil, errors.New("usage: fh issue create TEAM/REPO --title TITLE [--body TEXT]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("issue create", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		title := fs.String("title", "", "issue title")
		body := fs.String("body", "", "issue body")
		if e := fs.Parse(args[2:]); e != nil {
			return nil, e
		}
		if *title == "" {
			return nil, errors.New("--title is required")
		}
		o, _, e := c.Do(ctx, http.MethodPost, RepoPath(team, r)+"/issues", map[string]string{"title": *title, "body": *body})
		return o, e
	case "view":
		if len(args) < 3 {
			return nil, errors.New("usage: fh issue view TEAM/REPO NUMBER")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		o, _, e := c.Do(ctx, http.MethodGet, RepoPath(team, r)+"/issues/"+args[2], nil)
		return o, e
	default:
		return nil, fmt.Errorf("unknown issue command %q", args[0])
	}
}
func pull(ctx context.Context, args []string, c *Client) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: fh pr list|create|view|diff|reviews|approve|request-changes|merge TEAM/REPO [NUMBER]")
	}
	switch args[0] {
	case "list":
		if len(args) < 2 {
			return nil, errors.New("usage: fh pr list TEAM/REPO [--state open|closed|merged|all]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("pr list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		state := fs.String("state", "open", "PR state")
		if e := fs.Parse(args[2:]); e != nil {
			return nil, e
		}
		o, _, e := c.Do(ctx, http.MethodGet, RepoPath(team, r)+"/pulls?state="+*state, nil)
		return o, e
	case "create":
		if len(args) < 2 {
			return nil, errors.New("usage: fh pr create TEAM/REPO --head BRANCH --title TITLE [--body TEXT]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		fs := flag.NewFlagSet("pr create", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		head := fs.String("head", "", "source branch")
		title := fs.String("title", "", "PR title")
		body := fs.String("body", "", "PR body")
		if e := fs.Parse(args[2:]); e != nil {
			return nil, e
		}
		if *head == "" || *title == "" {
			return nil, errors.New("--head and --title are required")
		}
		o, _, e := c.Do(ctx, http.MethodPost, RepoPath(team, r)+"/pulls", map[string]string{"headRef": *head, "title": *title, "body": *body})
		return o, e
	case "view":
		if len(args) < 3 {
			return nil, errors.New("usage: fh pr view TEAM/REPO NUMBER")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		o, _, e := c.Do(ctx, http.MethodGet, RepoPath(team, r)+"/pulls/"+args[2], nil)
		return o, e
	case "diff", "reviews", "approve", "request-changes":
		if len(args) < 3 {
			return nil, fmt.Errorf("usage: fh pr %s TEAM/REPO NUMBER [--body TEXT]", args[0])
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		number, err := strconv.Atoi(args[2])
		if err != nil || number < 1 {
			return nil, errors.New("PR number must be positive")
		}
		path := RepoPath(team, r) + "/pulls/" + strconv.Itoa(number)
		if args[0] == "diff" {
			o, _, err := c.Do(ctx, http.MethodGet, path+"/diff", nil)
			return o, err
		}
		if args[0] == "reviews" {
			o, _, err := c.Do(ctx, http.MethodGet, path+"/reviews", nil)
			return o, err
		}
		fs := flag.NewFlagSet("pr "+args[0], flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		body := fs.String("body", "", "review comment")
		if err := fs.Parse(args[3:]); err != nil || len(fs.Args()) != 0 {
			return nil, errors.New("unexpected review arguments")
		}
		if args[0] == "request-changes" && strings.TrimSpace(*body) == "" {
			return nil, errors.New("--body is required when requesting changes")
		}
		detail, _, err := c.Do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		pull, ok := detail["pull"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid PR detail response")
		}
		baseSHA, _ := pull["currentBaseSha"].(string)
		headSHA, _ := pull["currentHeadSha"].(string)
		if baseSHA == "" || headSHA == "" {
			return nil, errors.New("PR has no current base/head revision")
		}
		state := "approved"
		if args[0] == "request-changes" {
			state = "changes_requested"
		}
		o, _, err := c.Do(ctx, http.MethodPost, path+"/reviews", map[string]string{
			"state": state, "body": *body,
			"expectedBaseSha": baseSHA, "expectedHeadSha": headSHA,
		})
		return o, err
	case "comments":
		if len(args) < 3 {
			return nil, errors.New("usage: fh pr comments TEAM/REPO NUMBER [--path FILE --line N --side new|old --body TEXT [--reply-to ID]]")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		number, err := strconv.Atoi(args[2])
		if err != nil || number < 1 {
			return nil, errors.New("PR number must be positive")
		}
		path := RepoPath(team, r) + "/pulls/" + strconv.Itoa(number) + "/comments"
		fs := flag.NewFlagSet("pr comments", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		file := fs.String("path", "", "file path inside the repository")
		line := fs.Int("line", 0, "line number on the chosen side of the diff")
		side := fs.String("side", "new", "new or old")
		body := fs.String("body", "", "comment text")
		replyTo := fs.String("reply-to", "", "comment ID to reply to")
		if err := fs.Parse(args[3:]); err != nil || len(fs.Args()) != 0 {
			return nil, errors.New("unexpected comment arguments")
		}
		// Without --body the command lists comments, which is the read form.
		if strings.TrimSpace(*body) == "" && *replyTo == "" {
			o, _, err := c.Do(ctx, http.MethodGet, path, nil)
			return o, err
		}
		if *side != "new" && *side != "old" {
			return nil, errors.New("--side must be new or old")
		}
		if *file == "" || *line < 1 {
			return nil, errors.New("--path and --line are required to add a comment")
		}
		payload := map[string]any{"path": *file, "line": *line, "side": *side, "body": *body}
		if *replyTo != "" {
			payload["replyToId"] = *replyTo
		}
		o, _, err := c.Do(ctx, http.MethodPost, path, payload)
		return o, err
	case "merge":
		if len(args) < 3 {
			return nil, errors.New("usage: fh pr merge TEAM/REPO NUMBER")
		}
		team, r, ok := splitRepo(args[1])
		if !ok {
			return nil, errors.New("repository must be TEAM/REPO")
		}
		path := RepoPath(team, r) + "/pulls/" + args[2]
		detail, _, e := c.Do(ctx, http.MethodGet, path, nil)
		if e != nil {
			return nil, e
		}
		pull, ok := detail["pull"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid PR detail response")
		}
		sha, _ := pull["currentHeadSha"].(string)
		if sha == "" {
			return nil, errors.New("PR does not have a current mergeable head SHA")
		}
		o, _, e := c.Do(ctx, http.MethodPost, path+"/merge", map[string]string{"expectedHeadSha": sha})
		return o, e
	default:
		return nil, fmt.Errorf("unknown pr command %q", args[0])
	}
}
func splitRepo(s string) (string, string, bool) {
	p := strings.Split(s, "/")
	return func() (string, string, bool) {
		if len(p) != 2 || p[0] == "" || p[1] == "" {
			return "", "", false
		}
		return p[0], p[1], true
	}()
}
func printHelp(w io.Writer) {
	fmt.Fprintln(w, `FitHub CLI
Usage: fh [--server URL] [--json] <command>
Commands:
  auth status|login|logout
  repo list | view TEAM/REPO
  repo branches TEAM/REPO
  repo commit TEAM/REPO --branch BRANCH --head SHA --path REPO_PATH --file LOCAL_FILE --message MESSAGE
  issue list TEAM/REPO [--state open|closed|all] [--label LABEL_ID] [--assignee USER_ID] [--milestone N]
  issue create TEAM/REPO --title TITLE [--body TEXT]
  issue view TEAM/REPO NUMBER
  issue labels TEAM/REPO NUMBER [--set LABEL_ID,LABEL_ID]
  issue assign TEAM/REPO NUMBER [--users USER_ID,USER_ID]
  issue milestone TEAM/REPO NUMBER --number N
  label list TEAM/REPO
  label create TEAM/REPO --name NAME --color RRGGBB [--description TEXT]
  label update TEAM/REPO LABEL_ID --name NAME --color RRGGBB [--description TEXT]
  label delete TEAM/REPO LABEL_ID
  milestone list TEAM/REPO [--state open|closed|all]
  milestone create TEAM/REPO --title TITLE [--description TEXT] [--due YYYY-MM-DD]
  milestone update TEAM/REPO NUMBER --title TITLE [--description TEXT] [--due YYYY-MM-DD] --state open|closed
  milestone delete TEAM/REPO NUMBER
  pr list TEAM/REPO [--state open|closed|merged|all]
  pr create TEAM/REPO --head BRANCH --title TITLE [--body TEXT]
  pr view TEAM/REPO NUMBER
  pr diff TEAM/REPO NUMBER
  pr reviews TEAM/REPO NUMBER
  pr comments TEAM/REPO NUMBER [--path FILE --line N --side new|old --body TEXT [--reply-to ID]]
  pr approve TEAM/REPO NUMBER [--body TEXT]
  pr request-changes TEAM/REPO NUMBER --body TEXT
  pr merge TEAM/REPO NUMBER
FITHUB_URL selects the server, FITHUB_TOKEN supplies a bearer token, and FITHUB_CONFIG selects the credential file.`)
}
func printValue(w io.Writer, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(w, string(b))
}
