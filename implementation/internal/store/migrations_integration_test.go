package store

import (
	"context"
	"testing"
)

func TestMigrateRemovesBranchProtectionTable(t *testing.T) {
	s := testStore(t)
	var removed bool
	if err := s.Pool.QueryRow(context.Background(), `SELECT to_regclass('branch_protections') IS NULL`).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("branch_protections table still exists after migrations")
	}
}
