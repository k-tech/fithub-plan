package store

import (
	"context"
	"strings"
)

const userColumns = `id::text, email, name, role, status, created_at`

// UpsertDevUser is for local development identities only. Repeated sign-in never
// restores a suspended account or changes a platform role.
func (s *Store) UpsertDevUser(ctx context.Context, email, name string) (User, error) {
	email = cleanEmail(email)
	name = strings.TrimSpace(name)
	if email == "" || name == "" {
		return User{}, invalid("email and name are required")
	}
	var u User
	err := s.Pool.QueryRow(ctx, `
        INSERT INTO users (email, name) VALUES ($1, $2)
        ON CONFLICT (email) DO UPDATE SET name=EXCLUDED.name, updated_at=now()
        RETURNING `+userColumns, email, name).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.CreatedAt)
	return u, dbErr(err)
}

func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	var u User
	err := s.Pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1`, id).
		Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.CreatedAt)
	return u, dbErr(err)
}

// SetUserRole is intended for explicit local admin bootstrap and later audited
// platform administration. No sign-in path should call it.
func (s *Store) SetUserRole(ctx context.Context, userID, role string) error {
	if role != "employee" && role != "admin" {
		return invalid("role")
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE users SET role=$2, updated_at=now() WHERE id=$1`, userID, role)
	if err != nil {
		return dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetUserStatus(ctx context.Context, userID, status string) error {
	if status != "active" && status != "suspended" {
		return invalid("status")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return dbErr(err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE users SET status=$2, updated_at=now() WHERE id=$1`, userID, status)
	if err != nil {
		return dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if status == "suspended" {
		if _, err := tx.Exec(ctx, `UPDATE browser_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
			return dbErr(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE access_tokens SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
			return dbErr(err)
		}
	}
	return dbErr(tx.Commit(ctx))
}
