package store

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var canonicalStoreUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// RecordRefDecision writes an audit row for each accepted reference update.
func (s *Store) RecordRefDecision(ctx context.Context, repoID, actorID, actorKind string, references []string) error {
	if !canonicalStoreUUID.MatchString(repoID) || !canonicalStoreUUID.MatchString(actorID) {
		return invalid("ref decision identity")
	}
	if actorKind != "token" && actorKind != "session" && actorKind != "ssh" {
		return invalid("actor kind")
	}
	if len(references) == 0 || len(references) > 200 {
		return invalid("ref count")
	}
	batch := make([][]any, 0, len(references))
	for _, reference := range references {
		if len(reference) < 1 || len(reference) > 300 || strings.ContainsRune(reference, 0) {
			return invalid("ref name")
		}
		batch = append(batch, []any{repoID, actorID, actorKind, reference})
	}
	_, err := s.Pool.CopyFrom(ctx, pgx.Identifier{"ref_decisions"},
		[]string{"repo_id", "actor_id", "actor_kind", "reference"},
		pgx.CopyFromRows(batch))
	return dbErr(err)
}
