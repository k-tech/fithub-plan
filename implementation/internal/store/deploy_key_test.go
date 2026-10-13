package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRepoDeployKeyLifecycle(t *testing.T) {
	s, _, repo, _ := issueFixture(t)
	ctx := context.Background()
	info, err := s.RepoDeployKeyInfo(ctx, repo.ID)
	if err != nil || info.Configured {
		t.Fatalf("empty key info: %+v, %v", info, err)
	}
	if err := s.SetRepoDeployKey(ctx, repo.ID, "Demo", "-----BEGIN OPENSSH PRIVATE KEY-----\ndemo\n-----END OPENSSH PRIVATE KEY-----"); err != nil {
		t.Fatal(err)
	}
	info, err = s.RepoDeployKeyInfo(ctx, repo.ID)
	if err != nil || !info.Configured || info.Name != "Demo" {
		t.Fatalf("saved key info: %+v, %v", info, err)
	}
	key, err := s.GetRepoDeployKey(ctx, repo.ID)
	if err != nil || key.Name != "Demo" || key.PrivateKey == "" {
		t.Fatalf("saved key: %+v, %v", key, err)
	}
	if err := s.SetRepoDeployKey(ctx, repo.ID, "Updated", "-----BEGIN OPENSSH PRIVATE KEY-----\nnew\n-----END OPENSSH PRIVATE KEY-----"); err != nil {
		t.Fatal(err)
	}
	key, err = s.GetRepoDeployKey(ctx, repo.ID)
	if err != nil || key.Name != "Updated" || !strings.Contains(key.PrivateKey, "new") {
		t.Fatalf("updated key: %+v, %v", key, err)
	}
	if err := s.SetRepoDeployKey(ctx, repo.ID, "bad", "not-a-private-key"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid key error = %v", err)
	}
	if err := s.DeleteRepoDeployKey(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}
	info, err = s.RepoDeployKeyInfo(ctx, repo.ID)
	if err != nil || info.Configured {
		t.Fatalf("deleted key info: %+v, %v", info, err)
	}
}
