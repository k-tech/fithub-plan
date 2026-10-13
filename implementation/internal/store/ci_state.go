package store

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// terminalAttemptStates are the attempt states after which no further result may
// be reported.
var terminalAttemptStates = map[string]bool{
	CIJobSucceeded:            true,
	CIJobFailed:               true,
	CIJobCancelled:            true,
	CIJobTimedOut:             true,
	CIJobInfrastructureFailed: true,
}

// HeartbeatAttempt extends a lease. It only succeeds while the attempt is still
// the current one and has not finished, so a cancelled or superseded attempt
// learns immediately that it must stop.
func (s *Store) HeartbeatAttempt(ctx context.Context, attemptID string, lease time.Duration) (time.Time, error) {
	if !canonicalStoreUUID.MatchString(attemptID) || lease <= 0 {
		return time.Time{}, invalid("heartbeat request")
	}
	var expiresAt time.Time
	err := s.Pool.QueryRow(ctx, `UPDATE ci_job_attempts a
		SET heartbeat_at=clock_timestamp(), expires_at=clock_timestamp()+$2::interval
		WHERE a.id=$1 AND a.state IN ('leased','running')
		  AND EXISTS (SELECT 1 FROM ci_jobs j WHERE j.id=a.job_pk AND j.current_attempt=a.attempt)
		RETURNING a.expires_at`, attemptID, durationInterval(lease)).Scan(&expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrLeaseExpired
	}
	if err != nil {
		return time.Time{}, dbErr(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE ci_runners SET last_seen_at=clock_timestamp()
		WHERE id=(SELECT runner_id FROM ci_job_attempts WHERE id=$1)`, attemptID); err != nil {
		return time.Time{}, dbErr(err)
	}
	return expiresAt, nil
}

// MarkAttemptRunning records that the runner started executing the job. It is
// idempotent so a retried notification after a network failure is harmless.
func (s *Store) MarkAttemptRunning(ctx context.Context, attemptID string) error {
	if !canonicalStoreUUID.MatchString(attemptID) {
		return invalid("attempt id")
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE ci_job_attempts a SET state='running'
		WHERE a.id=$1 AND a.state='leased'
		  AND EXISTS (SELECT 1 FROM ci_jobs j WHERE j.id=a.job_pk AND j.current_attempt=a.attempt)`, attemptID)
	if err != nil {
		return dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		// Already running is not an error; anything else means the lease is gone.
		var state string
		err := s.Pool.QueryRow(ctx, `SELECT state FROM ci_job_attempts WHERE id=$1`, attemptID).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return dbErr(err)
		}
		if state == CIAttemptRunning {
			return nil
		}
		return ErrLeaseExpired
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE ci_jobs SET state='running'
		WHERE id=(SELECT job_pk FROM ci_job_attempts WHERE id=$1) AND state='leased'`, attemptID); err != nil {
		return dbErr(err)
	}
	return nil
}

// CIAttemptResult is the outcome a runner reports for an attempt.
type CIAttemptResult struct {
	State        string
	ExitCode     *int
	ErrorMessage string
}

// FinishAttempt records a terminal result and advances the run. A result from an
// attempt that is no longer current, or that already finished with a different
// outcome, is refused so a late or duplicated callback cannot overwrite the
// recorded result.
func (s *Store) FinishAttempt(ctx context.Context, attemptID string, result CIAttemptResult) (CIJob, CIRun, error) {
	if !canonicalStoreUUID.MatchString(attemptID) || !terminalAttemptStates[result.State] ||
		len(result.ErrorMessage) > 4096 || !utf8.ValidString(result.ErrorMessage) {
		return CIJob{}, CIRun{}, invalid("attempt result")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return CIJob{}, CIRun{}, err
	}
	defer tx.Rollback(ctx)
	var jobPK, state string
	var attempt int
	err = tx.QueryRow(ctx, `SELECT job_pk::text,state,attempt FROM ci_job_attempts
		WHERE id=$1 FOR UPDATE`, attemptID).Scan(&jobPK, &state, &attempt)
	if err != nil {
		return CIJob{}, CIRun{}, dbErr(err)
	}
	// Fencing comes first: if a newer attempt owns this job, this result belongs
	// to a lease that no longer exists and must be refused regardless of what it
	// claims. Checking the terminal state first would let a reaped attempt report
	// a different outcome than the one recorded for it.
	var current int
	if err := tx.QueryRow(ctx, `SELECT current_attempt FROM ci_jobs WHERE id=$1`, jobPK).Scan(&current); err != nil {
		return CIJob{}, CIRun{}, dbErr(err)
	}
	if current != attempt {
		return CIJob{}, CIRun{}, ErrLeaseExpired
	}
	if terminalAttemptStates[state] {
		// A repeated identical report is accepted so a retry after a lost reply
		// succeeds; a different outcome is refused.
		if state != result.State {
			return CIJob{}, CIRun{}, ErrConflict
		}
	} else {
		var exitCode any
		if result.ExitCode != nil {
			exitCode = *result.ExitCode
		}
		var message any
		if result.ErrorMessage != "" {
			message = result.ErrorMessage
		}
		if _, err := tx.Exec(ctx, `UPDATE ci_job_attempts
			SET state=$2, finished_at=clock_timestamp(), exit_code=$3, error_message=$4
			WHERE id=$1`, attemptID, result.State, exitCode, message); err != nil {
			return CIJob{}, CIRun{}, dbErr(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE ci_jobs SET state=$2, finished_at=clock_timestamp()
			WHERE id=$1`, jobPK, result.State); err != nil {
			return CIJob{}, CIRun{}, dbErr(err)
		}
		if err := advanceDependents(ctx, tx, jobPK); err != nil {
			return CIJob{}, CIRun{}, err
		}
	}
	job, run, err := finalizeRun(ctx, tx, jobPK)
	if err != nil {
		return CIJob{}, CIRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CIJob{}, CIRun{}, dbErr(err)
	}
	return job, run, nil
}

