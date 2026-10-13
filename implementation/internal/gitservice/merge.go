package gitservice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

var (
	ErrInvalidRef       = errors.New("invalid merge head ref")
	ErrInvalidSHA       = errors.New("invalid expected SHA")
	ErrMainMissing      = errors.New("main branch is missing")
	ErrHeadMissing      = errors.New("merge head branch is missing")
	ErrHeadStale        = errors.New("merge head has changed")
	ErrBaseStale        = errors.New("main branch has changed")
	ErrMergeConflict    = errors.New("merge has conflicts")
	ErrAlreadyMerged    = errors.New("head is already merged into main")
	ErrInvalidCandidate = errors.New("invalid merge candidate")
	ErrUnsafeGitConfig  = errors.New("repository has unsafe Git configuration")
	ErrGitOperation     = errors.New("Git merge operation failed")
)

// validBaseRef accepts the fully qualified branch a pull request merges into.
func (s *Service) validBaseRef(ref string) error {
	if !strings.HasPrefix(ref, "refs/heads/") || strings.ContainsAny(ref, "\r\n\x00") {
		return ErrInvalidRef
	}
	for _, component := range strings.Split(strings.TrimPrefix(ref, "refs/heads/"), "/") {
		if component == "" || strings.HasPrefix(component, "-") {
			return ErrInvalidRef
		}
	}
	return nil
}

const maxMergeOutputBytes = 8 << 20

type mergeOutputBuffer struct {
	buf      bytes.Buffer
	overflow bool
}

func (b *mergeOutputBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > maxMergeOutputBytes {
		b.overflow = true
		return 0, ErrGitOperation
	}
	return b.buf.Write(p)
}

