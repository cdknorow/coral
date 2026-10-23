// Posting, reading, listing and searching messages (moved out of store.go, #4998; no behaviour change).

package board

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/subgentic/coral/internal/clitext"
)

// PostMessage posts a new message to a project board.
func (s *Store) PostMessage(ctx context.Context, project, subscriberID, content string, targetGroupID *string) (*Message, error) {
	return s.postMessage(ctx, project, subscriberID, content, targetGroupID, "", "", false, nil)
}

// OriginHubUser marks a chat post a Coral Hub user made through the tunnel (#3377).
const OriginHubUser = "hub_user"

// OriginHubOperator marks a chat post a Coral Hub user made as the Operator through the
// tunnel (#3404): sent as the Operator, with HubUser naming who.
const OriginHubOperator = "hub_operator"

// OriginHubUserUnknown marks a Coral Hub chat post with no Hub user header: a hub still on
// an older pkg/hubproxy (#3377). It's sent as HubUnknownSender.
const OriginHubUserUnknown = "hub_user_unknown"

// OriginHubNotice marks a fixed-template notice from Coral Hub about a team link (#3489):
// sent as HubUnknownSender ("Coral Hub"), never an agent or the Operator.
const OriginHubNotice = "hub_notice"

// PostHubNotice posts a notice Coral Hub asked for, as "Coral Hub" with origin hub_notice,
// marked as one of Coral's own messages. content is built by Coral from a fixed template.
func (s *Store) PostHubNotice(ctx context.Context, project, content string) (*Message, error) {
	return s.postMessage(ctx, project, HubUnknownSender, content, nil, OriginHubNotice, "", true, nil)
}

// PostMessageWithOrigin is PostMessage with a server-set origin ("" for none).
func (s *Store) PostMessageWithOrigin(ctx context.Context, project, subscriberID, content string, targetGroupID *string, origin string) (*Message, error) {
	return s.postMessage(ctx, project, subscriberID, content, targetGroupID, origin, "", false, nil)
}

// PostHubMessage posts a chat message a Coral Hub user made through the tunnel, with the
// validated Hub user recorded (#3404): sent as that user with origin hub_user, or, when
// asOperator, as the Operator with origin hub_operator.
func (s *Store) PostHubMessage(ctx context.Context, project, hubUser, content string, targetGroupID *string, asOperator bool) (*Message, error) {
	return s.PostHubMessageForTask(ctx, project, hubUser, content, targetGroupID, asOperator, nil)
}

// PostHubMessageForTask is PostHubMessage for a reply to a task (taskID, #5463); nil for a plain post.
func (s *Store) PostHubMessageForTask(ctx context.Context, project, hubUser, content string, targetGroupID *string, asOperator bool, taskID *int64) (*Message, error) {
	sender, origin := hubUser, OriginHubUser
	if asOperator {
		sender, origin = "Operator", OriginHubOperator
	}
	return s.postMessage(ctx, project, sender, content, targetGroupID, origin, hubUser, false, taskID)
}

// PostMessageForTask is PostMessageWithOrigin for a reply to a task (taskID, #5463); nil for a plain post.
func (s *Store) PostMessageForTask(ctx context.Context, project, subscriberID, content string, targetGroupID *string, origin string, taskID *int64) (*Message, error) {
	return s.postMessage(ctx, project, subscriberID, content, targetGroupID, origin, "", false, taskID)
}

// PostSystemMessage posts as one of Coral's own senders (the task queue, the health
// monitor) and marks the message is_system (#3387). Only server code calls it.
func (s *Store) PostSystemMessage(ctx context.Context, project, sender, content string, targetGroupID *string) (*Message, error) {
	return s.postMessage(ctx, project, sender, content, targetGroupID, "", "", true, nil)
}

// PostDefusedMessage posts content as subscriberID (the Operator's own message to an agent) exactly as given: the caller has already cleaned it (clitext.Clean) and then defused the
// mentions it quotes (DefuseMentions puts a zero-width space after an @, which Clean would remove again, so this path does not clean a second time). Only server code calls it, with
// text it built itself.
func (s *Store) PostDefusedMessage(ctx context.Context, project, subscriberID, content string) (*Message, error) {
	return s.insertMessage(ctx, project, subscriberID, content, nil, "", "", false, nil)
}

func (s *Store) postMessage(ctx context.Context, project, subscriberID, content string, targetGroupID *string, origin, hubUser string, system bool, taskID *int64) (*Message, error) {
	// No terminal control is stored (#3580): a post is printed in agents' terminals by
	// coral-board, and system posts quote task titles agents wrote.
	return s.insertMessage(ctx, project, subscriberID, clitext.Clean(content), targetGroupID, origin, hubUser, system, taskID)
}

