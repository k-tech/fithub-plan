package store

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

const teamColumns = `id::text, slug, name, created_at`

func (s *Store) ListTeams(ctx context.Context) ([]Team, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+teamColumns+` FROM teams ORDER BY slug`)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	teams := make([]Team, 0)
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.CreatedAt); err != nil {
			return nil, dbErr(err)
		}
		teams = append(teams, t)
	}
	return teams, dbErr(rows.Err())
}

func (s *Store) ListUserTeams(ctx context.Context, userID string) ([]Team, error) {
	rows, err := s.Pool.Query(ctx, `
        SELECT t.id::text, t.slug, t.name, t.created_at
        FROM teams t JOIN team_members tm ON tm.team_id=t.id
        JOIN users u ON u.id=tm.user_id
        WHERE tm.user_id=$1 AND u.status='active'
        ORDER BY t.slug`, userID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	teams := make([]Team, 0)
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.CreatedAt); err != nil {
			return nil, dbErr(err)
		}
		teams = append(teams, t)
	}
	return teams, dbErr(rows.Err())
}

func (s *Store) HasAnyTeam(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	var ok bool
	err := s.Pool.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1 FROM team_members tm JOIN users u ON u.id=tm.user_id
            WHERE tm.user_id=$1 AND u.status='active'
        )`, userID).Scan(&ok)
	return ok, dbErr(err)
}

func (s *Store) CreateTeam(ctx context.Context, slug, name string) (Team, error) {
	slug, name = strings.TrimSpace(slug), strings.TrimSpace(name)
	if slug == "" || name == "" {
		return Team{}, invalid("slug and name are required")
	}
	var t Team
	err := s.Pool.QueryRow(ctx, `INSERT INTO teams (slug,name) VALUES ($1,$2) RETURNING `+teamColumns,
		slug, name).Scan(&t.ID, &t.Slug, &t.Name, &t.CreatedAt)
	return t, dbErr(err)
}

const joinRequestColumns = `
    jr.id::text, jr.user_id::text, u.email, u.name,
    jr.team_id::text, t.slug, t.name, jr.status,
    jr.reviewed_by::text, jr.created_at, jr.reviewed_at`

const joinRequestFrom = `
    FROM team_join_requests jr
    JOIN users u ON u.id=jr.user_id
    JOIN teams t ON t.id=jr.team_id`

type scanner interface{ Scan(dest ...any) error }

func scanJoinRequest(row scanner) (JoinRequest, error) {
	var jr JoinRequest
	err := row.Scan(&jr.ID, &jr.UserID, &jr.UserEmail, &jr.UserName,
		&jr.TeamID, &jr.TeamSlug, &jr.TeamName, &jr.Status,
		&jr.ReviewedBy, &jr.CreatedAt, &jr.ReviewedAt)
	return jr, dbErr(err)
}

func getJoinRequest(ctx context.Context, tx pgx.Tx, id string) (JoinRequest, error) {
	return scanJoinRequest(tx.QueryRow(ctx, `SELECT `+joinRequestColumns+joinRequestFrom+` WHERE jr.id=$1`, id))
}

// RequestJoin serializes requests and reviews for the same user. A duplicate
// pending request returns the existing record; historical rejections and
// cancellations do not prevent a new request.
func (s *Store) RequestJoin(ctx context.Context, userID, teamID string) (JoinRequest, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return JoinRequest{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&status)
	if err != nil {
		return JoinRequest{}, dbErr(err)
	}
	if status != "active" {
		return JoinRequest{}, ErrForbidden
	}
	var teamExists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM teams WHERE id=$1)`, teamID).Scan(&teamExists)
	if err != nil {
		return JoinRequest{}, dbErr(err)
	}
	if !teamExists {
		return JoinRequest{}, ErrNotFound
	}
	var member bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_members WHERE user_id=$1 AND team_id=$2)`, userID, teamID).Scan(&member)
	if err != nil {
		return JoinRequest{}, dbErr(err)
	}
	if member {
		return JoinRequest{}, ErrConflict
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM team_join_requests
        WHERE user_id=$1 AND team_id=$2 AND status='pending'`, userID, teamID).Scan(&id)
	if err == nil {
		jr, err := getJoinRequest(ctx, tx, id)
		if err != nil {
			return JoinRequest{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return JoinRequest{}, dbErr(err)
		}
		return jr, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return JoinRequest{}, dbErr(err)
	}
	err = tx.QueryRow(ctx, `INSERT INTO team_join_requests (user_id,team_id)
        VALUES ($1,$2) RETURNING id::text`, userID, teamID).Scan(&id)
	if err != nil {
		return JoinRequest{}, dbErr(err)
	}
	jr, err := getJoinRequest(ctx, tx, id)
	if err != nil {
		return JoinRequest{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return JoinRequest{}, dbErr(err)
	}
	return jr, nil
}

func (s *Store) ListMyRequests(ctx context.Context, userID string) ([]JoinRequest, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+joinRequestColumns+joinRequestFrom+`
        WHERE jr.user_id=$1 ORDER BY jr.created_at DESC, jr.id DESC`, userID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	result := make([]JoinRequest, 0)
	for rows.Next() {
		jr, err := scanJoinRequest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, jr)
	}
	return result, dbErr(rows.Err())
}

