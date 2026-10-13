package store

import (
	"context"
	"testing"
)

// A trigger event is recorded once; a repeated delivery is ignored.
func TestTriggerEventDeduplication(t *testing.T) {
	s, _, repo, author := ciFixture(t)
	ctx := context.Background()
	event := CITriggerEvent{
		RepoID: repo.ID, Event: "push", Ref: "refs/heads/main",
		NewSHA: testHeadSHA, ActorID: &author.ID,
	}
	recorded, created, err := s.RecordTriggerEvent(ctx, event)
	if err != nil || !created {
		t.Fatalf("first record: created=%v err=%v", created, err)
	}
	if recorded.ID == "" {
		t.Fatal("recorded event must have an id")
	}
	if _, created, err := s.RecordTriggerEvent(ctx, event); err != nil || created {
		t.Fatalf("duplicate record: created=%v err=%v", created, err)
	}
	other := event
	other.NewSHA = testBaseSHA
	if _, created, err := s.RecordTriggerEvent(ctx, other); err != nil || !created {
		t.Fatalf("different revision: created=%v err=%v", created, err)
	}
	claimed, err := s.ClaimTriggerEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d events, want 2", len(claimed))
	}
	if err := s.MarkTriggerDone(ctx, claimed[0].ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.ClaimTriggerEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID == claimed[0].ID {
		t.Fatalf("remaining = %+v", remaining)
	}
}

// A failing event is retried a bounded number of times and then abandoned with
// its reason, so a broken workflow cannot loop forever.
func TestTriggerEventRetriesThenFails(t *testing.T) {
	s, _, repo, author := ciFixture(t)
	ctx := context.Background()
	recorded, _, err := s.RecordTriggerEvent(ctx, CITriggerEvent{
		RepoID: repo.ID, Event: "push", Ref: "refs/heads/main",
		NewSHA: testHeadSHA, ActorID: &author.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.MarkTriggerFailed(ctx, recorded.ID, "workflow is invalid"); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := s.ClaimTriggerEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("failed event was still claimable: %+v", claimed)
	}
	var state string
	var attempts int
	if err := s.Pool.QueryRow(ctx, `SELECT state,attempts FROM ci_trigger_events WHERE id=$1`, recorded.ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || attempts != 5 {
		t.Fatalf("state=%s attempts=%d", state, attempts)
	}
}
