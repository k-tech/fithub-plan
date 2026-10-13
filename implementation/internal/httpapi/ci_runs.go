package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/store"
	"fithub.local/fithub/internal/workflow"
	"github.com/go-chi/chi/v5"
)

// GET /api/v1/repos/{team}/{repo}/runs
func (s *Server) listCIRuns(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "all"
	}
	switch state {
	case "all", store.CIRunQueued, store.CIRunRunning, store.CIRunSucceeded,
		store.CIRunFailed, store.CIRunCancelled, store.CIRunTimedOut, store.CIRunInfrastructureFailed:
	default:
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid run state")
		return
	}
	page, perPage := 1, 20
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 1_000_000 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid page")
			return
		}
		page = parsed
	}
	runs, hasMore, err := s.Store.ListCIRuns(r.Context(), repo.ID, state, page, perPage)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to list runs")
		return
	}
	views := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		views = append(views, runView(run, run.JobCount))
	}
	respond(w, http.StatusOK, map[string]any{
		"runs": views, "page": page, "perPage": perPage, "hasMore": hasMore,
	})
}

// GET /api/v1/repos/{team}/{repo}/runs/{number}
func (s *Server) getCIRun(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	number, err := strconv.ParseInt(chi.URLParam(r, "number"), 10, 64)
	if err != nil || number < 1 {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid run number")
		return
	}
	run, err := s.Store.GetCIRun(r.Context(), repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Run not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load run")
		return
	}
	jobs, err := s.Store.ListCIJobs(r.Context(), run.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load jobs")
		return
	}
	jobViews := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		view := map[string]any{
			"id": job.ID, "jobId": job.JobID, "name": job.Name, "runsOn": job.RunsOn,
			"needs": job.Needs, "state": job.State, "attempt": job.CurrentAttempt,
			"createdAt": job.CreatedAt,
		}
		if job.StartedAt != nil {
			view["startedAt"] = *job.StartedAt
		}
		if job.FinishedAt != nil {
			view["finishedAt"] = *job.FinishedAt
		}
		jobViews = append(jobViews, view)
	}
	respond(w, http.StatusOK, map[string]any{
		"run": runView(run, len(jobs)), "jobs": jobViews,
	})
}

// POST /api/v1/repos/{team}/{repo}/runs/{number}/cancel
func (s *Server) cancelCIRun(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	number, err := strconv.ParseInt(chi.URLParam(r, "number"), 10, 64)
	if err != nil || number < 1 {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid run number")
		return
	}
	run, err := s.Store.CancelRun(r.Context(), repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Run not found")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		fail(w, http.StatusConflict, "RUN_FINISHED", "This run has already finished")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to cancel the run")
		return
	}
	respond(w, http.StatusOK, map[string]any{"run": runView(run, 0)})
}

// POST /api/v1/repos/{team}/{repo}/runs/dispatch queues a manual run.
//
// A dispatch runs the current trusted default-branch definitions against the
// default branch, so it cannot be used to execute an unreviewed workflow.
func (s *Server) dispatchCIRun(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	repo, ok := s.issueRepo(w, r, p, "write")
	if !ok {
		return
	}
	var body struct {
		WorkflowPath string `json:"workflowPath"`
	}
	if !decode(w, r, &body) {
		return
	}
	head, err := s.Git.BranchHead(r.Context(), repo.ID, "refs/heads/"+repo.DefaultBranch)
	if err != nil {
		fail(w, http.StatusConflict, "NO_DEFAULT_BRANCH", "The default branch has no commits yet")
		return
	}
	path := strings.TrimSpace(body.WorkflowPath)
	if path != "" && !workflow.IsWorkflowPath(path) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "workflowPath must name a workflow file")
		return
	}
	recorded, created, err := s.Store.RecordTriggerEvent(r.Context(), store.CITriggerEvent{
		RepoID: repo.ID, Event: "workflow_dispatch", Ref: "refs/heads/" + repo.DefaultBranch,
		NewSHA: head, ActorID: &p.user.ID, WorkflowPath: optionalPath(path),
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to queue the run")
		return
	}
	if !created {
		fail(w, http.StatusConflict, "DUPLICATE_DISPATCH", "This dispatch was already queued")
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"eventId": recorded.ID, "state": recorded.State})
}

func optionalPath(path string) *string {
	if path == "" {
		return nil
	}
	return &path
}

// CITriggerWorker turns queued triggers into runs. It is the only place that
// reads workflow files and enqueues jobs, so a push never waits on validation and
// the receive hook stays authoritative about refs only.
type CITriggerWorker struct {
	Store *store.Store
	Git   *gitservice.Service
	// MaxWorkflows bounds how many workflow files one event may expand into.
	MaxWorkflows int
}

// Run processes pending triggers until the context is cancelled.
func (w *CITriggerWorker) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.ProcessOnce(ctx)
		}
	}
}

// ProcessOnce handles one batch of events. A failure is recorded on the event
// rather than returned, so one bad workflow cannot stop the queue.
func (w *CITriggerWorker) ProcessOnce(ctx context.Context) {
	if w.Store == nil || w.Git == nil {
		return
	}
	events, err := w.Store.ClaimTriggerEvents(ctx, 20)
	if err != nil {
		return
	}
	for _, event := range events {
		if err := w.processEvent(ctx, event); err != nil {
			_ = w.Store.MarkTriggerFailed(ctx, event.ID, triggerFailureReason(err))
			continue
		}
		_ = w.Store.MarkTriggerDone(ctx, event.ID)
	}
}

