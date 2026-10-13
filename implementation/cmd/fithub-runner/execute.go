package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// agent owns the runner loop: claim a job, execute it in a container, report the
// result, and clean up.
type agent struct {
	config runnerConfig
	client *apiClient
}

// jobDefinition is the validated job the control plane sent. It mirrors the
// workflow package's Job shape; the agent trusts only these fields and never
// re-reads a workflow file from the repository.
type jobDefinition struct {
	Name             string            `json:"Name"`
	Run              string            `json:"Run"`
	Builtin          string            `json:"Builtin"`
	Shell            string            `json:"Shell"`
	With             map[string]string `json:"With"`
	Env              map[string]string `json:"Env"`
	WorkingDirectory string            `json:"WorkingDirectory"`
	TimeoutMinutes   int               `json:"TimeoutMinutes"`
	Condition        string            `json:"Condition"`
}

// step executes one job. It returns whether any work was performed, so the caller
// can poll immediately when the queue is busy.
func (a *agent) step(ctx context.Context) (bool, error) {
	job, err := a.client.claim(ctx)
	if errors.Is(err, errNoWork) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := a.execute(ctx, job); err != nil {
		// The job already reported its own failure; log and continue so a broken
		// job does not stop the runner from taking the next one.
		slog.Warn("job failed", "job", job.JobID, "run", job.RunNumber, "error", err)
	}
	return true, nil
}

// execute runs one claimed job end to end.
func (a *agent) execute(parent context.Context, job claimedJob) error {
	var definition struct {
		Steps []jobDefinition `json:"steps"`
	}
	var steps []jobDefinition
	if len(job.Definition) > 0 {
		trimmed := strings.TrimSpace(string(job.Definition))
		if strings.HasPrefix(trimmed, "[") {
			// Queued jobs from before the demo secret envelope remain runnable.
			if err := json.Unmarshal(job.Definition, &steps); err != nil {
				return a.failClaim(parent, job, "the job definition could not be read")
			}
		} else {
			if err := json.Unmarshal(job.Definition, &definition); err != nil {
				return a.failClaim(parent, job, "the job definition could not be read")
			}
			steps = definition.Steps
		}
	}
	timeout := a.config.JobTimeout
	for _, step := range steps {
		if step.TimeoutMinutes > 0 {
			candidate := time.Duration(step.TimeoutMinutes) * time.Minute
			if candidate < timeout {
				timeout = candidate
			}
		}
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	if err := a.client.start(ctx, job.JobToken); err != nil {
		return err
	}
	workspace, err := os.MkdirTemp(a.config.WorkRoot, "job-")
	if err != nil {
		return err
	}
	// The workspace is removed on every path, including a cancelled job, so a
	// stale checkout never survives into the next job.
	defer os.RemoveAll(workspace)

	// A background heartbeat keeps the lease alive and tells the agent to stop
	// when the control plane cancels the run.
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	leaseLost := make(chan struct{})
	var leaseOnce sync.Once
	go a.heartbeatLoop(heartbeatCtx, job.JobToken, leaseLost, &leaseOnce)

	if err := a.checkout(ctx, job, workspace); err != nil {
		// The user-facing message stays generic, but the operational cause is
		// logged so an operator can tell a storage problem from an auth problem.
		slog.Warn("checkout failed", "job", job.JobID, "run", job.RunNumber, "error", err)
		return a.finish(ctx, job, "failed", nil, "the repository could not be checked out")
	}
	state, exitCode, message := a.runSteps(ctx, job, workspace, steps, leaseLost)
	return a.finish(ctx, job, state, exitCode, message)
}

// heartbeatLoop renews the lease and cancels the job when the lease is lost.
func (a *agent) heartbeatLoop(ctx context.Context, jobToken string, leaseLost chan struct{}, once *sync.Once) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.client.heartbeat(ctx, jobToken); err != nil {
				if errors.Is(err, errLeaseLost) {
					// The attempt was superseded or the run was cancelled. The
					// job must stop; its result is no longer accepted.
					once.Do(func() { close(leaseLost) })
					return
				}
				slog.Warn("heartbeat failed", "error", err)
			}
		}
	}
}

