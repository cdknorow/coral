// Package board provides the message board store and HTTP handlers.
// It uses a separate SQLite database from the main Coral store.
package board

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// Subscriber represents a board subscriber.
type Subscriber struct {
	ID           int64   `db:"id" json:"id"`
	Project      string  `db:"project" json:"project"`
	SessionID    string  `db:"session_id" json:"session_id"`
	JobTitle     string  `db:"job_title" json:"job_title"`
	WebhookURL   *string `db:"webhook_url" json:"webhook_url,omitempty"`
	OriginServer *string `db:"origin_server" json:"origin_server,omitempty"`
	LastReadID   int64   `db:"last_read_id" json:"last_read_id"`
	SubscribedAt string  `db:"subscribed_at" json:"subscribed_at"`
}

// Message represents a board message.
type Message struct {
	ID        int64  `db:"id" json:"id"`
	Project   string `db:"project" json:"project"`
	SessionID string `db:"session_id" json:"session_id"`
	Content   string `db:"content" json:"content"`
	CreatedAt string `db:"created_at" json:"created_at"`
	JobTitle  string `db:"job_title" json:"job_title,omitempty"`
}

// ProjectInfo holds project summary info.
type ProjectInfo struct {
	Project         string `db:"project" json:"project"`
	SubscriberCount int    `db:"subscriber_count" json:"subscriber_count"`
	MessageCount    int    `db:"message_count" json:"message_count"`
}

// Store provides message board operations with its own SQLite database.
type Store struct {
	db *sqlx.DB
}

// NewStore creates a new board Store with its own database.
func NewStore(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create board db directory: %w", err)
	}

	db, err := sqlx.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open board database: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.ensureSchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) ensureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS board_subscribers (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			project       TEXT NOT NULL,
			session_id    TEXT NOT NULL,
			job_title     TEXT NOT NULL,
			webhook_url   TEXT,
			origin_server TEXT,
			last_read_id  INTEGER NOT NULL DEFAULT 0,
			subscribed_at TEXT NOT NULL,
			UNIQUE(project, session_id)
		);
		CREATE TABLE IF NOT EXISTS board_messages (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project     TEXT NOT NULL,
			session_id  TEXT NOT NULL,
			content     TEXT NOT NULL,
			created_at  TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_board_messages_project ON board_messages(project, id);
	`)
	return err
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// ── Subscribers ──────────────────────────────────────────────────────

// Subscribe adds or updates a subscriber on a board project.
func (s *Store) Subscribe(ctx context.Context, project, sessionID, jobTitle string, webhookURL, originServer *string) (*Subscriber, error) {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO board_subscribers (project, session_id, job_title, webhook_url, origin_server, subscribed_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project, session_id) DO UPDATE SET
		     job_title = excluded.job_title,
		     webhook_url = excluded.webhook_url,
		     origin_server = excluded.origin_server`,
		project, sessionID, jobTitle, webhookURL, originServer, now)
	if err != nil {
		return nil, err
	}
	var sub Subscriber
	err = s.db.GetContext(ctx, &sub,
		"SELECT * FROM board_subscribers WHERE project = ? AND session_id = ?",
		project, sessionID)
	return &sub, err
}

// Unsubscribe removes a subscriber. Returns true if a row was deleted.
func (s *Store) Unsubscribe(ctx context.Context, project, sessionID string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM board_subscribers WHERE project = ? AND session_id = ?",
		project, sessionID)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// ListSubscribers returns all subscribers for a project.
func (s *Store) ListSubscribers(ctx context.Context, project string) ([]Subscriber, error) {
	var subs []Subscriber
	err := s.db.SelectContext(ctx, &subs,
		"SELECT * FROM board_subscribers WHERE project = ? ORDER BY subscribed_at", project)
	return subs, err
}

