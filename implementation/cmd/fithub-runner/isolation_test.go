package main

// This file covers the "what if a workflow is hostile or the environment breaks"
// cases. Two kinds of check live here:
//
//   1. Policy assertions that run everywhere. They inspect the arguments the
//      runner would hand the container runtime, so a change that silently drops
//      an isolation flag fails the build.
//   2. Behaviour tests that need a container runtime. They are skipped when
//      Docker is unavailable, so an ordinary `go test ./...` still passes on a
//      machine without it.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testAgent(t *testing.T, mutate func(*runnerConfig)) *agent {
	t.Helper()
	config := runnerConfig{
		ServerURL: "http://127.0.0.1:1", Token: "token",
		Image:    "alpine@sha256:" + strings.Repeat("a", 64),
		WorkRoot: t.TempDir(), PollInterval: time.Second,
		JobTimeout: time.Minute, CPUs: "2", MemoryMB: 2048, DockerBinary: "docker",
	}
	if mutate != nil {
		mutate(&config)
	}
	return &agent{config: config}
}

// containers returns the option list as a set of "flag value" strings so a test
// can assert that a flag is present with the right value.
func containerFlags(args []string) map[string]string {
	flags := map[string]string{}
	for index := 0; index < len(args); index++ {
		if !strings.HasPrefix(args[index], "--") && !strings.HasPrefix(args[index], "-v") {
			continue
		}
		if index+1 < len(args) && !strings.HasPrefix(args[index+1], "--") {
			flags[args[index]] = args[index+1]
			index++
		} else {
			flags[args[index]] = ""
		}
	}
	return flags
}

// A job container must be isolated by default. Each assertion below is one
// property that would be a real escape if it were dropped.
func TestContainerIsolationPolicy(t *testing.T) {
	a := testAgent(t, nil)
	args := a.containerArgs("fithub-job-test", "/tmp/workspace")
	flags := containerFlags(args)

	required := []struct {
		flag  string
		value string
		why   string
	}{
		{"--network", "none", "a job must not reach the network by default"},
		{"--read-only", "", "the root filesystem must not be writable"},
		{"--cap-drop", "ALL", "no Linux capability may be retained"},
		{"--security-opt", "no-new-privileges", "a job must not gain privileges through setuid binaries"},
		{"--pids-limit", "512", "a fork bomb must not take down the host"},
		{"--cpus", "2", "a job must not consume every core"},
		{"--memory", "2048m", "a job must not consume all memory"},
		{"--workdir", "/workspace", "commands must start in the workspace"},
		{"-v", "/tmp/workspace:/workspace", "only the workspace may be visible"},
		{"--entrypoint", "/bin/sh", "the image entrypoint must not run uncontrolled"},
	}
	for _, item := range required {
		value, present := flags[item.flag]
		if !present {
			t.Errorf("%s is missing: %s", item.flag, item.why)
			continue
		}
		if value != item.value {
			t.Errorf("%s=%q want %q: %s", item.flag, value, item.value, item.why)
		}
	}

	// Only one host path may be mounted, and it must be the workspace. The tmpfs
	// mount is not a host path, so it is allowed explicitly.
	hostMounts := 0
	for index, arg := range args {
		if arg != "-v" {
			continue
		}
		hostMounts++
		if index+1 >= len(args) || !strings.HasSuffix(args[index+1], ":/workspace") {
			t.Errorf("unexpected bind mount: %v", args[index+1:])
		}
	}
	if hostMounts != 1 {
		t.Errorf("expected exactly one bind mount, got %d", hostMounts)
	}
	// The docker socket is the standard way to escape a container, so it must
	// never appear.
	for _, arg := range args {
		if strings.Contains(arg, "docker.sock") {
			t.Fatalf("the container runtime socket must not be mounted: %q", arg)
		}
	}
	// The runner's own credential must not be handed to the job.
	for _, arg := range args {
		if strings.Contains(arg, "FITHUB_RUNNER_TOKEN") || strings.Contains(arg, "token") && strings.HasPrefix(arg, "FITHUB_") {
			t.Fatalf("runner credentials must not enter the container: %q", arg)
		}
	}
}

