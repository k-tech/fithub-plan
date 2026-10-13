package store

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

// Notification kinds. The set is closed so the list query can label every row
// without knowing about later event types, and so a typo cannot store a row the
// interface cannot describe.
const (
	NotifyIssueComment  = "issue_comment"
	NotifyIssueAssigned = "issue_assigned"
	NotifyPullComment   = "pull_comment"
	NotifyPullReview    = "pull_review"
	NotifyPullMerged    = "pull_merged"
	NotifyRunFailed     = "run_failed"
)

// Notification is one row of a user's inbox. It carries no permission snapshot:
// whether the reader may still see the subject is decided when the list is read.
type Notification struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	ActorID       *string    `json:"actorId"`
	ActorName     string     `json:"actorName"`
	RepoID        string     `json:"repoId"`
	TeamSlug      string     `json:"teamSlug"`
	RepoSlug      string     `json:"repoSlug"`
	SubjectNumber *int64     `json:"subjectNumber"`
	RunNumber     *int64     `json:"runNumber"`
	SubjectTitle  string     `json:"subjectTitle"`
	Read          bool       `json:"read"`
	CreatedAt     time.Time  `json:"createdAt"`
	ReadAt        *time.Time `json:"readAt"`
}

// notificationColumns is shared by the inbox queries so every read returns the
// same shape.
const notificationColumns = `n.id::text, n.kind, n.actor_id::text,
    COALESCE(actor.name, ''), n.repo_id::text, t.slug, r.slug,
    n.subject_number, n.run_number, n.subject_title,
    (n.read_at IS NOT NULL), n.created_at, n.read_at`

// validNotificationKind reports whether kind is one the schema accepts.
func validNotificationKind(kind string) bool {
	switch kind {
	case NotifyIssueComment, NotifyIssueAssigned, NotifyPullComment,
		NotifyPullReview, NotifyPullMerged, NotifyRunFailed:
		return true
	}
	return false
}