// MergeInspection describes the current PR head against the current main.
// File statistics describe changes from their merge base to the head commit.
type MergeInspection struct {
	BaseSHA      string `json:"base_sha"`
	HeadSHA      string `json:"head_sha"`
	Conflicted   bool   `json:"conflicted"`
	FilesChanged int    `json:"files_changed"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
}

// MergeCandidate is prepared without moving main. Persist it before ApplyMerge
// if the application needs a durable merge-attempt record. Its fields are
// validated again by ApplyMerge; callers must not trust client-supplied values.
type MergeCandidate struct {
	BaseSHA   string `json:"base_sha"`
	HeadSHA   string `json:"head_sha"`
	MergedSHA string `json:"merged_sha"`
	// BaseRef is the target branch this merge updates.
	BaseRef string `json:"base_ref"`
	HeadRef string `json:"head_ref"`
}

type MergeResult struct {
	BaseSHA   string `json:"base_sha"`
	HeadSHA   string `json:"head_sha"`
	MergedSHA string `json:"merged_sha"`
}

// InspectMerge checks the current branch tips, native Git merge result, and
// file statistics. A conflict is reported in the result without moving refs.
func (s *Service) InspectMerge(ctx context.Context, id, baseRef, headRef string) (MergeInspection, error) {
	repo, base, head, err := s.mergeState(ctx, id, baseRef, headRef)
	if err != nil {
		return MergeInspection{}, err
	}
	inspection := MergeInspection{BaseSHA: base, HeadSHA: head}
	mergeBase, err := s.mergeBase(ctx, repo, base, head)
	if err != nil {
		if !errors.Is(err, ErrMergeConflict) {
			return MergeInspection{}, err
		}
		inspection.Conflicted = true // Unrelated histories have no safe merge base.
		mergeBase = base
	}
	inspection.FilesChanged, inspection.Additions, inspection.Deletions, err = s.diffStats(ctx, repo, mergeBase, head)
	if err != nil {
		return MergeInspection{}, err
	}
	if !inspection.Conflicted {
		_, inspection.Conflicted, err = s.mergeTree(ctx, repo, base, head)
		if err != nil {
			return MergeInspection{}, err
		}
	}
	return inspection, nil
}

// PrepareMerge builds a native Git two-parent merge commit but does not move
// main. The expected head must equal the current branch tip before any commit
// is created; ApplyMerge rechecks both refs atomically.
func (s *Service) PrepareMerge(ctx context.Context, id, baseRef, headRef, expectedHeadSHA string) (MergeCandidate, error) {
	if !validSHA(expectedHeadSHA) {
		return MergeCandidate{}, ErrInvalidSHA
	}
	repo, base, head, err := s.mergeState(ctx, id, baseRef, headRef)
	if err != nil {
		return MergeCandidate{}, err
	}
	if head != expectedHeadSHA {
		return MergeCandidate{}, ErrHeadStale
	}
	mergedAlready, err := s.isAncestor(ctx, repo, head, base)
	if err != nil {
		return MergeCandidate{}, err
	}
	if mergedAlready {
		return MergeCandidate{}, ErrAlreadyMerged
	}
	mergeBase, err := s.mergeBase(ctx, repo, base, head)
	if err != nil {
		return MergeCandidate{}, err
	}
	filesChanged, _, _, err := s.diffStats(ctx, repo, mergeBase, head)
	if err != nil {
		return MergeCandidate{}, err
	}
	if filesChanged == 0 {
		return MergeCandidate{}, ErrAlreadyMerged
	}
	tree, conflicted, err := s.mergeTree(ctx, repo, base, head)
	if err != nil {
		return MergeCandidate{}, err
	}
	if conflicted {
		return MergeCandidate{}, ErrMergeConflict
	}
	message := "Merge " + headRef + " into " + strings.TrimPrefix(baseRef, "refs/heads/")
	output, err := s.mergeGit(ctx, repo, nil, true, "commit-tree", tree, "-p", base, "-p", head, "-m", message)
	if err != nil {
		return MergeCandidate{}, ErrGitOperation
	}
	merged := strings.TrimSpace(string(output))
	if !validSHA(merged) {
		return MergeCandidate{}, ErrGitOperation
	}
	return MergeCandidate{BaseSHA: base, HeadSHA: head, MergedSHA: merged, BaseRef: baseRef, HeadRef: headRef}, nil
}

// ApplyMerge verifies a prepared commit and atomically checks the head and
// old target SHA before moving the target ref. This Git plumbing command
// bypasses the pre-receive hook, so callers must verify current write access.
func (s *Service) ApplyMerge(ctx context.Context, id string, candidate MergeCandidate) (MergeResult, error) {
	if err := s.validBaseRef(candidate.BaseRef); err != nil {
		return MergeResult{}, ErrInvalidCandidate
	}
	if err := s.validHeadRef(ctx, candidate.BaseRef, candidate.HeadRef); err != nil {
		return MergeResult{}, ErrInvalidCandidate
	}
	if !validSHA(candidate.BaseSHA) || !validSHA(candidate.HeadSHA) || !validSHA(candidate.MergedSHA) {
		return MergeResult{}, ErrInvalidCandidate
	}
	repo, base, head, err := s.mergeState(ctx, id, candidate.BaseRef, candidate.HeadRef)
	if err != nil {
		return MergeResult{}, err
	}
	if head != candidate.HeadSHA {
		return MergeResult{}, ErrHeadStale
	}
	if base != candidate.BaseSHA {
		return MergeResult{}, ErrBaseStale
	}
	if err := s.verifyCandidate(ctx, repo, candidate); err != nil {
		return MergeResult{}, err
	}
	transaction := "start\nverify " + candidate.HeadRef + " " + candidate.HeadSHA +
		"\nupdate " + candidate.BaseRef + " " + candidate.MergedSHA + " " + candidate.BaseSHA +
		"\nprepare\ncommit\n"
	if _, err := s.mergeGit(ctx, repo, strings.NewReader(transaction), false, "update-ref", "--no-deref", "--stdin"); err != nil {
		// A failed transaction does not publish the candidate. Re-read both refs
		// to distinguish a racing branch update from a Git/storage failure.
		_, currentBase, currentHead, stateErr := s.mergeState(ctx, id, candidate.BaseRef, candidate.HeadRef)
		if stateErr == nil {
			if currentHead != candidate.HeadSHA {
				return MergeResult{}, ErrHeadStale
			}
			if currentBase != candidate.BaseSHA {
				return MergeResult{}, ErrBaseStale
			}
		}
		if errors.Is(stateErr, ErrMainMissing) || errors.Is(stateErr, ErrHeadMissing) {
			return MergeResult{}, stateErr
		}
		return MergeResult{}, ErrGitOperation
	}
	return MergeResult{BaseSHA: base, HeadSHA: head, MergedSHA: candidate.MergedSHA}, nil
}

func (s *Service) mergeState(ctx context.Context, id, baseRef, headRef string) (repo, base, head string, err error) {
	if err := s.validBaseRef(baseRef); err != nil {
		return "", "", "", err
	}
	if err := s.validHeadRef(ctx, baseRef, headRef); err != nil {
		return "", "", "", err
	}
	repo, err = s.Path(id)
	if err != nil {
		return "", "", "", err
	}
	if err := s.rejectUnsafeConfig(ctx, repo); err != nil {
		return "", "", "", err
	}
	base, err = s.resolveCommitRef(ctx, repo, baseRef, ErrMainMissing)
	if err != nil {
		return "", "", "", err
	}
	head, err = s.resolveCommitRef(ctx, repo, headRef, ErrHeadMissing)
	if err != nil {
		return "", "", "", err
	}
	return repo, base, head, nil
}

func (s *Service) validHeadRef(ctx context.Context, baseRef, ref string) error {
	if !strings.HasPrefix(ref, "refs/heads/") || ref == baseRef || strings.ContainsAny(ref, "\r\n\x00") {
		return ErrInvalidRef
	}
	for _, component := range strings.Split(strings.TrimPrefix(ref, "refs/heads/"), "/") {
		if component == "" || strings.HasPrefix(component, "-") {
			return ErrInvalidRef
		}
	}
	if _, err := s.mergeGit(ctx, s.root, nil, false, "check-ref-format", ref); err != nil {
		return ErrInvalidRef
	}
	return nil
}

func (s *Service) resolveCommitRef(ctx context.Context, repo, ref string, missing error) (string, error) {
	output, err := s.mergeGit(ctx, repo, nil, false, "show-ref", "--verify", "--hash", ref)
	if err != nil {
		matches, matchErr := s.mergeGit(ctx, repo, nil, false, "for-each-ref", "--format=%(refname)", ref)
		if matchErr == nil && !containsRefLine(matches, ref) {
			return "", missing
		}
		return "", ErrGitOperation
	}
	sha := strings.TrimSpace(string(output))
	if !validSHA(sha) {
		return "", ErrGitOperation
	}
	typeOutput, err := s.mergeGit(ctx, repo, nil, false, "cat-file", "-t", sha)
	if err != nil || strings.TrimSpace(string(typeOutput)) != "commit" {
		return "", ErrGitOperation
	}
	return sha, nil
}

func containsRefLine(output []byte, ref string) bool {
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == ref {
			return true
		}
	}
	return false
}

func (s *Service) rejectUnsafeConfig(ctx context.Context, repo string) error {
	output, err := s.mergeGit(ctx, repo, nil, false, "config", "--local", "--name-only", "--list", "--no-includes")
	if err != nil {
		return ErrGitOperation
	}
	for _, key := range strings.Split(string(output), "\n") {
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "filter.") || strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.") ||
			strings.HasPrefix(key, "merge.") && strings.HasSuffix(key, ".driver") ||
			strings.HasPrefix(key, "diff.") && strings.HasSuffix(key, ".command") ||
			key == "diff.external" || key == "core.fsmonitor" || key == "core.attributesfile" {
			return ErrUnsafeGitConfig
		}
	}
	return nil
}

func (s *Service) mergeBase(ctx context.Context, repo, base, head string) (string, error) {
	output, err := s.mergeGit(ctx, repo, nil, false, "merge-base", base, head)
	if err != nil {
		if exitCode(err) == 1 {
			return "", ErrMergeConflict
		}
		return "", ErrGitOperation
	}
	sha := strings.TrimSpace(string(output))
	if !validSHA(sha) {
		return "", ErrGitOperation
	}
	return sha, nil
}

func (s *Service) mergeTree(ctx context.Context, repo, base, head string) (string, bool, error) {
	output, err := s.mergeGit(ctx, repo, nil, false, "merge-tree", "--write-tree", base, head)
	if err != nil {
		if exitCode(err) == 1 {
			return "", true, nil
		}
		return "", false, ErrGitOperation
	}
	first, _, _ := strings.Cut(string(output), "\n")
	if !validSHA(first) {
		return "", false, ErrGitOperation
	}
	return first, false, nil
}

func (s *Service) isAncestor(ctx context.Context, repo, ancestor, descendant string) (bool, error) {
	_, err := s.mergeGit(ctx, repo, nil, false, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, ErrGitOperation
}

func (s *Service) diffStats(ctx context.Context, repo, base, head string) (files, additions, deletions int, err error) {
	output, runErr := s.mergeGit(ctx, repo, nil, false, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--numstat", "-z", base, head, "--")
	if runErr != nil {
		return 0, 0, 0, ErrGitOperation
	}
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 3)
		if len(parts) != 3 {
			return 0, 0, 0, ErrGitOperation
		}
		files++
		if string(parts[0]) != "-" {
			value, parseErr := strconv.Atoi(string(parts[0]))
			if parseErr != nil {
				return 0, 0, 0, ErrGitOperation
			}
			additions += value
		}
		if string(parts[1]) != "-" {
			value, parseErr := strconv.Atoi(string(parts[1]))
			if parseErr != nil {
				return 0, 0, 0, ErrGitOperation
			}
			deletions += value
		}
	}
	return files, additions, deletions, nil
}

func (s *Service) verifyCandidate(ctx context.Context, repo string, candidate MergeCandidate) error {
	sizeOutput, err := s.mergeGit(ctx, repo, nil, false, "cat-file", "-s", candidate.MergedSHA)
	if err != nil {
		return ErrInvalidCandidate
	}
	size, err := strconv.Atoi(strings.TrimSpace(string(sizeOutput)))
	if err != nil || size > 64*1024 {
		return ErrInvalidCandidate
	}
	typeOutput, err := s.mergeGit(ctx, repo, nil, false, "cat-file", "-t", candidate.MergedSHA)
	if err != nil || strings.TrimSpace(string(typeOutput)) != "commit" {
		return ErrInvalidCandidate
	}
	contents, err := s.mergeGit(ctx, repo, nil, false, "cat-file", "-p", candidate.MergedSHA)
	if err != nil {
		return ErrInvalidCandidate
	}
	header, message, found := bytes.Cut(contents, []byte("\n\n"))
	if !found || string(message) != "Merge "+candidate.HeadRef+" into main\n" {
		return ErrInvalidCandidate
	}
	var tree string
	parents := make([]string, 0, 2)
	var author, committer bool
	for _, line := range strings.Split(string(header), "\n") {
		switch {
		case strings.HasPrefix(line, "tree "):
			if tree != "" {
				return ErrInvalidCandidate
			}
			tree = strings.TrimPrefix(line, "tree ")
		case strings.HasPrefix(line, "parent "):
			parents = append(parents, strings.TrimPrefix(line, "parent "))
		case strings.HasPrefix(line, "author FitHub <noreply@fithub.local> "):
			if author {
				return ErrInvalidCandidate
			}
			author = true
		case strings.HasPrefix(line, "committer FitHub <noreply@fithub.local> "):
			if committer {
				return ErrInvalidCandidate
			}
			committer = true
		default:
			return ErrInvalidCandidate
		}
	}
	if !validSHA(tree) || len(parents) != 2 || parents[0] != candidate.BaseSHA || parents[1] != candidate.HeadSHA || !author || !committer {
		return ErrInvalidCandidate
	}
	expectedTree, conflicted, err := s.mergeTree(ctx, repo, candidate.BaseSHA, candidate.HeadSHA)
	if err != nil || conflicted || tree != expectedTree {
		return ErrInvalidCandidate
	}
	return nil
}

func (s *Service) mergeGit(ctx context.Context, repo string, stdin io.Reader, identity bool, args ...string) ([]byte, error) {
	allArgs := append([]string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.attributesFile=" + os.DevNull,
		"-c", "merge.renormalize=false",
	}, args...)
	cmd := exec.CommandContext(ctx, s.git, allArgs...)
	if repo != "" {
		cmd.Dir = repo
	}
	cmd.Env = append(gitEnv(s.git), "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if identity {
		cmd.Env = append(cmd.Env,
			"GIT_AUTHOR_NAME=FitHub", "GIT_AUTHOR_EMAIL=noreply@fithub.local",
			"GIT_COMMITTER_NAME=FitHub", "GIT_COMMITTER_EMAIL=noreply@fithub.local",
		)
	}
	cmd.Stdin = stdin
	var output mergeOutputBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if output.overflow {
		return nil, ErrGitOperation
	}
	if err != nil {
		return nil, err
	}
	return output.buf.Bytes(), nil
}

// ValidSHA reports whether value is a full lowercase Git object ID. It is
// exported so callers can validate revisions without duplicating the rule or
// accepting a looser format.
func ValidSHA(value string) bool { return validSHA(value) }

// ValidSHAOrZero accepts a full object ID or the all-zero ID that Git uses in a
// pre-receive line to mean "ref created" or "ref deleted".
func ValidSHAOrZero(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c != '0' {
			return ValidSHA(value)
		}
	}
	return len(value) == 40 || len(value) == 64
}

func validSHA(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	for _, c := range sha {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}
