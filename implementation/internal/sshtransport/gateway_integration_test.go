package sshtransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSSHKeyLookupAndRepositoryAuthorization(t *testing.T) {
	dbURL := os.Getenv("FITHUB_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set FITHUB_TEST_DATABASE_URL to a disposable PostgreSQL instance")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := fmt.Sprintf("fithub_ssh_test_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})
	dbCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	dbCfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, dbCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	data := store.New(pool)
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	git, err := gitservice.New(filepath.Join(t.TempDir(), "repos"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := data.UpsertDevUser(ctx, "admin@example.test", "Admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetUserRole(ctx, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	owner, err := data.UpsertDevUser(ctx, "owner@example.test", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := data.UpsertDevUser(ctx, "outsider@example.test", "Outsider")
	if err != nil {
		t.Fatal(err)
	}
	team, err := data.CreateTeam(ctx, "engineering", "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	join, err := data.RequestJoin(ctx, owner.ID, team.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.ReviewJoin(ctx, join.ID, admin.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	for _, visibility := range []string{"private", "internal", "public"} {
		repo, err := data.CreateRepo(ctx, team.ID, visibility, visibility, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := git.InitBare(ctx, repo.ID); err != nil {
			t.Fatal(err)
		}
		if err := data.SetRepoState(ctx, repo.ID, "active"); err != nil {
			t.Fatal(err)
		}
	}
	ownerKey, err := ParsePublicKey(generateKey(t, "ed25519", ""))
	if err != nil {
		t.Fatal(err)
	}
	registered, err := data.CreateSSHKey(ctx, owner.ID, "Laptop", ownerKey.Type, ownerKey.PublicKey, ownerKey.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.CreateSSHKey(ctx, outsider.ID, "Duplicate", ownerKey.Type, ownerKey.PublicKey, ownerKey.Fingerprint); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate SSH key was not rejected: %v", err)
	}
	cfg := Config{SSHUser: "git"}
	line, err := AuthorizedKey(ctx, data, cfg, "git", ownerKey.Fingerprint, "/usr/local/bin/fithub-ssh", "/etc/fithub/ssh.json")
	if err != nil || !strings.Contains(line, "--key-id "+registered.ID) {
		t.Fatalf("registered key lookup: %q, %v", line, err)
	}
	for _, spec := range []struct{ user, fingerprint string }{{"other", ownerKey.Fingerprint}, {"git", "SHA256:unknown"}} {
		line, err := AuthorizedKey(ctx, data, cfg, spec.user, spec.fingerprint, "/usr/local/bin/fithub-ssh", "/etc/fithub/ssh.json")
		if err != nil || line != "" {
			t.Fatalf("unexpected key lookup: %q, %v", line, err)
		}
	}
	preparedRead, err := Prepare(ctx, data, git, registered.ID, "git-upload-pack 'engineering/private.git'")
	if err != nil {
		t.Fatalf("owner read denied: %v", err)
	}
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitBinary, err = filepath.Abs(gitBinary)
	if err != nil {
		t.Fatal(err)
	}
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := RunGit(gitCtx, Config{GitBinary: gitBinary}, preparedRead, bytes.NewReader(nil), io.Discard, io.Discard, "version=2"); err != nil {
		t.Fatalf("native git-upload-pack did not run: %v", err)
	}
	if _, err := Prepare(ctx, data, git, registered.ID, "git-receive-pack '/engineering/private.git'"); err != nil {
		t.Fatalf("owner write denied: %v", err)
	}
	outsiderKey, err := ParsePublicKey(generateKey(t, "ed25519", ""))
	if err != nil {
		t.Fatal(err)
	}
	otherRegistered, err := data.CreateSSHKey(ctx, outsider.ID, "Other", outsiderKey.Type, outsiderKey.PublicKey, outsiderKey.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"git-upload-pack 'engineering/private.git'", "git-upload-pack 'engineering/internal.git'", "git-receive-pack 'engineering/public.git'"} {
		if _, err := Prepare(ctx, data, git, otherRegistered.ID, command); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("teamless outsider allowed %q: %v", command, err)
		}
	}
	if _, err := Prepare(ctx, data, git, otherRegistered.ID, "git-upload-pack 'engineering/public.git'"); err != nil {
		t.Fatalf("public repository read denied: %v", err)
	}
	otherTeam, err := data.CreateTeam(ctx, "design", "Design")
	if err != nil {
		t.Fatal(err)
	}
	join, err = data.RequestJoin(ctx, outsider.ID, otherTeam.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.ReviewJoin(ctx, join.ID, admin.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(ctx, data, git, otherRegistered.ID, "git-upload-pack 'engineering/internal.git'"); err != nil {
		t.Fatalf("company member internal read denied: %v", err)
	}
	if _, err := Prepare(ctx, data, git, otherRegistered.ID, "git-receive-pack 'engineering/internal.git'"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("read-only member was allowed to push: %v", err)
	}
	if err := data.SetUserStatus(ctx, outsider.ID, "suspended"); err != nil {
		t.Fatal(err)
	}
	if line, err := AuthorizedKey(ctx, data, cfg, "git", outsiderKey.Fingerprint, "/usr/local/bin/fithub-ssh", "/etc/fithub/ssh.json"); err != nil || line != "" {
		t.Fatalf("suspended user's key was offered: %q, %v", line, err)
	}
	if _, err := Prepare(ctx, data, git, otherRegistered.ID, "git-upload-pack 'engineering/public.git'"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("suspended user's key was accepted: %v", err)
	}
	if err := data.TouchSSHKey(ctx, otherRegistered.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("suspended user's key was marked used: %v", err)
	}
	if err := data.DeleteSSHKey(ctx, owner.ID, registered.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(ctx, data, git, registered.ID, "git-upload-pack 'engineering/private.git'"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("deleted key was accepted: %v", err)
	}
}
