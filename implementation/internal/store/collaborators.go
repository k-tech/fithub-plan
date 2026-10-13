package store

import (
	"context"
	"regexp"
	"strings"
)

var collaboratorIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Collaborator struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Status string `json:"status"`
	Source string `json:"source"`
}

func (s *Store) ListRepoCollaborators(ctx context.Context, repoID string) ([]Collaborator, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT u.id::text,u.name,u.email,'admin'::text,u.status,'creator'::text
		FROM repositories r JOIN users u ON u.id=r.created_by WHERE r.id=$1
		UNION ALL
		SELECT u.id::text,u.name,u.email,g.role,u.status,'direct'::text
		FROM repositories r JOIN repo_grants g ON g.repo_id=r.id
		JOIN users u ON u.id=g.user_id
		WHERE r.id=$1 AND g.user_id<>r.created_by
		ORDER BY 6,3`, repoID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	result := make([]Collaborator, 0)
	for rows.Next() {
		var item Collaborator
		if err := rows.Scan(&item.UserID, &item.Name, &item.Email, &item.Role, &item.Status, &item.Source); err != nil {
			return nil, dbErr(err)
		}
		result = append(result, item)
	}
	return result, dbErr(rows.Err())
}

// SetRepoCollaborator grants one already-registered active employee access.
// The account must have joined some Team; there is no email invitation flow.
func (s *Store) SetRepoCollaborator(ctx context.Context, repoID, email, role string) (Collaborator, error) {
	email = cleanEmail(email)
	if email == "" || len(email) > 320 || !strings.Contains(email, "@") || !validRole(role) {
		return Collaborator{}, invalid("collaborator email or role")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Collaborator{}, err
	}
	defer tx.Rollback(ctx)
	var creatorID string
	err = tx.QueryRow(ctx, `SELECT created_by::text FROM repositories WHERE id=$1 AND state='active' FOR UPDATE`, repoID).Scan(&creatorID)
	if err != nil {
		return Collaborator{}, dbErr(err)
	}
	var item Collaborator
	err = tx.QueryRow(ctx, `SELECT id::text,name,email,status FROM users WHERE email=$1 FOR SHARE`, email).
		Scan(&item.UserID, &item.Name, &item.Email, &item.Status)
	if err != nil {
		return Collaborator{}, dbErr(err)
	}
	if item.Status != "active" {
		return Collaborator{}, ErrForbidden
	}
	var member bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_members WHERE user_id=$1)`, item.UserID).Scan(&member)
	if err != nil {
		return Collaborator{}, dbErr(err)
	}
	if !member {
		return Collaborator{}, ErrForbidden
	}
	if item.UserID == creatorID {
		if role != "admin" {
			return Collaborator{}, ErrForbidden
		}
		item.Role, item.Source = "admin", "creator"
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO repo_grants (repo_id,user_id,role) VALUES ($1,$2,$3)
			ON CONFLICT (repo_id,user_id) WHERE user_id IS NOT NULL
			DO UPDATE SET role=EXCLUDED.role`, repoID, item.UserID, role)
		if err != nil {
			return Collaborator{}, dbErr(err)
		}
		item.Role, item.Source = role, "direct"
	}
	if err := tx.Commit(ctx); err != nil {
		return Collaborator{}, dbErr(err)
	}
	return item, nil
}

func (s *Store) DeleteRepoCollaborator(ctx context.Context, repoID, userID string) error {
	if !collaboratorIDPattern.MatchString(userID) {
		return invalid("collaborator ID")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var creatorID string
	err = tx.QueryRow(ctx, `SELECT created_by::text FROM repositories WHERE id=$1 AND state='active' FOR UPDATE`, repoID).Scan(&creatorID)
	if err != nil {
		return dbErr(err)
	}
	if userID == creatorID {
		return ErrForbidden
	}
	tag, err := tx.Exec(ctx, `DELETE FROM repo_grants WHERE repo_id=$1 AND user_id=$2`, repoID, userID)
	if err != nil {
		return dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return dbErr(tx.Commit(ctx))
}