// advanceDependents releases jobs whose dependencies are now all satisfied. A
// dependent of a terminal non-success state is failed too, because it can never
// run and leaving it waiting would hang the run.
func advanceDependents(ctx context.Context, tx pgx.Tx, jobPK string) error {
	var runID, jobID string
	if err := tx.QueryRow(ctx, `SELECT run_id::text,job_id FROM ci_jobs WHERE id=$1`, jobPK).Scan(&runID, &jobID); err != nil {
		return dbErr(err)
	}
	rows, err := tx.Query(ctx, `SELECT id::text,needs FROM ci_jobs
		WHERE run_id=$1 AND state='waiting' AND $2 = ANY(needs) FOR UPDATE`, runID, jobID)
	if err != nil {
		return dbErr(err)
	}
	type waiting struct {
		pk    string
		needs []string
	}
	pending := make([]waiting, 0, 4)
	for rows.Next() {
		var item waiting
		if err := rows.Scan(&item.pk, &item.needs); err != nil {
			rows.Close()
			return dbErr(err)
		}
		pending = append(pending, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return dbErr(err)
	}
	for _, item := range pending {
		// Depends on at least one non-success job?
		var blocked bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci_jobs
			WHERE run_id=$1 AND job_id = ANY($2) AND state IN ('failed','cancelled','timed_out','infrastructure_failed'))`,
			runID, item.needs).Scan(&blocked)
		if err != nil {
			return dbErr(err)
		}
		if blocked {
			if _, err := tx.Exec(ctx, `UPDATE ci_jobs
				SET state='cancelled', finished_at=clock_timestamp() WHERE id=$1`, item.pk); err != nil {
				return dbErr(err)
			}
			continue
		}
		var unsatisfied bool
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM unnest($2::text[]) AS need
			WHERE NOT EXISTS (SELECT 1 FROM ci_jobs j2
				WHERE j2.run_id=$1 AND j2.job_id=need AND j2.state='succeeded'))`, runID, item.needs).Scan(&unsatisfied)
		if err != nil {
			return dbErr(err)
		}
		if !unsatisfied {
			if _, err := tx.Exec(ctx, `UPDATE ci_jobs SET state='queued' WHERE id=$1`, item.pk); err != nil {
				return dbErr(err)
			}
		}
	}
	return nil
}