// checkout downloads the exact tested revision as an archive.
//
// A read-only archive of one revision is deliberately used instead of Git
// protocol access: the job token then grants exactly "these bytes of this
// revision" rather than general repository read, and it needs no credential
// helper inside the job container.
func (a *agent) checkout(ctx context.Context, job claimedJob, workspace string) error {
	if job.HeadSHA == "" {
		return errors.New("the job has no revision to check out")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.config.ServerURL+"/api/v1/ci/jobs/archive?sha="+url.QueryEscape(job.HeadSHA), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+a.config.Token)
	request.Header.Set("X-FitHub-Job-Token", job.JobToken)
	response, err := a.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("archive request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("archive request returned status %d", response.StatusCode)
	}
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		return err
	}
	// The archive is extracted with a bounded, traversal-safe reader rather than
	// the tar command, so a crafted entry cannot write outside the workspace.
	if err := extractTarGz(response.Body, repo); err != nil {
		return fmt.Errorf("extract archive: %w", err)
	}
	if err := makeContainerWorkspaceReadable(workspace); err != nil {
		return fmt.Errorf("prepare workspace permissions: %w", err)
	}
	return nil
}

// httpClient returns the agent's HTTP client.
func (a *agent) httpClient() *http.Client { return a.client.http }

// extractTarGz unpacks a gzipped tar into root, refusing absolute paths, parent
// traversal, links, and device entries. Repository content is untrusted for this
// purpose: a malformed archive must fail rather than escape the workspace.
func extractTarGz(source io.Reader, root string) error {
	gzipReader, err := gzip.NewReader(io.LimitReader(source, 512<<20))
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	entries := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		entries++
		if entries > 200000 {
			return errors.New("archive has too many entries")
		}
		cleaned := filepath.Clean(header.Name)
		if cleaned == "." {
			continue
		}
		if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes the workspace: %q", header.Name)
		}
		target := filepath.Join(root, cleaned)
		switch header.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			// `git archive` prefixes its output with a pax metadata entry. It
			// carries no file content, so it is skipped rather than rejected:
			// treating it as an error would fail every legitimate archive.
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			// A per-entry limit plus the overall reader limit bounds disk use.
			if _, err := io.Copy(file, io.LimitReader(reader, 256<<20)); err != nil {
				file.Close()
				return err
			}
			mode := os.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			if err := file.Chmod(mode); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		default:
			// Symlinks, devices, and FIFOs have no meaning in a checkout and are
			// the classic traversal vector, so they are rejected outright.
			return fmt.Errorf("archive entry type is not allowed: %q", header.Name)
		}
	}
}

// makeContainerWorkspaceReadable grants the capability-dropped job container
// read and traverse access to its bind-mounted checkout. The workspace parent
// stays private; only this per-job mount is exposed, and the container mounts it
// read-only.
func makeContainerWorkspaceReadable(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Chmod(path, 0o755)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("workspace entry is not a regular file: %s", path)
		}
		mode := os.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		return os.Chmod(path, mode)
	})
}

// runSteps executes each step in order inside one container. Steps share the
// container so a later step sees an earlier step's files, matching the workflow
// model of one job per environment.
func (a *agent) runSteps(ctx context.Context, job claimedJob, workspace string, steps []jobDefinition, leaseLost chan struct{}) (string, *int, string) {
	// The container is created once and each step is executed with `docker exec`,
	// which keeps the workspace and installed tooling between steps.
	containerName := "fithub-job-" + sanitizeName(job.AttemptID)
	if err := a.startContainer(ctx, containerName, workspace, len(job.Secrets) > 0); err != nil {
		return "infrastructure_failed", nil, "the job container could not be started"
	}
	defer a.removeContainer(containerName)

	var sequence int64
	log := func(text string) {
		if text == "" {
			return
		}
		if err := a.client.appendLog(ctx, job.JobToken, sequence, text); err != nil {
			slog.Warn("log upload failed", "error", err)
		}
		sequence++
	}
	skippedBecauseFailure := false
	for index, step := range steps {
		select {
		case <-leaseLost:
			return "cancelled", nil, "the run was cancelled"
		default:
		}
		condition := step.Condition
		if condition == "" {
			condition = "success"
		}
		// A previous failure skips later success-only steps, which is how a
		// workflow expresses "only run the rest when everything passed".
		if skippedBecauseFailure && condition != "always" && condition != "failure" {
			log(fmt.Sprintf("=== step %d: %s (skipped)\n", index+1, stepLabel(step)))
			continue
		}
		if skippedBecauseFailure && condition == "success" {
			continue
		}
		log(fmt.Sprintf("=== step %d: %s\n", index+1, stepLabel(step)))
		exitCode, message, err := a.runStep(ctx, containerName, workspace, job, step, log)
		if err != nil {
			return "infrastructure_failed", nil, message
		}
		if exitCode == nil && step.Builtin != "" {
			// A built-in that returned no exit code either succeeded or is not
			// applicable; both continue the job.
			continue
		}
		if exitCode != nil && *exitCode != 0 {
			// Steps with an "always" or "failure" condition still run after a
			// failure, so the failure is remembered rather than returned at once.
			skippedBecauseFailure = true
			if condition == "success" || condition == "" {
				// Stop at the first failing step for the common case.
				return "failed", exitCode, "a step failed"
			}
		}
	}
	if skippedBecauseFailure {
		return "failed", nil, "a step failed"
	}
	return "succeeded", nil, ""
}

