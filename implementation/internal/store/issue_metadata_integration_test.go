package store

import (
	"context"
	"errors"
	"testing"
)

func metadataFixture(t *testing.T) (*Store, Repository, User, Repository) {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	author, err := s.UpsertDevUser(ctx, "author@example.test", "Author")
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, "engineering", "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := s.UpsertDevUser(ctx, "admin@example.test", "Admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserRole(ctx, reviewer.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestJoin(ctx, author.ID, team.ID); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingRequests(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending requests: %+v, %v", pending, err)
	}
	if _, err := s.ReviewJoin(ctx, pending[0].ID, reviewer.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	repo, err := s.CreateRepo(ctx, team.ID, "platform", "internal", author.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateRepo(ctx, team.ID, "other", "internal", author.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []Repository{repo, other} {
		if err := s.SetRepoState(ctx, item.ID, "active"); err != nil {
			t.Fatal(err)
		}
	}
	return s, repo, author, other
}

func TestLabelsAreRepositoryScoped(t *testing.T) {
	s, repo, _, other := metadataFixture(t)
	ctx := context.Background()
	label, err := s.CreateLabel(ctx, repo.ID, "  Bug  ", "#FF8800", "Something is broken")
	if err != nil {
		t.Fatal(err)
	}
	if label.Name != "Bug" || label.Color != "ff8800" {
		t.Fatalf("label normalization: %+v", label)
	}
	if _, err := s.CreateLabel(ctx, repo.ID, "bug", "00ff00", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("case-insensitive duplicate accepted: %v", err)
	}
	if _, err := s.CreateLabel(ctx, repo.ID, "Bad color", "zzzzzz", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid color accepted: %v", err)
	}
	if _, err := s.CreateLabel(ctx, repo.ID, "Same name", "00ff00", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLabel(ctx, other.ID, "Same name", "00ff00", ""); err != nil {
		t.Fatalf("same name in another repository rejected: %v", err)
	}
	updated, err := s.UpdateLabel(ctx, repo.ID, label.ID, "Defect", "000000", "renamed")
	if err != nil || updated.Name != "Defect" || updated.Color != "000000" {
		t.Fatalf("update label: %+v, %v", updated, err)
	}
	if _, err := s.UpdateLabel(ctx, other.ID, label.ID, "Nope", "000000", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-repository update allowed: %v", err)
	}
	if err := s.DeleteLabel(ctx, other.ID, label.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-repository delete allowed: %v", err)
	}
	labels, err := s.ListLabels(ctx, repo.ID)
	if err != nil || len(labels) != 2 {
		t.Fatalf("list labels: %+v, %v", labels, err)
	}
	if labels[0].Name != "Defect" || labels[1].Name != "Same name" {
		t.Fatalf("label ordering: %+v", labels)
	}
}

func TestIssueLabelAssignmentIsAtomicAndScoped(t *testing.T) {
	s, repo, author, other := metadataFixture(t)
	ctx := context.Background()
	bug, err := s.CreateLabel(ctx, repo.ID, "Bug", "ff0000", "")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := s.CreateLabel(ctx, repo.ID, "Docs", "0000ff", "")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.CreateLabel(ctx, other.ID, "Foreign", "00ff00", "")
	if err != nil {
		t.Fatal(err)
	}
	repoIssue, err := s.CreateIssue(ctx, repo.ID, author.ID, "Fix it", "")
	if err != nil {
		t.Fatal(err)
	}
	labels, err := s.SetIssueLabels(ctx, repo.ID, repoIssue.Number, []string{bug.ID, docs.ID})
	if err != nil || len(labels) != 2 {
		t.Fatalf("set labels: %+v, %v", labels, err)
	}
	loaded, err := s.GetIssue(ctx, repo.ID, repoIssue.Number)
	if err != nil || len(loaded.Labels) != 2 {
		t.Fatalf("labels not attached to issue: %+v, %v", loaded.Labels, err)
	}
	// A label of another repository must fail and leave the previous set intact.
	if _, err := s.SetIssueLabels(ctx, repo.ID, repoIssue.Number, []string{bug.ID, foreign.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign label accepted: %v", err)
	}
	after, err := s.GetIssue(ctx, repo.ID, repoIssue.Number)
	if err != nil || len(after.Labels) != 2 {
		t.Fatalf("failed replace changed labels: %+v, %v", after.Labels, err)
	}
	cleared, err := s.SetIssueLabels(ctx, repo.ID, repoIssue.Number, nil)
	if err != nil || len(cleared) != 0 {
		t.Fatalf("clear labels: %+v, %v", cleared, err)
	}
	if err := s.DeleteLabel(ctx, repo.ID, bug.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.ListLabels(ctx, repo.ID)
	if err != nil || len(remaining) != 1 || remaining[0].Name != "Docs" {
		t.Fatalf("delete label: %+v, %v", remaining, err)
	}
}

func TestIssueFiltersByLabelAssigneeAndMilestone(t *testing.T) {
	s, repo, author, _ := metadataFixture(t)
	ctx := context.Background()
	colleague, err := s.UpsertDevUser(ctx, "colleague@example.test", "Colleague")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserStatus(ctx, colleague.ID, "active"); err != nil {
		t.Fatal(err)
	}
	label, err := s.CreateLabel(ctx, repo.ID, "Bug", "ff0000", "")
	if err != nil {
		t.Fatal(err)
	}
	// Two more labels so multi-select filtering can be exercised: one that shares
	// an Issue with Bug, and one that never does.
	sharedLabel, err := s.CreateLabel(ctx, repo.ID, "Docs", "00ff00", "")
	if err != nil {
		t.Fatal(err)
	}
	otherLabel, err := s.CreateLabel(ctx, repo.ID, "Urgent", "0000ff", "")
	if err != nil {
		t.Fatal(err)
	}
	milestone, err := s.CreateMilestone(ctx, repo.ID, "v1.0", "first release", "2026-12-31")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateIssue(ctx, repo.ID, author.ID, "Labeled", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateIssue(ctx, repo.ID, author.ID, "Assigned", "")
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.CreateIssue(ctx, repo.ID, author.ID, "Plain", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIssueLabels(ctx, repo.ID, first.Number, []string{label.ID, sharedLabel.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIssueLabels(ctx, repo.ID, second.Number, []string{otherLabel.ID}); err != nil {
		t.Fatal(err)
	}
	// The colleague is a Team member, so they are assignable. A second teammate is
	// requested here too so both are approved by the same pass below.
	if _, err := s.RequestJoin(ctx, colleague.ID, teamIDOf(t, s, repo)); err != nil {
		t.Fatal(err)
	}
	reviewer, err := s.UpsertDevUser(ctx, "reviewer@example.test", "Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserStatus(ctx, reviewer.ID, "active"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestJoin(ctx, reviewer.ID, teamIDOf(t, s, repo)); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range pending {
		if _, err := s.ReviewJoin(ctx, request.ID, adminIDOf(t, s), "approved"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.SetIssueAssignees(ctx, repo.ID, second.Number, []string{colleague.ID}, author.ID); err != nil {
		t.Fatal(err)
	}
	number := milestone.Number
	if _, err := s.SetIssueMilestone(ctx, repo.ID, third.Number, &number); err != nil {
		t.Fatal(err)
	}

	assertOnly := func(name string, filter IssueFilter, want int64) {
		t.Helper()
		rows, _, err := s.ListIssues(ctx, repo.ID, filter, 1, 20)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(rows) != 1 || rows[0].Number != want {
			t.Fatalf("%s: got %+v, want issue %d", name, rows, want)
		}
	}
	assertOnly("label filter", IssueFilter{State: "all", LabelIDs: []string{label.ID}}, first.Number)
	assertOnly("assignee filter", IssueFilter{State: "all", AssigneeIDs: []string{colleague.ID}}, second.Number)
	assertOnly("milestone filter", IssueFilter{State: "all", MilestoneNumber: &number}, third.Number)

	// Selecting several labels narrows the list: an Issue must carry every one of
	// them. With "or" semantics this would return all three and the filter would
	// be useless whenever more than one label is in play.
	assertOnly("two labels on the same issue", IssueFilter{State: "all", LabelIDs: []string{label.ID, sharedLabel.ID}}, first.Number)
	none, _, err := s.ListIssues(ctx, repo.ID, IssueFilter{State: "all", LabelIDs: []string{label.ID, otherLabel.ID}}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	// otherLabel sits on a different Issue, so requiring both must match nothing.
	if len(none) != 0 {
		t.Fatalf("labels that never co-occur matched: %+v", none)
	}
	// Several assignees must all be assigned to the same Issue.
	if _, _, err := s.SetIssueAssignees(ctx, repo.ID, first.Number, []string{colleague.ID, reviewer.ID}, author.ID); err != nil {
		t.Fatal(err)
	}
	assertOnly("two assignees on the same issue", IssueFilter{State: "all", AssigneeIDs: []string{colleague.ID, reviewer.ID}}, first.Number)
	// Requiring an assignee who is on no Issue at all must match nothing.
	noneAssignees, _, err := s.ListIssues(ctx, repo.ID, IssueFilter{State: "all", AssigneeIDs: []string{colleague.ID, "11111111-1111-1111-1111-111111111111"}}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(noneAssignees) != 0 {
		t.Fatalf("assignees that never co-occur matched: %+v", noneAssignees)
	}

	all, _, err := s.ListIssues(ctx, repo.ID, IssueFilter{State: "all"}, 1, 20)
	if err != nil || len(all) != 3 {
		t.Fatalf("unfiltered list: %+v, %v", all, err)
	}
	// An assignee outside the repository simply matches nothing.
	unknown := "11111111-1111-1111-1111-111111111111"
	empty, _, err := s.ListIssues(ctx, repo.ID, IssueFilter{State: "all", AssigneeIDs: []string{unknown}}, 1, 20)
	if err != nil || len(empty) != 0 {
		t.Fatalf("unknown assignee filter: %+v, %v", empty, err)
	}
}

func teamIDOf(t *testing.T, s *Store, repo Repository) string {
	t.Helper()
	if repo.TeamID == "" {
		t.Fatal("repository has no team")
	}
	return repo.TeamID
}

func adminIDOf(t *testing.T, s *Store) string {
	t.Helper()
	admin, err := s.UpsertDevUser(context.Background(), "admin@example.test", "Admin")
	if err != nil {
		t.Fatal(err)
	}
	return admin.ID
}

func TestMilestoneNumberingAndLifecycle(t *testing.T) {
	s, repo, _, other := metadataFixture(t)
	ctx := context.Background()
	first, err := s.CreateMilestone(ctx, repo.ID, "v1.0", "", "")
	if err != nil || first.Number != 1 || first.State != "open" || first.DueOn != nil {
		t.Fatalf("create milestone: %+v, %v", first, err)
	}
	second, err := s.CreateMilestone(ctx, repo.ID, "v2.0", "later", "2026-06-01")
	if err != nil || second.Number != 2 || second.DueOn == nil || *second.DueOn != "2026-06-01" {
		t.Fatalf("second milestone: %+v, %v", second, err)
	}
	// Milestone numbering is independent per repository.
	independent, err := s.CreateMilestone(ctx, other.ID, "v1.0", "", "")
	if err != nil || independent.Number != 1 {
		t.Fatalf("independent numbering: %+v, %v", independent, err)
	}
	if _, err := s.CreateMilestone(ctx, repo.ID, "Bad date", "", "not-a-date"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid due date accepted: %v", err)
	}
	closed, err := s.UpdateMilestone(ctx, repo.ID, first.Number, "v1.0", "shipped", "2026-01-31", "closed")
	if err != nil || closed.State != "closed" || closed.ClosedAt == nil {
		t.Fatalf("close milestone: %+v, %v", closed, err)
	}
	reopened, err := s.UpdateMilestone(ctx, repo.ID, first.Number, "v1.0", "again", "", "open")
	if err != nil || reopened.State != "open" || reopened.ClosedAt != nil || reopened.DueOn != nil {
		t.Fatalf("reopen milestone: %+v, %v", reopened, err)
	}
	open, err := s.ListMilestones(ctx, repo.ID, "open")
	if err != nil || len(open) != 2 || open[0].Number != 2 {
		t.Fatalf("open milestones: %+v, %v", open, err)
	}
	if _, err := s.ListMilestones(ctx, repo.ID, "bogus"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid milestone state accepted: %v", err)
	}
	if err := s.DeleteMilestone(ctx, other.ID, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing milestone delete: %v", err)
	}
}

func TestMilestoneCountsAndIssueUnlink(t *testing.T) {
	s, repo, author, _ := metadataFixture(t)
	ctx := context.Background()
	milestone, err := s.CreateMilestone(ctx, repo.ID, "v1.0", "", "")
	if err != nil {
		t.Fatal(err)
	}
	open, err := s.CreateIssue(ctx, repo.ID, author.ID, "Open work", "")
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.CreateIssue(ctx, repo.ID, author.ID, "Done work", "")
	if err != nil {
		t.Fatal(err)
	}
	number := milestone.Number
	if _, err := s.SetIssueMilestone(ctx, repo.ID, open.Number, &number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIssueMilestone(ctx, repo.ID, done.Number, &number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIssueState(ctx, repo.ID, done.Number, "closed"); err != nil {
		t.Fatal(err)
	}
	counted, err := s.ListMilestones(ctx, repo.ID, "all")
	if err != nil || len(counted) != 1 {
		t.Fatalf("list milestones: %+v, %v", counted, err)
	}
	if counted[0].OpenIssues != 1 || counted[0].ClosedIssues != 1 {
		t.Fatalf("milestone counts: %+v", counted[0])
	}
	// Clearing the milestone leaves the Issue untouched.
	if _, err := s.SetIssueMilestone(ctx, repo.ID, open.Number, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetIssue(ctx, repo.ID, open.Number)
	if err != nil || loaded.MilestoneNumber != nil {
		t.Fatalf("milestone not cleared: %+v, %v", loaded, err)
	}
	// Deleting a milestone detaches its Issues instead of deleting them.
	if err := s.DeleteMilestone(ctx, repo.ID, milestone.Number); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetIssue(ctx, repo.ID, done.Number)
	if err != nil || after.MilestoneNumber != nil || after.State != "closed" {
		t.Fatalf("issue lost on milestone delete: %+v, %v", after, err)
	}
	foreign := int64(42)
	if _, err := s.SetIssueMilestone(ctx, repo.ID, open.Number, &foreign); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign milestone accepted: %v", err)
	}
}

func TestAssigneesRequireActiveTeamMemberAndAreReplaced(t *testing.T) {
	s, repo, author, _ := metadataFixture(t)
	ctx := context.Background()
	suspended, err := s.UpsertDevUser(ctx, "suspended@example.test", "Suspended")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserStatus(ctx, suspended.ID, "suspended"); err != nil {
		t.Fatal(err)
	}
	issue, err := s.CreateIssue(ctx, repo.ID, author.ID, "Needs an owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetIssueAssignees(ctx, repo.ID, issue.Number, []string{suspended.ID}, author.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("suspended assignee accepted: %v", err)
	}
	outsider, err := s.UpsertDevUser(ctx, "outsider@example.test", "Outsider")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetIssueAssignees(ctx, repo.ID, issue.Number, []string{outsider.ID}, author.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-employee assignee accepted: %v", err)
	}
	assignees, _, err := s.SetIssueAssignees(ctx, repo.ID, issue.Number, []string{author.ID}, author.ID)
	if err != nil || len(assignees) != 1 || assignees[0].UserID != author.ID {
		t.Fatalf("assign: %+v, %v", assignees, err)
	}
	cleared, _, err := s.SetIssueAssignees(ctx, repo.ID, issue.Number, nil, author.ID)
	if err != nil || len(cleared) != 0 {
		t.Fatalf("clear assignees: %+v, %v", cleared, err)
	}
	if _, _, err := s.SetIssueAssignees(ctx, repo.ID, 999, []string{author.ID}, author.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing issue assignment: %v", err)
	}
	users, err := s.ListAssignableUsers(ctx, repo.ID, "")
	if err != nil || len(users) != 1 || users[0].UserID != author.ID {
		t.Fatalf("assignable users: %+v, %v", users, err)
	}
	filtered, err := s.ListAssignableUsers(ctx, repo.ID, "nobody")
	if err != nil || len(filtered) != 0 {
		t.Fatalf("assignable search: %+v, %v", filtered, err)
	}
}
