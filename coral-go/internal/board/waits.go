package board

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RegisteredWait represents a parked wait condition for an agent on a board.
// When the condition is met (e.g. an agent replies, a task completes, or a commit lands),
// Coral resolves the wait and sends a terminal wake-up notification.
type RegisteredWait struct {
	ID           int64   `db:"id" json:"id"`
	Project      string  `db:"project" json:"project"`
	SubscriberID string  `db:"subscriber_id" json:"subscriber_id"`
	SessionName  string  `db:"session_name" json:"session_name"`
	WaitType     string  `db:"wait_type" json:"wait_type"` // "message", "task", "commit", "any"
	TargetID     string  `db:"target_id" json:"target_id"` // sender name/role, task ID, commit hash/ref
	Reason       string  `db:"reason" json:"reason"`
	Status       string  `db:"status" json:"status"` // "active", "resolved", "cancelled", "expired"
	CreatedAt    string  `db:"created_at" json:"created_at"`
	ResolvedAt   *string `db:"resolved_at" json:"resolved_at,omitempty"`
	ExpiresAt    string  `db:"expires_at" json:"expires_at"`
}

func (w *RegisteredWait) TargetLabel() string {
	switch w.WaitType {
	case WaitOperator:
		return "the Operator's reply"
	case "message":
		if w.TargetID != "" {
			return fmt.Sprintf("message from '%s'", w.TargetID)
		}
		return "board message"
	case "task":
		if w.TargetID != "" {
			return fmt.Sprintf("task #%s", w.TargetID)
		}
		return "task update"
	case "commit":
		if w.TargetID != "" {
			return fmt.Sprintf("commit %s", w.TargetID)
		}
		return "commit landing"
	default:
		if w.TargetID != "" {
			return w.TargetID
		}
		return "board update"
	}
}

