package gitservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"strings"
)

// EmptyTreeSHA is Git's well-known object ID for the empty tree. It is used as
// the left side when a change has no prior revision, such as the first commit of
// a repository.
const EmptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// ErrPathInspection marks a failure to determine which paths a change touches.
// Callers must treat it as a rejection: a change whose contents cannot be
// inspected might include a restricted file.
var ErrPathInspection = errors.New("gitservice: cannot inspect changed paths")

// maxChangedPaths bounds the path list so a very large change cannot exhaust
// memory. A change with more paths than this is rejected rather than truncated,
// because truncation could hide a restricted path.
const maxChangedPaths = 10_000

// ChangedPaths lists the repository-relative paths that differ between two
// commits. An empty or all-zero oldSHA means the new commit is compared against
// the empty tree, which is how a branch's first push is inspected. The paths
// come from Git's own diff of the stored objects, so the result reflects the tree
// that will actually be served and cannot be influenced by a client-supplied
// file list.
func (s *Service) ChangedPaths(ctx context.Context, id, oldSHA, newSHA string) ([]string, error) {
	repo, err := s.Path(id)
	if err != nil {
		return nil, err
	}
	return ChangedPathsInRepo(ctx, s.git, repo, oldSHA, newSHA)
}

// ChangedPathsInRepo is the underlying implementation, exported so the receive
// hook can inspect a push with the same rules without constructing a Service.
// The caller must pass an absolute Git executable and the repository path.
func ChangedPathsInRepo(ctx context.Context, git, repo, oldSHA, newSHA string) ([]string, error) {
	if git == "" || repo == "" || !ValidSHA(newSHA) {
		return nil, ErrPathInspection
	}
	left := oldSHA
	if left == "" || allZeroOID(left) {
		// An unrecognized non-empty value must not silently become the empty
		// tree, because that would hide deletions from the caller.
		if left != "" && !ValidSHAOrZero(left) {
			return nil, ErrPathInspection
		}
		left = EmptyTreeSHA
	} else if !ValidSHA(left) {
		return nil, ErrPathInspection
	}
	args := []string{
		// A receive hook must not be able to influence this inspection, so the
		// hooks path and attribute files are neutralized exactly as the merge
		// path does.
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.attributesFile=" + os.DevNull,
		"-c", "core.quotePath=false",
		"diff-tree", "--no-commit-id", "--name-only", "-r", "--no-renames",
		"-z", left, newSHA,
	}
	cmd := exec.CommandContext(ctx, git, args...)
	cmd.Dir = repo
	cmd.Env = changedPathsEnv(git)
	var output mergeOutputBuffer
	cmd.Stdout = &output
	cmd.Stderr = ioDiscard{}
	if err := cmd.Run(); err != nil || output.overflow {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrPathInspection
	}
	paths := make([]string, 0, 16)
	for _, name := range strings.Split(output.buf.String(), "\x00") {
		if name == "" {
			continue
		}
		if len(paths) >= maxChangedPaths {
			return nil, ErrPathInspection
		}
		paths = append(paths, name)
	}
	return paths, nil
}

// MergeChangedPaths lists the paths a pull request would change if merged, based
// on the merge base of the two branches. It is used to decide whether a merge
// touches a restricted directory, so it reports what the merge would actually
// bring in rather than everything that differs between the two tips.
func (s *Service) MergeChangedPaths(ctx context.Context, id, baseRef, headRef string) ([]string, error) {
	repo, base, head, err := s.mergeState(ctx, id, baseRef, headRef)
	if err != nil {
		return nil, err
	}
	mergeBase, err := s.mergeBase(ctx, repo, base, head)
	if err != nil {
		return nil, err
	}
	output, runErr := s.mergeGit(ctx, repo, nil, false, "diff", "--no-ext-diff", "--no-textconv",
		"--no-renames", "--name-only", "-z", mergeBase, head, "--")
	if runErr != nil {
		return nil, ErrGitOperation
	}
	paths := make([]string, 0, 16)
	for _, name := range strings.Split(string(output), "\x00") {
		if name != "" {
			paths = append(paths, name)
		}
	}
	return paths, nil
}

// changedPathsEnv builds the environment for the path inspection. It keeps the
// repository object-location variables that git-receive-pack sets while a push is
// being received: the new objects live in a quarantine directory during
// pre-receive, so a fresh environment would make them invisible and the
// inspection would fail on every push. Only the variables Git itself uses to
// locate objects are inherited, never PATH-like or credential inputs.
func changedPathsEnv(git string) []string {
	env := append(gitEnv(git), "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	for _, name := range []string{"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_QUARANTINE_PATH"} {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// allZeroOID reports whether value is the all-zero object ID.
func allZeroOID(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c != '0' {
			return false
		}
	}
	return true
}

// PathWithin reports whether candidate stays inside prefix when both are treated
// as slash-separated repository paths. Policy checks use it so "..", a redundant
// separator, or a Windows separator cannot slip outside the protected prefix.
func PathWithin(candidate, prefix string) bool {
	if candidate == "" || prefix == "" || strings.Contains(candidate, "\\") {
		return false
	}
	cleaned := path.Clean(candidate)
	if cleaned != candidate {
		return false
	}
	if cleaned == prefix {
		return true
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return strings.HasPrefix(cleaned, prefix)
}
