package authorization

import (
	"context"

	"fithub.local/fithub/internal/store"
)

func levelValue(level string) int {
	switch level {
	case "read":
		return 1
	case "write":
		return 2
	case "admin":
		return 3
	default:
		return 0
	}
}

// CanRepository is the shared decision for HTTP, token, and SSH Git entry
// points. A token may only narrow the current user's rights.
func CanRepository(ctx context.Context, data *store.Store, user *store.User, token *store.Token, repo store.Repository, level string) (bool, error) {
	if repo.State != "active" || levelValue(level) == 0 {
		return false, nil
	}
	if token != nil {
		if token.Scope == "repository" && (token.RepoID == nil || *token.RepoID != repo.ID) {
			return false, nil
		}
		if levelValue(token.Level) < levelValue(level) {
			return false, nil
		}
	}
	if level == "read" && repo.Visibility == "public" {
		return true, nil
	}
	if user == nil || user.Status != "active" {
		return false, nil
	}
	member, err := data.HasAnyTeam(ctx, user.ID)
	if err != nil {
		return false, err
	}
	if !member {
		return false, nil
	}
	if level == "read" && repo.Visibility == "internal" {
		return true, nil
	}
	grant, err := data.EffectiveRepoRole(ctx, user.ID, repo.ID)
	if err != nil {
		return false, err
	}
	return levelValue(grant) >= levelValue(level), nil
}