// Notify records one event for the given recipients.
//
// It never notifies the actor about their own action, and it never notifies the
// same user twice for the same event key, so a retried or replayed write cannot
// duplicate an entry. Notification failure is returned to the caller, which is
// expected to log it and keep the original operation's result: a comment that
// was stored must not be reported as failed because an inbox row could not be
// written.
func (s *Store) Notify(
	ctx context.Context,
	repoID, actorID, kind string,
	subjectNumber, runNumber *int64,
	subjectTitle, eventKey string,
	recipients []string,
) (int, error) {
	if !canonicalStoreUUID.MatchString(repoID) || !validNotificationKind(kind) {
		return 0, invalid("notification")
	}
	if actorID != "" && !canonicalStoreUUID.MatchString(actorID) {
		return 0, invalid("actor")
	}
	subjectTitle = strings.TrimSpace(subjectTitle)
	if subjectTitle == "" || len(subjectTitle) > 300 || !utf8.ValidString(subjectTitle) {
		// A title is the only content copied into the row, so it is bounded and
		// must be valid text. Overflow is truncated rather than rejected: losing a
		// long title must not lose the notification.
		if subjectTitle == "" {
			return 0, invalid("subject title")
		}
		subjectTitle = truncateUTF8(subjectTitle, 300)
	}
	if eventKey == "" || len(eventKey) > 200 || !utf8.ValidString(eventKey) {
		return 0, invalid("event key")
	}
	// Deduplicate and order the recipient list so one call cannot insert the same
	// recipient twice, whatever the caller passed.
	seen := map[string]bool{}
	unique := make([]string, 0, len(recipients))
	for _, userID := range recipients {
		if userID == "" || userID == actorID || seen[userID] {
			continue
		}
		if !canonicalStoreUUID.MatchString(userID) {
			return 0, invalid("recipient")
		}
		seen[userID] = true
		unique = append(unique, userID)
	}
	if len(unique) == 0 {
		return 0, nil
	}
	var actor any
	if actorID != "" {
		actor = actorID
	}
	inserted := 0
	for _, userID := range unique {
		tag, err := s.Pool.Exec(ctx, `INSERT INTO notifications
            (recipient_id, actor_id, repo_id, kind, subject_number, run_number, subject_title, event_key)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
            ON CONFLICT (recipient_id, event_key) DO NOTHING`,
			userID, actor, repoID, kind, subjectNumber, runNumber, subjectTitle, eventKey)
		if err != nil {
			return inserted, dbErr(err)
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

// truncateUTF8 shortens text to at most limit bytes without splitting a rune.
func truncateUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// ListNotifications returns one page of a user's inbox.
//
// Every row is re-checked against the reader's *current* access to the
// repository it points at. A notification for a repository the reader can no
// longer read is omitted rather than returned with its links removed, because a
// notification is only meaningful together with the ability to open it. The
// rows are not deleted, so restoring access restores the inbox.
func (s *Store) ListNotifications(ctx context.Context, userID string, unreadOnly bool, page, perPage int) ([]Notification, bool, int, error) {
	if !canonicalStoreUUID.MatchString(userID) || page < 1 || page > 1_000_000 || perPage < 1 || perPage > 100 {
		return nil, false, 0, invalid("pagination")
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+notificationColumns+`
        FROM notifications n
        JOIN repositories r ON r.id=n.repo_id
        JOIN teams t ON t.id=r.team_id
        -- The reader is aliased "u" because repoReadAccess resolves access
        -- through that alias; the actor gets its own alias.
        JOIN users u ON u.id=$1
        LEFT JOIN users actor ON actor.id=n.actor_id
        WHERE n.recipient_id=$1 AND ($2=false OR n.read_at IS NULL)
          AND u.status='active' AND r.state='active'
          AND `+repoReadAccess+`
        ORDER BY n.created_at DESC, n.id
        LIMIT $3 OFFSET $4`,
		userID, unreadOnly, perPage+1, (page-1)*perPage)
	if err != nil {
		return nil, false, 0, dbErr(err)
	}
	defer rows.Close()
	items := make([]Notification, 0, perPage)
	for rows.Next() {
		var item Notification
		if err := rows.Scan(&item.ID, &item.Kind, &item.ActorID, &item.ActorName,
			&item.RepoID, &item.TeamSlug, &item.RepoSlug, &item.SubjectNumber,
			&item.RunNumber, &item.SubjectTitle, &item.Read, &item.CreatedAt, &item.ReadAt); err != nil {
			return nil, false, 0, dbErr(err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, dbErr(err)
	}
	hasMore := len(items) > perPage
	if hasMore {
		items = items[:perPage]
	}
	// The unread badge is deliberately counted over the same access-checked set,
	// so a badge never counts something the list would refuse to show.
	unread, err := s.CountUnreadNotifications(ctx, userID)
	if err != nil {
		return nil, false, 0, err
	}
	return items, hasMore, unread, nil
}

// CountUnreadNotifications returns how many unread notifications the user can
// currently act on.
func (s *Store) CountUnreadNotifications(ctx context.Context, userID string) (int, error) {
	if !canonicalStoreUUID.MatchString(userID) {
		return 0, invalid("user")
	}
	var count int
	err := s.Pool.QueryRow(ctx, `SELECT count(*)
        FROM notifications n
        JOIN repositories r ON r.id=n.repo_id
        JOIN users u ON u.id=$1
        WHERE n.recipient_id=$1 AND n.read_at IS NULL
          AND u.status='active' AND r.state='active'
          AND `+repoReadAccess, userID).Scan(&count)
	return count, dbErr(err)
}

// MarkNotificationsRead marks the given notifications read for one user.
//
// An empty id list marks the whole visible inbox, which is what "mark all as
// read" means. Rows the user cannot currently see are left untouched so that
// hiding and re-exposing access does not silently lose an unread state.
func (s *Store) MarkNotificationsRead(ctx context.Context, userID string, ids []string) (int, error) {
	if !canonicalStoreUUID.MatchString(userID) || len(ids) > 100 {
		return 0, invalid("notifications")
	}
	for _, id := range ids {
		if !canonicalStoreUUID.MatchString(id) {
			return 0, invalid("notification id")
		}
	}
	// Both statements apply the same access filter, so marking read can never
	// touch a row the reader could not see.
	statement := `UPDATE notifications n SET read_at=now()
        FROM repositories r, users u
        WHERE n.recipient_id=$1 AND n.read_at IS NULL
          AND r.id=n.repo_id AND u.id=$1
          AND u.status='active' AND r.state='active'
          AND ` + repoReadAccess
	args := []any{userID}
	if len(ids) > 0 {
		statement = `UPDATE notifications n SET read_at=now()
            FROM repositories r, users u
            WHERE n.recipient_id=$1 AND n.read_at IS NULL AND n.id = ANY($2::uuid[])
              AND r.id=n.repo_id AND u.id=$1
              AND u.status='active' AND r.state='active'
              AND ` + repoReadAccess
		args = append(args, ids)
	}
	tag, err := s.Pool.Exec(ctx, statement, args...)
	if err != nil {
		return 0, dbErr(err)
	}
	return int(tag.RowsAffected()), nil
}

// IssueNotificationRecipients returns who should hear about an Issue event:
// the author plus everyone currently assigned to it, excluding the actor.
//
// Only people who can still read the repository are returned, so a recipient who
// lost access does not accumulate an inbox entry that could never be shown.
func (s *Store) IssueNotificationRecipients(ctx context.Context, repoID string, number int64, actorID string) ([]string, error) {
	if !canonicalStoreUUID.MatchString(repoID) || number < 1 {
		return nil, invalid("issue")
	}
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT candidates.user_id::text FROM (
            SELECT i.author_id AS user_id FROM issues i
                WHERE i.repo_id=$1 AND i.number=$2
            UNION
            SELECT a.user_id FROM issue_assignees a
                JOIN issues i2 ON i2.id=a.issue_id
                WHERE i2.repo_id=$1 AND i2.number=$2
        ) candidates
        JOIN users u ON u.id=candidates.user_id
        JOIN repositories r ON r.id=$1
        WHERE u.status='active' AND u.id <> $3::uuid AND `+repoReadAccess,
		repoID, number, actorID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	recipients := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, dbErr(err)
		}
		recipients = append(recipients, userID)
	}
	return recipients, dbErr(rows.Err())
}

// PullNotificationRecipients returns who should hear about a pull request event:
// the author plus every current reviewer, excluding the actor.
func (s *Store) PullNotificationRecipients(ctx context.Context, repoID string, number int64, actorID string) ([]string, error) {
	if !canonicalStoreUUID.MatchString(repoID) || number < 1 {
		return nil, invalid("pull request")
	}
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT candidates.user_id::text FROM (
            SELECT p.author_id AS user_id FROM pull_requests p
                WHERE p.repo_id=$1 AND p.number=$2
            UNION
            SELECT rv.reviewer_id FROM pull_reviews rv
                JOIN pull_requests p2 ON p2.id=rv.pull_id
                WHERE p2.repo_id=$1 AND p2.number=$2
            UNION
            SELECT rc.author_id FROM pull_review_comments rc
                JOIN pull_requests p3 ON p3.id=rc.pull_id
                WHERE p3.repo_id=$1 AND p3.number=$2
        ) candidates
        JOIN users u ON u.id=candidates.user_id
        JOIN repositories r ON r.id=$1
        WHERE u.status='active' AND u.id <> $3::uuid AND `+repoReadAccess,
		repoID, number, actorID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	recipients := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, dbErr(err)
		}
		recipients = append(recipients, userID)
	}
	return recipients, dbErr(rows.Err())
}

// RunNotificationRecipients returns who should hear that a run failed: the
// account that triggered it, provided it can still read the repository and is
// still active.
//
// A run failure has no human actor, so there is nobody to exclude; the notice is
// recorded as an automatic event.
func (s *Store) RunNotificationRecipients(ctx context.Context, runID string) ([]string, error) {
	if !canonicalStoreUUID.MatchString(runID) {
		return nil, invalid("run")
	}
	// The run is aliased "run" so "r" is free for the repository: repoReadAccess
	// resolves access through the aliases "r" and "u".
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT run.actor_id::text
        FROM ci_runs run
        JOIN users u ON u.id=run.actor_id
        JOIN repositories r ON r.id=run.repo_id
        WHERE run.id=$1 AND run.actor_id IS NOT NULL
          AND u.status='active' AND r.state='active' AND `+repoReadAccess, runID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	recipients := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, dbErr(err)
		}
		recipients = append(recipients, userID)
	}
	return recipients, dbErr(rows.Err())
}
