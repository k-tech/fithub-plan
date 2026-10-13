package sshtransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"fithub.local/fithub/internal/authorization"
	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/store"
)

var (
	ErrUnauthorized = errors.New("SSH Git access denied")
	ErrBadCommand   = errors.New("unsupported SSH Git command")
	fingerprintRE   = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]+$`)
	sshUserRE       = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	keyIDRE         = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	teamRE          = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	repoRE          = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
)

type Config struct {
	DatabaseURL string `json:"databaseUrl"`
	GitRoot     string `json:"gitRoot"`
	GitBinary   string `json:"gitBinary"`
	SSHUser     string `json:"sshUser"`
}

func LoadConfig(path string) (Config, error) {
	if !safeCommandPath(path) {
		return Config{}, errors.New("SSH config path must be absolute and shell-safe")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Config{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0027 != 0 || info.Size() > 16<<10 {
		return Config{}, errors.New("SSH config must be a private regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	d := json.NewDecoder(bytes.NewReader(content))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return Config{}, err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("SSH config must contain one JSON object")
	}
	if cfg.DatabaseURL == "" || !filepath.IsAbs(cfg.GitRoot) || !safeCommandPath(cfg.GitBinary) || !sshUserRE.MatchString(cfg.SSHUser) {
		return Config{}, errors.New("SSH config is missing a valid database URL, Git root, binary, or login user")
	}
	return cfg, nil
}

// AuthorizedKey returns exactly one OpenSSH authorized_keys line for a
// registered active user's matching fingerprint, or an empty string. sshd
// performs the actual signature check against the canonical key in the line.
func AuthorizedKey(ctx context.Context, data *store.Store, cfg Config, loginUser, fingerprint, executable, configPath string) (string, error) {
	if loginUser != cfg.SSHUser || !fingerprintRE.MatchString(fingerprint) {
		return "", nil
	}
	key, _, err := data.LookupSSHKeyByFingerprint(ctx, fingerprint)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return AuthorizedKeyLine(key, executable, configPath)
}

type GitCommand struct {
	Operation string
	Team      string
	Repo      string
}

// ParseOriginalCommand deliberately accepts the only two command forms Git
// emits for clone/fetch and push, with either an absolute or relative URL
// path. It does not invoke a shell or accept shell escaping/extensions.
func ParseOriginalCommand(original string) (GitCommand, error) {
	if len(original) == 0 || len(original) > 256 {
		return GitCommand{}, ErrBadCommand
	}
	op, quotedPath, ok := strings.Cut(original, " '")
	if !ok || (op != "git-upload-pack" && op != "git-receive-pack") || !strings.HasSuffix(quotedPath, "'") {
		return GitCommand{}, ErrBadCommand
	}
	path := strings.TrimSuffix(quotedPath, "'")
	path = strings.TrimPrefix(path, "/")
	team, repoWithSuffix, ok := strings.Cut(path, "/")
	if !ok || !strings.HasSuffix(repoWithSuffix, ".git") {
		return GitCommand{}, ErrBadCommand
	}
	repo := strings.TrimSuffix(repoWithSuffix, ".git")
	if !teamRE.MatchString(team) || !repoRE.MatchString(repo) {
		return GitCommand{}, ErrBadCommand
	}
	return GitCommand{Operation: op, Team: team, Repo: repo}, nil
}

type Prepared struct {
	KeyID string
	// UserID is the account that owns the key, resolved at command execution.
	UserID    string
	RepoID    string
	RepoPath  string
	Operation string
}

// Prepare rechecks the registered key and current user/repository rights at
// command execution, independently of OpenSSH's earlier key lookup.
func Prepare(ctx context.Context, data *store.Store, git *gitservice.Service, keyID, original string) (Prepared, error) {
	if !keyIDRE.MatchString(keyID) {
		return Prepared{}, ErrUnauthorized
	}
	command, err := ParseOriginalCommand(original)
	if err != nil {
		return Prepared{}, err
	}
	key, user, err := data.LookupSSHKeyByID(ctx, keyID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Prepared{}, ErrUnauthorized
		}
		return Prepared{}, err
	}
	repo, err := data.GetRepoBySlug(ctx, command.Team, command.Repo)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Prepared{}, ErrUnauthorized
		}
		return Prepared{}, err
	}
	level := "read"
	if command.Operation == "git-receive-pack" {
		level = "write"
	}
	allowed, err := authorization.CanRepository(ctx, data, &user, nil, repo, level)
	if err != nil {
		return Prepared{}, err
	}
	if !allowed {
		return Prepared{}, ErrUnauthorized
	}
	path, err := git.Path(repo.ID)
	if err != nil {
		return Prepared{}, fmt.Errorf("repository storage unavailable: %w", err)
	}
	return Prepared{KeyID: key.ID, UserID: user.ID, RepoID: repo.ID, RepoPath: path, Operation: command.Operation}, nil
}

func RunGit(ctx context.Context, cfg Config, prepared Prepared, stdin io.Reader, stdout, stderr io.Writer, protocol string) error {
	if prepared.Operation != "git-upload-pack" && prepared.Operation != "git-receive-pack" {
		return ErrBadCommand
	}
	if protocol != "" && protocol != "version=2" && protocol != "version=1" && protocol != "version=0" {
		return ErrBadCommand
	}
	subcommand := strings.TrimPrefix(prepared.Operation, "git-")
	cmd := exec.CommandContext(ctx, cfg.GitBinary, subcommand, prepared.RepoPath)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = []string{
		"PATH=" + filepath.Dir(cfg.GitBinary) + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_NO_REPLACE_OBJECTS=1",
	}
	// A receive-pack must tell the hook which account the key belongs to. The
	// SSH gateway authenticated the key, so this value is server-known and the
	// hook re-checks that account against the current database state.
	if prepared.Operation == "git-receive-pack" {
		if prepared.UserID == "" {
			return ErrUnauthorized
		}
		cmd.Env = append(cmd.Env, "FITHUB_USER_ID="+prepared.UserID, "FITHUB_ACTOR_KIND=ssh")
	}
	if protocol != "" {
		cmd.Env = append(cmd.Env, "GIT_PROTOCOL="+protocol)
	}
	return cmd.Run()
}