// insertMessage stores a message whose content is already clean.
func (s *Store) insertMessage(ctx context.Context, project, subscriberID, content string, targetGroupID *string, origin, hubUser string, system bool, taskID *int64) (*Message, error) {
	now := nowUTC()
	var originVal, hubUserVal *string
	if origin != "" {
		originVal = &origin
	}
	if hubUser != "" {
		hubUserVal = &hubUser
	}
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO board_messages (project, session_id, subscriber_id, content, target_group_id, created_at, origin, hub_user, is_system, task_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		project, subscriberID, subscriberID, content, targetGroupID, now, originVal, hubUserVal, system, taskID)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	s.ensureBoardRow(ctx, project)
	msg := &Message{ID: id, Project: project, SubscriberID: subscriberID, Content: content, CreatedAt: now, Origin: originVal, HubUser: hubUserVal, IsSystem: system, TaskID: taskID}
	if targetGroupID != nil {
		msg.TargetGroupID = targetGroupID
	}
	return msg, nil
}

// ReadMessages returns unread messages for a subscriber (cursor-based).
// By default, for non-orchestrator subscribers, only messages where they are
// explicitly tagged (or @all / @notify-all) are returned.
// For orchestrators or when readAll is true, all unread messages from others are returned.
func (s *Store) ReadMessages(ctx context.Context, project, subscriberID string, limit int, readAll ...bool) ([]Message, error) {
	// Get subscriber cursor and role info
	var sub struct {
		LastReadID  int64  `db:"last_read_id"`
		JobTitle    string `db:"job_title"`
		ReceiveMode string `db:"receive_mode"`
		CanPeek     int    `db:"can_peek"`
	}
	err := s.reader().GetContext(ctx, &sub,
		"SELECT last_read_id, job_title, receive_mode, can_peek FROM board_subscribers WHERE project = ? AND subscriber_id = ?",
		project, subscriberID)
	if err != nil {
		return nil, nil // Not subscribed
	}

	wantAll := len(readAll) > 0 && readAll[0]
	isOrch := isOrchestrator(subscriberID, sub.JobTitle, sub.CanPeek)
	filterTagged := !wantAll && !isOrch

	if !wantAll && sub.ReceiveMode == "none" {
		return nil, nil
	}

	var messages []Message
	if filterTagged {
		terms := mentionTerms(subscriberID, sub.JobTitle)
		patterns := make([]string, len(terms))
		for i, t := range terms {
			patterns[i] = "%" + t + "%"
		}

		fetchLimit := limit * 2
		if fetchLimit < 50 {
			fetchLimit = 50
		}
		candidates, indexed, err := s.indexedMentionCandidates(ctx, project, subscriberID, sub.LastReadID, terms, fetchLimit)
		if err != nil {
			return nil, err
		}
		if !indexed {
			if candidates, err = s.likeMentionCandidates(ctx, project, subscriberID, sub.LastReadID, patterns, fetchLimit); err != nil {
				return nil, err
			}
		}

		numbered := s.numberedNamesCached(ctx, project)
		for _, msg := range candidates {
			if containsExplicitTag(msg.Content, subscriberID, sub.JobTitle, numbered) {
				messages = append(messages, msg)
				if len(messages) >= limit {
					break
				}
			}
		}
	} else {
		err = s.reader().SelectContext(ctx, &messages,
			`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system, m.task_id, m.created_at,
			        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title
			 FROM board_messages m
			 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			 WHERE m.project = ? AND m.id > ? AND m.subscriber_id != ?
			 ORDER BY m.id ASC LIMIT ?`,
			project, sub.LastReadID, subscriberID, limit)
		if err != nil {
			return nil, err
		}
	}
	// Advance cursor past returned messages and own messages
	newCursor := sub.LastReadID
	if len(messages) > 0 {
		for _, m := range messages {
			if m.ID > newCursor {
				newCursor = m.ID
			}
		}
	}

	// Skip past own messages
	var ownMax int64
	getHot(ctx, s.hot().ownMaxMessageID, s.reader(), &ownMax, sqlOwnMaxMessageID, project, subscriberID)
	if ownMax > newCursor {
		newCursor = ownMax
	}

	if newCursor > sub.LastReadID {
		s.db.ExecContext(ctx,
			"UPDATE board_subscribers SET last_read_id = ? WHERE project = ? AND subscriber_id = ?",
			newCursor, project, subscriberID)
	}

	return messages, nil
}

