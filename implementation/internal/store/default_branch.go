package store

import (
	"context"
	"regexp"
	"strings"
)

var branchNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

// SetDefaultBranch changes the branch shown as the repository's default.
func (s *Store) SetDefaultBranch(ctx context.Context, repoID, branch string) error {
	branch = strings.TrimSpace(branch)
	if !validBranchName(branch) {
		return invalid("branch name")
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE repositories SET default_branch=$2 WHERE id=$1`, repoID, branch)
	if err != nil {
		return dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func validBranchName(branch string) bool {
	return branchNameRE.MatchString(branch) &&
		!strings.Contains(branch, "..") &&
		!strings.Contains(branch, "//") &&
		!strings.HasSuffix(branch, "/") &&
		!strings.HasSuffix(branch, ".lock") &&
		!strings.Contains(branch, "@{") &&
		!strings.Contains(branch, "\\") &&
		branch != "@"
}
