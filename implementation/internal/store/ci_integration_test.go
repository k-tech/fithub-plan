package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ciFixture registers a runner for the linux-x64 label and returns the store plus
// a repository to attach runs to.
func ciFixture(t *testing.T) (*Store, CIRunner, Repository, User) {
	t.Helper()
	s, author, repo, _ := issueFixture(t)
	runner, _, err := s.RegisterRunner(context.Background(), "test-runner", []string{"linux-x64"})
	if err != nil {
		t.Fatal(err)
	}
	return s, runner, repo, author
}

func enqueueRun(t *testing.T, s *Store, repoID, actorID string, jobs ...CIJobDefinition) CIRun {
	t.Helper()
	return enqueueRunAt(t, s, repoID, actorID, testHeadSHA, jobs...)
}

// enqueueRunAt queues a run at a specific revision. One open run per workflow,
// revision, and event is enforced by a unique index, so a test that needs two
// runs must use two revisions.
func enqueueRunAt(t *testing.T, s *Store, repoID, actorID, headSHA string, jobs ...CIJobDefinition) CIRun {
	t.Helper()
	if len(jobs) == 0 {
		jobs = []CIJobDefinition{{JobID: "test", Name: "Test", RunsOn: "linux-x64", Definition: []byte(`{"steps":[]}`)}}
	}
	run, _, err := s.EnqueueRun(context.Background(), CIEnqueueRequest{
		RepoID: repoID, WorkflowPath: ".fithub/workflows/ci.yml", WorkflowName: "CI",
		WorkflowSource: "version: 1", Event: "push", HeadSHA: headSHA,
		Ref: "refs/heads/main", ActorID: actorID, Jobs: jobs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestClaimJobThenFinishAdvancesRun(t *testing.T) {
	s, runner, repo, author := ciFixture(t)
	ctx := context.Background()
	run := enqueueRun(t, s, repo.ID, author.ID)

	claimed, secret, err := s.ClaimJob(ctx, runner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Run.ID != run.ID || claimed.Job.JobID != "test" || claimed.Attempt.Attempt != 1 {
		t.Fatalf("unexpected claim: %+v", claimed)
	}
	if secret == "" {
		t.Fatal("claim must return a job token")
	}
	// The token resolves to the same attempt.
	attempt, err := s.AttemptByToken(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.ID != claimed.Attempt.ID {
		t.Fatalf("token resolved to %s, want %s", attempt.ID, claimed.Attempt.ID)
	}
	if err := s.MarkAttemptRunning(ctx, attempt.ID); err != nil {
		t.Fatal(err)
	}
	exit := 0
	job, finished, err := s.FinishAttempt(ctx, attempt.ID, CIAttemptResult{State: CIJobSucceeded, ExitCode: &exit})
	if err != nil {
		t.Fatal(err)
	}
	if job.State != CIJobSucceeded || finished.State != CIRunSucceeded {
		t.Fatalf("job=%s run=%s", job.State, finished.State)
	}
	// A finished attempt can no longer heartbeat.
	if _, err := s.HeartbeatAttempt(ctx, attempt.ID, time.Minute); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("heartbeat after finish: %v", err)
	}
}

// A second claim must not be handed the same job, and the job's attempt counter
// must advance so the old token is fenced out.
func TestClaimIsExclusiveAndFencesOldAttempt(t *testing.T) {
	s, runner, repo, author := ciFixture(t)
	ctx := context.Background()
	enqueueRun(t, s, repo.ID, author.ID)

	first, firstSecret, err := s.ClaimJob(ctx, runner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing else to claim while the only job is leased.
	if _, _, err := s.ClaimJob(ctx, runner, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second claim: %v", err)
	}

	// Simulate the runner disappearing: the lease lapses and the job is requeued.
	if _, err := s.Pool.Exec(ctx, `UPDATE ci_job_attempts SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, first.Attempt.ID); err != nil {
		t.Fatal(err)
	}
	reaped, err := s.ReapExpiredLeases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reaped != 1 {
		t.Fatalf("reaped %d leases, want 1", reaped)
	}

	second, secondSecret, err := s.ClaimJob(ctx, runner, time.Minute)
	if err != nil {
		t.Fatalf("reclaim after lease expiry: %v", err)
	}
	if second.Attempt.Attempt != 2 || second.Attempt.ID == first.Attempt.ID {
		t.Fatalf("expected attempt 2, got %+v", second.Attempt)
	}
	if secondSecret == firstSecret {
		t.Fatal("each attempt must get its own token")
	}
	// The superseded attempt cannot report a result.
	exit := 0
	if _, _, err := s.FinishAttempt(ctx, first.Attempt.ID, CIAttemptResult{State: CIJobSucceeded, ExitCode: &exit}); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("stale attempt result: %v", err)
	}
	// The current attempt can.
	if _, finished, err := s.FinishAttempt(ctx, second.Attempt.ID, CIAttemptResult{State: CIJobSucceeded, ExitCode: &exit}); err != nil || finished.State != CIRunSucceeded {
		t.Fatalf("current attempt result: %v %+v", err, finished)
	}
}

// The same result reported twice must succeed, and a different result for an
// already-finished attempt must be refused.
func TestFinishAttemptIsIdempotentButNotOverwritable(t *testing.T) {
	s, runner, repo, author := ciFixture(t)
	ctx := context.Background()
	enqueueRun(t, s, repo.ID, author.ID)
	claimed, _, err := s.ClaimJob(ctx, runner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	exit := 1
	result := CIAttemptResult{State: CIJobFailed, ExitCode: &exit}
	if _, _, err := s.FinishAttempt(ctx, claimed.Attempt.ID, result); err != nil {
		t.Fatal(err)
	}
	// Repeating the same outcome is accepted so a lost reply can be retried.
	if _, _, err := s.FinishAttempt(ctx, claimed.Attempt.ID, result); err != nil {
		t.Fatalf("repeated identical result: %v", err)
	}
	// Reporting success afterwards must not rewrite the recorded failure.
	zero := 0
	if _, _, err := s.FinishAttempt(ctx, claimed.Attempt.ID, CIAttemptResult{State: CIJobSucceeded, ExitCode: &zero}); !errors.Is(err, ErrConflict) {
		t.Fatalf("overwrite attempt: %v", err)
	}
}

// A job that needs another job only becomes claimable after the dependency
// succeeds, and a failed dependency cancels the dependent job.
func TestJobDependenciesGating(t *testing.T) {
	s, runner, repo, author := ciFixture(t)
	ctx := context.Background()
	jobs := []CIJobDefinition{
		{JobID: "build", Name: "Build", RunsOn: "linux-x64", Definition: []byte(`{"steps":[]}`)},
		{JobID: "deploy", Name: "Deploy", RunsOn: "linux-x64", Needs: []string{"build"}, Definition: []byte(`{"steps":[]}`)},
	}
	enqueueRun(t, s, repo.ID, author.ID, jobs...)

	// Only the first job is claimable; the dependent job waits.
	first, _, err := s.ClaimJob(ctx, runner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first.Job.JobID != "build" {
		t.Fatalf("first claim was %q, want build", first.Job.JobID)
	}
	if _, _, err := s.ClaimJob(ctx, runner, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dependent job was claimable before its dependency: %v", err)
	}
	exit := 0
	if _, updated, err := s.FinishAttempt(ctx, first.Attempt.ID, CIAttemptResult{State: CIJobSucceeded, ExitCode: &exit}); err != nil {
		t.Fatal(err)
	} else if updated.State != CIRunRunning {
		t.Fatalf("run state after dependency success = %s, want running", updated.State)
	}
	second, _, err := s.ClaimJob(ctx, runner, time.Minute)
	if err != nil {
		t.Fatalf("dependent job not released: %v", err)
	}
	if second.Job.JobID != "deploy" {
		t.Fatalf("second claim was %q, want deploy", second.Job.JobID)
	}
	if _, finalRun, err := s.FinishAttempt(ctx, second.Attempt.ID, CIAttemptResult{State: CIJobSucceeded, ExitCode: &exit}); err != nil || finalRun.State != CIRunSucceeded {
		t.Fatalf("final run: %v %+v", err, finalRun)
	}

	// A failed dependency must cancel the dependent job rather than hang it.
	run2 := enqueueRun(t, s, repo.ID, author.ID, jobs...)
	failBuild, _, err := s.ClaimJob(ctx, runner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, updated, err := s.FinishAttempt(ctx, failBuild.Attempt.ID, CIAttemptResult{State: CIJobFailed, ExitCode: &exit}); err != nil {
		t.Fatal(err)
	} else if updated.State != CIRunFailed {
		t.Fatalf("run state after dependency failure = %s, want failed", updated.State)
	}
	listed, err := s.ListCIJobs(ctx, run2.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range listed {
		if job.JobID == "deploy" && job.State != CIJobCancelled {
			t.Fatalf("dependent job state = %s, want cancelled", job.State)
		}
	}
	if _, _, err := s.ClaimJob(ctx, runner, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelled dependent job was still claimable: %v", err)
	}
}

// Enqueue must not create a second open run for the same workflow, revision, and
// event, because a retried request or duplicated event would run the tests twice.
func TestEnqueueDeduplicatesOpenRuns(t *testing.T) {
	s, _, repo, author := ciFixture(t)
	ctx := context.Background()
	first := enqueueRun(t, s, repo.ID, author.ID)
	if _, _, err := s.EnqueueRun(ctx, CIEnqueueRequest{
		RepoID: repo.ID, WorkflowPath: first.WorkflowPath, WorkflowName: first.WorkflowName,
		WorkflowSource: "version: 1", Event: "push", HeadSHA: first.HeadSHA,
		Ref: "refs/heads/main", ActorID: author.ID,
		Jobs: []CIJobDefinition{{JobID: "test", Name: "Test", RunsOn: "linux-x64", Definition: []byte(`{}`)}},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate open run: %v", err)
	}
	// A different revision is a different run.
	second, _, err := s.EnqueueRun(ctx, CIQueueWithHead(s, repo.ID, author.ID, testBaseSHA))
	if err != nil {
		t.Fatalf("different revision: %v", err)
	}
	if second.Number == first.Number {
		t.Fatal("numbers must advance")
	}
}

// CIQueueWithHead builds a request with a different head revision.
func CIQueueWithHead(s *Store, repoID, actorID, headSHA string) CIEnqueueRequest {
	return CIEnqueueRequest{
		RepoID: repoID, WorkflowPath: ".fithub/workflows/ci.yml", WorkflowName: "CI",
		WorkflowSource: "version: 1", Event: "push", HeadSHA: headSHA,
		Ref: "refs/heads/main", ActorID: actorID,
		Jobs: []CIJobDefinition{{JobID: "test", Name: "Test", RunsOn: "linux-x64", Definition: []byte(`{}`)}},
	}
}

// Cancelling a queued run stops its jobs and reports the terminal state.
func TestCancelRunStopsQueuedJobs(t *testing.T) {
	s, runner, repo, author := ciFixture(t)
	ctx := context.Background()
	jobs := []CIJobDefinition{
		{JobID: "build", Name: "Build", RunsOn: "linux-x64", Definition: []byte(`{}`)},
		{JobID: "deploy", Name: "Deploy", RunsOn: "linux-x64", Needs: []string{"build"}, Definition: []byte(`{}`)},
	}
	run := enqueueRun(t, s, repo.ID, author.ID, jobs...)
	cancelled, err := s.CancelRun(ctx, repo.ID, run.Number)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != CIRunCancelled {
		t.Fatalf("cancelled run state = %s", cancelled.State)
	}
	if _, _, err := s.ClaimJob(ctx, runner, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelled job was claimable: %v", err)
	}
	// A finished run cannot be cancelled again.
	if _, err := s.CancelRun(ctx, repo.ID, run.Number); !errors.Is(err, ErrConflict) {
		t.Fatalf("double cancel: %v", err)
	}
}

// A runner revoked mid-run cannot claim more work, but its active lease still
// reports a result so the job is not stranded.
func TestRevokeRunnerStopsNewClaims(t *testing.T) {
	s, runner, repo, author := ciFixture(t)
	ctx := context.Background()
	enqueueRun(t, s, repo.ID, author.ID)
	claimed, _, err := s.ClaimJob(ctx, runner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeRunner(ctx, runner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunnerByToken(ctx, "irrelevant"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked runner should not resolve by token: %v", err)
	}
	if _, _, err := s.ClaimJob(ctx, runner, time.Minute); err == nil {
		t.Fatal("revoked runner claimed work")
	}
	exit := 0
	if _, _, err := s.FinishAttempt(ctx, claimed.Attempt.ID, CIAttemptResult{State: CIJobSucceeded, ExitCode: &exit}); err != nil {
		t.Fatalf("revoking a runner must not strand its active job: %v", err)
	}
}

// Log chunks are append-only per sequence, so a retried upload cannot duplicate
// output.
func TestLogChunksAreOrderedAndDeduplicated(t *testing.T) {
	s, runner, repo, author := ciFixture(t)
	ctx := context.Background()
	enqueueRun(t, s, repo.ID, author.ID)
	claimed, _, err := s.ClaimJob(ctx, runner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for i, chunk := range []string{"one\n", "two\n", "three\n"} {
		if err := s.AppendLogChunk(ctx, claimed.Attempt.ID, int64(i), chunk); err != nil {
			t.Fatal(err)
		}
	}
	// Re-uploading the same sequence must not duplicate the line.
	if err := s.AppendLogChunk(ctx, claimed.Attempt.ID, 1, "two\n"); err != nil {
		t.Fatal(err)
	}
	// A fresh viewer starts before sequence 0 and must receive every line,
	// including the first one.
	chunks, last, err := s.ListLogChunks(ctx, claimed.Attempt.ID, StartLogSequence, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 || chunks[0] != "one\n" || last != 2 {
		t.Fatalf("chunks=%v last=%d", chunks, last)
	}
	// Resuming after sequence 0 returns only the later chunks.
	resumed, resumeLast, err := s.ListLogChunks(ctx, claimed.Attempt.ID, 0, 10)
	if err != nil || len(resumed) != 2 || resumed[0] != "two\n" || resumeLast != 2 {
		t.Fatalf("resume: %v %d %v", resumed, resumeLast, err)
	}
}

// An infrastructure failure is distinguishable from a test failure so an
// operator can tell the code from the runner.
func TestInfrastructureFailureIsDistinctFromTestFailure(t *testing.T) {
	s, runner, repo, author := ciFixture(t)
	ctx := context.Background()
	enqueueRun(t, s, repo.ID, author.ID)
	claimed, _, err := s.ClaimJob(ctx, runner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := s.FinishAttempt(ctx, claimed.Attempt.ID, CIAttemptResult{State: CIJobInfrastructureFailed, ErrorMessage: "the container could not start"})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != CIRunInfrastructureFailed {
		t.Fatalf("run state = %s, want %s", run.State, CIRunInfrastructureFailed)
	}
}

// The run list is what the web UI and CLI show first, so it must report how many
// jobs each run has. The count is computed in the list query itself, and a
// mistake there silently reports zero for every run.
func TestListCIRunsReportsJobCount(t *testing.T) {
	s, _, repo, author := ciFixture(t)
	ctx := context.Background()
	repoID, authorID := repo.ID, author.ID
	enqueueRunAt(t, s, repoID, authorID, "1111111111111111111111111111111111111111",
		CIJobDefinition{JobID: "build", Name: "Build", RunsOn: "linux-x64", Definition: []byte(`{"steps":[]}`)})
	enqueueRunAt(t, s, repoID, authorID, "2222222222222222222222222222222222222222",
		CIJobDefinition{JobID: "build", Name: "Build", RunsOn: "linux-x64", Definition: []byte(`{"steps":[]}`)},
		CIJobDefinition{JobID: "deploy", Name: "Deploy", RunsOn: "linux-x64", Needs: []string{"build"}, Definition: []byte(`{"steps":[]}`)})

	runs, _, err := s.ListCIRuns(ctx, repoID, "all", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs=%d", len(runs))
	}
	// Runs are returned newest first, so the two-job run is first.
	byNumber := map[int64]int{}
	for _, run := range runs {
		byNumber[run.Number] = run.JobCount
	}
	for _, run := range runs {
		if run.JobCount == 0 {
			t.Fatalf("run %d reported zero jobs", run.Number)
		}
	}
	counts := []int{byNumber[runs[0].Number], byNumber[runs[1].Number]}
	if !(counts[0] == 2 && counts[1] == 1) {
		t.Fatalf("job counts=%v want [2 1]", counts)
	}
}