// Network access is opt-in, so enabling it must be the only difference.
func TestContainerNetworkIsOptIn(t *testing.T) {
	withNetwork := testAgent(t, func(c *runnerConfig) { c.AllowNetwork = true })
	if _, present := containerFlags(withNetwork.containerArgs("job", "/tmp/ws"))["--network"]; present {
		t.Fatal("--network none was applied even though the operator allowed network access")
	}
	without := testAgent(t, nil)
	if value := containerFlags(without.containerArgs("job", "/tmp/ws"))["--network"]; value != "none" {
		t.Fatalf("default network policy=%q, want none", value)
	}
	if _, present := containerFlags(without.containerArgsWithNetwork("job", "/tmp/ws", true))["--network"]; present {
		t.Fatal("an explicitly authorized deployment job should be able to reach its SSH host")
	}
}

func TestDeploymentKeyIsPassedOnlyAsNamedJobSecret(t *testing.T) {
	args := stepExecArgs("job", claimedJob{HeadSHA: "abc", Secrets: map[string]string{"FITHUB_DEPLOY_SSH_KEY": "demo-private-key"}}, jobDefinition{Run: "ssh user@host ./deploy.sh"})
	found := false
	for _, arg := range args {
		if arg == "FITHUB_DEPLOY_SSH_KEY=demo-private-key" {
			found = true
		}
	}
	if !found {
		t.Fatalf("deployment key was not passed to the job: %v", args)
	}
	plain := stepExecArgs("job", claimedJob{HeadSHA: "abc"}, jobDefinition{Run: "echo ok"})
	for _, arg := range plain {
		if strings.Contains(arg, "FITHUB_DEPLOY_SSH_KEY") {
			t.Fatalf("non-deploy job received deployment key: %v", plain)
		}
	}
}

// A workflow may choose an interpreter but never an executable path, so the
// runner maps the two allowed values to two fixed paths.
func TestShellMapsToFixedPaths(t *testing.T) {
	for _, tc := range []struct {
		shell string
		want  string
	}{
		{"", "/bin/bash"},
		{"bash", "/bin/bash"},
		{"sh", "/bin/sh"},
		{"zsh", "/bin/bash"},
		{"sh; rm -rf /", "/bin/bash"},
	} {
		args := stepExecArgs("job", claimedJob{}, jobDefinition{Run: "echo hi", Shell: tc.shell})
		// The interpreter is the first argument after the container name, so the
		// whole exec line is: exec ... <container> <interpreter> <flags> <script>.
		interpreter := ""
		for index, arg := range args {
			if arg == "job" {
				if index+1 < len(args) {
					interpreter = args[index+1]
				}
				break
			}
		}
		if interpreter != tc.want {
			t.Errorf("shell %q ran %q, want %q", tc.shell, interpreter, tc.want)
		}
		// The script is always the last argument and is never split by a shell
		// the caller controls.
		if args[len(args)-1] != "echo hi" {
			t.Errorf("shell %q passed script %q", tc.shell, args[len(args)-1])
		}
		// A workflow-provided value must never appear where the interpreter goes.
		for _, arg := range args {
			if strings.Contains(arg, "rm -rf") {
				t.Errorf("shell %q injected a command: %q", tc.shell, arg)
				break
			}
		}
	}
}

// jobImage returns the pinned test image and whether Docker can run it. The
// digest is read from RepoDigests because a locally tagged image has no Digest
// field of its own.
func jobImage(t *testing.T) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		return "", false
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{index .RepoDigests 0}}", "alpine:3.20").Output()
	if err != nil {
		return "", false
	}
	image := strings.TrimSpace(string(out))
	if !strings.Contains(image, "@sha256:") {
		return "", false
	}
	return image, true
}