func (s *Store) ensureWaitsSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS board_waits (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			project        TEXT NOT NULL,
			subscriber_id  TEXT NOT NULL,
			session_name   TEXT NOT NULL,
			wait_type      TEXT NOT NULL DEFAULT 'message',
			target_id      TEXT NOT NULL DEFAULT '',
			reason         TEXT NOT NULL DEFAULT '',
			status         TEXT NOT NULL DEFAULT 'active'
				CHECK (status IN ('active', 'resolved', 'cancelled', 'expired')),
			created_at     TEXT NOT NULL,
			resolved_at    TEXT,
			expires_at     TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_board_waits_active ON board_waits(project, status);
		CREATE INDEX IF NOT EXISTS idx_board_waits_subscriber ON board_waits(project, subscriber_id, status);
	`)
	return err
}

const DefaultWaitTimeout = 2 * time.Hour

// WaitOperator is the wait type of a question the agent has asked the Operator
// (coral-board wait --operator "question"); its reason is the question, and the
// orchestrator's health check lists it, with its age, until the Operator posts.
const WaitOperator = "operator"

// ListOperatorQuestions returns the active operator waits on a project, oldest first.
func (s *Store) ListOperatorQuestions(ctx context.Context, project string) ([]RegisteredWait, error) {
	waits, err := s.ListActiveWaits(ctx, project)
	if err != nil {
		return nil, err
	}
	var out []RegisteredWait
	for _, w := range waits {
		if w.WaitType == WaitOperator {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// MaxActiveWaitsPerSubscriber is how many active waits one subscriber may have across all boards (#4602, M10). A subscriber
// has one active wait per board (registering again replaces it), so this bounds the boards it can park on at once.
// Resolved and cancelled waits are kept: nothing the user's boards recorded is deleted to make room.
const MaxActiveWaitsPerSubscriber = 50

// ErrTooManyWaits is a new wait past MaxActiveWaitsPerSubscriber.
var ErrTooManyWaits = fmt.Errorf("a subscriber can have at most %d active waits (one per board): cancel one first", MaxActiveWaitsPerSubscriber)

// RegisterWait creates or replaces an active registered wait for a subscriber on a project.
func (s *Store) RegisterWait(ctx context.Context, project, subscriberID, sessionName, waitType, targetID, reason string, timeout time.Duration) (*RegisteredWait, error) {
	if project == "" || subscriberID == "" {
		return nil, fmt.Errorf("project and subscriber_id required")
	}
	if waitType == "" {
		waitType = "message"
	}
	if timeout <= 0 {
		timeout = DefaultWaitTimeout
	}
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	expiresStr := now.Add(timeout).Format(time.RFC3339)

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Cancel any existing active wait for this subscriber on this project
	_, err = tx.ExecContext(ctx,
		"UPDATE board_waits SET status = 'cancelled', resolved_at = ? WHERE project = ? AND subscriber_id = ? AND status = 'active'",
		nowStr, project, subscriberID)
	if err != nil {
		return nil, fmt.Errorf("cancel prior wait: %w", err)
	}

	var active int
	if err := tx.GetContext(ctx, &active, "SELECT COUNT(*) FROM board_waits WHERE subscriber_id = ? AND status = 'active' AND expires_at > ?", subscriberID, nowStr); err != nil {
		return nil, fmt.Errorf("count waits: %w", err)
	}
	if active >= MaxActiveWaitsPerSubscriber {
		return nil, ErrTooManyWaits
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO board_waits (project, subscriber_id, session_name, wait_type, target_id, reason, status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		project, subscriberID, sessionName, waitType, targetID, reason, nowStr, expiresStr)
	if err != nil {
		return nil, fmt.Errorf("insert wait: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.waitChanges.signal()

	return &RegisteredWait{
		ID:           id,
		Project:      project,
		SubscriberID: subscriberID,
		SessionName:  sessionName,
		WaitType:     waitType,
		TargetID:     targetID,
		Reason:       reason,
		Status:       "active",
		CreatedAt:    nowStr,
		ExpiresAt:    expiresStr,
	}, nil
}

// GetActiveWait returns the current active wait for a subscriber on a project, expiring it if timed out.
func (s *Store) GetActiveWait(ctx context.Context, project, subscriberID string) (*RegisteredWait, error) {
	var wait RegisteredWait
	err := s.reader().GetContext(ctx, &wait,
		"SELECT id, project, subscriber_id, session_name, wait_type, target_id, reason, status, created_at, resolved_at, expires_at FROM board_waits WHERE project = ? AND subscriber_id = ? AND status = 'active'",
		project, subscriberID)
	if err != nil {
		return nil, nil
	}
	if t, parseErr := time.Parse(time.RFC3339, wait.ExpiresAt); parseErr == nil && time.Now().UTC().After(t) {
		nowStr := nowUTC()
		_, _ = s.db.ExecContext(ctx,
			"UPDATE board_waits SET status = 'expired', resolved_at = ? WHERE id = ?",
			nowStr, wait.ID)
		s.waitChanges.signal()
		return nil, nil
	}
	return &wait, nil
}

// ListActiveWaits returns all active waits on a project, expiring any whose timeout has elapsed.
func (s *Store) ListActiveWaits(ctx context.Context, project string) ([]RegisteredWait, error) {
	var waits []RegisteredWait
	err := s.reader().SelectContext(ctx, &waits,
		"SELECT id, project, subscriber_id, session_name, wait_type, target_id, reason, status, created_at, resolved_at, expires_at FROM board_waits WHERE project = ? AND status = 'active'",
		project)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var active []RegisteredWait
	for _, w := range waits {
		if t, parseErr := time.Parse(time.RFC3339, w.ExpiresAt); parseErr == nil && now.After(t) {
			_, _ = s.db.ExecContext(ctx,
				"UPDATE board_waits SET status = 'expired', resolved_at = ? WHERE id = ?",
				now.Format(time.RFC3339), w.ID)
			s.waitChanges.signal()
			continue
		}
		active = append(active, w)
	}
	return active, nil
}

// CancelActiveWait cancels any active wait for a subscriber on a project.
func (s *Store) CancelActiveWait(ctx context.Context, project, subscriberID string) error {
	nowStr := nowUTC()
	_, err := s.db.ExecContext(ctx,
		"UPDATE board_waits SET status = 'cancelled', resolved_at = ? WHERE project = ? AND subscriber_id = ? AND status = 'active'",
		nowStr, project, subscriberID)
	if err == nil {
		s.waitChanges.signal()
	}
	return err
}

// ResolveMatchingMessageWaits matches active message waits against a new board message.
// Matching waits are marked resolved and returned so callers can deliver wake-up nudges.
func (s *Store) ResolveMatchingMessageWaits(ctx context.Context, project, senderID, content string) ([]RegisteredWait, error) {
	waits, err := s.ListActiveWaits(ctx, project)
	if err != nil || len(waits) == 0 {
		return nil, err
	}
	var matched []RegisteredWait
	nowStr := nowUTC()
	// A message explicitly addressed to another teammate is not a reply to a
	// broad "from" wait. Directly addressed waits and broadcast tags still win.
	var subscribers []struct {
		SubscriberID string `db:"subscriber_id"`
		JobTitle     string `db:"job_title"`
	}
	_ = s.reader().SelectContext(ctx, &subscribers, "SELECT subscriber_id, job_title FROM board_subscribers WHERE project = ?", project)
	for _, w := range waits {
		if w.SubscriberID == senderID {
			continue
		}
		if w.WaitType == WaitOperator {
			// A question for the Operator ends only with the Operator's own message. The name
			// is reserved (no agent can subscribe as it), so an exact match is enough.
			if senderID == "Operator" {
				if _, err := s.db.ExecContext(ctx,
					"UPDATE board_waits SET status = 'resolved', resolved_at = ? WHERE id = ?", nowStr, w.ID); err == nil {
					w.Status = "resolved"
					resAt := nowStr
					w.ResolvedAt = &resAt
					matched = append(matched, w)
				}
			}
			continue
		}
		var jobTitle string
		var sub struct {
			JobTitle string `db:"job_title"`
		}
		if err := s.reader().GetContext(ctx, &sub, "SELECT job_title FROM board_subscribers WHERE project = ? AND subscriber_id = ?", project, w.SubscriberID); err == nil {
			jobTitle = sub.JobTitle
		}
		numbered := s.numberedNamesCached(ctx, project)
		explicitlyForWaiter := containsExplicitTag(content, w.SubscriberID, jobTitle, numbered)

		if w.WaitType != "message" && w.WaitType != "any" {
			// A task or commit wait also ends on a message that explicitly tags the waiter (#247): whoever wrote to it by
			// name wants it awake now, not when the task or the commit it waits for comes. Anything else leaves it waiting.
			if explicitlyForWaiter && s.resolveWait(ctx, &w, nowStr) {
				matched = append(matched, w)
			}
			continue
		}

		target := strings.TrimSpace(w.TargetID)
		matches := false
		explicitlyForSomeoneElse := false
		if !explicitlyForWaiter {
			for _, sub := range subscribers {
				if sub.SubscriberID == w.SubscriberID || sub.SubscriberID == senderID {
					continue
				}
				if containsExplicitTag(content, sub.SubscriberID, sub.JobTitle, numbered) {
					explicitlyForSomeoneElse = true
					break
				}
			}
		}
		if target == "" || target == "*" {
			matches = !explicitlyForSomeoneElse
		} else if strings.EqualFold(target, senderID) {
			matches = !explicitlyForSomeoneElse || explicitlyForWaiter
		} else if jobTitle != "" && strings.EqualFold(target, jobTitle) {
			matches = !explicitlyForSomeoneElse || explicitlyForWaiter
		} else if explicitlyForWaiter {
			matches = true
		}

		if matches {
			_, updateErr := s.db.ExecContext(ctx,
				"UPDATE board_waits SET status = 'resolved', resolved_at = ? WHERE id = ?",
				nowStr, w.ID)
			if updateErr == nil {
				w.Status = "resolved"
				resAt := nowStr
				w.ResolvedAt = &resAt
				matched = append(matched, w)
			}
		}
	}
	if len(matched) > 0 {
		s.waitChanges.signal()
	}
	return matched, nil
}

// ResolveMatchingTaskWaits matches active task waits against a completed or unblocked task.
func (s *Store) ResolveMatchingTaskWaits(ctx context.Context, project string, taskID int64) ([]RegisteredWait, error) {
	waits, err := s.ListActiveWaits(ctx, project)
	if err != nil || len(waits) == 0 {
		return nil, err
	}
	var matched []RegisteredWait
	nowStr := nowUTC()
	taskIDStr := strconv.FormatInt(taskID, 10)
	for _, w := range waits {
		if w.WaitType != "task" && w.WaitType != "any" {
			continue
		}
		target := strings.TrimSpace(w.TargetID)
		matches := false
		if target == "" || target == "*" || target == taskIDStr {
			matches = true
		}
		if matches {
			_, updateErr := s.db.ExecContext(ctx,
				"UPDATE board_waits SET status = 'resolved', resolved_at = ? WHERE id = ?",
				nowStr, w.ID)
			if updateErr == nil {
				w.Status = "resolved"
				resAt := nowStr
				w.ResolvedAt = &resAt
				matched = append(matched, w)
			}
		}
	}
	if len(matched) > 0 {
		s.waitChanges.signal()
	}
	return matched, nil
}

// ResolveMatchingCommitWaits matches active commit waits against a landed git commit hash.
func (s *Store) ResolveMatchingCommitWaits(ctx context.Context, project, commitHash string) ([]RegisteredWait, error) {
	waits, err := s.ListActiveWaits(ctx, project)
	if err != nil || len(waits) == 0 {
		return nil, err
	}
	var matched []RegisteredWait
	nowStr := nowUTC()
	cleanHash := strings.TrimSpace(commitHash)
	for _, w := range waits {
		if w.WaitType != "commit" && w.WaitType != "any" {
			continue
		}
		target := strings.TrimSpace(w.TargetID)
		matches := false
		if target == "" || target == "*" {
			matches = true
		} else if cleanHash != "" && (strings.HasPrefix(cleanHash, target) || strings.HasPrefix(target, cleanHash)) {
			matches = true
		}
		if matches {
			_, updateErr := s.db.ExecContext(ctx,
				"UPDATE board_waits SET status = 'resolved', resolved_at = ? WHERE id = ?",
				nowStr, w.ID)
			if updateErr == nil {
				w.Status = "resolved"
				resAt := nowStr
				w.ResolvedAt = &resAt
				matched = append(matched, w)
			}
		}
	}
	if len(matched) > 0 {
		s.waitChanges.signal()
	}
	return matched, nil
}

// resolveWait marks w resolved at now; false when the update failed (w is left as it was).
func (s *Store) resolveWait(ctx context.Context, w *RegisteredWait, now string) bool {
	if _, err := s.db.ExecContext(ctx, "UPDATE board_waits SET status = 'resolved', resolved_at = ? WHERE id = ?", now, w.ID); err != nil {
		return false
	}
	w.Status = "resolved"
	resAt := now
	w.ResolvedAt = &resAt
	return true
}