// finalizeRun derives the run's state from its jobs. A run is finished when no
// job is queued, waiting, leased, or running.
func finalizeRun(ctx context.Context, tx pgx.Tx, jobPK string) (CIJob, CIRun, error) {
	var runID string
	if err := tx.QueryRow(ctx, `SELECT run_id::text FROM ci_jobs WHERE id=$1`, jobPK).Scan(&runID); err != nil {
		return CIJob{}, CIRun{}, dbErr(err)
	}
	var blocked, failed, cancelled, timedOut, infrastructure bool
	err := tx.QueryRow(ctx, `SELECT
		COUNT(*) FILTER (WHERE state IN ('queued','waiting','leased','running')) > 0,
		COUNT(*) FILTER (WHERE state='failed') > 0,
		COUNT(*) FILTER (WHERE state='cancelled') > 0,
		COUNT(*) FILTER (WHERE state='timed_out') > 0,
		COUNT(*) FILTER (WHERE state='infrastructure_failed') > 0
		FROM ci_jobs WHERE run_id=$1`, runID).
		Scan(&blocked, &failed, &cancelled, &timedOut, &infrastructure)
	if err != nil {
		return CIJob{}, CIRun{}, dbErr(err)
	}
	if !blocked {
		// An infrastructure failure is reported separately from a test failure so
		// an operator can tell "the code is broken" from "the runner broke".
		state, reason := CIRunSucceeded, "all jobs succeeded"
		switch {
		case infrastructure:
			state, reason = CIRunInfrastructureFailed, "a job could not run to completion"
		case timedOut:
			state, reason = CIRunTimedOut, "a job exceeded its time limit"
		case failed:
			// A failure outranks the cancellation of its dependents: those jobs were
			// cancelled because an earlier job failed, so the run did not run to
			// completion either way and the failure is the actionable cause.
			state, reason = CIRunFailed, "a job failed"
		case cancelled:
			state, reason = CIRunCancelled, "a job was cancelled"
		}
		if _, err := tx.Exec(ctx, `UPDATE ci_runs
			SET state=$2, finished_at=clock_timestamp(), conclusion_reason=$3
			WHERE id=$1 AND finished_at IS NULL`, runID, state, reason); err != nil {
			return CIJob{}, CIRun{}, dbErr(err)
		}
	}
	var job CIJob
	err = tx.QueryRow(ctx, `SELECT id::text,run_id::text,job_id,name,runs_on,needs,definition,
		state,current_attempt,created_at,started_at,finished_at
		FROM ci_jobs WHERE id=$1`, jobPK).
		Scan(&job.ID, &job.RunID, &job.JobID, &job.Name, &job.RunsOn, &job.Needs,
			&job.Definition, &job.State, &job.CurrentAttempt, &job.CreatedAt, &job.StartedAt, &job.FinishedAt)
	if err != nil {
		return CIJob{}, CIRun{}, dbErr(err)
	}
	run, err := scanCIRun(tx.QueryRow(ctx, `SELECT `+ciRunColumns+` FROM ci_runs r WHERE r.id=$1`, runID))
	if err != nil {
		return CIJob{}, CIRun{}, err
	}
	return job, run, nil
}

// CancelRun stops every job of a run that has not started and marks the run
// cancelled. A running job is asked to stop through its cancellation flag, which
// the runner reads on heartbeat; the result stays authoritative from the runner.
func (s *Store) CancelRun(ctx context.Context, repoID string, number int64) (CIRun, error) {
	if !canonicalStoreUUID.MatchString(repoID) || number < 1 {
		return CIRun{}, invalid("cancel request")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return CIRun{}, err
	}
	defer tx.Rollback(ctx)
	var runID, state string
	err = tx.QueryRow(ctx, `SELECT id::text,state FROM ci_runs
		WHERE repo_id=$1 AND number=$2 FOR UPDATE`, repoID, number).Scan(&runID, &state)
	if err != nil {
		return CIRun{}, dbErr(err)
	}
	if state != CIRunQueued && state != CIRunRunning {
		return CIRun{}, ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE ci_jobs SET state='cancelled', finished_at=clock_timestamp()
		WHERE run_id=$1 AND state IN ('queued','waiting')`, runID); err != nil {
		return CIRun{}, dbErr(err)
	}
	// A leased or running job is marked so the runner stops on its next
	// heartbeat; FinishAttempt will then record the cancellation.
	if _, err := tx.Exec(ctx, `UPDATE ci_job_attempts SET state='cancelled'
		WHERE state IN ('leased','running')
		  AND job_pk IN (SELECT id FROM ci_jobs WHERE run_id=$1)`, runID); err != nil {
		return CIRun{}, dbErr(err)
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE state IN ('queued','waiting','leased','running')) > 0
		FROM ci_jobs WHERE run_id=$1`, runID).Scan(&blocked); err != nil {
		return CIRun{}, dbErr(err)
	}
	if !blocked {
		if _, err := tx.Exec(ctx, `UPDATE ci_runs SET state='cancelled', finished_at=clock_timestamp(),
			conclusion_reason='cancelled by a user' WHERE id=$1`, runID); err != nil {
			return CIRun{}, dbErr(err)
		}
	} else if _, err := tx.Exec(ctx, `UPDATE ci_runs SET conclusion_reason='cancellation requested'
		WHERE id=$1`, runID); err != nil {
		return CIRun{}, dbErr(err)
	}
	run, err := scanCIRun(tx.QueryRow(ctx, `SELECT `+ciRunColumns+` FROM ci_runs r WHERE r.id=$1`, runID))
	if err != nil {
		return CIRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CIRun{}, dbErr(err)
	}
	return run, nil
}