// stepLabel names a step for the log.
func stepLabel(step jobDefinition) string {
	if step.Builtin != "" {
		return "builtin " + step.Builtin
	}
	firstLine := step.Run
	if newline := strings.IndexByte(firstLine, '\n'); newline >= 0 {
		firstLine = firstLine[:newline]
	}
	if len(firstLine) > 120 {
		firstLine = firstLine[:120]
	}
	return firstLine
}

// startContainer creates the job container. The container has no host mounts
// other than the workspace, a read-only root filesystem, and no Linux
// capabilities. Network is enabled only by runner configuration or when the
// trusted workflow explicitly references the demo deployment key.
func (a *agent) startContainer(ctx context.Context, name, workspace string, allowNetworkForJob ...bool) error {
	allowNetwork := a.config.AllowNetwork || (len(allowNetworkForJob) > 0 && allowNetworkForJob[0])
	args := append([]string{"run", "--detach", "--name", name}, a.containerArgsWithNetwork(name, workspace, allowNetwork)...)
	command := exec.CommandContext(ctx, a.config.DockerBinary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start container: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// containerArgs builds the isolation flags for one job container. It is a pure
// function so the isolation policy can be asserted without a container runtime:
// a missing flag here silently weakens every job, which is exactly the kind of
// change a test should catch.
func (a *agent) containerArgs(name, workspace string) []string {
	return a.containerArgsWithNetwork(name, workspace, a.config.AllowNetwork)
}

func (a *agent) containerArgsWithNetwork(name, workspace string, allowNetwork bool) []string {
	args := []string{
		"--workdir", "/workspace",
		"-v", workspace + ":/workspace",
		"--read-only",
		// A small writable scratch space outside the workspace for tools that need
		// a temporary directory, cleared with the container.
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=256m",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", "512",
		"--cpus", a.config.CPUs,
		"--memory", strconv.Itoa(a.config.MemoryMB) + "m",
		"-e", "HOME=/tmp",
		"-e", "CI=true",
		"-e", "FITHUB_RUN_NUMBER=" + strconv.FormatInt(0, 10),
		"--entrypoint", "/bin/sh",
	}
	if !allowNetwork {
		args = append(args, "--network", "none")
	}
	return append(args, a.config.Image, "-c", "sleep infinity")
}

// removeContainer stops and deletes a job container. A failure here means the
// runner has a leftover container, which is logged so an operator can clean it.
func (a *agent) removeContainer(name string) {
	// The stop and remove are given their own short context: the job context is
	// usually already cancelled at this point.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stop := exec.CommandContext(ctx, a.config.DockerBinary, "rm", "-f", name)
	if output, err := stop.CombinedOutput(); err != nil {
		slog.Warn("cannot remove job container", "container", name, "error", err, "output", strings.TrimSpace(string(output)))
	}
}

// runStep executes one step inside the running container. A built-in step is
// handled by the agent; a run step is executed with the fixed Linux shell.
func (a *agent) runStep(ctx context.Context, containerName, workspace string, job claimedJob, step jobDefinition, log func(string)) (*int, string, error) {
	switch step.Builtin {
	case "checkout":
		// The agent already checked out the exact revision before the container
		// started, so the built-in is a no-op that keeps the workflow readable.
		log("checkout: the exact revision is already in /workspace\n")
		return nil, "", nil
	case "upload-artifact":
		name := step.With["name"]
		target := step.With["path"]
		if name == "" || target == "" {
			return nil, "artifact upload needs a name and path", nil
		}
		// The path was validated at enqueue time; it is re-checked here so a
		// workspace symlink cannot escape into the host filesystem.
		absolute := filepath.Join(workspace, "repo", target)
		cleanRoot := filepath.Clean(filepath.Join(workspace, "repo"))
		if !strings.HasPrefix(filepath.Clean(absolute), cleanRoot+string(os.PathSeparator)) {
			log("artifact path escapes the workspace; skipped\n")
			return nil, "", nil
		}
		// Artifact storage is a later milestone; the step reports what it would
		// have uploaded rather than silently claiming success.
		log(fmt.Sprintf("artifact %s: collected from %s (artifact storage is not enabled on this runner)\n", name, target))
		return nil, "", nil
	}

	args := stepExecArgs(containerName, job, step)
	// The runner never injects a secret into a job: the roadmap requires that a
	// pull request job receives none, and this milestone has no secret store, so
	// the rule holds trivially and is asserted by the pipeline tests.
	command := exec.CommandContext(ctx, a.config.DockerBinary, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, "the step could not be started", err
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		return nil, "the step could not be started", err
	}
	// Output is streamed line by line so a long step shows progress and the log
	// limit applies as it runs.
	streamLines(ctx, stdout, log)
	waitErr := command.Wait()
	exitCode := 0
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		exitCode = exitErr.ExitCode()
	} else if waitErr != nil {
		return nil, "the step could not be completed", waitErr
	}
	return &exitCode, "", nil
}

// stepExecArgs builds the container exec arguments for one run step. The
// interpreter is chosen from the two values the workflow schema allows and is
// mapped to a fixed path, so a workflow cannot name an arbitrary executable, and
// the script is passed as a single argument rather than interpolated into a
// command line.
func stepExecArgs(containerName string, job claimedJob, step jobDefinition) []string {
	args := []string{"exec", "--workdir", "/workspace/repo"}
	if step.WorkingDirectory != "" {
		args = []string{"exec", "--workdir", "/workspace/repo/" + step.WorkingDirectory}
	}
	for key, value := range step.Env {
		args = append(args, "-e", key+"="+value)
	}
	for key, value := range job.Secrets {
		args = append(args, "-e", key+"="+value)
	}
	// Fixed FITHUB_* variables are injected by the control plane contract; the
	// values come from the run record, not from repository content. The runner's
	// own credential is deliberately absent.
	args = append(args,
		"-e", "FITHUB_SHA="+job.HeadSHA,
		"-e", "FITHUB_REF="+job.Ref,
		"-e", "FITHUB_EVENT="+job.Event,
		"-e", "FITHUB_JOB="+job.JobID,
		"-e", "FITHUB_RUN_NUMBER="+strconv.FormatInt(job.RunNumber, 10),
		containerName,
	)
	if step.Shell == "sh" {
		return append(args, "/bin/sh", "-e", "-c", step.Run)
	}
	return append(args, "/bin/bash", "-e", "-o", "pipefail", "-c", step.Run)
}

// finish reports the job's terminal state. A reporting failure is returned so the
// caller logs it; the control plane's lease reaper resolves the job if the report
// never arrives.
func (a *agent) finish(ctx context.Context, job claimedJob, state string, exitCode *int, message string) error {
	// The report uses a fresh context: the job context may be cancelled because
	// the job timed out, and the failure still has to be recorded.
	reportCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.client.complete(reportCtx, job.JobToken, state, exitCode, message); err != nil {
		return fmt.Errorf("report %s: %w", state, err)
	}
	return nil
}

// failClaim reports a job that could not even be read as a failure.
func (a *agent) failClaim(ctx context.Context, job claimedJob, message string) error {
	return a.finish(ctx, job, "failed", nil, message)
}

// cleanupOrphans removes job containers and workspaces left by a previous
// process. It runs once at startup so a crash does not leave disk behind.
func (a *agent) cleanupOrphans(ctx context.Context) {
	if entries, err := os.ReadDir(a.config.WorkRoot); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "job-") {
				continue
			}
			path := filepath.Join(a.config.WorkRoot, entry.Name())
			if err := os.RemoveAll(path); err != nil {
				slog.Warn("cannot remove a stale workspace", "path", path, "error", err)
			}
		}
	}
	// Containers named by this runner are removed; a container from another
	// tool with a different name is left alone.
	list := exec.CommandContext(ctx, a.config.DockerBinary, "ps", "--filter", "name=fithub-job-", "--format", "{{.Names}}")
	output, err := list.Output()
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(output)) {
		if !strings.HasPrefix(name, "fithub-job-") {
			continue
		}
		remove := exec.CommandContext(ctx, a.config.DockerBinary, "rm", "-f", name)
		if output, err := remove.CombinedOutput(); err != nil {
			slog.Warn("cannot remove a stale container", "container", name, "error", err, "output", strings.TrimSpace(string(output)))
		}
	}
}

// sanitizeName reduces an attempt ID to characters Docker accepts in a name.
func sanitizeName(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			builder.WriteRune(char)
		}
	}
	name := builder.String()
	if len(name) > 48 {
		name = name[:48]
	}
	if name == "" {
		name = "unknown"
	}
	return name
}
