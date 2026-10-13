// fithub-hook is the database-backed Git receive hook. It is installed as the
// pre-receive hook of every managed repository and reads the proposed ref
// updates on stdin.
//
// It re-reads the pushing account and repository write permission from the
// database for every update, so a revoked account cannot finish a push.
//
// The pushing account comes from the gateway process as environment variables,
// never from the client: HTTP sets them after token authentication and SSH sets
// them after public-key authentication.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"fithub.local/fithub/internal/authorization"
	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/store"
	"fithub.local/fithub/internal/workflow"
	"github.com/jackc/pgx/v5/pgxpool"
)

// gitExecutable is resolved once at startup. The workflow-path check runs Git
// itself, so an absolute path avoids depending on the hook process PATH.
var gitExecutable string

func main() {
	path, err := exec.LookPath("git")
	if err != nil {
		fmt.Fprintln(os.Stderr, "git executable is unavailable")
		os.Exit(1)
	}
	gitExecutable = path
	if code := run(os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

func run(args []string) int {
	if len(args) < 1 || args[0] != "pre-receive" {
		fmt.Fprintln(os.Stderr, "usage: fithub-hook pre-receive --config PATH --repo-id UUID")
		return 2
	}
	var configPath, repoID, gitDir string
	fs := flag.NewFlagSet("pre-receive", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&configPath, "config", "", "absolute path to the hook configuration")
	fs.StringVar(&repoID, "repo-id", "", "repository UUID owning this hook")
	fs.StringVar(&gitDir, "git-dir", "", "absolute path to the bare repository")
	if err := fs.Parse(args[1:]); err != nil || len(fs.Args()) != 0 || configPath == "" || repoID == "" || gitDir == "" {
		return 2
	}
	cfg, err := gitservice.LoadHookConfig(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Git receive policy configuration unavailable")
		return 1
	}
	// A push always has an authenticated account. Treating a missing identity
	// as anonymous would silently skip the permission part of the decision.
	userID := strings.TrimSpace(os.Getenv("FITHUB_USER_ID"))
	actorKind := strings.TrimSpace(os.Getenv("FITHUB_ACTOR_KIND"))
	if actorKind == "" {
		actorKind = "token"
	}
	if userID == "" {
		fmt.Fprintln(os.Stderr, "Git receive policy has no authenticated account")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Git receive policy unavailable")
		return 1
	}
	defer pool.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		fmt.Fprintln(os.Stderr, "Git receive policy unavailable")
		return 1
	}
	data := store.New(pool)
	updates, err := readUpdates(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rejected: %v\n", err)
		return 1
	}
	if len(updates) == 0 {
		return 0
	}
	repo, err := data.GetRepo(ctx, repoID)
	if err != nil || repo.State != "active" {
		fmt.Fprintln(os.Stderr, "rejected: repository is unavailable")
		return 1
	}
	actor, err := data.GetUser(ctx, userID)
	if err != nil || actor.Status != "active" {
		fmt.Fprintln(os.Stderr, "rejected: pushing account is unavailable")
		return 1
	}
	checkCtx, checkCancel := context.WithTimeout(ctx, 10*time.Second)
	canWrite, err := authorization.CanRepository(checkCtx, data, &actor, nil, repo, "write")
	checkCancel()
	if err != nil || !canWrite {
		fmt.Fprintln(os.Stderr, "rejected: repository write access is required")
		return 1
	}
	adminCtx, adminCancel := context.WithTimeout(ctx, 10*time.Second)
	isAdmin, err := authorization.CanRepository(adminCtx, data, &actor, nil, repo, "admin")
	adminCancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot verify repository role")
		return 1
	}
	actorAccess := "write"
	if isAdmin {
		actorAccess = "admin"
	}
	refs := make([]string, 0, len(updates))
	for _, update := range updates {
		// Workflow files decide what code the runner executes, so only a
		// repository administrator may change them. Inspect uploaded Git objects,
		// not a client-supplied file list.
		if reason := checkWorkflowPaths(ctx, gitDir, actorAccess, update); reason != "" {
			fmt.Fprintf(os.Stderr, "rejected: %s\n", reason)
			return 1
		}
		refs = append(refs, update.Ref)
	}
	// Record the accepted decision. A failure here must reject the push because
	// accepted Git updates need an audit record.
	auditCtx, done := context.WithTimeout(ctx, 10*time.Second)
	defer done()
	if err := data.RecordRefDecision(auditCtx, repoID, userID, actorKind, refs); err != nil {
		fmt.Fprintln(os.Stderr, "cannot record Git push audit")
		return 1
	}
	// Queue CI triggers for accepted branch updates. The control plane turns these
	// into runs, so a push never waits on workflow validation. A failure to queue
	// is reported but does not reject the push: the ref update itself is already
	// authorized, and CI can be re-dispatched manually.
	for _, update := range updates {
		if !strings.HasPrefix(update.Ref, "refs/heads/") || isZero(update.NewSHA) {
			continue
		}
		var oldSHA *string
		if !isZero(update.OldSHA) && update.OldSHA != "" {
			old := update.OldSHA
			oldSHA = &old
		}
		actor := userID
		triggerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, _, err := data.RecordTriggerEvent(triggerCtx, store.CITriggerEvent{
			RepoID: repoID, Event: "push", Ref: update.Ref, OldSHA: oldSHA,
			NewSHA: update.NewSHA, ActorID: &actor,
		})
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "note: the push succeeded but CI could not be queued")
		}
	}
	return 0
}

