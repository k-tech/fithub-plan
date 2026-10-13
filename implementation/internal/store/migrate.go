package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
)

// migrationFiles is also exposed through db/migrations for operator-run SQL.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate applies each embedded SQL migration once. A transaction-level advisory
// lock serializes concurrent instances during startup.
func (s *Store) Migrate(ctx context.Context) error {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	for _, name := range names {
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		// Rollback after Commit is harmless and covers all early returns.
		func() {
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(727361219)"); err != nil {
				return
			}
			if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
                version text PRIMARY KEY,
                applied_at timestamptz NOT NULL DEFAULT now()
            )`); err != nil {
				return
			}
			var applied bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, path.Base(name)).Scan(&applied)
			if err != nil {
				return
			}
			if !applied {
				var sql []byte
				sql, err = migrationFiles.ReadFile(name)
				if err != nil {
					return
				}
				if _, err = tx.Exec(ctx, string(sql)); err != nil {
					return
				}
				if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, path.Base(name)); err != nil {
					return
				}
			}
			err = tx.Commit(ctx)
		}()
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}
