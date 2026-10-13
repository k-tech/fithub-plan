package gitservice

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// HookConfig is the small, root-owned file the receive hook reads. It contains
// only the database URL, so the database password never appears in a repository
// or in a process argument list.
type HookConfig struct {
	DatabaseURL string `json:"databaseUrl"`
}

// LoadHookConfig reads and validates the hook configuration file.
func LoadHookConfig(path string) (HookConfig, error) {
	if !safeHookPath(path) {
		return HookConfig{}, errors.New("invalid hook configuration path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return HookConfig{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return HookConfig{}, errors.New("hook configuration must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return HookConfig{}, err
	}
	var cfg HookConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return HookConfig{}, err
	}
	if !strings.HasPrefix(cfg.DatabaseURL, "postgres://") && !strings.HasPrefix(cfg.DatabaseURL, "postgresql://") {
		return HookConfig{}, errors.New("hook configuration needs a PostgreSQL URL")
	}
	if len(cfg.DatabaseURL) > 2048 || strings.ContainsAny(cfg.DatabaseURL, "\r\n\x00\"") {
		return HookConfig{}, errors.New("hook configuration database URL is invalid")
	}
	return cfg, nil
}

// safeHookPath rejects relative paths, shell metacharacters, and whitespace so
// the path can be interpolated into a hook script without quoting surprises.
func safeHookPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, " \t\r\n\"'\\,=\x00")
}

// HookInstallation describes how the receive hook is wired into a repository.
type HookInstallation struct {
	// ConfigPath is the absolute path to the hook configuration file.
	ConfigPath string
	// ExecutablePath is the absolute path to the fithub-hook binary.
	ExecutablePath string
}

// InstallReceiveHook writes the pre-receive hook for one repository. The hook
// passes this repository's UUID explicitly, so a repository file cannot make
// the hook decide against a different repository.
func InstallReceiveHook(repo, repositoryID string, installation HookInstallation) error {
	if !canonicalUUID.MatchString(repositoryID) {
		return ErrInvalidID
	}
	if !safeHookPath(installation.ConfigPath) || !safeHookPath(installation.ExecutablePath) {
		return errors.New("invalid hook installation paths")
	}
	return writeHook(repo, hookScript(repositoryID, installation))
}

// writeHook creates or replaces the pre-receive hook of one repository. It
// refuses to overwrite a hook that FitHub did not write.
func writeHook(repo, script string) error {
	hooksDir := filepath.Join(repo, "hooks")
	if err := os.MkdirAll(hooksDir, 0700); err != nil {
		return fmt.Errorf("create hooks directory: %w", err)
	}
	path := filepath.Join(hooksDir, "pre-receive")
	// Create the file exclusively so a pre-existing hook is never overwritten
	// silently; an existing FitHub hook is rewritten in place.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			// The marker check keeps a foreign script from being replaced.
			return replaceOwnHook(path, script)
		}
		return fmt.Errorf("create pre-receive hook: %w", err)
	}
	if _, err := file.WriteString(script); err != nil {
		file.Close()
		return fmt.Errorf("write pre-receive hook: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close pre-receive hook: %w", err)
	}
	return nil
}

// replaceOwnHook rewrites a hook only when it carries our marker comment.
func replaceOwnHook(path, script string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pre-receive hook: %w", err)
	}
	if !isOwnHook(string(existing)) {
		return errors.New("pre-receive hook is not managed by FitHub")
	}
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		return fmt.Errorf("rewrite pre-receive hook: %w", err)
	}
	return nil
}

// isOwnHook reports whether a hook script was written by FitHub. Besides the
// current marker it accepts the header of the provisional bootstrap hook that
// shipped before the database-backed policy, so upgrading an existing
// installation replaces the outdated rule instead of leaving it in place or
// failing startup. A foreign hook contains neither signature.
func isOwnHook(script string) bool {
	return strings.Contains(script, hookMarker) || strings.Contains(script, legacyHookHeader)
}

// hookMarker identifies a hook script written by FitHub.
const hookMarker = "# fithub-receive-policy v2"

// legacyHookHeader identifies the provisional bootstrap hook that predates the
// database-backed policy, so an existing installation can be upgraded in place.
const legacyHookHeader = "# Provisional bootstrap policy. Replace with the DB-backed policy hook."

func hookScript(repositoryID string, installation HookInstallation) string {
	// The script lives in <repo>/hooks/pre-receive, so the repository path is
	// derived from its own location. GIT_DIR is not guaranteed for a hook run by
	// git-http-backend, and guessing a path could inspect the wrong repository.
	return "#!/bin/sh\n" + hookMarker + "\n" +
		"set -eu\n" +
		"repo=$(CDPATH= cd -- \"$(dirname -- \"$0\")/..\" && pwd -P)\n" +
		"exec " + installation.ExecutablePath + " pre-receive --config " + installation.ConfigPath +
		" --repo-id " + repositoryID + " --git-dir \"$repo\"\n"
}

// namespaceHookScript is installed when no policy program is configured. It
// enforces only the fixed ref-namespace rule, which needs no database and must
// hold even in a development or partially configured deployment. It never
// consumes stdin: the policy program reads the update lines.
func namespaceHookScript() string {
	return "#!/bin/sh\n" + hookMarker + "\n" +
		"set -eu\n" +
		"while IFS=' ' read -r old new ref; do\n" +
		"  case \"$ref\" in\n" +
		"    refs/heads/*|refs/tags/*)\n" +
		"      git check-ref-format \"$ref\" >/dev/null 2>&1 || { echo \"invalid ref name\" >&2; exit 1; }\n" +
		"      ;;\n" +
		"    *) echo \"ref namespace is not writable\" >&2; exit 1 ;;\n" +
		"  esac\n" +
		"done\n"
}
