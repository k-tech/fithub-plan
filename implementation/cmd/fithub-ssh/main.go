// fithub-ssh is intended to be invoked only by OpenSSH's
// AuthorizedKeysCommand and its per-key forced command. It does not implement
// SSH or authenticate a public-key signature itself.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/sshtransport"
	"fithub.local/fithub/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if code := run(os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

func run(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: fithub-ssh authorized-key|serve [options]")
		return 2
	}
	var configPath, loginUser, fingerprint, keyID string
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&configPath, "config", "", "absolute path to private SSH gateway config")
	switch args[0] {
	case "authorized-key":
		fs.StringVar(&loginUser, "user", "", "OpenSSH login user")
		fs.StringVar(&fingerprint, "fingerprint", "", "OpenSSH key fingerprint")
	case "serve":
		fs.StringVar(&keyID, "key-id", "", "registered SSH key ID")
	default:
		fmt.Fprintln(os.Stderr, "unsupported SSH gateway command")
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil || len(fs.Args()) != 0 || configPath == "" {
		return 2
	}
	cfg, err := sshtransport.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "SSH gateway configuration unavailable")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(connectCtx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "SSH authentication unavailable")
		return 1
	}
	defer pool.Close()
	if err := pool.Ping(connectCtx); err != nil {
		fmt.Fprintln(os.Stderr, "SSH authentication unavailable")
		return 1
	}
	data := store.New(pool)
	if args[0] == "authorized-key" {
		if loginUser == "" || fingerprint == "" {
			return 2
		}
		executable, err := os.Executable()
		if err != nil {
			return 1
		}
		executable, err = filepath.EvalSymlinks(executable)
		if err != nil {
			return 1
		}
		lookupCtx, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		line, err := sshtransport.AuthorizedKey(lookupCtx, data, cfg, loginUser, fingerprint, executable, configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "SSH key lookup unavailable")
			return 1
		}
		if line != "" {
			fmt.Fprintln(os.Stdout, line)
		}
		return 0
	}
	if keyID == "" || os.Getenv("SSH_ORIGINAL_COMMAND") == "" {
		fmt.Fprintln(os.Stderr, "unsupported SSH Git command")
		return 1
	}
	git, err := gitservice.New(cfg.GitRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Git repository storage unavailable")
		return 1
	}
	prepareCtx, done := context.WithTimeout(ctx, 10*time.Second)
	prepared, err := sshtransport.Prepare(prepareCtx, data, git, keyID, os.Getenv("SSH_ORIGINAL_COMMAND"))
	done()
	if err != nil {
		fmt.Fprintln(os.Stderr, "repository unavailable or access denied")
		return 1
	}
	if err := data.TouchSSHKey(ctx, prepared.KeyID); err != nil {
		fmt.Fprintln(os.Stderr, "SSH authentication unavailable")
		return 1
	}
	// The only process launched for a successful request is native Git with a
	// fixed subcommand and a repository path resolved from a database UUID.
	gitCtx, done := context.WithTimeout(ctx, 30*time.Minute)
	defer done()
	if err := sshtransport.RunGit(gitCtx, cfg, prepared, os.Stdin, os.Stdout, os.Stderr, os.Getenv("GIT_PROTOCOL")); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "SSH Git operation failed")
		return 1
	}
	return 0
}
