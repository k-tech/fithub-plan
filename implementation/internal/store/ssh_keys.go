package store

import (
	"context"
	"strings"
	"time"
)

type SSHKey struct {
	ID          string     `json:"id"`
	UserID      string     `json:"-"`
	Name        string     `json:"name"`
	KeyType     string     `json:"keyType"`
	PublicKey   string     `json:"publicKey"`
	Fingerprint string     `json:"fingerprint"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
}

const sshKeyColumns = `id::text, user_id::text, name, key_type, public_key, fingerprint, created_at, last_used_at`

func scanSSHKey(row scanner) (SSHKey, error) {
	var k SSHKey
	err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.KeyType, &k.PublicKey, &k.Fingerprint, &k.CreatedAt, &k.LastUsedAt)
	return k, dbErr(err)
}

// CreateSSHKey locks the user while creating a key, so concurrent suspension
// cannot leave a newly accepted key behind a completed revocation.
func (s *Store) CreateSSHKey(ctx context.Context, userID, name, keyType, publicKey, fingerprint string) (SSHKey, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 || publicKey == "" || len(publicKey) > 8192 || keyType == "" || fingerprint == "" {
		return SSHKey{}, invalid("SSH key")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return SSHKey{}, dbErr(err)
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&status); err != nil {
		return SSHKey{}, dbErr(err)
	}
	if status != "active" {
		return SSHKey{}, ErrForbidden
	}
	k, err := scanSSHKey(tx.QueryRow(ctx, `INSERT INTO ssh_public_keys (user_id,name,key_type,public_key,fingerprint)
		VALUES ($1,$2,$3,$4,$5) RETURNING `+sshKeyColumns, userID, name, keyType, publicKey, fingerprint))
	if err != nil {
		return SSHKey{}, err
	}
	return k, dbErr(tx.Commit(ctx))
}

func (s *Store) ListSSHKeys(ctx context.Context, userID string) ([]SSHKey, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+sshKeyColumns+` FROM ssh_public_keys WHERE user_id=$1 ORDER BY created_at DESC,id DESC`, userID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	keys := make([]SSHKey, 0)
	for rows.Next() {
		k, err := scanSSHKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, dbErr(rows.Err())
}

func (s *Store) DeleteSSHKey(ctx context.Context, userID, keyID string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM ssh_public_keys WHERE id=$1 AND user_id=$2`, keyID, userID)
	if err != nil {
		return dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// LookupSSHKeyByFingerprint and LookupSSHKeyByID both require an active user.
// The latter is repeated at forced-command execution so deletion or suspension
// between public-key authentication and a Git command fails closed.
func (s *Store) LookupSSHKeyByFingerprint(ctx context.Context, fingerprint string) (SSHKey, User, error) {
	return s.lookupSSHKey(ctx, `k.fingerprint=$1`, fingerprint)
}

func (s *Store) LookupSSHKeyByID(ctx context.Context, keyID string) (SSHKey, User, error) {
	return s.lookupSSHKey(ctx, `k.id=$1`, keyID)
}

func (s *Store) lookupSSHKey(ctx context.Context, predicate, value string) (SSHKey, User, error) {
	var k SSHKey
	var u User
	err := s.Pool.QueryRow(ctx, `SELECT k.id::text,k.user_id::text,k.name,k.key_type,k.public_key,k.fingerprint,k.created_at,k.last_used_at,
		u.id::text,u.email,u.name,u.role,u.status,u.created_at
		FROM ssh_public_keys k JOIN users u ON u.id=k.user_id
		WHERE `+predicate+` AND u.status='active'`, value).Scan(
		&k.ID, &k.UserID, &k.Name, &k.KeyType, &k.PublicKey, &k.Fingerprint, &k.CreatedAt, &k.LastUsedAt,
		&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.CreatedAt,
	)
	return k, u, dbErr(err)
}

func (s *Store) TouchSSHKey(ctx context.Context, keyID string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE ssh_public_keys k SET last_used_at=now()
		FROM users u WHERE k.id=$1 AND u.id=k.user_id AND u.status='active'`, keyID)
	if err != nil {
		return dbErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
