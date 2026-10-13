package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// CIRunner is a registered job executor. Only the hash of its token is stored.
type CIRunner struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Labels     []string   `json:"labels"`
	State      string     `json:"state"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
}

// CIRun is one execution of one workflow file at one revision.
type CIRun struct {
	ID             string  `json:"id"`
	RepoID         string  `json:"repoId"`
	Number         int64   `json:"number"`
	WorkflowPath   string  `json:"workflowPath"`
	WorkflowName   string  `json:"workflowName"`
	WorkflowSource string  `json:"-"`
	Event          string  `json:"event"`
	HeadSHA        string  `json:"headSha"`
	BaseSHA        *string `json:"baseSha"`
	Ref            string  `json:"ref"`
	PullNumber     *int64  `json:"pullNumber"`
	Candidate      bool    `json:"candidate"`
	Trusted        bool    `json:"trusted"`
	// JobCount is only populated by list queries, which compute it alongside the
	// run so a list view does not need one query per row.
	JobCount        int        `json:"jobCount"`
	ActorID         *string    `json:"actorId"`
	State           string     `json:"state"`
	CreatedAt       time.Time  `json:"createdAt"`
	StartedAt       *time.Time `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
	ConclusionCause *string    `json:"conclusionReason"`
}

// CIJob is one job of a run. Definition is the validated JSON the runner will
// execute, captured at enqueue time so a later workflow edit cannot change it.
type CIJob struct {
	ID             string     `json:"id"`
	RunID          string     `json:"runId"`
	JobID          string     `json:"jobId"`
	Name           string     `json:"name"`
	RunsOn         string     `json:"runsOn"`
	Needs          []string   `json:"needs"`
	Definition     []byte     `json:"-"`
	State          string     `json:"state"`
	CurrentAttempt int        `json:"currentAttempt"`
	CreatedAt      time.Time  `json:"createdAt"`
	StartedAt      *time.Time `json:"startedAt"`
	FinishedAt     *time.Time `json:"finishedAt"`
}

