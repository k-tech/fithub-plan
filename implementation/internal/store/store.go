// Package store owns FitHub's PostgreSQL persistence for the first vertical slice.
// Callers are responsible for authenticating requests and checking route-level
// authorization; mutations here additionally enforce their critical invariants.
package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound  = errors.New("store: not found")
	ErrConflict  = errors.New("store: conflict")
	ErrForbidden = errors.New("store: forbidden")
	ErrInvalid   = errors.New("store: invalid input")
)

type Store struct {
	Pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{Pool: pool} }

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type Team struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type JoinRequest struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	UserEmail  string     `json:"user_email"`
	UserName   string     `json:"user_name"`
	TeamID     string     `json:"team_id"`
	TeamSlug   string     `json:"team_slug"`
	TeamName   string     `json:"team_name"`
	Status     string     `json:"status"`
	ReviewedBy *string    `json:"reviewed_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
}

type Repository struct {
	ID         string    `json:"id"`
	TeamID     string    `json:"team_id"`
	TeamSlug   string    `json:"team_slug"`
	TeamName   string    `json:"team_name"`
	Slug       string    `json:"slug"`
	Visibility string    `json:"visibility"`
	State      string    `json:"state"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
	// DefaultBranch is the branch shown as the repository default and the usual
	// target of a pull request. It starts as main and an administrator may change
	// it once that branch exists.
	DefaultBranch string `json:"default_branch"`
}

type Token struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Scope      string     `json:"scope"`
	RepoID     *string    `json:"repo_id,omitempty"`
	Level      string     `json:"level"`
	Prefix     string     `json:"prefix"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func dbErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrConflict
		case "23503", "23514", "23502", "22P02":
			return ErrInvalid
		}
	}
	return err
}

func invalid(field string) error { return fmt.Errorf("%w: %s", ErrInvalid, field) }

func validRole(role string) bool { return role == "read" || role == "write" || role == "admin" }

func cleanEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }
