package store

import (
	"context"
	"strings"
	"time"
)

// UpsertGoogleUser binds an account solely to Google's immutable subject.
// An existing local account with the same email is intentionally not linked.
func (s *Store) UpsertGoogleUser(ctx context.Context, subject, email, name string) (User, error) {
	email = cleanEmail(email)
	name = strings.TrimSpace(name)
	if subject == "" || subject != strings.TrimSpace(subject) || len(subject) > 255 || email == "" || len(email) > 320 || name == "" || len(name) > 200 {
		return User{}, invalid("google identity")
	}
	var u User
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO users (google_sub, email, name) VALUES ($1, $2, $3)
		ON CONFLICT (google_sub) DO UPDATE
		SET email=EXCLUDED.email, name=EXCLUDED.name, updated_at=now()
		RETURNING `+userColumns, subject, email, name).
		Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.CreatedAt)
	return u, dbErr(err)
}

func (s *Store) CreateBrowserSession(ctx context.Context, userID, secretHash string, expiresAt time.Time) error {
	if len(secretHash) != 64 || !expiresAt.After(time.Now()) {
		return invalid("browser session")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return dbErr(err)
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&status); err != nil {
		return dbErr(err)
	}
	if status != "active" {
		return ErrForbidden
	}
	if _, err := tx.Exec(ctx, `INSERT INTO browser_sessions (user_id, secret_hash, expires_at) VALUES ($1,$2,$3)`, userID, secretHash, expiresAt); err != nil {
		return dbErr(err)
	}
	return dbErr(tx.Commit(ctx))
}

// LookupBrowserSession returns only an unexpired, unrevoked session for an
// active account. A suspension therefore takes effect on the next request.
func (s *Store) LookupBrowserSession(ctx context.Context, secretHash string) (User, error) {
	var u User
	err := s.Pool.QueryRow(ctx, `
		SELECT u.id::text, u.email, u.name, u.role, u.status, u.created_at FROM users u
		JOIN browser_sessions bs ON bs.user_id=u.id
		WHERE bs.secret_hash=$1 AND bs.revoked_at IS NULL
		AND bs.expires_at>now() AND u.status='active'`, secretHash).
		Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.CreatedAt)
	return u, dbErr(err)
}

func (s *Store) RevokeBrowserSession(ctx context.Context, secretHash string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE browser_sessions SET revoked_at=now() WHERE secret_hash=$1 AND revoked_at IS NULL`, secretHash)
	return dbErr(err)
}

func (s *Store) CreateOIDCLoginAttempt(ctx context.Context, stateHash, verifier, nonce string, expiresAt time.Time) error {
	if len(stateHash) != 64 || len(verifier) < 43 || len(verifier) > 128 || len(nonce) < 32 || len(nonce) > 128 || !expiresAt.After(time.Now()) {
		return invalid("OIDC login attempt")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO oidc_login_attempts (state_hash, code_verifier, nonce, expires_at) VALUES ($1,$2,$3,$4)`, stateHash, verifier, nonce, expiresAt)
	return dbErr(err)
}

// ConsumeOIDCLoginAttempt is atomic and single-use. It consumes the state
// before token exchange, so retries cannot replay a completed authorization.
func (s *Store) ConsumeOIDCLoginAttempt(ctx context.Context, stateHash string) (verifier, nonce string, err error) {
	err = s.Pool.QueryRow(ctx, `
		UPDATE oidc_login_attempts SET consumed_at=now()
		WHERE state_hash=$1 AND consumed_at IS NULL AND expires_at>now()
		RETURNING code_verifier, nonce`, stateHash).Scan(&verifier, &nonce)
	return verifier, nonce, dbErr(err)
}

func (s *Store) PruneExpiredAuth(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM oidc_login_attempts WHERE expires_at < now()-interval '1 day'`); err != nil {
		return dbErr(err)
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM browser_sessions WHERE expires_at < now()-interval '1 day'`)
	return dbErr(err)
}
