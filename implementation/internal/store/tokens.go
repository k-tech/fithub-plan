package store

import (
	"context"
	"strings"
	"time"
)

const tokenColumns = `at.id::text, at.user_id::text, at.scope, at.repo_id::text, at.level,
    at.prefix, at.expires_at, at.revoked_at, at.last_used_at, at.created_at`

func scanToken(row scanner) (Token, error) {
	var t Token
	err := row.Scan(&t.ID, &t.UserID, &t.Scope, &t.RepoID, &t.Level,
		&t.Prefix, &t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt, &t.CreatedAt)
	return t, dbErr(err)
}

// CreateToken persists only a SHA-256 hex digest, never the bearer secret.
// Callers must validate requested repository access before creating a token.
func (s *Store) CreateToken(ctx context.Context, userID, scope, repoID, level, prefix, secretHash string, expiresAt time.Time) (Token, error) {
	if !validRole(level) {
		return Token{}, invalid("level")
	}
	if scope != "user" && scope != "repository" {
		return Token{}, invalid("scope")
	}
	if (scope == "repository" && repoID == "") || (scope == "user" && repoID != "") {
		return Token{}, invalid("repo_id")
	}
	if !expiresAt.After(time.Now()) {
		return Token{}, invalid("expires_at")
	}
	secretHash = strings.ToLower(strings.TrimSpace(secretHash))
	if len(secretHash) != 64 {
		return Token{}, invalid("secret_hash")
	}
	var repoParam any
	if repoID != "" {
		repoParam = repoID
	}
	return scanToken(s.Pool.QueryRow(ctx, `
        INSERT INTO access_tokens AS at (user_id,scope,repo_id,level,prefix,secret_hash,expires_at)
        SELECT u.id,$2,$3,$4,$5,$6,$7 FROM users u WHERE u.id=$1 AND u.status='active'
        RETURNING `+tokenColumns, userID, scope, repoParam, level, prefix, secretHash, expiresAt))
}

// LookupToken hides expired, revoked, and suspended-user credentials.
func (s *Store) LookupToken(ctx context.Context, secretHash string) (Token, error) {
	secretHash = strings.ToLower(strings.TrimSpace(secretHash))
	return scanToken(s.Pool.QueryRow(ctx, `SELECT `+tokenColumns+`
        FROM access_tokens at JOIN users u ON u.id=at.user_id
        WHERE at.secret_hash=$1 AND at.revoked_at IS NULL
            AND at.expires_at > now() AND u.status='active'`, secretHash))
}

func (s *Store) ListTokens(ctx context.Context, userID string) ([]Token, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+tokenColumns+` FROM access_tokens at
        WHERE at.user_id=$1 ORDER BY at.created_at DESC,at.id DESC`, userID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	result := make([]Token, 0)
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, dbErr(rows.Err())
}

func (s *Store) RevokeToken(ctx context.Context, userID, tokenID string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE access_tokens SET revoked_at=COALESCE(revoked_at,now())
        WHERE id=$1 AND user_id=$2`, tokenID, userID)
	if err != nil {
		return dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
