package gitservice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrTextCommitInvalid = errors.New("invalid text commit input")
	ErrTextCommitUnsafe  = errors.New("unsafe text commit path")
)

// TextCommitCandidate contains objects written by native Git but not yet
// published. The API rechecks permission between preparation and publication.
type TextCommitCandidate struct {
	Ref         string `json:"ref"`
	ExpectedSHA string `json:"expectedHeadSha"`
	CommitSHA   string `json:"commitSha"`
	Path        string `json:"path"`
}

// PrepareTextCommit changes exactly one UTF-8 regular file on an existing
// branch. Git's temporary index, hash-object, write-tree, and commit-tree produce
// the objects; no repository file is checked out.
func (s *Service) PrepareTextCommit(ctx context.Context, id, branch, expectedSHA, path, content, message, authorName, authorEmail string) (TextCommitCandidate, error) {
	parts, err := browsePathParts(path, false)
	if err != nil || !validSHA(expectedSHA) || !validBrowseBranch(branch) ||
		!utf8.ValidString(content) || strings.ContainsRune(content, 0) || len(content) > 64<<10 ||
		!utf8.ValidString(message) || strings.TrimSpace(message) == "" || len(message) > 200 || strings.ContainsAny(message, "\r\n\x00") {
		return TextCommitCandidate{}, ErrTextCommitInvalid
	}
	for _, part := range parts {
		if strings.EqualFold(part, ".git") {
			return TextCommitCandidate{}, ErrTextCommitUnsafe
		}
	}
	if parts[0] == ".fithub" || parts[0] == ".github" || path == ".gitmodules" {
		return TextCommitCandidate{}, ErrTextCommitUnsafe
	}
	ref := "refs/heads/" + branch
	if err := s.validHeadRef(ctx, "", ref); err != nil {
		return TextCommitCandidate{}, ErrTextCommitInvalid
	}
	repo, err := s.Path(id)
	if err != nil {
		return TextCommitCandidate{}, err
	}
	if err := s.rejectUnsafeConfig(ctx, repo); err != nil {
		return TextCommitCandidate{}, err
	}
	current, err := s.resolveCommitRef(ctx, repo, ref, ErrHeadMissing)
	if err != nil {
		return TextCommitCandidate{}, err
	}
	if current != expectedSHA {
		return TextCommitCandidate{}, ErrHeadStale
	}
	tmp, err := os.MkdirTemp(s.root, ".text-commit-*")
	if err != nil {
		return TextCommitCandidate{}, ErrGitOperation
	}
	defer os.RemoveAll(tmp)
	index := filepath.Join(tmp, "index")
	if _, err = s.textGit(ctx, repo, index, nil, "", "", "read-tree", expectedSHA); err != nil {
		return TextCommitCandidate{}, ErrGitOperation
	}
	for _, parent := range parentPaths(parts) {
		mode, _, err := s.indexPathState(ctx, repo, index, parent)
		if err != nil {
			return TextCommitCandidate{}, err
		}
		if mode != "" {
			return TextCommitCandidate{}, ErrTextCommitUnsafe
		}
	}
	mode, isDirectory, err := s.indexPathState(ctx, repo, index, path)
	if err != nil {
		return TextCommitCandidate{}, err
	}
	if isDirectory || mode != "" && mode != "100644" && mode != "100755" {
		return TextCommitCandidate{}, ErrTextCommitUnsafe
	}
	if mode == "" {
		mode = "100644"
	}
	blobOut, err := s.textGit(ctx, repo, index, strings.NewReader(content), "", "", "hash-object", "-w", "--stdin")
	if err != nil {
		return TextCommitCandidate{}, ErrGitOperation
	}
	blob := strings.TrimSpace(string(blobOut))
	if !validSHA(blob) {
		return TextCommitCandidate{}, ErrGitOperation
	}
	if _, err := s.textGit(ctx, repo, index, nil, "", "", "update-index", "--add", "--cacheinfo", mode+","+blob+","+path); err != nil {
		return TextCommitCandidate{}, ErrGitOperation
	}
	treeOut, err := s.textGit(ctx, repo, index, nil, "", "", "write-tree")
	if err != nil {
		return TextCommitCandidate{}, ErrGitOperation
	}
	tree := strings.TrimSpace(string(treeOut))
	if !validSHA(tree) {
		return TextCommitCandidate{}, ErrGitOperation
	}
	oldTreeOut, err := s.textGit(ctx, repo, index, nil, "", "", "rev-parse", expectedSHA+"^{tree}")
	if err != nil || strings.TrimSpace(string(oldTreeOut)) == tree {
		return TextCommitCandidate{}, ErrAlreadyMerged
	}
	commitOut, err := s.textGit(ctx, repo, index, nil, safeGitName(authorName), safeGitEmail(authorEmail),
		"commit-tree", tree, "-p", expectedSHA, "-m", strings.TrimSpace(message))
	if err != nil {
		return TextCommitCandidate{}, ErrGitOperation
	}
	commit := strings.TrimSpace(string(commitOut))
	if !validSHA(commit) {
		return TextCommitCandidate{}, ErrGitOperation
	}
	return TextCommitCandidate{Ref: ref, ExpectedSHA: expectedSHA, CommitSHA: commit, Path: path}, nil
}

