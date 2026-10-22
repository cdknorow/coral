// Subscribing, unsubscribing and looking up subscribers (moved out of store.go, #4998; no behaviour change).

package board

import (
	"context"
	"database/sql"
)

// Subscribe adds or updates a subscriber on a board project.
// subscriberID is the stable identity (role name). sessionName is the current tmux/pty session.
func (s *Store) Subscribe(ctx context.Context, project, subscriberID, jobTitle, sessionName string, originServer *string, receiveMode string, canPeek ...bool) (*Subscriber, error) {
	peekFlag := 0
	if len(canPeek) > 0 && canPeek[0] {
		peekFlag = 1
	}
	if receiveMode == "" {
		if isOrchestrator(subscriberID, jobTitle, peekFlag) {
			receiveMode = "all"
		} else {
			receiveMode = "mentions"
		}
	}
	now := nowUTC()

	// For new subscribers who haven't been on this board before, start their
	// cursor at the latest message so they don't get flooded with history.
	var carryForwardCursor int64
	_ = s.db.GetContext(ctx, &carryForwardCursor,
		"SELECT COALESCE(MAX(last_read_id), 0) FROM board_subscribers WHERE project = ? AND subscriber_id = ?",
		project, subscriberID)
	if carryForwardCursor == 0 {
		_ = s.db.GetContext(ctx, &carryForwardCursor,
			"SELECT COALESCE(MAX(last_read_id), 0) FROM board_subscribers WHERE project = ?",
			project)
	}

	// session_id mirrors subscriber_id so UNIQUE(project, session_id) enforces subscriber uniqueness.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO board_subscribers (project, session_id, subscriber_id, session_name, job_title, origin_server, receive_mode, last_read_id, subscribed_at, is_active, can_peek, session_joined_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		 ON CONFLICT(project, session_id) DO UPDATE SET
		     job_title = excluded.job_title,
		     webhook_url = NULL,
		     origin_server = excluded.origin_server,
		     receive_mode = excluded.receive_mode,
		     session_joined_at = CASE
		         WHEN board_subscribers.session_name IS excluded.session_name AND board_subscribers.is_active = 1
		         THEN COALESCE(board_subscribers.session_joined_at, board_subscribers.subscribed_at)
		         ELSE excluded.session_joined_at END,
		     session_name = excluded.session_name,
		     subscriber_id = excluded.subscriber_id,
		     is_active = 1,
		     can_peek = excluded.can_peek`,
		project, subscriberID, subscriberID, sessionName, jobTitle, originServer, receiveMode, carryForwardCursor, now, peekFlag, now)
	if err == nil {
		s.ensureBoardRow(ctx, project)
	}
	s.subscribersChanged()
	if err != nil {
		return nil, err
	}
	var sub Subscriber
	err = s.db.GetContext(ctx, &sub,
		"SELECT * FROM board_subscribers WHERE project = ? AND subscriber_id = ?",
		project, subscriberID)
	return &sub, err
}

// Unsubscribe marks a subscriber as inactive. Returns true if a row was updated.
func (s *Store) Unsubscribe(ctx context.Context, project, subscriberID string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		"UPDATE board_subscribers SET is_active = 0 WHERE project = ? AND subscriber_id = ? AND is_active = 1",
		project, subscriberID)
	s.subscribersChanged()
	if err != nil {
		return false, err
	}
	s.unreadCounts.forget(project, subscriberID)
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// AdvanceReadCursor sets a subscriber's last_read_id to the current max
// message ID on the board, so they see no stale unreads after a reset.
func (s *Store) AdvanceReadCursor(ctx context.Context, project, subscriberID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE board_subscribers
		 SET last_read_id = COALESCE((SELECT MAX(id) FROM board_messages WHERE project = ?), 0)
		 WHERE project = ? AND subscriber_id = ?`,
		project, project, subscriberID)
	return err
}

// ListSubscribers returns all active subscribers for a project.
func (s *Store) ListSubscribers(ctx context.Context, project string) ([]Subscriber, error) {
	var subs []Subscriber
	err := s.reader().SelectContext(ctx, &subs,
		"SELECT * FROM board_subscribers WHERE project = ? AND is_active = 1 ORDER BY subscribed_at, id", project)
	return subs, err
}

// GetSubscription returns the active subscription for a subscriber.
func (s *Store) GetSubscription(ctx context.Context, subscriberID string) (*Subscriber, error) {
	var sub Subscriber
	err := s.reader().GetContext(ctx, &sub,
		"SELECT * FROM board_subscribers WHERE subscriber_id = ? AND is_active = 1 ORDER BY subscribed_at DESC, rowid DESC LIMIT 1", subscriberID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

// GetProjectSubscription returns a subscriber's active subscription on one
// board. The same subscriber_id (e.g. "Frontend Dev") can be subscribed on
// several boards with different sessions, so task nudges must use this rather
// than GetSubscription.
func (s *Store) GetProjectSubscription(ctx context.Context, project, subscriberID string) (*Subscriber, error) {
	var sub Subscriber
	err := s.reader().GetContext(ctx, &sub,
		"SELECT * FROM board_subscribers WHERE project = ? AND subscriber_id = ? AND is_active = 1 ORDER BY subscribed_at DESC, id DESC LIMIT 1", project, subscriberID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

// GetSubscriptionBySessionName returns the active subscription for a specific tmux session.
// This is more precise than GetSubscription when the same subscriber_id has
// multiple active subscriptions across different boards.
func (s *Store) GetSubscriptionBySessionName(ctx context.Context, sessionName string) (*Subscriber, error) {
	var sub Subscriber
	err := getHot(ctx, s.hot().subscriptionBySessionName, s.reader(), &sub, sqlSubscriptionBySessionName, sessionName)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

// SessionSubscription is the agent session sessionName's active subscription on project, or
// nil when it has none there.
func (s *Store) SessionSubscription(ctx context.Context, project, sessionName string) (*Subscriber, error) {
	var sub Subscriber
	err := s.reader().GetContext(ctx, &sub,
		"SELECT * FROM board_subscribers WHERE project = ? AND session_name = ? AND is_active = 1 ORDER BY subscribed_at DESC, rowid DESC LIMIT 1", project, sessionName)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

// SessionSubscribedOn reports whether the agent session sessionName has an active
// subscription on project: what a notice about that board must find before it is typed into
// the session (#3864).
func (s *Store) SessionSubscribedOn(ctx context.Context, project, sessionName string) (bool, error) {
	var n int
	err := s.reader().GetContext(ctx, &n,
		"SELECT COUNT(*) FROM board_subscribers WHERE project = ? AND session_name = ? AND is_active = 1", project, sessionName)
	return n > 0, err
}

// GetAllSubscriptions returns all active subscriptions keyed by session_name
// (the tmux session identifier) for compatibility with live session lookups.
func (s *Store) GetAllSubscriptions(ctx context.Context) (map[string]*Subscriber, error) {
	var subs []Subscriber
	err := s.reader().SelectContext(ctx, &subs, "SELECT * FROM board_subscribers WHERE is_active = 1")
	if err != nil {
		return nil, err
	}
	result := make(map[string]*Subscriber, len(subs))
	for i := range subs {
		result[subs[i].SessionName] = &subs[i]
	}
	return result, nil
}

// UpdateSessionName updates the mutable tmux/pty session name for a subscriber.
// Used when an agent restarts and gets a new session but keeps its identity.
func (s *Store) UpdateSessionName(ctx context.Context, project, subscriberID, sessionName string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE board_subscribers SET session_name = ? WHERE project = ? AND subscriber_id = ?",
		sessionName, project, subscriberID)
	return err
}