// Caller must establish platform-admin session authorization before invoking.
func (s *Store) ListPendingRequests(ctx context.Context) ([]JoinRequest, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+joinRequestColumns+joinRequestFrom+`
        WHERE jr.status='pending' ORDER BY jr.created_at, jr.id`)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	result := make([]JoinRequest, 0)
	for rows.Next() {
		jr, err := scanJoinRequest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, jr)
	}
	return result, dbErr(rows.Err())
}

// ReviewJoin atomically changes a pending request and grants membership.
// Repeating the same decision returns the prior result without changing it.
func (s *Store) ReviewJoin(ctx context.Context, requestID, reviewerID, decision string) (JoinRequest, error) {
	if decision != "approved" && decision != "rejected" {
		return JoinRequest{}, invalid("decision")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return JoinRequest{}, err
	}
	defer tx.Rollback(ctx)
	var applicantID string
	err = tx.QueryRow(ctx, `SELECT user_id::text FROM team_join_requests WHERE id=$1`, requestID).Scan(&applicantID)
	if err != nil {
		return JoinRequest{}, dbErr(err)
	}
	// Lock both identities in stable order. This keeps role/suspension changes
	// from racing with approval and avoids deadlocks when admins review each
	// other's requests concurrently.
	ids := []string{applicantID}
	if reviewerID != applicantID {
		ids = append(ids, reviewerID)
	}
	sort.Strings(ids)
	var applicantStatus, reviewerRole, reviewerStatus string
	for _, id := range ids {
		var role, status string
		err = tx.QueryRow(ctx, `SELECT role,status FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&role, &status)
		if err != nil {
			return JoinRequest{}, dbErr(err)
		}
		if id == applicantID {
			applicantStatus = status
		}
		if id == reviewerID {
			reviewerRole, reviewerStatus = role, status
		}
	}
	if reviewerRole != "admin" || reviewerStatus != "active" {
		return JoinRequest{}, ErrForbidden
	}
	var currentStatus, teamID string
	err = tx.QueryRow(ctx, `SELECT status, team_id::text FROM team_join_requests
        WHERE id=$1 FOR UPDATE`, requestID).Scan(&currentStatus, &teamID)
	if err != nil {
		return JoinRequest{}, dbErr(err)
	}
	if currentStatus == decision {
		jr, err := getJoinRequest(ctx, tx, requestID)
		if err != nil {
			return JoinRequest{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return JoinRequest{}, dbErr(err)
		}
		return jr, nil
	}
	if currentStatus != "pending" {
		return JoinRequest{}, ErrConflict
	}
	if applicantStatus != "active" {
		return JoinRequest{}, ErrForbidden
	}
	if _, err = tx.Exec(ctx, `UPDATE team_join_requests SET status=$2,
        reviewed_by=$3, reviewed_at=now() WHERE id=$1`, requestID, decision, reviewerID); err != nil {
		return JoinRequest{}, dbErr(err)
	}
	if decision == "approved" {
		if _, err = tx.Exec(ctx, `INSERT INTO team_members (team_id,user_id,approved_request_id)
            VALUES ($1,$2,$3) ON CONFLICT (team_id,user_id) DO NOTHING`, teamID, applicantID, requestID); err != nil {
			return JoinRequest{}, dbErr(err)
		}
	}
	jr, err := getJoinRequest(ctx, tx, requestID)
	if err != nil {
		return JoinRequest{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return JoinRequest{}, dbErr(err)
	}
	return jr, nil
}

func (s *Store) CancelJoin(ctx context.Context, userID, requestID string) (JoinRequest, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return JoinRequest{}, err
	}
	defer tx.Rollback(ctx)
	var statusOfUser string
	if err = tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&statusOfUser); err != nil {
		return JoinRequest{}, dbErr(err)
	}
	if statusOfUser != "active" {
		return JoinRequest{}, ErrForbidden
	}
	var ownerID, status string
	err = tx.QueryRow(ctx, `SELECT user_id::text,status FROM team_join_requests WHERE id=$1 FOR UPDATE`, requestID).
		Scan(&ownerID, &status)
	if err != nil {
		return JoinRequest{}, dbErr(err)
	}
	if ownerID != userID {
		return JoinRequest{}, ErrNotFound
	}
	if status != "pending" && status != "cancelled" {
		return JoinRequest{}, ErrConflict
	}
	if status == "pending" {
		if _, err = tx.Exec(ctx, `UPDATE team_join_requests SET status='cancelled',reviewed_at=now()
            WHERE id=$1`, requestID); err != nil {
			return JoinRequest{}, dbErr(err)
		}
	}
	jr, err := getJoinRequest(ctx, tx, requestID)
	if err != nil {
		return JoinRequest{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return JoinRequest{}, dbErr(err)
	}
	return jr, nil
}