// CIJobAttempt is one lease of one job by one runner.
type CIJobAttempt struct {
	ID           string     `json:"id"`
	JobPK        string     `json:"jobPk"`
	Attempt      int        `json:"attempt"`
	RunnerID     *string    `json:"runnerId"`
	State        string     `json:"state"`
	LeasedAt     time.Time  `json:"leasedAt"`
	ExpiresAt    time.Time  `json:"expiresAt"`
	HeartbeatAt  *time.Time `json:"heartbeatAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	ExitCode     *int       `json:"exitCode"`
	ErrorMessage *string    `json:"errorMessage"`
}

// CIRunState values. A run ends in one terminal state and never leaves it.
const (
	CIRunQueued               = "queued"
	CIRunRunning              = "running"
	CIRunSucceeded            = "succeeded"
	CIRunFailed               = "failed"
	CIRunCancelled            = "cancelled"
	CIRunTimedOut             = "timed_out"
	CIRunInfrastructureFailed = "infrastructure_failed"
)

// CIJobState values. waiting means a needed job has not finished successfully.
const (
	CIJobQueued               = "queued"
	CIJobWaiting              = "waiting"
	CIJobLeased               = "leased"
	CIJobRunning              = "running"
	CIJobSucceeded            = "succeeded"
	CIJobFailed               = "failed"
	CIJobCancelled            = "cancelled"
	CIJobTimedOut             = "timed_out"
	CIJobInfrastructureFailed = "infrastructure_failed"
)

// CIAttemptLeased and CIAttemptRunning are the non-terminal attempt states.
const (
	CIAttemptLeased  = "leased"
	CIAttemptRunning = "running"
)

// ErrLeaseExpired means the attempt no longer holds a valid lease, so the caller
// must not act on its result. It is distinct from a generic conflict so the
// runner protocol can answer with a specific status.
var ErrLeaseExpired = errors.New("store: job lease expired or superseded")

// StartLogSequence is the cursor a log viewer passes to request output from the
// beginning. The first stored chunk has sequence 0, so the cursor sits before it.
const StartLogSequence int64 = -1

// CIEnqueueRequest describes a run to create. Jobs come from the validated
// workflow document, not from the caller's own parse.
type CIEnqueueRequest struct {
	RepoID         string
	WorkflowPath   string
	WorkflowName   string
	WorkflowSource string
	Event          string
	HeadSHA        string
	BaseSHA        string
	Ref            string
	PullNumber     int64
	Candidate      bool
	Trusted        bool
	ActorID        string
	Jobs           []CIJobDefinition
}

// CIJobDefinition is one validated job to queue.
type CIJobDefinition struct {
	JobID      string
	Name       string
	RunsOn     string
	Needs      []string
	Definition []byte
}

// NewRunnerSecret generates a runner or job token and its stored hash. The
// plaintext is returned once and never persisted.
func NewRunnerSecret() (secret, hash string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	secret = hex.EncodeToString(raw)
	return secret, HashRunnerSecret(secret), nil
}

// HashRunnerSecret hashes a token for lookup. Tokens are high-entropy random
// strings, so a fast hash is sufficient and keeps the hot path cheap; it is not
// a password store.
func HashRunnerSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// RegisterRunner stores a new runner and returns it with the one-time token.
func (s *Store) RegisterRunner(ctx context.Context, name string, labels []string) (CIRunner, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 || !utf8.ValidString(name) {
		return CIRunner{}, "", invalid("runner name")
	}
	normalized := make([]string, 0, len(labels))
	seen := map[string]bool{}
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" || len(label) > 32 || seen[label] {
			return CIRunner{}, "", invalid("runner label")
		}
		for _, char := range label {
			if !(char == '-' || char == '_' || char == '.' || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')) {
				return CIRunner{}, "", invalid("runner label")
			}
		}
		seen[label] = true
		normalized = append(normalized, label)
	}
	secret, hash, err := NewRunnerSecret()
	if err != nil {
		return CIRunner{}, "", err
	}
	var runner CIRunner
	err = s.Pool.QueryRow(ctx, `INSERT INTO ci_runners (name, token_hash, labels)
		VALUES ($1,$2,$3)
		RETURNING id::text,name,labels,state,last_seen_at,created_at`,
		name, hash, normalized).Scan(&runner.ID, &runner.Name, &runner.Labels,
		&runner.State, &runner.LastSeenAt, &runner.CreatedAt)
	if err != nil {
		return CIRunner{}, "", dbErr(err)
	}
	return runner, secret, nil
}

// RunnerByToken resolves an active runner from its token. A revoked runner is
// treated as unknown so a revoked token cannot claim more work.
func (s *Store) RunnerByToken(ctx context.Context, secret string) (CIRunner, error) {
	if secret == "" {
		return CIRunner{}, ErrNotFound
	}
	var runner CIRunner
	err := s.Pool.QueryRow(ctx, `SELECT id::text,name,labels,state,last_seen_at,created_at
		FROM ci_runners WHERE token_hash=$1 AND state='active'`, HashRunnerSecret(secret)).
		Scan(&runner.ID, &runner.Name, &runner.Labels, &runner.State, &runner.LastSeenAt, &runner.CreatedAt)
	if err != nil {
		return CIRunner{}, dbErr(err)
	}
	return runner, nil
}

// RevokeRunner stops a runner from claiming work. Existing leases keep running so
// a revoke does not strand a job, but no new lease is issued.
func (s *Store) RevokeRunner(ctx context.Context, runnerID string) (CIRunner, error) {
	if !canonicalStoreUUID.MatchString(runnerID) {
		return CIRunner{}, invalid("runner id")
	}
	var runner CIRunner
	err := s.Pool.QueryRow(ctx, `UPDATE ci_runners
		SET state='revoked', revoked_at=clock_timestamp()
		WHERE id=$1 AND state='active'
		RETURNING id::text,name,labels,state,last_seen_at,created_at,revoked_at`,
		runnerID).Scan(&runner.ID, &runner.Name, &runner.Labels, &runner.State,
		&runner.LastSeenAt, &runner.CreatedAt, &runner.RevokedAt)
	if err != nil {
		return CIRunner{}, dbErr(err)
	}
	return runner, nil
}

// ListRunners returns every registered runner for the administration UI.
func (s *Store) ListRunners(ctx context.Context) ([]CIRunner, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text,name,labels,state,last_seen_at,created_at,revoked_at
		FROM ci_runners ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	runners := make([]CIRunner, 0, 8)
	for rows.Next() {
		var runner CIRunner
		if err := rows.Scan(&runner.ID, &runner.Name, &runner.Labels, &runner.State,
			&runner.LastSeenAt, &runner.CreatedAt, &runner.RevokedAt); err != nil {
			return nil, dbErr(err)
		}
		runners = append(runners, runner)
	}
	return runners, dbErr(rows.Err())
}

// ciRunColumns is shared by the run queries so every read returns the same shape.
const ciRunColumns = `r.id::text,r.repo_id::text,r.number,r.workflow_path,r.workflow_name,
    r.workflow_source,r.event,r.head_sha,r.base_sha,r.ref,r.pull_number,r.candidate,
    r.trusted,r.actor_id::text,r.state,r.created_at,r.started_at,r.finished_at,r.conclusion_reason`

func scanCIRun(row scanner) (CIRun, error) {
	var run CIRun
	err := row.Scan(&run.ID, &run.RepoID, &run.Number, &run.WorkflowPath, &run.WorkflowName,
		&run.WorkflowSource, &run.Event, &run.HeadSHA, &run.BaseSHA, &run.Ref, &run.PullNumber,
		&run.Candidate, &run.Trusted, &run.ActorID, &run.State, &run.CreatedAt, &run.StartedAt,
		&run.FinishedAt, &run.ConclusionCause)
	return run, dbErr(err)
}

// scanCIRunWithJobs reads a run row that also carries the job count produced by
// the list query.
func scanCIRunWithJobs(row scanner) (CIRun, error) {
	var run CIRun
	err := row.Scan(&run.ID, &run.RepoID, &run.Number, &run.WorkflowPath, &run.WorkflowName,
		&run.WorkflowSource, &run.Event, &run.HeadSHA, &run.BaseSHA, &run.Ref, &run.PullNumber,
		&run.Candidate, &run.Trusted, &run.ActorID, &run.State, &run.CreatedAt, &run.StartedAt,
		&run.FinishedAt, &run.ConclusionCause, &run.JobCount)
	return run, dbErr(err)
}

// EnqueueRun creates a run and its jobs in one transaction. A concurrent
// duplicate for the same workflow/revision/event returns ErrConflict so a retried
// request cannot start a second execution.
func (s *Store) EnqueueRun(ctx context.Context, request CIEnqueueRequest) (CIRun, []CIJob, error) {
	if !canonicalStoreUUID.MatchString(request.RepoID) || !validGitSHA(request.HeadSHA) ||
		request.WorkflowPath == "" || len(request.WorkflowPath) > 512 ||
		request.WorkflowName == "" || len(request.WorkflowName) > 128 ||
		request.WorkflowSource == "" || len(request.WorkflowSource) > 131072 ||
		(request.Event != "push" && request.Event != "pull_request" && request.Event != "workflow_dispatch") ||
		(request.BaseSHA != "" && !validGitSHA(request.BaseSHA)) ||
		request.Ref == "" || len(request.Ref) > 512 || len(request.Jobs) == 0 ||
		(request.ActorID != "" && !canonicalStoreUUID.MatchString(request.ActorID)) {
		return CIRun{}, nil, invalid("run request")
	}
	if request.PullNumber < 0 {
		return CIRun{}, nil, invalid("pull number")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return CIRun{}, nil, err
	}
	defer tx.Rollback(ctx)
	// Serialize number allocation per repository so two enqueues cannot pick the
	// same number.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM repositories WHERE id=$1 FOR UPDATE`, request.RepoID); err != nil {
		return CIRun{}, nil, dbErr(err)
	}
	var number int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM ci_runs WHERE repo_id=$1`, request.RepoID).Scan(&number); err != nil {
		return CIRun{}, nil, dbErr(err)
	}
	var actor any
	if request.ActorID != "" {
		actor = request.ActorID
	}
	var baseSHA any
	if request.BaseSHA != "" {
		baseSHA = request.BaseSHA
	}
	var pullNumber any
	if request.PullNumber > 0 {
		pullNumber = request.PullNumber
	}
	var runID string
	err = tx.QueryRow(ctx, `INSERT INTO ci_runs
		(repo_id,number,workflow_path,workflow_name,workflow_source,event,head_sha,base_sha,ref,pull_number,candidate,trusted,actor_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id::text`,
		request.RepoID, number, request.WorkflowPath, request.WorkflowName, request.WorkflowSource,
		request.Event, request.HeadSHA, baseSHA, request.Ref, pullNumber, request.Candidate,
		request.Trusted, actor).Scan(&runID)
	if err != nil {
		if isUniqueViolation(err) {
			return CIRun{}, nil, ErrConflict
		}
		return CIRun{}, nil, dbErr(err)
	}
	jobs := make([]CIJob, 0, len(request.Jobs))
	known := make(map[string]bool, len(request.Jobs))
	for _, job := range request.Jobs {
		if job.JobID == "" || len(job.JobID) > 64 || job.Name == "" || len(job.Name) > 128 ||
			job.RunsOn == "" || len(job.RunsOn) > 32 || len(job.Definition) == 0 {
			return CIRun{}, nil, invalid("job definition")
		}
		if known[job.JobID] {
			return CIRun{}, nil, invalid("duplicate job id")
		}
		known[job.JobID] = true
	}
	// A job whose dependencies are not satisfied waits instead of running, which
	// keeps the graph visible in the UI from the start.
	for _, job := range request.Jobs {
		state := CIJobQueued
		if len(job.Needs) > 0 {
			state = CIJobWaiting
		}
		// A nil slice would be sent as SQL NULL and violate the NOT NULL column,
		// so an empty dependency list is always materialized explicitly.
		needs := job.Needs
		if needs == nil {
			needs = []string{}
		}
		var definition CIJob
		err = tx.QueryRow(ctx, `INSERT INTO ci_jobs (run_id,job_id,name,runs_on,needs,definition,state)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			RETURNING id::text,run_id::text,job_id,name,runs_on,needs,definition,state,current_attempt,created_at`,
			runID, job.JobID, job.Name, job.RunsOn, needs, job.Definition, state).
			Scan(&definition.ID, &definition.RunID, &definition.JobID, &definition.Name,
				&definition.RunsOn, &definition.Needs, &definition.Definition, &definition.State,
				&definition.CurrentAttempt, &definition.CreatedAt)
		if err != nil {
			return CIRun{}, nil, dbErr(err)
		}
		jobs = append(jobs, definition)
	}
	run, err := scanCIRun(tx.QueryRow(ctx, `SELECT `+ciRunColumns+` FROM ci_runs r WHERE r.id=$1`, runID))
	if err != nil {
		return CIRun{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CIRun{}, nil, dbErr(err)
	}
	return run, jobs, nil
}

// isUniqueViolation reports whether an error is a PostgreSQL unique violation.
func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

// GetCIRun loads one run by repository and number.
func (s *Store) GetCIRun(ctx context.Context, repoID string, number int64) (CIRun, error) {
	if !canonicalStoreUUID.MatchString(repoID) || number < 1 {
		return CIRun{}, invalid("run lookup")
	}
	return scanCIRun(s.Pool.QueryRow(ctx, `SELECT `+ciRunColumns+`
		FROM ci_runs r WHERE r.repo_id=$1 AND r.number=$2`, repoID, number))
}

// ListCIRuns returns runs newest first, optionally filtered by state.
func (s *Store) ListCIRuns(ctx context.Context, repoID, state string, page, perPage int) ([]CIRun, bool, error) {
	if !canonicalStoreUUID.MatchString(repoID) || page < 1 || perPage < 1 || perPage > 100 {
		return nil, false, invalid("pagination")
	}
	// The job count is computed in the same query so a list view does not have to
	// issue one query per run just to show how many jobs there are.
	query := `SELECT ` + ciRunColumns + `,
        (SELECT count(*) FROM ci_jobs j WHERE j.run_id=r.id)
        FROM ci_runs r WHERE r.repo_id=$1`
	args := []any{repoID}
	if state != "" && state != "all" {
		query += ` AND r.state=$2`
		args = append(args, state)
	}
	query += ` ORDER BY r.number DESC LIMIT ` + itoa(perPage+1) + ` OFFSET ` + itoa((page-1)*perPage)
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, false, dbErr(err)
	}
	defer rows.Close()
	runs := make([]CIRun, 0, perPage)
	for rows.Next() {
		run, err := scanCIRunWithJobs(rows)
		if err != nil {
			return nil, false, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, false, dbErr(err)
	}
	hasMore := len(runs) > perPage
	if hasMore {
		runs = runs[:perPage]
	}
	return runs, hasMore, nil
}

// itoa avoids pulling strconv into several call sites for a bounded integer.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 12)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// ListCIJobs returns every job of a run in declaration order.
func (s *Store) ListCIJobs(ctx context.Context, runID string) ([]CIJob, error) {
	if !canonicalStoreUUID.MatchString(runID) {
		return nil, invalid("run id")
	}
	rows, err := s.Pool.Query(ctx, `SELECT id::text,run_id::text,job_id,name,runs_on,needs,definition,
		state,current_attempt,created_at,started_at,finished_at
		FROM ci_jobs WHERE run_id=$1 ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	jobs := make([]CIJob, 0, 8)
	for rows.Next() {
		var job CIJob
		if err := rows.Scan(&job.ID, &job.RunID, &job.JobID, &job.Name, &job.RunsOn, &job.Needs,
			&job.Definition, &job.State, &job.CurrentAttempt, &job.CreatedAt, &job.StartedAt,
			&job.FinishedAt); err != nil {
			return nil, dbErr(err)
		}
		jobs = append(jobs, job)
	}
	return jobs, dbErr(rows.Err())
}

