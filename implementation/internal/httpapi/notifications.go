package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"fithub.local/fithub/internal/store"
)

// Notification delivery is best effort by design. The operation that caused an
// event (a comment, a review, a merge) has already been committed by the time a
// notification is written, so failing the request would tell the user their
// comment was rejected when it was stored. A failure is therefore logged and the
// original result is returned unchanged. The event key makes a later retry safe.
func (s *Server) notify(ctx context.Context, repoID, actorID, kind string, subjectNumber, runNumber *int64, subjectTitle, eventKey string, recipients []string) {
	if len(recipients) == 0 {
		return
	}
	if _, err := s.Store.Notify(ctx, repoID, actorID, kind, subjectNumber, runNumber, subjectTitle, eventKey, recipients); err != nil {
		slog.Warn("notification was not recorded", "kind", kind, "repo", repoID, "error", err)
	}
}

// notifyIssueEvent tells the Issue author and its current assignees about an
// event on one Issue.
func (s *Server) notifyIssueEvent(ctx context.Context, repoID string, number int64, actorID, kind, title, eventKey string) {
	recipients, err := s.Store.IssueNotificationRecipients(ctx, repoID, number, actorID)
	if err != nil {
		slog.Warn("notification recipients could not be resolved", "kind", kind, "error", err)
		return
	}
	s.notify(ctx, repoID, actorID, kind, &number, nil, title, eventKey, recipients)
}

// notifyPullEvent tells the pull request author, its reviewers, and its inline
// commenters about an event on one pull request.
func (s *Server) notifyPullEvent(ctx context.Context, repoID string, number int64, actorID, kind, title, eventKey string) {
	recipients, err := s.Store.PullNotificationRecipients(ctx, repoID, number, actorID)
	if err != nil {
		slog.Warn("notification recipients could not be resolved", "kind", kind, "error", err)
		return
	}
	s.notify(ctx, repoID, actorID, kind, &number, nil, title, eventKey, recipients)
}

// notifyRunFailure tells the account that triggered a run that it failed. The
// actor is recorded as the system's best available identity: the run's own
// triggering account is the recipient, so no actor is set and the inbox row
// reads as an automatic notice rather than as something a colleague did.
func (s *Server) notifyRunFailure(ctx context.Context, repoID, runID string, runNumber int64, title string) {
	recipients, err := s.Store.RunNotificationRecipients(ctx, runID)
	if err != nil {
		slog.Warn("notification recipients could not be resolved", "kind", store.NotifyRunFailed, "error", err)
		return
	}
	if len(recipients) == 0 {
		return
	}
	eventKey := "run_failed:" + runID
	s.notify(ctx, repoID, "", store.NotifyRunFailed, nil, &runNumber, title, eventKey, recipients)
}

// GET /api/v1/notifications
//
// The inbox is always the caller's own; there is no way to read another
// account's notifications, and a token cannot widen it.
func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	page, perPage := 1, 20
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1_000_000 {
			fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid page")
			return
		}
		page = parsed
	}
	unreadOnly := r.URL.Query().Get("unread") == "true"
	items, hasMore, unread, err := s.Store.ListNotifications(r.Context(), p.user.ID, unreadOnly, page, perPage)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load notifications")
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, notificationView(item))
	}
	respond(w, http.StatusOK, map[string]any{
		"notifications": views, "page": page, "perPage": perPage,
		"hasMore": hasMore, "unreadCount": unread,
	})
}

// notificationView flattens a notification for the API. It never includes
// anything the reader is not entitled to see: the row would not have been
// selected otherwise.
func notificationView(item store.Notification) map[string]any {
	view := map[string]any{
		"id": item.ID, "kind": item.Kind, "actorName": item.ActorName,
		"repoId": item.RepoID, "teamSlug": item.TeamSlug, "repoSlug": item.RepoSlug,
		"subjectTitle": item.SubjectTitle, "read": item.Read, "createdAt": item.CreatedAt,
	}
	if item.ActorID != nil {
		view["actorId"] = *item.ActorID
	}
	if item.SubjectNumber != nil {
		view["subjectNumber"] = *item.SubjectNumber
	}
	if item.RunNumber != nil {
		view["runNumber"] = *item.RunNumber
	}
	if item.ReadAt != nil {
		view["readAt"] = *item.ReadAt
	}
	return view
}

// POST /api/v1/notifications/read
//
// With no ids the whole inbox is marked read, which is what "mark all" means. An
// explicit id list is bounded; an unbounded list would let one request touch an
// arbitrary number of rows.
func (s *Server) markNotificationsRead(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &body) {
		return
	}
	updated, err := s.Store.MarkNotificationsRead(r.Context(), p.user.ID, body.IDs)
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "At most 100 notification IDs may be marked at once")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to update notifications")
		return
	}
	unread, err := s.Store.CountUnreadNotifications(r.Context(), p.user.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to count notifications")
		return
	}
	respond(w, http.StatusOK, map[string]any{"marked": updated, "unreadCount": unread})
}
