package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fithub.local/fithub/internal/store"
	"github.com/go-chi/chi/v5"
)

// The runner protocol is intentionally narrow. A runner authenticates with its
// own token (never a user token), may only see the jobs assigned to it, and every
// result call must present the job token of the attempt it is reporting. The
// control plane never trusts a runner's claim about repository or user identity.

// ciRunnerLease bounds how long a job is leased before the control plane assumes
// the runner is gone. It is generous enough for a normal build but short enough
// that a dead runner does not stall a queue.
const ciRunnerLease = 10 * time.Minute

// maxLogChunkBytes bounds one log upload so a runner cannot fill memory.
const maxLogChunkBytes = 64 << 10

// runnerToken extracts the runner credential from the Authorization header.
func runnerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if value == "" {
		return ""
	}
	const bearer = "Bearer "
	if len(value) <= len(bearer) || !strings.EqualFold(value[:len(bearer)], bearer) {
		return ""
	}
	return strings.TrimSpace(value[len(bearer):])
}

// requireRunner resolves the calling runner. A revoked or unknown token is
// answered uniformly so a token cannot be probed for validity.
func (s *Server) requireRunner(w http.ResponseWriter, r *http.Request) (store.CIRunner, bool) {
	token := runnerToken(r)
	if token == "" {
		fail(w, http.StatusUnauthorized, "UNAUTHENTICATED", "A runner token is required")
		return store.CIRunner{}, false
	}
	runner, err := s.Store.RunnerByToken(r.Context(), token)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusUnauthorized, "UNAUTHENTICATED", "A runner token is required")
		return store.CIRunner{}, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to authenticate runner")
		return store.CIRunner{}, false
	}
	return runner, true
}

// requireAttempt resolves the attempt named by a job token, and verifies the
// token belongs to the authenticated runner. A job token alone is not enough,
// so a token leaked from one runner cannot be used by another.
func (s *Server) requireAttempt(w http.ResponseWriter, r *http.Request, runner store.CIRunner) (store.CIJobAttempt, bool) {
	token := strings.TrimSpace(r.Header.Get("X-FitHub-Job-Token"))
	if token == "" {
		fail(w, http.StatusUnauthorized, "JOB_TOKEN_REQUIRED", "A job token is required")
		return store.CIJobAttempt{}, false
	}
	attempt, err := s.Store.AttemptByToken(r.Context(), token)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusUnauthorized, "JOB_TOKEN_INVALID", "This job token is not valid")
		return store.CIJobAttempt{}, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to verify job token")
		return store.CIJobAttempt{}, false
	}
	if attempt.RunnerID == nil || *attempt.RunnerID != runner.ID {
		fail(w, http.StatusForbidden, "JOB_TOKEN_MISMATCH", "This job token belongs to another runner")
		return store.CIJobAttempt{}, false
	}
	return attempt, true
}

