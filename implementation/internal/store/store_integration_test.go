package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Set FITHUB_TEST_DATABASE_URL to a disposable PostgreSQL database. Each test
// creates its own schema and removes it, leaving other schemas untouched.
func testStore(t *testing.T) *Store {
	t.Helper()
	dbURL := os.Getenv("FITHUB_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("FITHUB_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := fmt.Sprintf("fithub_test_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := New(pool)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	return s
}

func TestJoinApprovalRepoAndToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin, err := s.UpsertDevUser(ctx, "ADMIN@example.test", "Admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserRole(ctx, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	employee, err := s.UpsertDevUser(ctx, "employee@example.test", "Employee")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := s.UpsertDevUser(ctx, "outsider@example.test", "Outsider")
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, "engineering", "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTeam(ctx, "engineering", "Duplicate"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate team: got %v", err)
	}
	req, err := s.RequestJoin(ctx, employee.ID, team.ID)
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.RequestJoin(ctx, employee.ID, team.ID)
	if err != nil || same.ID != req.ID {
		t.Fatalf("duplicate request: %v, %+v", err, same)
	}
	if member, err := s.HasAnyTeam(ctx, employee.ID); err != nil || member {
		t.Fatalf("pending applicant has membership: %v, %v", member, err)
	}
	if _, err := s.ReviewJoin(ctx, req.ID, outsider.ID, "approved"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin review: %v", err)
	}
	if _, err := s.CreateRepo(ctx, team.ID, "premature", "private", employee.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("pending applicant created repo: %v", err)
	}
	const reviewers = 4
	results := make(chan error, reviewers)
	var wg sync.WaitGroup
	for i := 0; i < reviewers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			approved, err := s.ReviewJoin(ctx, req.ID, admin.ID, "approved")
			if err == nil && approved.Status != "approved" {
				err = fmt.Errorf("unexpected status %s", approved.Status)
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var memberCount int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM team_members WHERE user_id=$1 AND team_id=$2`, employee.ID, team.ID).Scan(&memberCount); err != nil || memberCount != 1 {
		t.Fatalf("membership count: %d, %v", memberCount, err)
	}
	if member, err := s.HasAnyTeam(ctx, employee.ID); err != nil || !member {
		t.Fatalf("approved applicant lacks membership: %v, %v", member, err)
	}
	if teams, err := s.ListUserTeams(ctx, employee.ID); err != nil || len(teams) != 1 || teams[0].ID != team.ID {
		t.Fatalf("approved applicant teams: %+v, %v", teams, err)
	}
	repo, err := s.CreateRepo(ctx, team.ID, "service", "private", employee.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repo.State != "initializing" {
		t.Fatalf("unexpected repo state: %s", repo.State)
	}
	if role, err := s.EffectiveRepoRole(ctx, employee.ID, repo.ID); err != nil || role != "admin" {
		t.Fatalf("creator role: %q, %v", role, err)
	}
	if role, err := s.EffectiveRepoRole(ctx, outsider.ID, repo.ID); err != nil || role != "" {
		t.Fatalf("outsider role: %q, %v", role, err)
	}
	if err := s.GrantRepoToUser(ctx, repo.ID, outsider.ID, "read"); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantRepoToUser(ctx, repo.ID, outsider.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if role, err := s.EffectiveRepoRole(ctx, outsider.ID, repo.ID); err != nil || role != "write" {
		t.Fatalf("updated explicit grant: %q, %v", role, err)
	}
	if err := s.GrantRepoToTeam(ctx, repo.ID, team.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRepoState(ctx, repo.ID, "active"); err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListRepos(ctx, "")
	if err != nil || len(listed) != 1 || listed[0].ID != repo.ID {
		t.Fatalf("all active repos listing: %+v, %v", listed, err)
	}
	if _, err := s.CreateRepo(ctx, team.ID, "service", "private", employee.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate repo slug: %v", err)
	}
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	token, err := s.CreateToken(ctx, employee.ID, "repository", repo.ID, "write", "fh_test", hash, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if found, err := s.LookupToken(ctx, hash); err != nil || found.ID != token.ID {
		t.Fatalf("token lookup: %+v, %v", found, err)
	}
	if err := s.RevokeToken(ctx, employee.ID, token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupToken(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token lookup: %v", err)
	}
	if err := s.SetUserStatus(ctx, employee.ID, "suspended"); err != nil {
		t.Fatal(err)
	}
	repeated, err := s.UpsertDevUser(ctx, employee.Email, "Renamed")
	if err != nil || repeated.Status != "suspended" {
		t.Fatalf("sign-in restored suspended account: %+v, %v", repeated, err)
	}
}

func TestCancelledJoinCannotBeApproved(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin, err := s.UpsertDevUser(ctx, "admin@example.test", "Admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserRole(ctx, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	employee, err := s.UpsertDevUser(ctx, "employee@example.test", "Employee")
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, "design", "Design")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.RequestJoin(ctx, employee.ID, team.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.CancelJoin(ctx, employee.ID, req.ID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel: %+v, %v", cancelled, err)
	}
	if _, err := s.ReviewJoin(ctx, req.ID, admin.ID, "approved"); !errors.Is(err, ErrConflict) {
		t.Fatalf("approved cancelled request: %v", err)
	}
	if member, err := s.HasAnyTeam(ctx, employee.ID); err != nil || member {
		t.Fatalf("cancelled applicant has membership: %v, %v", member, err)
	}
	next, err := s.RequestJoin(ctx, employee.ID, team.ID)
	if err != nil || next.ID == req.ID {
		t.Fatalf("new request after cancellation: %+v, %v", next, err)
	}
}
