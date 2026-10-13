// fithub-runner executes FitHub CI jobs inside isolated containers.
//
// The agent itself runs outside the job container and holds the runner token, so
// repository code never sees a runner credential, the host Docker socket, or a
// database connection. Each job gets a fresh container with no host mounts, a
// read-only root filesystem apart from its one writable workspace directory, and
// dropped capabilities.
//
// The container boundary is not a security boundary against a hostile kernel
// exploit: containers share the runner host's kernel. This agent is intended for
// code from authorized colleagues in a small team, which the roadmap states
// explicitly. Running untrusted external contributions requires a one-shot VM per
// job instead.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fithub-runner:", err)
		os.Exit(1)
	}
}

// runnerConfig is the agent's static configuration.
type runnerConfig struct {
	// ServerURL is the FitHub base URL.
	ServerURL string
	// Token is the runner credential. It is read from the environment so it can
	// be supplied by a service manager rather than a file in a repository.
	Token string
	// Image is the container image used for jobs, pinned by digest in production.
	Image string
	// WorkRoot is the host directory under which job workspaces are created.
	WorkRoot string
	// PollInterval is how long to wait between claim attempts when the queue is
	// empty.
	PollInterval time.Duration
	// JobTimeout bounds one job; a workflow may request less but never more.
	JobTimeout time.Duration
	// CPUs and MemoryMB bound a job container.
	CPUs     string
	MemoryMB int
	// DockerBinary is the container runtime client.
	DockerBinary string
	// AllowNetwork enables job network access. It is disabled by default because
	// a build usually does not need it and it is the main exfiltration path.
	AllowNetwork bool
}

func loadConfig() (runnerConfig, error) {
	config := runnerConfig{
		ServerURL:    strings.TrimRight(strings.TrimSpace(os.Getenv("FITHUB_RUNNER_SERVER")), "/"),
		Token:        strings.TrimSpace(os.Getenv("FITHUB_RUNNER_TOKEN")),
		Image:        strings.TrimSpace(os.Getenv("FITHUB_RUNNER_IMAGE")),
		WorkRoot:     strings.TrimSpace(os.Getenv("FITHUB_RUNNER_WORK_ROOT")),
		PollInterval: 5 * time.Second,
		JobTimeout:   30 * time.Minute,
		CPUs:         "2",
		MemoryMB:     2048,
		DockerBinary: "docker",
	}
	if raw := strings.TrimSpace(os.Getenv("FITHUB_RUNNER_ALLOW_NETWORK")); raw == "1" || strings.EqualFold(raw, "true") {
		config.AllowNetwork = true
	}
	if raw := strings.TrimSpace(os.Getenv("FITHUB_RUNNER_JOB_TIMEOUT_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 30 || seconds > 7200 {
			return runnerConfig{}, errors.New("FITHUB_RUNNER_JOB_TIMEOUT_SECONDS must be between 30 and 7200")
		}
		config.JobTimeout = time.Duration(seconds) * time.Second
	}
	if config.ServerURL == "" || config.Token == "" || config.Image == "" || config.WorkRoot == "" {
		return runnerConfig{}, errors.New("FITHUB_RUNNER_SERVER, FITHUB_RUNNER_TOKEN, FITHUB_RUNNER_IMAGE, and FITHUB_RUNNER_WORK_ROOT are required")
	}
	if !strings.HasPrefix(config.ServerURL, "http://") && !strings.HasPrefix(config.ServerURL, "https://") {
		return runnerConfig{}, errors.New("FITHUB_RUNNER_SERVER must be an http or https URL")
	}
	// A job image must be pinned by digest: a moving tag would let a later image
	// change reach every job without any repository change.
	if !strings.Contains(config.Image, "@sha256:") && !isLocalImageID(config.Image) {
		return runnerConfig{}, errors.New("FITHUB_RUNNER_IMAGE must be pinned by digest (image@sha256:...) or local image ID (sha256:...)")
	}
	return config, nil
}

func isLocalImageID(image string) bool {
	if !strings.HasPrefix(image, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(image, "sha256:"))
	return len(strings.TrimPrefix(image, "sha256:")) == 64 && err == nil
}

func run() error {
	config, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The workspace root is created private so another local account cannot read
	// a job's source or write into its workspace.
	if err := os.MkdirAll(config.WorkRoot, 0o700); err != nil {
		return fmt.Errorf("create work root: %w", err)
	}
	agent := &agent{config: config, client: newAPIClient(config.ServerURL, config.Token)}
	slog.Info("FitHub runner ready", "server", config.ServerURL, "image", config.Image,
		"network", config.AllowNetwork, "jobTimeout", config.JobTimeout.String())

	// Reclaim anything left by a previous crash before taking new work, so a
	// restart does not leak containers or workspaces.
	agent.cleanupOrphans(context.Background())

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		worked, err := agent.step(ctx)
		if err != nil {
			slog.Warn("job execution failed", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(config.PollInterval):
		}
	}
}