// checkWorkflowPaths reports a rejection reason when a ref update changes a
// workflow file and the pushing account is not a repository administrator. An
// unchanged workflow is allowed, because a branch that already contains one must
// remain pushable by its authors. A failure to inspect the change is itself a
// rejection: an uninspectable update might contain a workflow file.
func checkWorkflowPaths(ctx context.Context, gitDir, access string, update readUpdate) string {
	// A deletion or a ref that no longer points at a commit cannot add a working
	// workflow definition, so it needs no path inspection. An unrecognized value
	// is refused by the diff helper below.
	if update.NewSHA == "" || isZero(update.NewSHA) {
		return ""
	}
	// The same inspection the merge path uses decides which paths the update
	// touches, so a client cannot claim a push avoids the workflow directory.
	inspectCtx, done := context.WithTimeout(ctx, 10*time.Second)
	defer done()
	paths, err := gitservice.ChangedPathsInRepo(inspectCtx, gitExecutable, gitDir, update.OldSHA, update.NewSHA)
	if err != nil {
		return "cannot inspect the changed paths of this push"
	}
	touchesWorkflow := false
	for _, changed := range paths {
		if workflow.IsWorkflowPath(changed) {
			touchesWorkflow = true
			break
		}
	}
	if !touchesWorkflow {
		return ""
	}
	if access != "admin" {
		return "workflow files under " + workflow.FilePrefix + " may only be changed by a repository administrator"
	}
	return ""
}

func isZero(oid string) bool {
	if oid == "" {
		return false
	}
	for _, c := range oid {
		if c != '0' {
			return false
		}
	}
	return true
}

// readUpdate is one parsed pre-receive line.
type readUpdate struct {
	OldSHA string
	NewSHA string
	Ref    string
}

// readUpdates parses Git's pre-receive stdin format: three space-separated
// fields per line. It rejects anything it cannot fully understand instead of
// skipping a line, because a skipped line would be an unchecked update.
func readUpdates(input *os.File) ([]readUpdate, error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	updates := make([]readUpdate, 0)
	seen := make(map[string]bool)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, " ")
		if len(fields) != 3 {
			return nil, errors.New("unexpected update line")
		}
		oldSHA, newSHA, ref := fields[0], fields[1], fields[2]
		if !gitservice.ValidSHAOrZero(oldSHA) || !gitservice.ValidSHAOrZero(newSHA) {
			return nil, errors.New("unexpected object id")
		}
		if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
			return nil, errors.New("ref namespace is not writable")
		}
		if seen[ref] {
			return nil, errors.New("duplicate ref update")
		}
		seen[ref] = true
		updates = append(updates, readUpdate{OldSHA: oldSHA, NewSHA: newSHA, Ref: ref})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return updates, nil
}