// MarkReadThrough moves a subscriber's read position forward to throughID
// (never back), for readers that looked at the newest messages rather than
// reading in order (coral-board read --last N). It reports the messages from
// others that were unread and older than fromID, the oldest one shown, which
// this skips without their having been displayed.
func (s *Store) MarkReadThrough(ctx context.Context, project, subscriberID string, throughID, fromID int64) (skipped int, firstSkipped, lastSkipped int64, err error) {
	var lastReadID int64
	if err = s.reader().GetContext(ctx, &lastReadID,
		"SELECT last_read_id FROM board_subscribers WHERE project = ? AND subscriber_id = ?",
		project, subscriberID); err != nil {
		if err == sql.ErrNoRows {
			err = fmt.Errorf("%s is not subscribed to %s", subscriberID, project)
		}
		return 0, 0, 0, err
	}
	if throughID <= lastReadID {
		return 0, 0, 0, nil
	}
	var r struct {
		N     int   `db:"n"`
		First int64 `db:"first"`
		Last  int64 `db:"last"`
	}
	if err = s.reader().GetContext(ctx, &r,
		`SELECT COUNT(*) AS n, COALESCE(MIN(id), 0) AS first, COALESCE(MAX(id), 0) AS last FROM board_messages
		 WHERE project = ? AND id > ? AND id < ? AND subscriber_id != ? AND subscriber_id NOT IN ('Coral Task Queue')`,
		project, lastReadID, fromID, subscriberID); err != nil {
		return 0, 0, 0, err
	}
	_, err = s.db.ExecContext(ctx,
		"UPDATE board_subscribers SET last_read_id = ? WHERE project = ? AND subscriber_id = ? AND last_read_id < ?",
		throughID, project, subscriberID, throughID)
	return r.N, r.First, r.Last, err
}

// ListMessages returns recent messages (no cursor, no side effects).
// If beforeID > 0, only messages with id < beforeID are returned (keyset pagination).
func (s *Store) ListMessages(ctx context.Context, project string, limit, offset int, beforeID int64) ([]Message, error) {
	var messages []Message
	var err error
	if beforeID > 0 {
		err = s.reader().SelectContext(ctx, &messages,
			`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
			        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
			        m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system, m.task_id
			 FROM board_messages m
			 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			 WHERE m.project = ? AND m.id < ?
			 ORDER BY m.id ASC LIMIT ? OFFSET ?`,
			project, beforeID, limit, offset)
	} else if offset > 0 {
		// Paginated load: use ASC order with offset for consistent pagination
		err = s.reader().SelectContext(ctx, &messages,
			`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
			        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
			        m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system, m.task_id
			 FROM board_messages m
			 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			 WHERE m.project = ?
			 ORDER BY m.id ASC LIMIT ? OFFSET ?`,
			project, limit, offset)
	} else {
		// Initial load (offset=0): get most recent messages
		err = s.reader().SelectContext(ctx, &messages,
			`SELECT * FROM (
			    SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
			           COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
			           m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system, m.task_id
			    FROM board_messages m
			    LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			    WHERE m.project = ?
			    ORDER BY m.id DESC LIMIT ?
			 ) sub ORDER BY id ASC`,
			project, limit)
	}
	return messages, err
}

// listMessagesSelect is the dashboard message row; callers append the WHERE.
const listMessagesSelect = `SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
	        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
	        m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system, m.task_id
	 FROM board_messages m
	 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id`

// Keyset paging queries; the EXPLAIN test checks these exact strings.
const (
	listMessagesBeforeSQL = `SELECT * FROM (` + listMessagesSelect + `
	    WHERE m.project = ? AND m.id < ?
	    ORDER BY m.id DESC LIMIT ?
	 ) sub ORDER BY id ASC`
	listMessagesAfterSQL = listMessagesSelect + `
	 WHERE m.project = ? AND m.id > ?
	 ORDER BY m.id ASC LIMIT ?`
	countMessagesBeforeSQL = "SELECT COUNT(*) FROM board_messages WHERE project = ? AND id < ?"
	// A board's total, counted from where its cached total stopped (messageTotal).
	messageTotalHighSQL  = "SELECT COALESCE(MAX(id), ?) FROM board_messages WHERE project = ? AND id > ?"
	messageTotalCountSQL = "SELECT COUNT(*) FROM board_messages WHERE project = ? AND id > ? AND id <= ?"
	// The messages from an id up to the counted high, at most a limit of them.
	countMessagesTailSQL = "SELECT COUNT(*) FROM (SELECT 1 FROM board_messages WHERE project = ? AND id >= ? AND id <= ? LIMIT ?)"
)