// ReapExpiredLeases marks attempts whose lease lapsed as infrastructure failures
// and requeues their jobs. It returns the number of jobs requeued so the caller
// can log the event. Only an attempt that is still current is reaped, so a
// superseded attempt cannot cause a spurious requeue.
func (s *Store) ReapExpiredLeases(ctx context.Context) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT a.id::text,a.job_pk::text FROM ci_job_attempts a
		JOIN ci_jobs j ON j.id=a.job_pk
		JOIN ci_runs r ON r.id=j.run_id
		WHERE a.state IN ('leased','running') AND a.expires_at < clock_timestamp()
		  AND j.current_attempt=a.attempt AND r.state IN ('queued','running')
		FOR UPDATE OF a SKIP LOCKED LIMIT 50`)
	if err != nil {
		return 0, dbErr(err)
	}
	type expired struct{ attemptID, jobPK string }
	var list []expired
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.attemptID, &item.jobPK); err != nil {
			rows.Close()
			return 0, dbErr(err)
		}
		list = append(list, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, dbErr(err)
	}
	for _, item := range list {
		if _, err := tx.Exec(ctx, `UPDATE ci_job_attempts
			SET state='infrastructure_failed', finished_at=clock_timestamp(),
			    error_message='the runner stopped responding'
			WHERE id=$1`, item.attemptID); err != nil {
			return 0, dbErr(err)
		}
		// The job returns to the queue so another runner can take it; the run
		// itself is not failed, because the work is still expected to complete.
		if _, err := tx.Exec(ctx, `UPDATE ci_jobs SET state='queued'
			WHERE id=$1 AND current_attempt=(SELECT attempt FROM ci_job_attempts WHERE id=$2)`,
			item.jobPK, item.attemptID); err != nil {
			return 0, dbErr(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, dbErr(err)
	}
	return len(list), nil
}

// AppendLogChunk stores one log chunk for an attempt. The sequence must continue
// from the previous chunk so a retried upload cannot duplicate or reorder output.
func (s *Store) AppendLogChunk(ctx context.Context, attemptID string, sequence int64, content string) error {
	if !canonicalStoreUUID.MatchString(attemptID) || sequence < 0 ||
		len(content) > 65536 || !utf8.ValidString(content) {
		return invalid("log chunk")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO ci_log_chunks (attempt_id,sequence,content)
		SELECT $1,$2,$3 FROM ci_job_attempts a WHERE a.id=$1
		ON CONFLICT (attempt_id,sequence) DO NOTHING`, attemptID, sequence, content)
	return dbErr(err)
}

// ListLogChunks returns chunks after a sequence, bounded so a log viewer cannot
// pull an unbounded amount in one request. after is the last sequence the client
// already has; pass StartLogSequence for a fresh viewer. The filter is strictly
// greater than after, so sequence 0 is included when a client starts from the
// beginning rather than being silently skipped.
func (s *Store) ListLogChunks(ctx context.Context, attemptID string, after int64, limit int) ([]string, int64, error) {
	if !canonicalStoreUUID.MatchString(attemptID) || after < StartLogSequence || limit < 1 || limit > 1000 {
		return nil, 0, invalid("log query")
	}
	rows, err := s.Pool.Query(ctx, `SELECT sequence,content FROM ci_log_chunks
		WHERE attempt_id=$1 AND sequence > $2 ORDER BY sequence LIMIT $3`, attemptID, after, limit)
	if err != nil {
		return nil, 0, dbErr(err)
	}
	defer rows.Close()
	chunks := make([]string, 0, limit)
	last := after
	for rows.Next() {
		var sequence int64
		var content string
		if err := rows.Scan(&sequence, &content); err != nil {
			return nil, 0, dbErr(err)
		}
		chunks = append(chunks, content)
		last = sequence
	}
	return chunks, last, dbErr(rows.Err())
}
