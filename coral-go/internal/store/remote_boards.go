package store

import (
	"context"
)

// RemoteBoardSub represents a remote board subscription for local tmux notification.
type RemoteBoardSub struct {
	ID                 int64  `db:"id" json:"id"`
	SessionID          string `db:"session_id" json:"session_id"`
	RemoteServer       string `db:"remote_server" json:"remote_server"`
	Project            string `db:"project" json:"project"`
	JobTitle           string `db:"job_title" json:"job_title"`
	LastNotifiedUnread int    `db:"last_notified_unread" json:"last_notified_unread"`
	CreatedAt          string `db:"created_at" json:"created_at"`
}

// RemoteBoardStore provides operations on the remote_board_subscriptions table.
type RemoteBoardStore struct {
	db *DB
}

// NewRemoteBoardStore creates a new RemoteBoardStore.
func NewRemoteBoardStore(db *DB) *RemoteBoardStore {
	return &RemoteBoardStore{db: db}
}

// AddRemoteSub adds or updates a remote board subscription.
func (s *RemoteBoardStore) AddRemoteSub(ctx context.Context, sessionID, remoteServer, project, jobTitle string) (*RemoteBoardSub, error) {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO remote_board_subscriptions
		 (session_id, remote_server, project, job_title, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, remote_server, project)
		 DO UPDATE SET job_title = excluded.job_title`,
		sessionID, remoteServer, project, jobTitle, now)
	if err != nil {
		return nil, err
	}

	var sub RemoteBoardSub
	err = s.db.GetContext(ctx, &sub,
		`SELECT * FROM remote_board_subscriptions
		 WHERE session_id = ? AND remote_server = ? AND project = ?`,
		sessionID, remoteServer, project)
	return &sub, err
}

// RemoveRemoteSubs removes all remote board subscriptions for a session.
func (s *RemoteBoardStore) RemoveRemoteSubs(ctx context.Context, sessionID string) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM remote_board_subscriptions WHERE session_id = ?", sessionID)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, nil
}

// ListAllRemoteSubs returns all remote board subscriptions.
func (s *RemoteBoardStore) ListAllRemoteSubs(ctx context.Context) ([]RemoteBoardSub, error) {
	var subs []RemoteBoardSub
	err := s.db.SelectContext(ctx, &subs,
		"SELECT * FROM remote_board_subscriptions ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	if subs == nil {
		subs = []RemoteBoardSub{}
	}
	return subs, nil
}

// UpdateLastNotified updates the last notified unread count for a subscription.
func (s *RemoteBoardStore) UpdateLastNotified(ctx context.Context, subID int64, unread int) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE remote_board_subscriptions SET last_notified_unread = ? WHERE id = ?",
		unread, subID)
	return err
}