// POST /api/v1/ci/jobs/claim
func (s *Server) claimCIJob(w http.ResponseWriter, r *http.Request) {
	runner, ok := s.requireRunner(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	claimed, jobSecret, err := s.Store.ClaimJob(ctx, runner, ciRunnerLease)
	if errors.Is(err, store.ErrNotFound) {
		// No work is not an error; the runner polls again.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to claim a job")
		return
	}
	secrets := map[string]string{}
	var metadata struct {
		SecretRefs []string `json:"secretRefs"`
	}
	_ = json.Unmarshal(claimed.Job.Definition, &metadata)
	if claimed.Run.Event == "push" && claimed.Run.Trusted {
		for _, ref := range metadata.SecretRefs {
			if ref != "DEPLOY_SSH_KEY" {
				continue
			}
			if key, keyErr := s.Store.GetRepoDeployKey(ctx, claimed.Run.RepoID); keyErr == nil {
				secrets["FITHUB_DEPLOY_SSH_KEY"] = key.PrivateKey
			}
		}
	}
	respond(w, http.StatusOK, map[string]any{
		"attemptId":    claimed.Attempt.ID,
		"attempt":      claimed.Attempt.Attempt,
		"expiresAt":    claimed.Attempt.ExpiresAt,
		"jobToken":     jobSecret,
		"jobId":        claimed.Job.JobID,
		"jobName":      claimed.Job.Name,
		"runNumber":    claimed.Run.Number,
		"runId":        claimed.Run.ID,
		"workflowPath": claimed.Run.WorkflowPath,
		"event":        claimed.Run.Event,
		"headSha":      claimed.Run.HeadSHA,
		"ref":          claimed.Run.Ref,
		"candidate":    claimed.Run.Candidate,
		"trusted":      claimed.Run.Trusted,
		"definition":   jsonOrEmpty(claimed.Job.Definition),
		"secrets":      secrets,
	})
}

// jsonOrEmpty keeps a definition field valid JSON in the response even when the
// stored value is somehow empty, so the runner always parses the same shape. The
// stored bytes are already JSON, so they are embedded verbatim: returning a bare
// []byte would make encoding/json emit base64 and the runner would receive a
// string instead of the step list.
func jsonOrEmpty(raw []byte) any {
	if len(raw) == 0 || !json.Valid(raw) {
		return map[string]any{}
	}
	return json.RawMessage(raw)
}

// POST /api/v1/ci/jobs/heartbeat
func (s *Server) heartbeatCIJob(w http.ResponseWriter, r *http.Request) {
	runner, ok := s.requireRunner(w, r)
	if !ok {
		return
	}
	attempt, ok := s.requireAttempt(w, r, runner)
	if !ok {
		return
	}
	expiresAt, err := s.Store.HeartbeatAttempt(r.Context(), attempt.ID, ciRunnerLease)
	if errors.Is(err, store.ErrLeaseExpired) {
		// The lease is gone: either the attempt was superseded or the run was
		// cancelled. The runner must stop rather than keep working.
		fail(w, http.StatusConflict, "LEASE_LOST", "This job lease is no longer valid")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to renew the lease")
		return
	}
	respond(w, http.StatusOK, map[string]any{"expiresAt": expiresAt, "cancel": false})
}

// POST /api/v1/ci/jobs/start
func (s *Server) startCIJob(w http.ResponseWriter, r *http.Request) {
	runner, ok := s.requireRunner(w, r)
	if !ok {
		return
	}
	attempt, ok := s.requireAttempt(w, r, runner)
	if !ok {
		return
	}
	if err := s.Store.MarkAttemptRunning(r.Context(), attempt.ID); err != nil {
		if errors.Is(err, store.ErrLeaseExpired) {
			fail(w, http.StatusConflict, "LEASE_LOST", "This job lease is no longer valid")
			return
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to mark the job as running")
		return
	}
	respond(w, http.StatusOK, map[string]any{"ok": true})
}

// completeRequest is the body of a job completion.
type completeRequest struct {
	State        string `json:"state"`
	ExitCode     *int   `json:"exitCode"`
	ErrorMessage string `json:"errorMessage"`
}

// POST /api/v1/ci/jobs/complete
func (s *Server) completeCIJob(w http.ResponseWriter, r *http.Request) {
	runner, ok := s.requireRunner(w, r)
	if !ok {
		return
	}
	attempt, ok := s.requireAttempt(w, r, runner)
	if !ok {
		return
	}
	var body completeRequest
	if !decode(w, r, &body) {
		return
	}
	job, run, err := s.Store.FinishAttempt(r.Context(), attempt.ID, store.CIAttemptResult{
		State: body.State, ExitCode: body.ExitCode, ErrorMessage: body.ErrorMessage,
	})
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "State must be a terminal job state")
		return
	case errors.Is(err, store.ErrLeaseExpired):
		fail(w, http.StatusConflict, "LEASE_LOST", "This job lease is no longer valid")
		return
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "RESULT_CONFLICT", "This attempt already reported a different result")
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to record the job result")
		return
	}
	// Only a run that has just reached a failing terminal state notifies, and only
	// once per run: the event key is the run, so later jobs of the same run or a
	// retried report cannot produce a second notice.
	if run.State == store.CIRunFailed && run.ActorID != nil {
		s.notifyRunFailure(r.Context(), run.RepoID, run.ID, run.Number, run.WorkflowName)
	}
	respond(w, http.StatusOK, map[string]any{
		"jobState": job.State, "runState": run.State,
	})
}

