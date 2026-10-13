package gitservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPathWithin(t *testing.T) {
	accepted := []struct{ candidate, prefix string }{
		{".fithub/workflows/ci.yml", ".fithub/workflows"},
		{".fithub/workflows/ci.yml", ".fithub/workflows/"},
		{".fithub/workflows", ".fithub/workflows"},
	}
	for _, tc := range accepted {
		if !PathWithin(tc.candidate, tc.prefix) {
			t.Errorf("PathWithin(%q, %q) = false, want true", tc.candidate, tc.prefix)
		}
	}
	rejected := []struct{ candidate, prefix string }{
		{".fithub/workflows/../secret.yml", ".fithub/workflows"},
		{".fithub/workflows", ".fithub/workflows/ci.yml"},
		{"", ".fithub/workflows"},
		{".fithub/workflows", ""},
		{".fithub/other/ci.yml", ".fithub/workflows"},
		{".fithub/workflows\\ci.yml", ".fithub/workflows"},
		{"../.fithub/workflows/ci.yml", ".fithub/workflows"},
	}
	for _, tc := range rejected {
		if PathWithin(tc.candidate, tc.prefix) {
			t.Errorf("PathWithin(%q, %q) = true, want false", tc.candidate, tc.prefix)
		}
	}
}

// changedPathsFixture builds a bare repository with one commit and returns its
// path plus the commit ID, so the diff-tree inspection can run against real Git
// objects.
func changedPathsFixture(t *testing.T) (repoPath, commitSHA string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}
	work := t.TempDir()
	run := func(dir string, args ...string) string {
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test")
		output, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("git %v: %v: %s", args, runErr, output)
		}
		return string(output)
	}
	run(work, "init", "--bare", "repo.git")
	repoPath = filepath.Join(work, "repo.git")
	source := filepath.Join(work, "src")
	if err := os.MkdirAll(filepath.Join(source, ".fithub", "workflows"), 0700); err != nil {
		t.Fatal(err)
	}
	run(work, "init", "--initial-branch=main", source)
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hi\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".fithub", "workflows", "ci.yml"), []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(source, "add", ".")
	run(source, "commit", "-m", "one")
	run(source, "push", repoPath, "main")
	return repoPath, ""
}

// ChangedPaths must use the empty tree when the update creates a ref, and must
// list every path the commit introduces. This is the case a first push hits.
func TestChangedPathsFromEmptyTree(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}
	repo, _ := changedPathsFixture(t)
	cmd := exec.Command(git, "rev-parse", "refs/heads/main")
	cmd.Dir = repo
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	head := trimNewline(string(output))

	paths, err := ChangedPathsInRepo(context.Background(), git, repo, "", head)
	if err != nil {
		t.Fatalf("ChangedPathsInRepo: %v", err)
	}
	want := map[string]bool{"README.md": true, ".fithub/workflows/ci.yml": true}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for _, path := range paths {
		if !want[path] {
			t.Errorf("unexpected path %q", path)
		}
	}

	// The all-zero object ID is the deletion sentinel but must be treated as
	// "no previous revision" for the left side, matching a ref creation.
	if _, err := ChangedPathsInRepo(context.Background(), git, repo, zeroOID, head); err != nil {
		t.Fatalf("all-zero old side must be accepted: %v", err)
	}
}

// An uninspectable or malformed revision must be reported, never treated as a
// change with no paths, because that would silently skip the workflow rule.
func TestChangedPathsRejectsUninspectableInput(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}
	repo, _ := changedPathsFixture(t)
	cases := []struct {
		name           string
		oldSHA, newSHA string
	}{
		{"missing new", "", ""},
		{"short new", "", "abc"},
		{"unknown commit", "", "1234567890123456789012345678901234567890"},
		{"malformed old", "nope", "1234567890123456789012345678901234567890"},
		{"no repository", "", "1234567890123456789012345678901234567890"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := repo
			if tc.name == "no repository" {
				target = ""
			}
			if _, err := ChangedPathsInRepo(context.Background(), git, target, tc.oldSHA, tc.newSHA); !errors.Is(err, ErrPathInspection) {
				t.Fatalf("expected ErrPathInspection, got %v", err)
			}
		})
	}
}

// zeroOID is the all-zero object ID Git uses for a deleted ref.
const zeroOID = "0000000000000000000000000000000000000000"

func trimNewline(value string) string {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}
