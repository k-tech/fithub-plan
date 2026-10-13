package store

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// RepoDeployKey is a repository's demo deployment credential. The private key
// is never included in list or settings responses.
type RepoDeployKey struct {
	RepoID     string
	Name       string
	PrivateKey string
}

type RepoDeployKeyInfo struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
}

func (s *Store) SetRepoDeployKey(ctx context.Context, repoID, name, privateKey string) error {
	name = strings.TrimSpace(name)
	privateKey = strings.TrimSpace(privateKey)
	if name == "" || len(name) > 128 || !utf8.ValidString(name) || privateKey == "" || len(privateKey) > 16<<10 || !utf8.ValidString(privateKey) || !strings.Contains(privateKey, "PRIVATE KEY") {
		return invalid("repository deploy key")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO repo_deploy_keys (repo_id,name,private_key)
		VALUES ($1,$2,$3) ON CONFLICT (repo_id) DO UPDATE SET name=EXCLUDED.name,
		private_key=EXCLUDED.private_key,updated_at=now()`, repoID, name, privateKey)
	return dbErr(err)
}

func (s *Store) GetRepoDeployKey(ctx context.Context, repoID string) (RepoDeployKey, error) {
	var key RepoDeployKey
	err := s.Pool.QueryRow(ctx, `SELECT repo_id::text,name,private_key FROM repo_deploy_keys WHERE repo_id=$1`, repoID).
		Scan(&key.RepoID, &key.Name, &key.PrivateKey)
	if err != nil {
		return RepoDeployKey{}, dbErr(err)
	}
	return key, nil
}

func (s *Store) RepoDeployKeyInfo(ctx context.Context, repoID string) (RepoDeployKeyInfo, error) {
	var info RepoDeployKeyInfo
	err := s.Pool.QueryRow(ctx, `SELECT name,true FROM repo_deploy_keys WHERE repo_id=$1`, repoID).Scan(&info.Name, &info.Configured)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RepoDeployKeyInfo{Configured: false}, nil
		}
		return RepoDeployKeyInfo{}, dbErr(err)
	}
	return info, nil
}

func (s *Store) DeleteRepoDeployKey(ctx context.Context, repoID string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM repo_deploy_keys WHERE repo_id=$1`, repoID)
	return dbErr(err)
}