// ApplyTextCommit is an atomic compare-and-swap of the development branch.
// A concurrent push or API edit changes the expected old SHA and fails closed.
func (s *Service) ApplyTextCommit(ctx context.Context, id string, candidate TextCommitCandidate) error {
	if !validSHA(candidate.ExpectedSHA) || !validSHA(candidate.CommitSHA) || s.validHeadRef(ctx, "", candidate.Ref) != nil {
		return ErrTextCommitInvalid
	}
	repo, err := s.Path(id)
	if err != nil {
		return err
	}
	current, err := s.resolveCommitRef(ctx, repo, candidate.Ref, ErrHeadMissing)
	if err != nil {
		return err
	}
	if current != candidate.ExpectedSHA {
		return ErrHeadStale
	}
	if _, err := s.textGit(ctx, repo, "", nil, "", "", "update-ref", "--no-deref", candidate.Ref, candidate.CommitSHA, candidate.ExpectedSHA); err != nil {
		current, checkErr := s.resolveCommitRef(ctx, repo, candidate.Ref, ErrHeadMissing)
		if checkErr == nil && current != candidate.ExpectedSHA {
			return ErrHeadStale
		}
		return ErrGitOperation
	}
	return nil
}

func parentPaths(parts []string) []string {
	parents := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		parents = append(parents, strings.Join(parts[:i], "/"))
	}
	return parents
}

func (s *Service) indexPathState(ctx context.Context, repo, index, path string) (string, bool, error) {
	out, err := s.textGit(ctx, repo, index, nil, "", "", "ls-files", "--stage", "-z", "--", path)
	if err != nil {
		return "", false, ErrGitOperation
	}
	var directory bool
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			return "", false, ErrGitOperation
		}
		if strings.HasPrefix(string(name), path+"/") {
			directory = true
		}
		if string(name) == path {
			fields := bytes.Fields(meta)
			if len(fields) != 3 || string(fields[2]) != "0" {
				return "", false, ErrGitOperation
			}
			return string(fields[0]), false, nil
		}
	}
	return "", directory, nil
}

func safeGitName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 || !utf8.ValidString(name) {
		return "FitHub User"
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '<' || r == '>' {
			return "FitHub User"
		}
	}
	return name
}

func safeGitEmail(email string) string {
	if len(email) < 3 || len(email) > 254 || !strings.Contains(email, "@") {
		return "noreply@fithub.local"
	}
	for _, c := range email {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("._+-@", c)) {
			return "noreply@fithub.local"
		}
	}
	return email
}

func (s *Service) textGit(ctx context.Context, repo, index string, stdin io.Reader, authorName, authorEmail string, args ...string) ([]byte, error) {
	all := append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.attributesFile=" + os.DevNull}, args...)
	cmd := exec.CommandContext(ctx, s.git, all...)
	cmd.Dir = repo
	cmd.Env = append(gitEnv(s.git), "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_LITERAL_PATHSPECS=1")
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	if authorName != "" {
		cmd.Env = append(cmd.Env,
			"GIT_AUTHOR_NAME="+authorName, "GIT_AUTHOR_EMAIL="+authorEmail,
			"GIT_COMMITTER_NAME=FitHub", "GIT_COMMITTER_EMAIL=noreply@fithub.local")
	}
	cmd.Stdin = stdin
	var out boundedBrowseBuffer
	out.max = 512 << 10
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || out.overflow {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrGitOperation
	}
	return out.data, nil
}