// triggerFailureReason keeps a recorded reason short. The text comes from this
// package, never from repository content, so it cannot leak a secret.
func triggerFailureReason(err error) string {
	switch {
	case errors.Is(err, store.ErrInvalid):
		return "the trigger referenced an invalid revision or workflow"
	case errors.Is(err, store.ErrNotFound):
		return "the workflow or repository no longer exists"
	default:
		return "the run could not be prepared"
	}
}

func (w *CITriggerWorker) processEvent(ctx context.Context, event store.CITriggerEvent) error {
	// Workflows come from the trusted default branch. A pull request therefore
	// runs the definitions that protect the branch rather than the definition the
	// pull request proposes, so a workflow change cannot alter the checks that
	// govern its own merge.
	repo, err := w.Store.GetRepo(ctx, event.RepoID)
	if err != nil {
		return err
	}
	if repo.State != "active" {
		return store.ErrInvalid
	}
	trusted := event.Ref == "refs/heads/"+repo.DefaultBranch
	if event.Event == "workflow_dispatch" && !trusted {
		return store.ErrInvalid
	}
	files, err := w.Git.ListWorkflowFiles(ctx, repo.ID, repo.DefaultBranch)
	if err != nil {
		return err
	}
	max := w.MaxWorkflows
	if max <= 0 {
		max = 8
	}
	if len(files) > max {
		files = files[:max]
	}
	enqueued := 0
	var firstErr error
	// A workflow file that exists but cannot be read or parsed would otherwise
	// produce no run at all, leaving the push silently unexplained. Remember it so
	// the event is recorded as failed when nothing else could run.
	var parseFailure error
	for _, file := range files {
		if event.WorkflowPath != nil && *event.WorkflowPath != "" && file != *event.WorkflowPath {
			continue
		}
		blob, readErr := w.Git.BrowseBlob(ctx, repo.ID, repo.DefaultBranch, file)
		if readErr != nil || blob.TooLarge || blob.Binary || blob.Text == "" {
			// A file the service cannot read as text cannot define a workflow, so
			// it is skipped rather than failing the whole event.
			if parseFailure == nil {
				parseFailure = fmt.Errorf("workflow %s could not be read: %w", file, store.ErrInvalid)
			}
			continue
		}
		document, _, parseErr := workflow.Parse(file, []byte(blob.Text))
		if parseErr != nil {
			if parseFailure == nil {
				parseFailure = fmt.Errorf("workflow %s is not valid: %w", file, store.ErrInvalid)
			}
			continue
		}
		if !documentMatchesEvent(document, event.Event) {
			continue
		}
		jobs := make([]store.CIJobDefinition, 0, len(document.Jobs))
		for _, job := range document.Jobs {
			// Only the steps travel to the runner. Job identity, runner label, and
			// dependency order are already stored as their own columns and are
			// resolved by the control plane, so the runner never has to trust a
			// copy of them.
			definition, marshalErr := json.Marshal(struct {
				Steps      []workflow.Step `json:"steps"`
				SecretRefs []string        `json:"secretRefs"`
			}{Steps: job.Steps, SecretRefs: job.SecretRefs})
			if marshalErr != nil {
				continue
			}
			jobs = append(jobs, store.CIJobDefinition{
				JobID: job.ID, Name: jobDisplayName(job), RunsOn: job.RunsOn,
				Needs: job.Needs, Definition: definition,
			})
		}
		if len(jobs) == 0 {
			continue
		}
		request := store.CIEnqueueRequest{
			RepoID: repo.ID, WorkflowPath: document.Path, WorkflowName: document.Name,
			WorkflowSource: document.Raw, Event: event.Event, HeadSHA: event.NewSHA,
			Ref: event.Ref, Candidate: event.Event == "pull_request",
			Trusted: trusted, ActorID: derefString(event.ActorID), Jobs: jobs,
		}
		if event.Event == "pull_request" && event.OldSHA != nil {
			request.BaseSHA = *event.OldSHA
		}
		if event.PullNumber != nil {
			request.PullNumber = *event.PullNumber
		}
		if _, _, enqueueErr := w.Store.EnqueueRun(ctx, request); enqueueErr == nil {
			enqueued++
		} else if !errors.Is(enqueueErr, store.ErrConflict) && firstErr == nil {
			// A conflict means a run for this workflow and revision is already
			// open, which satisfies the event.
			firstErr = enqueueErr
		}
	}
	if enqueued == 0 && firstErr != nil {
		return firstErr
	}
	// Nothing ran because nothing could be parsed: report it instead of leaving
	// the push with no visible outcome.
	if enqueued == 0 && firstErr == nil && parseFailure != nil {
		return parseFailure
	}
	return nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// documentMatchesEvent reports whether a workflow's triggers cover this event.
func documentMatchesEvent(document workflow.Document, event string) bool {
	for _, trigger := range document.Triggers {
		if trigger == event {
			return true
		}
	}
	return false
}

// jobDisplayName uses the workflow's name for a job when present.
func jobDisplayName(job workflow.Job) string {
	if job.Name != "" {
		return job.Name
	}
	return job.ID
}
