package store

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// CITriggerEvent is a queued CI trigger. The receive hook and the API record one
// and the server's worker turns it into runs, so a push never waits on workflow
// validation.
type CITriggerEvent struct {
	ID           string     `json:"id"`
	RepoID       string     `json:"repoId"`
	Event        string     `json:"event"`
	Ref          string     `json:"ref"`
	OldSHA       *string    `json:"oldSha"`
	NewSHA       string     `json:"newSha"`
	PullNumber   *int64     `json:"pullNumber"`
	ActorID      *string    `json:"actorId"`
	WorkflowPath *string    `json:"workflowPath"`
	State        string     `json:"state"`
	Attempts     int        `json:"attempts"`
	LastError    *string    `json:"lastError"`
	CreatedAt    time.Time  `json:"createdAt"`
	ProcessedAt  *time.Time `json:"processedAt"`
}

// RecordTriggerEvent queues a trigger. A repeated delivery of the same event is
// ignored rather than queued twice, so a retried hook or a replayed delivery
// cannot start duplicate runs.
func (s *Store) RecordTriggerEvent(ctx context.Context, event CITriggerEvent) (CITriggerEvent, bool, error) {
	if !canonicalStoreUUID.MatchString(event.RepoID) || !validGitSHA(event.NewSHA) ||
		(event.Event != "push" && event.Event != "pull_request" && event.Event != "workflow_dispatch") ||
		event.Ref == "" || len(event.Ref) > 512 ||
		(event.OldSHA != nil && !validGitSHA(*event.OldSHA)) ||
		(event.ActorID != nil && !canonicalStoreUUID.MatchString(*event.ActorID)) ||
		(event.WorkflowPath != nil && *event.WorkflowPath != "" && len(*event.WorkflowPath) > 512) {
		return CITriggerEvent{}, false, invalid("trigger event")
	}
	var pullNumber any
	if event.PullNumber != nil && *event.PullNumber > 0 {
		pullNumber = *event.PullNumber
	}
	var actor, oldSHA, workflowPath any
	if event.ActorID != nil {
		actor = *event.ActorID
	}
	if event.OldSHA != nil {
		oldSHA = *event.OldSHA
	}
	if event.WorkflowPath != nil && *event.WorkflowPath != "" {
		workflowPath = *event.WorkflowPath
	}
	var recorded CITriggerEvent
	err := s.Pool.QueryRow(ctx, `INSERT INTO ci_trigger_events
		(repo_id,event,ref,old_sha,new_sha,pull_number,actor_id,workflow_path)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (repo_id,event,ref,new_sha,COALESCE(pull_number,0),COALESCE(workflow_path,'')) DO NOTHING
		RETURNING id::text,repo_id::text,event,ref,old_sha,new_sha,pull_number,actor_id::text,
		          workflow_path,state,attempts,last_error,created_at,processed_at`,
		event.RepoID, event.Event, event.Ref, oldSHA, event.NewSHA, pullNumber, actor, workflowPath).
		Scan(&recorded.ID, &recorded.RepoID, &recorded.Event, &recorded.Ref, &recorded.OldSHA,
			&recorded.NewSHA, &recorded.PullNumber, &recorded.ActorID, &recorded.WorkflowPath,
			&recorded.State, &recorded.Attempts, &recorded.LastError, &recorded.CreatedAt, &recorded.ProcessedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// The event was already recorded, which is the success case for a retry.
		return CITriggerEvent{}, false, nil
	}
	if err != nil {
		return CITriggerEvent{}, false, dbErr(err)
	}
	return recorded, true, nil
}

// ClaimTriggerEvents locks a batch of pending events for processing. A claim is
// only a read marker: the row stays pending until MarkTriggerDone or
// MarkTriggerFailed, so a crashed worker cannot lose an event.
func (s *Store) ClaimTriggerEvents(ctx context.Context, limit int) ([]CITriggerEvent, error) {
	if limit < 1 || limit > 100 {
		return nil, invalid("trigger limit")
	}
	rows, err := s.Pool.Query(ctx, `SELECT id::text,repo_id::text,event,ref,old_sha,new_sha,pull_number,
		actor_id::text,workflow_path,state,attempts,last_error,created_at,processed_at
		FROM ci_trigger_events
		WHERE state='pending' AND attempts < 5
		ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	events := make([]CITriggerEvent, 0, limit)
	for rows.Next() {
		var event CITriggerEvent
		if err := rows.Scan(&event.ID, &event.RepoID, &event.Event, &event.Ref, &event.OldSHA,
			&event.NewSHA, &event.PullNumber, &event.ActorID, &event.WorkflowPath, &event.State,
			&event.Attempts, &event.LastError, &event.CreatedAt, &event.ProcessedAt); err != nil {
			return nil, dbErr(err)
		}
		events = append(events, event)
	}
	return events, dbErr(rows.Err())
}

// MarkTriggerDone records that an event was turned into runs.
func (s *Store) MarkTriggerDone(ctx context.Context, eventID string) error {
	if !canonicalStoreUUID.MatchString(eventID) {
		return invalid("event id")
	}
	_, err := s.Pool.Exec(ctx, `UPDATE ci_trigger_events
		SET state='done', processed_at=clock_timestamp(), attempts=attempts+1
		WHERE id=$1`, eventID)
	return dbErr(err)
}

// MarkTriggerFailed records why an event could not be processed. The event is
// retried until the attempt limit is reached, then abandoned with its reason so
// an operator can see it instead of it disappearing.
func (s *Store) MarkTriggerFailed(ctx context.Context, eventID, reason string) error {
	if !canonicalStoreUUID.MatchString(eventID) || !utf8.ValidString(reason) || len(reason) > 1024 {
		return invalid("event failure")
	}
	_, err := s.Pool.Exec(ctx, `UPDATE ci_trigger_events
		SET attempts=attempts+1,
		    state=CASE WHEN attempts+1 >= 5 THEN 'failed' ELSE 'pending' END,
		    last_error=$2,
		    processed_at=CASE WHEN attempts+1 >= 5 THEN clock_timestamp() ELSE NULL END
		WHERE id=$1`, eventID, reason)
	return dbErr(err)
}
