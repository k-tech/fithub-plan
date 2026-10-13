package store

import (
	"context"
	"testing"
)

// notificationFixture adds a second account that can read the first repository,
// so notification delivery and access revocation can both be exercised.
func notificationFixture(t *testing.T) (*Store, User, User, Repository) {
	t.Helper()
	s, author, repo, _ := issueFixture(t)
	ctx := context.Background()
	reader, err := s.UpsertDevUser(ctx, "notify-reader@example.test", "Notify Reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserRole(ctx, reader.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	// The repository's own team is the one to join, so the reader ends up with
	// exactly one route to access: the grant added below.
	request, err := s.RequestJoin(ctx, reader.ID, repo.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewJoin(ctx, request.ID, author.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantRepoToUser(ctx, repo.ID, reader.ID, "write"); err != nil {
		t.Fatal(err)
	}
	return s, author, reader, repo
}

// notifyOne records one event for one recipient and fails the test on error.
func notifyOne(t *testing.T, s *Store, repoID, actorID, kind, title, eventKey, recipient string) {
	t.Helper()
	number := int64(1)
	if _, err := s.Notify(context.Background(), repoID, actorID, kind, &number, nil, title, eventKey, []string{recipient}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
}

func TestNotificationIsDeliveredAndNeverToTheActor(t *testing.T) {
	s, author, reader, repo := notificationFixture(t)
	ctx := context.Background()

	notifyOne(t, s, repo.ID, author.ID, NotifyIssueComment, "Fix the parser", "issue_comment:1", reader.ID)
	// The actor must not receive their own event even if named explicitly.
	notifyOne(t, s, repo.ID, author.ID, NotifyIssueComment, "Fix the parser", "issue_comment:2", author.ID)

	items, _, unread, err := s.ListNotifications(ctx, reader.ID, false, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("recipient inbox has %d entries, want 1", len(items))
	}
	if items[0].Kind != NotifyIssueComment || items[0].SubjectTitle != "Fix the parser" {
		t.Fatalf("unexpected notification %+v", items[0])
	}
	if items[0].ActorName != author.Name {
		t.Fatalf("actor name=%q want %q", items[0].ActorName, author.Name)
	}
	if items[0].TeamSlug == "" || items[0].RepoSlug == "" {
		t.Fatalf("notification must carry a usable link target: %+v", items[0])
	}
	if unread != 1 {
		t.Fatalf("unread=%d want 1", unread)
	}

	own, _, ownUnread, err := s.ListNotifications(ctx, author.ID, false, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 0 || ownUnread != 0 {
		t.Fatalf("the actor received their own event: %+v", own)
	}
}

// The same event must not be stored twice, so a retried or replayed write cannot
// inflate an inbox or the unread badge.
func TestNotificationIsIdempotentPerEventKey(t *testing.T) {
	s, author, reader, repo := notificationFixture(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		notifyOne(t, s, repo.ID, author.ID, NotifyIssueComment, "Fix the parser", "issue_comment:42", reader.ID)
	}
	items, _, unread, err := s.ListNotifications(ctx, reader.ID, false, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || unread != 1 {
		t.Fatalf("items=%d unread=%d, want 1 and 1", len(items), unread)
	}
}

// This is the property that keeps the inbox honest: a notification points at
// content, and once the reader may no longer read that content the notification
// must disappear from every list and from the unread count, without being
// deleted. Restoring access must bring it back.
func TestNotificationHidesWhenRepositoryAccessIsRevoked(t *testing.T) {
	s, author, reader, repo := notificationFixture(t)
	ctx := context.Background()
	notifyOne(t, s, repo.ID, author.ID, NotifyIssueComment, "Private work", "issue_comment:7", reader.ID)

	if _, _, unread, err := s.ListNotifications(ctx, reader.ID, false, 1, 20); err != nil || unread != 1 {
		t.Fatalf("unread before revocation=%d err=%v, want 1", unread, err)
	}

	// Remove the reader's write grant. The repository is private, so there is no
	// other route to read access.
	if _, err := s.Pool.Exec(ctx, `DELETE FROM repo_grants WHERE repo_id=$1 AND user_id=$2`, repo.ID, reader.ID); err != nil {
		t.Fatal(err)
	}
	items, _, unread, err := s.ListNotifications(ctx, reader.ID, false, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 || unread != 0 {
		t.Fatalf("a notification survived access revocation: items=%d unread=%d", len(items), unread)
	}
	// The row must still exist: hiding is a read-time decision, not a deletion.
	var stored int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_id=$1`, reader.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("stored rows=%d, want the row to remain for a later restore", stored)
	}

	// Granting access again must restore it, unread state included.
	if err := s.GrantRepoToUser(ctx, repo.ID, reader.ID, "read"); err != nil {
		t.Fatal(err)
	}
	items, _, unread, err = s.ListNotifications(ctx, reader.ID, false, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || unread != 1 {
		t.Fatalf("restoring access did not restore the inbox: items=%d unread=%d", len(items), unread)
	}
}

// A suspended account must not be able to read its inbox, and its unread badge
// must not count anything.
func TestNotificationHiddenForSuspendedAccount(t *testing.T) {
	s, author, reader, repo := notificationFixture(t)
	ctx := context.Background()
	notifyOne(t, s, repo.ID, author.ID, NotifyPullReview, "Review me", "pull_review:1", reader.ID)

	if err := s.SetUserStatus(ctx, reader.ID, "suspended"); err != nil {
		t.Fatal(err)
	}
	items, _, unread, err := s.ListNotifications(ctx, reader.ID, false, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 || unread != 0 {
		t.Fatalf("a suspended account still saw notifications: items=%d unread=%d", len(items), unread)
	}
}

// Marking read must only touch rows the reader can currently see, so a hidden
// notification stays unread rather than being silently consumed.
func TestMarkReadAppliesOnlyToVisibleNotifications(t *testing.T) {
	s, author, reader, repo := notificationFixture(t)
	ctx := context.Background()
	notifyOne(t, s, repo.ID, author.ID, NotifyIssueComment, "First", "issue_comment:11", reader.ID)
	notifyOne(t, s, repo.ID, author.ID, NotifyIssueComment, "Second", "issue_comment:12", reader.ID)

	// Hide both by revoking access, then mark all read. Nothing may change.
	if _, err := s.Pool.Exec(ctx, `DELETE FROM repo_grants WHERE repo_id=$1 AND user_id=$2`, repo.ID, reader.ID); err != nil {
		t.Fatal(err)
	}
	marked, err := s.MarkNotificationsRead(ctx, reader.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if marked != 0 {
		t.Fatalf("marked=%d while every notification was hidden, want 0", marked)
	}
	var unreadRows int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_id=$1 AND read_at IS NULL`, reader.ID).Scan(&unreadRows); err != nil {
		t.Fatal(err)
	}
	if unreadRows != 2 {
		t.Fatalf("unread rows=%d, want both to remain unread", unreadRows)
	}

	// Restore access; marking all read must now consume both.
	if err := s.GrantRepoToUser(ctx, repo.ID, reader.ID, "read"); err != nil {
		t.Fatal(err)
	}
	marked, err = s.MarkNotificationsRead(ctx, reader.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if marked != 2 {
		t.Fatalf("marked=%d want 2", marked)
	}
	if _, _, unread, err := s.ListNotifications(ctx, reader.ID, false, 1, 20); err != nil || unread != 0 {
		t.Fatalf("unread after mark-all=%d err=%v, want 0", unread, err)
	}
}

// Marking a specific notification read must not let one account touch another
// account's inbox.
func TestMarkReadCannotTouchAnotherAccount(t *testing.T) {
	s, author, reader, repo := notificationFixture(t)
	ctx := context.Background()
	notifyOne(t, s, repo.ID, author.ID, NotifyIssueComment, "Mine", "issue_comment:21", reader.ID)
	notifyOne(t, s, repo.ID, author.ID, NotifyPullMerged, "Theirs", "pull_merged:22", author.ID)

	items, _, _, err := s.ListNotifications(ctx, reader.ID, false, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("reader inbox=%d want 1", len(items))
	}
	// The author tries to mark the reader's notification as read.
	marked, err := s.MarkNotificationsRead(ctx, author.ID, []string{items[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if marked != 0 {
		t.Fatalf("marked=%d, want another account's notification to be untouched", marked)
	}
	if _, _, unread, err := s.ListNotifications(ctx, reader.ID, false, 1, 20); err != nil || unread != 1 {
		t.Fatalf("reader unread=%d err=%v, want the notification to remain unread", unread, err)
	}
}

// The recipient resolvers must include the author and assignees for an Issue,
// exclude the actor, and never return an account that cannot read the repository.
func TestIssueRecipientsFollowAssigneesAndAccess(t *testing.T) {
	s, author, reader, repo := notificationFixture(t)
	ctx := context.Background()

	issue, err := s.CreateIssue(ctx, repo.ID, author.ID, "Assign me", "")
	if err != nil {
		t.Fatal(err)
	}
	// With nobody assigned, the author is the only recipient and the actor is
	// excluded, so an event authored by the author has no recipients.
	recipients, err := s.IssueNotificationRecipients(ctx, repo.ID, issue.Number, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 0 {
		t.Fatalf("recipients=%v, want none when the actor is the only candidate", recipients)
	}
	// A different actor reaches the author.
	recipients, err = s.IssueNotificationRecipients(ctx, repo.ID, issue.Number, reader.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0] != author.ID {
		t.Fatalf("recipients=%v want the author", recipients)
	}

	// Once the reader is assigned, they are included for events from the author.
	if _, _, err := s.SetIssueAssignees(ctx, repo.ID, issue.Number, []string{reader.ID}, author.ID); err != nil {
		t.Fatal(err)
	}
	recipients, err = s.IssueNotificationRecipients(ctx, repo.ID, issue.Number, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0] != reader.ID {
		t.Fatalf("recipients=%v want the new assignee", recipients)
	}

	// An account that lost access must not be returned as a recipient: an inbox
	// entry they could never open is worse than no entry.
	if _, err := s.Pool.Exec(ctx, `DELETE FROM repo_grants WHERE repo_id=$1 AND user_id=$2`, repo.ID, reader.ID); err != nil {
		t.Fatal(err)
	}
	recipients, err = s.IssueNotificationRecipients(ctx, repo.ID, issue.Number, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, userID := range recipients {
		if userID == reader.ID {
			t.Fatal("an account without repository access was returned as a recipient")
		}
	}
}

// Assigning the same person again must not re-announce the assignment.
func TestSetIssueAssigneesReportsOnlyNewAssignees(t *testing.T) {
	s, author, reader, repo := notificationFixture(t)
	ctx := context.Background()
	issue, err := s.CreateIssue(ctx, repo.ID, author.ID, "Assign twice", "")
	if err != nil {
		t.Fatal(err)
	}
	assignees, added, err := s.SetIssueAssignees(ctx, repo.ID, issue.Number, []string{reader.ID}, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(assignees) != 1 || len(added) != 1 || added[0] != reader.ID {
		t.Fatalf("first assignment: assignees=%v added=%v", assignees, added)
	}
	// Re-applying the same set must report no addition.
	_, added, err = s.SetIssueAssignees(ctx, repo.ID, issue.Number, []string{reader.ID}, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Fatalf("re-assigning the same person reported an addition: %v", added)
	}
}

// Reviewers and commenters must be included in pull request recipients so a
// discussion does not die silently.
func TestPullRecipientsIncludeReviewersAndCommenters(t *testing.T) {
	s, author, reader, repo := notificationFixture(t)
	ctx := context.Background()
	base := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	head := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	pull, err := s.CreatePullRequest(ctx, repo.ID, author.ID, "Review my change", "", "main", "feature", base, head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePullReview(ctx, repo.ID, pull.Number, reader.ID, "approved", "looks fine", base, head); err != nil {
		t.Fatal(err)
	}
	// The author is excluded when the author is the actor; the reviewer is not.
	recipients, err := s.PullNotificationRecipients(ctx, repo.ID, pull.Number, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0] != reader.ID {
		t.Fatalf("recipients=%v want the reviewer", recipients)
	}
	recipients, err = s.PullNotificationRecipients(ctx, repo.ID, pull.Number, reader.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0] != author.ID {
		t.Fatalf("recipients=%v want the author", recipients)
	}
}

// A run that fails must notify the account that triggered it.
func TestRunRecipientsReachTheTriggeringAccount(t *testing.T) {
	s, author, _, repo := notificationFixture(t)
	ctx := context.Background()
	run := enqueueRun(t, s, repo.ID, author.ID)
	recipients, err := s.RunNotificationRecipients(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0] != author.ID {
		t.Fatalf("recipients=%v want the triggering account", recipients)
	}
}

// An unknown event kind must be rejected outright rather than stored as a row no
// interface can describe.
func TestNotificationRejectsUnknownKind(t *testing.T) {
	s, author, reader, repo := notificationFixture(t)
	number := int64(1)
	if _, err := s.Notify(context.Background(), repo.ID, author.ID, "not_a_kind", &number, nil, "title", "key", []string{reader.ID}); err == nil {
		t.Fatal("an unknown notification kind was accepted")
	}
}

// A notification for a deleted repository must vanish rather than dangle.
func TestNotificationRemovedWithRepository(t *testing.T) {
	s, author, reader, repo := notificationFixture(t)
	ctx := context.Background()
	notifyOne(t, s, repo.ID, author.ID, NotifyIssueComment, "Soon gone", "issue_comment:31", reader.ID)
	if _, err := s.Pool.Exec(ctx, `DELETE FROM repositories WHERE id=$1`, repo.ID); err != nil {
		t.Fatal(err)
	}
	items, _, unread, err := s.ListNotifications(ctx, reader.ID, false, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 || unread != 0 {
		t.Fatalf("a deleted repository left a visible notification: %+v", items)
	}
	var remaining int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_id=$1`, reader.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("rows=%d, want the cascade to remove them", remaining)
	}
}