// GetSubscription returns the active subscription for a session.
func (s *Store) GetSubscription(ctx context.Context, sessionID string) (*Subscriber, error) {
	var sub Subscriber
	err := s.db.GetContext(ctx, &sub,
		"SELECT * FROM board_subscribers WHERE session_id = ? LIMIT 1", sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

// GetAllSubscriptions returns all subscriptions keyed by session_id.
func (s *Store) GetAllSubscriptions(ctx context.Context) (map[string]*Subscriber, error) {
	var subs []Subscriber
	err := s.db.SelectContext(ctx, &subs, "SELECT * FROM board_subscribers")
	if err != nil {
		return nil, err
	}
	result := make(map[string]*Subscriber, len(subs))
	for i := range subs {
		result[subs[i].SessionID] = &subs[i]
	}
	return result, nil
}

// ── Messages ─────────────────────────────────────────────────────────

// PostMessage posts a new message to a project board.
func (s *Store) PostMessage(ctx context.Context, project, sessionID, content string) (*Message, error) {
	now := nowUTC()
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO board_messages (project, session_id, content, created_at) VALUES (?, ?, ?, ?)",
		project, sessionID, content, now)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &Message{ID: id, Project: project, SessionID: sessionID, Content: content, CreatedAt: now}, nil
}

// ReadMessages returns unread messages for a subscriber (cursor-based).
func (s *Store) ReadMessages(ctx context.Context, project, sessionID string, limit int) ([]Message, error) {
	// Get subscriber cursor
	var lastReadID int64
	err := s.db.GetContext(ctx, &lastReadID,
		"SELECT last_read_id FROM board_subscribers WHERE project = ? AND session_id = ?",
		project, sessionID)
	if err != nil {
		return nil, nil // Not subscribed
	}

	// Fetch new messages from others
	var messages []Message
	err = s.db.SelectContext(ctx, &messages,
		`SELECT m.id, m.project, m.session_id, m.content, m.created_at,
		        COALESCE(s.job_title, 'Unknown') as job_title
		 FROM board_messages m
		 LEFT JOIN board_subscribers s ON m.project = s.project AND m.session_id = s.session_id
		 WHERE m.project = ? AND m.id > ? AND m.session_id != ?
		 ORDER BY m.id ASC LIMIT ?`,
		project, lastReadID, sessionID, limit)
	if err != nil {
		return nil, err
	}

	// Advance cursor past returned messages and own messages
	newCursor := lastReadID
	if len(messages) > 0 {
		for _, m := range messages {
			if m.ID > newCursor {
				newCursor = m.ID
			}
		}
	}
	// Skip past own messages
	var ownMax int64
	s.db.GetContext(ctx, &ownMax,
		"SELECT COALESCE(MAX(id), 0) FROM board_messages WHERE project = ? AND session_id = ?",
		project, sessionID)
	if ownMax > newCursor {
		newCursor = ownMax
	}

	if newCursor > lastReadID {
		s.db.ExecContext(ctx,
			"UPDATE board_subscribers SET last_read_id = ? WHERE project = ? AND session_id = ?",
			newCursor, project, sessionID)
	}

	return messages, nil
}

// ListMessages returns recent messages (no cursor, no side effects).
func (s *Store) ListMessages(ctx context.Context, project string, limit, offset int) ([]Message, error) {
	var messages []Message
	err := s.db.SelectContext(ctx, &messages,
		`SELECT m.id, m.project, m.session_id, m.content, m.created_at,
		        COALESCE(s.job_title, 'Unknown') as job_title
		 FROM board_messages m
		 LEFT JOIN board_subscribers s ON m.project = s.project AND m.session_id = s.session_id
		 WHERE m.project = ?
		 ORDER BY m.id ASC LIMIT ? OFFSET ?`,
		project, limit, offset)
	return messages, err
}

// CountMessages returns the total message count for a project.
func (s *Store) CountMessages(ctx context.Context, project string) (int, error) {
	var count int
	err := s.db.GetContext(ctx, &count,
		"SELECT COUNT(*) FROM board_messages WHERE project = ?", project)
	return count, err
}

// CheckUnread returns the count of unread messages that mention this agent.
func (s *Store) CheckUnread(ctx context.Context, project, sessionID string) (int, error) {
	var sub struct {
		LastReadID int64  `db:"last_read_id"`
		JobTitle   string `db:"job_title"`
	}
	err := s.db.GetContext(ctx, &sub,
		"SELECT last_read_id, job_title FROM board_subscribers WHERE project = ? AND session_id = ?",
		project, sessionID)
	if err != nil {
		return 0, nil
	}

	patterns := []string{
		"%@notify-all%", "%@notify_all%", "%@notifyall%", "%@all%",
		fmt.Sprintf("%%@%s%%", sessionID),
	}
	if sub.JobTitle != "" {
		patterns = append(patterns, fmt.Sprintf("%%@%s%%", sub.JobTitle))
	}

	whereClauses := make([]string, len(patterns))
	args := []interface{}{project, sub.LastReadID, sessionID}
	for i, p := range patterns {
		whereClauses[i] = "content LIKE ? COLLATE NOCASE"
		args = append(args, p)
	}

	var count int
	query := fmt.Sprintf(
		`SELECT COUNT(*) FROM board_messages
		 WHERE project = ? AND id > ? AND session_id != ? AND (%s)`,
		strings.Join(whereClauses, " OR "))
	err = s.db.GetContext(ctx, &count, query, args...)
	return count, err
}

// GetAllUnreadCounts returns unread mention counts for all subscribers.
func (s *Store) GetAllUnreadCounts(ctx context.Context) (map[string]int, error) {
	var subs []struct {
		Project    string `db:"project"`
		SessionID  string `db:"session_id"`
		JobTitle   string `db:"job_title"`
		LastReadID int64  `db:"last_read_id"`
	}
	err := s.db.SelectContext(ctx, &subs,
		"SELECT project, session_id, job_title, last_read_id FROM board_subscribers")
	if err != nil || len(subs) == 0 {
		return map[string]int{}, nil
	}

	// Group by project
	byProject := make(map[string][]struct {
		SessionID  string
		JobTitle   string
		LastReadID int64
	})
	for _, sub := range subs {
		byProject[sub.Project] = append(byProject[sub.Project], struct {
			SessionID  string
			JobTitle   string
			LastReadID int64
		}{sub.SessionID, sub.JobTitle, sub.LastReadID})
	}

	result := make(map[string]int)

	for project, projectSubs := range byProject {
		minCursor := projectSubs[0].LastReadID
		for _, sub := range projectSubs {
			if sub.LastReadID < minCursor {
				minCursor = sub.LastReadID
			}
		}

		var msgs []struct {
			ID        int64  `db:"id"`
			SessionID string `db:"session_id"`
			Content   string `db:"content"`
		}
		s.db.SelectContext(ctx, &msgs,
			"SELECT id, session_id, content FROM board_messages WHERE project = ? AND id > ? ORDER BY id",
			project, minCursor)

		for _, sub := range projectSubs {
			mentionTerms := []string{"@notify-all", "@notify_all", "@notifyall", "@all",
				"@" + sub.SessionID}
			if sub.JobTitle != "" {
				mentionTerms = append(mentionTerms, "@"+sub.JobTitle)
			}

			count := 0
			for _, msg := range msgs {
				if msg.ID <= sub.LastReadID || msg.SessionID == sub.SessionID {
					continue
				}
				contentLower := strings.ToLower(msg.Content)
				for _, term := range mentionTerms {
					if strings.Contains(contentLower, strings.ToLower(term)) {
						count++
						break
					}
				}
			}
			result[sub.SessionID] = count
		}
	}

	return result, nil
}

// DeleteMessage deletes a single message by ID.
func (s *Store) DeleteMessage(ctx context.Context, messageID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM board_messages WHERE id = ?", messageID)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// GetWebhookTargets returns subscribers with webhook URLs (excluding sender).
func (s *Store) GetWebhookTargets(ctx context.Context, project, excludeSessionID string) ([]Subscriber, error) {
	var subs []Subscriber
	err := s.db.SelectContext(ctx, &subs,
		`SELECT * FROM board_subscribers
		 WHERE project = ? AND session_id != ? AND webhook_url IS NOT NULL AND webhook_url != ''`,
		project, excludeSessionID)
	return subs, err
}

// ── Projects ─────────────────────────────────────────────────────────

// ListProjects returns all known projects with subscriber and message counts.
func (s *Store) ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	var projects []ProjectInfo
	err := s.db.SelectContext(ctx, &projects,
		`SELECT project,
		        (SELECT COUNT(*) FROM board_subscribers s WHERE s.project = p.project) as subscriber_count,
		        (SELECT COUNT(*) FROM board_messages m WHERE m.project = p.project) as message_count
		 FROM (
		     SELECT DISTINCT project FROM board_subscribers
		     UNION
		     SELECT DISTINCT project FROM board_messages
		 ) p ORDER BY project`)
	return projects, err
}

// DeleteProject removes all messages and subscribers for a project.
func (s *Store) DeleteProject(ctx context.Context, project string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tx.ExecContext(ctx, "DELETE FROM board_messages WHERE project = ?", project)
	tx.ExecContext(ctx, "DELETE FROM board_subscribers WHERE project = ?", project)
	return tx.Commit()
}
