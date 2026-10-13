package store

import (
	"context"
	"strings"
)

const repoColumns = `
    r.id::text, r.team_id::text, t.slug, t.name, r.slug,
    r.visibility, r.state, r.created_by::text, r.created_at, r.default_branch`

func scanRepo(row scanner) (Repository, error) {
	var r Repository
	err := row.Scan(&r.ID, &r.TeamID, &r.TeamSlug, &r.TeamName, &r.Slug,
		&r.Visibility, &r.State, &r.CreatedBy, &r.CreatedAt, &r.DefaultBranch)
	return r, dbErr(err)
}

// ListRepos returns active repositories of every visibility. The caller must
// run its shared read policy on each row before exposing it to a user.
func (s *Store) ListRepos(ctx context.Context, _ string) ([]Repository, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+repoColumns+`
        FROM repositories r JOIN teams t ON t.id=r.team_id
        WHERE r.state='active' ORDER BY t.slug,r.slug`)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	result := make([]Repository, 0)
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, dbErr(rows.Err())
}

// ListRepositoryIDs returns every repository that needs a current receive hook.
func (s *Store) ListRepositoryIDs(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text FROM repositories ORDER BY id`)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, dbErr(err)
		}
		ids = append(ids, id)
	}
	return ids, dbErr(rows.Err())
}

func (s *Store) GetRepoBySlug(ctx context.Context, teamSlug, repoSlug string) (Repository, error) {
	return scanRepo(s.Pool.QueryRow(ctx, `SELECT `+repoColumns+`
        FROM repositories r JOIN teams t ON t.id=r.team_id
        WHERE t.slug=$1 AND r.slug=$2`, teamSlug, repoSlug))
}

func (s *Store) GetRepo(ctx context.Context, repoID string) (Repository, error) {
	return scanRepo(s.Pool.QueryRow(ctx, `SELECT `+repoColumns+`
        FROM repositories r JOIN teams t ON t.id=r.team_id WHERE r.id=$1`, repoID))
}

// CreateRepo requires an active member of the owning team and grants the
// creator repo admin. Team membership itself grants no repo access.
func (s *Store) CreateRepo(ctx context.Context, teamID, slug, visibility, creatorID string) (Repository, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return Repository{}, invalid("slug")
	}
	if visibility == "" {
		visibility = "internal"
	}
	if visibility != "public" && visibility != "internal" && visibility != "private" {
		return Repository{}, invalid("visibility")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Repository{}, err
	}
	defer tx.Rollback(ctx)
	var userStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 FOR UPDATE`, creatorID).Scan(&userStatus)
	if err != nil {
		return Repository{}, dbErr(err)
	}
	if userStatus != "active" {
		return Repository{}, ErrForbidden
	}
	var member bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_members WHERE user_id=$1 AND team_id=$2)`,
		creatorID, teamID).Scan(&member)
	if err != nil {
		return Repository{}, dbErr(err)
	}
	if !member {
		return Repository{}, ErrForbidden
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO repositories (team_id,slug,visibility,created_by)
        VALUES ($1,$2,$3,$4) RETURNING id::text`, teamID, slug, visibility, creatorID).Scan(&id)
	if err != nil {
		return Repository{}, dbErr(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO repo_grants (repo_id,user_id,role) VALUES ($1,$2,'admin')`, id, creatorID)
	if err != nil {
		return Repository{}, dbErr(err)
	}
	r, err := scanRepo(tx.QueryRow(ctx, `SELECT `+repoColumns+`
        FROM repositories r JOIN teams t ON t.id=r.team_id WHERE r.id=$1`, id))
	if err != nil {
		return Repository{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Repository{}, dbErr(err)
	}
	return r, nil
}

func (s *Store) SetRepoState(ctx context.Context, repoID, state string) error {
	if state != "initializing" && state != "active" && state != "failed" {
		return invalid("state")
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE repositories SET state=$2 WHERE id=$1`, repoID, state)
	if err != nil {
		return dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EffectiveRepoRole returns the strongest explicit role. It deliberately does
// not include public/internal read visibility, account status, or token limits.
func (s *Store) EffectiveRepoRole(ctx context.Context, userID, repoID string) (string, error) {
	if userID == "" {
		return "", nil
	}
	var role string
	err := s.Pool.QueryRow(ctx, `
        SELECT COALESCE((
            SELECT grants.role FROM (
                SELECT 'admin'::text AS role WHERE r.created_by=$2
                UNION ALL
                SELECT g.role FROM repo_grants g
                    WHERE g.repo_id=r.id AND g.user_id=$2
                UNION ALL
                SELECT g.role FROM repo_grants g
                    JOIN team_members tm ON tm.team_id=g.team_id
                    WHERE g.repo_id=r.id AND tm.user_id=$2
            ) grants
            ORDER BY CASE grants.role WHEN 'admin' THEN 3 WHEN 'write' THEN 2 ELSE 1 END DESC
            LIMIT 1
        ), '')
        FROM repositories r WHERE r.id=$1`, repoID, userID).Scan(&role)
	return role, dbErr(err)
}

// GrantRepoToUser and GrantRepoToTeam require a repo-admin authorization check
// by the caller; they are storage operations, not public endpoints.
func (s *Store) GrantRepoToUser(ctx context.Context, repoID, userID, role string) error {
	if !validRole(role) {
		return invalid("role")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO repo_grants (repo_id,user_id,role) VALUES ($1,$2,$3)
        ON CONFLICT (repo_id,user_id) WHERE user_id IS NOT NULL
        DO UPDATE SET role=EXCLUDED.role`, repoID, userID, role)
	return dbErr(err)
}

func (s *Store) GrantRepoToTeam(ctx context.Context, repoID, teamID, role string) error {
	if !validRole(role) {
		return invalid("role")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO repo_grants (repo_id,team_id,role) VALUES ($1,$2,$3)
        ON CONFLICT (repo_id,team_id) WHERE team_id IS NOT NULL
        DO UPDATE SET role=EXCLUDED.role`, repoID, teamID, role)
	return dbErr(err)
}