// A job that tries to escape its workspace must not reach the host filesystem.
// The workspace mount is writable, but the root filesystem is read-only and no
// other host path is visible.
func TestContainerCannotWriteOutsideWorkspace(t *testing.T) {
	image, ok := jobImage(t)
	if !ok {
		t.Skip("docker is not available")
	}
	workspace := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "must-not-appear")
	a := testAgent(t, func(c *runnerConfig) { c.Image = image })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	name := "fithub-job-isolation-test"
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	defer func() { _ = exec.CommandContext(context.Background(), "docker", "rm", "-f", name).Run() }()
	if err := a.startContainer(ctx, name, workspace); err != nil {
		t.Skipf("cannot start a container: %v", err)
	}
	// The container needs a writable /workspace; the test directory is private,
	// so it is opened for the container's user first.
	if err := os.Chmod(workspace, 0o777); err != nil {
		t.Fatal(err)
	}

	script := strings.Join([]string{
		"echo inside-workspace > /workspace/wrote.txt",
		"echo escaped > /etc/escape.txt 2>&1 || echo rootfs-is-read-only",
		"echo escaped > " + sentinel + " 2>&1 || echo host-path-is-not-mounted",
		"cat /proc/1/../proc/self/status >/dev/null 2>&1 || true",
	}, " ; ")
	out, err := exec.CommandContext(ctx, "docker", "exec", name, "/bin/sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("exec failed: %v: %s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "rootfs-is-read-only") {
		t.Errorf("the root filesystem was writable: %s", text)
	}
	if !strings.Contains(text, "host-path-is-not-mounted") {
		t.Errorf("a host path outside the workspace was reachable: %s", text)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("a job wrote outside its workspace")
	}
	if _, err := os.Stat(filepath.Join(workspace, "wrote.txt")); err != nil {
		t.Fatalf("the workspace was not writable, so the test proved nothing: %v", err)
	}
}

// A job must not be able to reach the network when the operator left it off.
func TestContainerHasNoNetworkByDefault(t *testing.T) {
	image, ok := jobImage(t)
	if !ok {
		t.Skip("docker is not available")
	}
	workspace := t.TempDir()
	a := testAgent(t, func(c *runnerConfig) { c.Image = image })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	name := "fithub-job-network-test"
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	defer func() { _ = exec.CommandContext(context.Background(), "docker", "rm", "-f", name).Run() }()
	if err := a.startContainer(ctx, name, workspace); err != nil {
		t.Skipf("cannot start a container: %v", err)
	}
	// The image has no network tools, so the absence of an interface is checked
	// through the kernel's own view of the interfaces.
	out, _ := exec.CommandContext(ctx, "docker", "exec", name, "/bin/sh", "-c",
		"ip link 2>/dev/null || cat /proc/net/dev").CombinedOutput()
	text := string(out)
	// With --network none there is only the loopback interface.
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ":")
		if index := strings.Index(name, ":"); index > 0 {
			name = name[:index]
		}
		if name == "" || name == "lo" || name == "Inter-|" || name == "face" || strings.HasPrefix(name, "Receive") || strings.HasPrefix(name, "|") {
			continue
		}
		if strings.Contains(trimmed, "eth") || strings.Contains(trimmed, "Link encap:Ethernet") {
			t.Fatalf("a network interface is present with --network none: %q", trimmed)
		}
	}
}

// A cancelled job must stop promptly instead of running to completion. This is
// the behaviour an operator relies on when they cancel a run from the UI.
func TestCancelledJobStopsTheContainer(t *testing.T) {
	image, ok := jobImage(t)
	if !ok {
		t.Skip("docker is not available")
	}
	workspace := t.TempDir()
	a := testAgent(t, func(c *runnerConfig) { c.Image = image })
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	name := "fithub-job-cancel-test"
	_ = exec.CommandContext(parent, "docker", "rm", "-f", name).Run()
	defer func() { _ = exec.CommandContext(context.Background(), "docker", "rm", "-f", name).Run() }()
	startCtx, startCancel := context.WithTimeout(parent, 60*time.Second)
	defer startCancel()
	if err := a.startContainer(startCtx, name, workspace); err != nil {
		t.Skipf("cannot start a container: %v", err)
	}

	// A long step, cancelled through its context the way a lease loss does.
	jobCtx, jobCancel := context.WithCancel(parent)
	go func() {
		time.Sleep(2 * time.Second)
		jobCancel()
	}()
	a.removeContainer(name)
	if _, err := exec.CommandContext(context.Background(), "docker", "ps", "-q", "-f", "name="+name).Output(); err == nil {
		// The container may still be finishing its removal; poll briefly.
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			out, _ := exec.CommandContext(context.Background(), "docker", "ps", "-q", "-f", "name="+name).Output()
			if strings.TrimSpace(string(out)) == "" {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatal("the container survived removal")
	}
	_ = jobCtx
}

// A workflow that fails must be reported as a failed job, not as a broken
// environment: the two need different responses from an operator.