// POST /api/v1/ci/jobs/log
func (s *Server) appendCIJobLog(w http.ResponseWriter, r *http.Request) {
	runner, ok := s.requireRunner(w, r)
	if !ok {
		return
	}
	attempt, ok := s.requireAttempt(w, r, runner)
	if !ok {
		return
	}
	var body struct {
		Sequence int64  `json:"sequence"`
		Content  string `json:"content"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Content) > maxLogChunkBytes {
		fail(w, http.StatusRequestEntityTooLarge, "LOG_TOO_LARGE", "One log chunk may be at most 64 KiB")
		return
	}
	if err := s.Store.AppendLogChunk(r.Context(), attempt.ID, body.Sequence, body.Content); err != nil {
		if errors.Is(err, store.ErrInvalid) {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid log chunk")
			return
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to store the log chunk")
		return
	}
	respond(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /api/v1/repos/{team}/{repo}/runs/{number}/logs
//
// Reading logs is an ordinary repository read, but the caller must be able to
// read the repository, so a private repository's logs are not exposed to a
// runner or an unrelated account.
func (s *Server) ciRunLogs(w http.ResponseWriter, r *http.Request) {
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
	after := store.StartLogSequence
	if raw := r.URL.Query().Get("after"); raw != "" {
		parsed, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || parsed < store.StartLogSequence {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid log cursor")
			return
		}
		after = parsed
	}
	jobs, err := s.Store.ListCIJobs(r.Context(), run.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load jobs")
		return
	}
	// Logs are returned per job so a viewer can show each job's output without
	// knowing internal attempt identifiers.
	type jobLog struct {
		JobID   string   `json:"jobId"`
		Name    string   `json:"name"`
		State   string   `json:"state"`
		Lines   []string `json:"lines"`
		Cursor  int64    `json:"cursor"`
		Attempt int      `json:"attempt"`
	}
	result := make([]jobLog, 0, len(jobs))
	for _, job := range jobs {
		entry := jobLog{JobID: job.JobID, Name: job.Name, State: job.State, Lines: []string{}, Cursor: after}
		if job.CurrentAttempt > 0 {
			var attemptID string
			err := s.Store.Pool.QueryRow(r.Context(), `SELECT id::text FROM ci_job_attempts
				WHERE job_pk=$1 AND attempt=$2`, job.ID, job.CurrentAttempt).Scan(&attemptID)
			if err == nil {
				chunks, cursor, listErr := s.Store.ListLogChunks(r.Context(), attemptID, after, 500)
				if listErr != nil {
					fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load logs")
					return
				}
				entry.Lines = chunks
				entry.Cursor = cursor
				entry.Attempt = job.CurrentAttempt
			}
		}
		result = append(result, entry)
	}
	respond(w, http.StatusOK, map[string]any{
		"run": runView(run, len(jobs)), "jobs": result,
	})
}
func runView(run store.CIRun, jobCount int) map[string]any {
	view := map[string]any{
		"id": run.ID, "number": run.Number, "workflowPath": run.WorkflowPath,
		"workflowName": run.WorkflowName, "event": run.Event, "headSha": run.HeadSHA,
		"ref": run.Ref, "state": run.State, "createdAt": run.CreatedAt,
		"candidate": run.Candidate, "trusted": run.Trusted, "jobCount": jobCount,
	}
	if run.BaseSHA != nil {
		view["baseSha"] = *run.BaseSHA
	}
	if run.PullNumber != nil {
		view["pullNumber"] = *run.PullNumber
	}
	if run.ActorID != nil {
		view["actorId"] = *run.ActorID
	}
	if run.StartedAt != nil {
		view["startedAt"] = *run.StartedAt
	}
	if run.FinishedAt != nil {
		view["finishedAt"] = *run.FinishedAt
	}
	if run.ConclusionCause != nil {
		view["conclusionReason"] = *run.ConclusionCause
	}
	return view
}

// POST /api/v1/admin/ci/runners registers a runner. Only a platform
// administrator may register a runner, because a runner executes repository code
// and is a trusted component.
func (s *Server) registerCIRunner(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if p.user.Role != "admin" {
		fail(w, http.StatusForbidden, "ADMIN_REQUIRED", "Only a platform administrator may register a runner")
		return
	}
	var body struct {
		Name   string   `json:"name"`
		Labels []string `json:"labels"`
	}
	if !decode(w, r, &body) {
		return
	}
	runner, secret, err := s.Store.RegisterRunner(r.Context(), body.Name, body.Labels)
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Runner name and labels must be short, lowercase identifiers")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to register the runner")
		return
	}
	// The token is returned exactly once; only its hash is stored.
	respond(w, http.StatusCreated, map[string]any{
		"runner": map[string]any{
			"id": runner.ID, "name": runner.Name, "labels": runner.Labels,
			"state": runner.State, "createdAt": runner.CreatedAt,
		},
		"token": secret,
	})
}

// GET /api/v1/admin/ci/runners lists runners for an administrator.
func (s *Server) listCIRunners(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if p.user.Role != "admin" {
		fail(w, http.StatusForbidden, "ADMIN_REQUIRED", "Only a platform administrator may list runners")
		return
	}
	runners, err := s.Store.ListRunners(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to list runners")
		return
	}
	views := make([]map[string]any, 0, len(runners))
	for _, runner := range runners {
		view := map[string]any{
			"id": runner.ID, "name": runner.Name, "labels": runner.Labels,
			"state": runner.State, "createdAt": runner.CreatedAt,
		}
		if runner.LastSeenAt != nil {
			view["lastSeenAt"] = *runner.LastSeenAt
		}
		if runner.RevokedAt != nil {
			view["revokedAt"] = *runner.RevokedAt
		}
		views = append(views, view)
	}
	respond(w, http.StatusOK, map[string]any{"runners": views})
}

// DELETE /api/v1/admin/ci/runners/{runnerID} revokes a runner.
func (s *Server) revokeCIRunner(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	if p.user.Role != "admin" {
		fail(w, http.StatusForbidden, "ADMIN_REQUIRED", "Only a platform administrator may revoke a runner")
		return
	}
	runnerID := chi.URLParam(r, "runnerID")
	runner, err := s.Store.RevokeRunner(r.Context(), runnerID)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Runner not found")
		return
	}
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid runner id")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to revoke the runner")
		return
	}
	respond(w, http.StatusOK, map[string]any{"runner": map[string]any{
		"id": runner.ID, "state": runner.State,
	}})
}

// GET /api/v1/ci/jobs/archive?sha=...
//
// A runner downloads exactly the revision under test. The repository and the
// revision both come from the attempt record, never from the request, so a job
// token cannot be used to read another repository or another revision: the sha
// parameter is only accepted when it matches the attempt's own head.
func (s *Server) ciJobArchive(w http.ResponseWriter, r *http.Request) {
	runner, ok := s.requireRunner(w, r)
	if !ok {
		return
	}
	attempt, ok := s.requireAttempt(w, r, runner)
	if !ok {
		return
	}
	_, run, err := s.Store.AttemptContext(r.Context(), attempt.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to resolve the job revision")
		return
	}
	requested := strings.TrimSpace(r.URL.Query().Get("sha"))
	if requested != "" && requested != run.HeadSHA {
		fail(w, http.StatusForbidden, "REVISION_MISMATCH", "This job may only read the revision it was assigned")
		return
	}
	// The archive is streamed with its own timeout; a large repository must not
	// hold the request open forever.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", "attachment; filename=revision.tar.gz")
	w.WriteHeader(http.StatusOK)
	if err := s.Git.Archive(ctx, run.RepoID, run.HeadSHA, w); err != nil {
		// The status line is already sent, so a failure can only be recorded in
		// the log; the runner sees a truncated archive and reports the job as
		// failed rather than running an incomplete checkout.
		return
	}
}