// ClaimedJob is the payload a runner receives when it claims work.
type ClaimedJob struct {
	Attempt CIJobAttempt `json:"attempt"`
	Job     CIJob        `json:"job"`
	Run     CIRun        `json:"run"`
}

// ClaimJob leases one queued job matching a runner's labels. It returns
// ErrNotFound when there is nothing to run, which the runner treats as "poll
// again later". The lease is exclusive: the row is locked while it is claimed,
// so two runners cannot take the same job.
func (s *Store) ClaimJob(ctx context.Context, runner CIRunner, lease time.Duration) (ClaimedJob, string, error) {
	if !canonicalStoreUUID.MatchString(runner.ID) || lease <= 0 {
		return ClaimedJob{}, "", invalid("claim request")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ClaimedJob{}, "", err
	}
	defer tx.Rollback(ctx)
	// A job is claimable when it is queued and every needed job succeeded. A
	// needed job that failed leaves the dependent job waiting forever, which the
	// run finalizer resolves.
	var jobPK string
	err = tx.QueryRow(ctx, `SELECT j.id::text FROM ci_jobs j
		JOIN ci_runs r ON r.id=j.run_id
		WHERE j.state='queued' AND r.state IN ('queued','running') AND j.runs_on = ANY($1)
		ORDER BY j.created_at, j.id
		FOR UPDATE OF j SKIP LOCKED
		LIMIT 1`, runner.Labels).Scan(&jobPK)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClaimedJob{}, "", ErrNotFound
	}
	if err != nil {
		return ClaimedJob{}, "", dbErr(err)
	}
	var attemptNumber int
	if err := tx.QueryRow(ctx, `UPDATE ci_jobs SET current_attempt=current_attempt+1
		WHERE id=$1 RETURNING current_attempt`, jobPK).Scan(&attemptNumber); err != nil {
		return ClaimedJob{}, "", dbErr(err)
	}
	secret, hash, err := NewRunnerSecret()
	if err != nil {
		return ClaimedJob{}, "", err
	}
	var attemptID string
	err = tx.QueryRow(ctx, `INSERT INTO ci_job_attempts
		(job_pk,attempt,runner_id,token_hash,state,signature,leased_at,expires_at)
		VALUES ($1,$2,$3,$4,'leased',$5,clock_timestamp(),clock_timestamp()+$6::interval)
		RETURNING id::text`,
		jobPK, attemptNumber, runner.ID, hash, attemptSignature(jobPK, attemptNumber, hash), durationInterval(lease)).Scan(&attemptID)
	if err != nil {
		return ClaimedJob{}, "", dbErr(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ci_jobs SET state='leased', started_at=COALESCE(started_at,clock_timestamp())
		WHERE id=$1`, jobPK); err != nil {
		return ClaimedJob{}, "", dbErr(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ci_runs SET state='running', started_at=COALESCE(started_at,clock_timestamp())
		WHERE id=(SELECT run_id FROM ci_jobs WHERE id=$1) AND state='queued'`, jobPK); err != nil {
		return ClaimedJob{}, "", dbErr(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ci_runners SET last_seen_at=clock_timestamp() WHERE id=$1`, runner.ID); err != nil {
		return ClaimedJob{}, "", dbErr(err)
	}
	claimed, err := loadClaimedJob(ctx, tx, attemptID, jobPK)
	if err != nil {
		return ClaimedJob{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return ClaimedJob{}, "", dbErr(err)
	}
	return claimed, secret, nil
}

// durationInterval renders a duration as a PostgreSQL interval literal. The value
// comes from server configuration, never from a client.
func durationInterval(value time.Duration) string {
	seconds := int64(value / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return itoa(int(seconds)) + " seconds"
}

// attemptSignature binds an attempt to its job and attempt number. A result that
// only carries a job ID therefore cannot be attributed to the current attempt,
// which is the fencing property the runner protocol relies on.
func attemptSignature(jobPK string, attempt int, tokenHash string) string {
	return HashRunnerSecret(jobPK + ":" + itoa(attempt) + ":" + tokenHash)[:32]
}

func loadClaimedJob(ctx context.Context, tx pgx.Tx, attemptID, jobPK string) (ClaimedJob, error) {
	var claimed ClaimedJob
	err := tx.QueryRow(ctx, `SELECT a.id::text,a.job_pk::text,a.attempt,a.runner_id::text,a.state,
		a.leased_at,a.expires_at,a.heartbeat_at
		FROM ci_job_attempts a WHERE a.id=$1`, attemptID).
		Scan(&claimed.Attempt.ID, &claimed.Attempt.JobPK, &claimed.Attempt.Attempt,
			&claimed.Attempt.RunnerID, &claimed.Attempt.State, &claimed.Attempt.LeasedAt,
			&claimed.Attempt.ExpiresAt, &claimed.Attempt.HeartbeatAt)
	if err != nil {
		return ClaimedJob{}, dbErr(err)
	}
	err = tx.QueryRow(ctx, `SELECT id::text,run_id::text,job_id,name,runs_on,needs,definition,
		state,current_attempt,created_at
		FROM ci_jobs WHERE id=$1`, jobPK).
		Scan(&claimed.Job.ID, &claimed.Job.RunID, &claimed.Job.JobID, &claimed.Job.Name,
			&claimed.Job.RunsOn, &claimed.Job.Needs, &claimed.Job.Definition, &claimed.Job.State,
			&claimed.Job.CurrentAttempt, &claimed.Job.CreatedAt)
	if err != nil {
		return ClaimedJob{}, dbErr(err)
	}
	claimed.Run, err = scanCIRun(tx.QueryRow(ctx, `SELECT `+ciRunColumns+` FROM ci_runs r WHERE r.id=$1`, claimed.Job.RunID))
	if err != nil {
		return ClaimedJob{}, err
	}
	return claimed, nil
}

// AttemptByToken resolves an attempt from its job token. Only a non-terminal
// attempt can be returned, so a finished attempt cannot be resumed or extended.
func (s *Store) AttemptByToken(ctx context.Context, secret string) (CIJobAttempt, error) {
	if secret == "" {
		return CIJobAttempt{}, ErrNotFound
	}
	var attempt CIJobAttempt
	err := s.Pool.QueryRow(ctx, `SELECT id::text,job_pk::text,attempt,runner_id::text,state,
		leased_at,expires_at,heartbeat_at,finished_at,exit_code,error_message
		FROM ci_job_attempts WHERE token_hash=$1`, HashRunnerSecret(secret)).
		Scan(&attempt.ID, &attempt.JobPK, &attempt.Attempt, &attempt.RunnerID, &attempt.State,
			&attempt.LeasedAt, &attempt.ExpiresAt, &attempt.HeartbeatAt, &attempt.FinishedAt,
			&attempt.ExitCode, &attempt.ErrorMessage)
	if err != nil {
		return CIJobAttempt{}, dbErr(err)
	}
	return attempt, nil
}

// AttemptContext resolves the run and repository an attempt belongs to. The
// archive endpoint uses it to prove the job token is entitled to exactly one
// repository and revision, instead of trusting a repository ID from the request.
func (s *Store) AttemptContext(ctx context.Context, attemptID string) (CIJobAttempt, CIRun, error) {
	if !canonicalStoreUUID.MatchString(attemptID) {
		return CIJobAttempt{}, CIRun{}, invalid("attempt id")
	}
	var attempt CIJobAttempt
	err := s.Pool.QueryRow(ctx, `SELECT id::text,job_pk::text,attempt,runner_id::text,state,
		leased_at,expires_at,heartbeat_at,finished_at,exit_code,error_message
		FROM ci_job_attempts WHERE id=$1`, attemptID).
		Scan(&attempt.ID, &attempt.JobPK, &attempt.Attempt, &attempt.RunnerID, &attempt.State,
			&attempt.LeasedAt, &attempt.ExpiresAt, &attempt.HeartbeatAt, &attempt.FinishedAt,
			&attempt.ExitCode, &attempt.ErrorMessage)
	if err != nil {
		return CIJobAttempt{}, CIRun{}, dbErr(err)
	}
	run, err := scanCIRun(s.Pool.QueryRow(ctx, `SELECT `+ciRunColumns+`
		FROM ci_runs r JOIN ci_jobs j ON j.run_id=r.id WHERE j.id=$1`, attempt.JobPK))
	if err != nil {
		return CIJobAttempt{}, CIRun{}, err
	}
	return attempt, run, nil
}