// beforeCountTail: CountMessagesBefore subtracts the messages from beforeID on from the
// board's total when there are at most this many (a dashboard poll or the latest page: none
// to a page's worth), and counts the older ones directly when paging deep into the history.
const beforeCountTail = 5000

// ListMessagesBefore returns the newest limit messages with id < beforeID,
// oldest first: the page just before a loaded one. Unlike OFFSET paging it
// seeks the (project, id) index, so its cost does not grow with board size.
func (s *Store) ListMessagesBefore(ctx context.Context, project string, beforeID int64, limit int) ([]Message, error) {
	var messages []Message
	err := s.reader().SelectContext(ctx, &messages, listMessagesBeforeSQL, project, beforeID, limit)
	return messages, err
}

// ListMessagesAfter returns the oldest limit messages with id > afterID,
// oldest first: what arrived after the newest loaded message.
func (s *Store) ListMessagesAfter(ctx context.Context, project string, afterID int64, limit int) ([]Message, error) {
	var messages []Message
	err := s.reader().SelectContext(ctx, &messages, listMessagesAfterSQL, project, afterID, limit)
	return messages, err
}

// CountMessagesBefore counts the project's messages with id < beforeID.
func (s *Store) CountMessagesBefore(ctx context.Context, project string, beforeID int64) (int, error) {
	total, hi, err := s.messageTotal(ctx, project)
	if err != nil {
		return 0, err
	}
	if beforeID > hi {
		return total, nil
	}
	var tail int
	if err := s.reader().GetContext(ctx, &tail, countMessagesTailSQL, project, beforeID, hi, beforeCountTail+1); err != nil {
		return 0, err
	}
	if tail <= beforeCountTail {
		return max(total-tail, 0), nil
	}
	var count int
	err = s.reader().GetContext(ctx, &count, countMessagesBeforeSQL, project, beforeID)
	return count, err
}

// GetMessageByID returns one message of a board by its id. Message ids are numbered across
// every board, so an id from another board is not found here (sql.ErrNoRows), as if it did
// not exist (#3864: an agent read other teams' messages by walking ids under its own board).
func (s *Store) GetMessageByID(ctx context.Context, project string, id int64) (*Message, error) {
	var msg Message
	err := s.reader().GetContext(ctx, &msg,
		`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
		        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
		        m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system, m.task_id
		 FROM board_messages m
		 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
		 WHERE m.id = ? AND m.project = ?`, id, project)
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// ExistingMessageIDs is which of these message ids are on the board: one query for all of them (a board's message ids are numbered across every board, so an id of another
// board is not there). Saved for Later uses it to mark the bookmarks whose message was deleted (#5802).
func (s *Store) ExistingMessageIDs(ctx context.Context, project string, ids []int64) (map[int64]bool, error) {
	have := map[int64]bool{}
	for len(ids) > 0 {
		n := min(len(ids), 500) // SQLite's variable limit stays far away
		chunk := ids[:n]
		ids = ids[n:]
		args := make([]any, 0, n+1)
		args = append(args, project)
		for _, id := range chunk {
			args = append(args, id)
		}
		var found []int64
		q := "SELECT id FROM board_messages WHERE project = ? AND id IN (?" + strings.Repeat(",?", n-1) + ")"
		if err := s.reader().SelectContext(ctx, &found, q, args...); err != nil {
			return nil, err
		}
		for _, id := range found {
			have[id] = true
		}
	}
	return have, nil
}

// SearchMessageHits returns bounded board messages containing query text. It
// exposes the immutable message ID used by chat search navigation.
func (s *Store) SearchMessageHits(ctx context.Context, query string, limit int) ([]Message, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	var messages []Message
	err := s.reader().SelectContext(ctx, &messages,
		`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
		        COALESCE(bs.job_title, m.subscriber_id, 'Unknown') as job_title,
		        m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system, m.task_id
		 FROM board_messages m
		 LEFT JOIN board_subscribers bs ON m.project = bs.project AND m.subscriber_id = bs.subscriber_id
		 WHERE m.content LIKE ? COLLATE NOCASE
		 ORDER BY m.id DESC LIMIT ?`, "%"+query+"%", limit)
	return messages, err
}

// CountMessages returns the total message count for a project.
func (s *Store) CountMessages(ctx context.Context, project string) (int, error) {
	total, _, err := s.messageTotal(ctx, project)
	return total, err
}

// CoralSender is the system sender of Coral's notices to one agent (#3636: "Coral restarted").
// Such a notice @-names its agent; a subscriber reading everything counts it only when it is the
// one named.
const CoralSender = "Coral"
