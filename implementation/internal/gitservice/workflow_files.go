package gitservice

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
)

// BranchHead resolves a branch's current commit. It is used before queueing work
// so a run always records an immutable revision rather than a moving ref.
func (s *Service) BranchHead(ctx context.Context, id, ref string) (string, error) {
	// A fully qualified ref is reduced to the branch name the browse helpers
	// expect, so callers may pass either form.
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if !validBrowseBranch(branch) {
		return "", ErrBrowseInvalid
	}
	repo, err := s.Path(id)
	if err != nil {
		return "", err
	}
	return s.resolveBrowseBranch(ctx, repo, branch)
}

// ListWorkflowFiles returns the workflow file paths on a branch, sorted so the
// same repository always produces the same order. Only direct children of the
// workflow directory are returned, which is the same rule the validator enforces,
// so the loader can never read a file the admin-only rule does not cover.
func (s *Service) ListWorkflowFiles(ctx context.Context, id, branch string) ([]string, error) {
	if branch == "" {
		return nil, ErrBrowseInvalid
	}
	// Listing a missing workflow directory is a normal state: a repository that
	// has not configured CI yet. That is reported as an empty list, not an error.
	tree, err := s.BrowseTree(ctx, id, branch, ".fithub/workflows")
	if err != nil {
		if errors.Is(err, ErrBrowseNotFound) {
			return []string{}, nil
		}
		return nil, err
	}
	files := make([]string, 0, len(tree.Entries))
	for _, entry := range tree.Entries {
		if entry.Type != "file" {
			continue
		}
		if !isWorkflowFilePath(entry.Path) {
			continue
		}
		files = append(files, entry.Path)
	}
	return files, nil
}

// isWorkflowFilePath mirrors the workflow package's path rule without importing
// it, so this package keeps no dependency on the CI layer. A change to one rule
// must be reflected in the other; the workflow package's own tests pin its
// behavior.
func isWorkflowFilePath(candidate string) bool {
	const prefix = ".fithub/workflows/"
	if len(candidate) <= len(prefix) || candidate[:len(prefix)] != prefix {
		return false
	}
	rest := candidate[len(prefix):]
	if rest == "" || rest[0] == '.' {
		return false
	}
	for i := 0; i < len(rest); i++ {
		char := rest[i]
		if char == '/' || char == '\\' {
			return false
		}
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return hasYAMLSuffix(rest)
}

func hasYAMLSuffix(name string) bool {
	return len(name) > 4 && (name[len(name)-4:] == ".yml" || name[len(name)-5:] == ".yaml")
}

// Archive streams a gzipped tar of one revision to the writer. It is used by the
// CI runner to obtain exactly the revision under test: the caller passes a job
// token that was already validated for that attempt, so the archive is a
// read-only projection of one commit rather than repository access.
//
// The revision must be a full object ID that exists as a commit; a ref name is
// rejected so a moving branch cannot change what a queued job receives. The
// archive is produced by Git itself, which omits the .git directory, so the job
// container never receives repository metadata.
func (s *Service) Archive(ctx context.Context, id, revision string, destination io.Writer) error {
	if !validSHA(revision) {
		return ErrBrowseInvalid
	}
	repo, err := s.Path(id)
	if err != nil {
		return err
	}
	commit, err := s.browseGit(ctx, repo, 128, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBrowseNotFound
	}
	if !validSHA(strings.TrimSpace(string(commit))) {
		return ErrBrowseNotFound
	}
	command := exec.CommandContext(ctx, s.git, "archive", "--format=tar.gz", revision)
	command.Dir = repo
	command.Env = append(gitEnv(s.git), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	command.Stdout = destination
	command.Stderr = ioDiscard{}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBrowseOperation
	}
	return nil
}
