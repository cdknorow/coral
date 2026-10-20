// Unread counts and states, and the unread checks (moved out of store.go, #4998; no behaviour change).

package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

// UnreadState is the startup notification baseline for one active session.
// LatestID identifies the newest message currently eligible for that
// subscriber, allowing the notifier to distinguish a later same-count batch.
type UnreadState struct {
	Count    int
	LatestID int64
}

func (s *Store) CheckUnread(ctx context.Context, project, subscriberID string) (int, error) {
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
		if errors.Is(err, sql.ErrNoRows) {
			s.unreadCounts.forget(project, subscriberID)
		}
		return 0, nil
	}

	receiveMode := sub.ReceiveMode
	if receiveMode == "" {
		receiveMode = "mentions"
	}

	if receiveMode == "none" {
		return 0, nil
	}

	// System senders post audit messages that should not trigger notifications
	systemFilter := " AND subscriber_id NOT IN ('Coral Task Queue')"

	if receiveMode == "all" {
		// Coral's own notice to one agent (CoralSender, #3636) counts only for the agent it
		// names: never a fan-out to every subscriber that reads everything.
		var count int
		err := s.reader().GetContext(ctx, &count,
			`SELECT COUNT(*) FROM board_messages WHERE project = ? AND id > ? AND subscriber_id != ?`+systemFilter+
				` AND (subscriber_id != '`+CoralSender+`' OR instr(content, ?) > 0)`,
			project, sub.LastReadID, subscriberID, "@"+sub.JobTitle)
		return count, err
	}

	if receiveMode == "mentions" {
		terms := mentionTerms(subscriberID, sub.JobTitle)
		// Convert terms to SQL LIKE patterns (% wildcard matches any substring)
		whereClauses := make([]string, len(terms))
		args := []any{subscriberID}
		for i, t := range terms {
			whereClauses[i] = "content LIKE ? COLLATE NOCASE"
			args = append(args, "%"+t+"%")
		}
		filter := "subscriber_id != ?" + systemFilter + " AND (" + strings.Join(whereClauses, " OR ") + ")"
		// The same count from the mention index when it can answer (#4483): the sender filter alone in SQL
		// plus the index's candidates; countMentionsUnread falls back to filter (the OR of LIKE) otherwise.
		return s.countMentionsUnread(ctx, project, subscriberID, sub.JobTitle, sub.LastReadID, filter, args,
			&mentionIndexCount{terms: terms, senderArgs: []any{subscriberID}})
	}

	// Group-based mode: count messages from group members only
	var memberIDs []string
	err = s.reader().SelectContext(ctx, &memberIDs,
		"SELECT subscriber_id FROM board_groups WHERE project = ? AND group_id = ?",
		project, receiveMode)
	if err != nil || len(memberIDs) == 0 {
		return 0, nil
	}

	placeholders := make([]string, len(memberIDs))
	args := []interface{}{project, sub.LastReadID, subscriberID}
	for i, id := range memberIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	var count int
	query := fmt.Sprintf(
		`SELECT COUNT(*) FROM board_messages
		 WHERE project = ? AND id > ? AND subscriber_id != ?`+systemFilter+` AND subscriber_id IN (%s)`,
		strings.Join(placeholders, ","))
	err = s.reader().GetContext(ctx, &count, query, args...)
	return count, err
}

// LatestUnreadMessageID returns the newest message eligible for a subscriber's
// current receive mode without advancing its cursor. It lets delivery code
// distinguish a new one-message batch from the previous one-message batch.
func (s *Store) LatestUnreadMessageID(ctx context.Context, project, subscriberID string) (int64, error) {
	var sub struct {
		LastReadID  int64  `db:"last_read_id"`
		JobTitle    string `db:"job_title"`
		ReceiveMode string `db:"receive_mode"`
		CanPeek     int    `db:"can_peek"`
	}
	if err := s.reader().GetContext(ctx, &sub, "SELECT last_read_id, job_title, receive_mode, can_peek FROM board_subscribers WHERE project=? AND subscriber_id=?", project, subscriberID); err != nil {
		return 0, nil
	}
	if sub.ReceiveMode == "none" {
		return 0, nil
	}
	args := []interface{}{project, sub.LastReadID, subscriberID}
	where := "m.project=? AND m.id>? AND m.subscriber_id!=? AND m.subscriber_id!='Coral Task Queue'"
	if sub.ReceiveMode != "all" && !isOrchestrator(subscriberID, sub.JobTitle, sub.CanPeek) {
		args[1] = max(sub.LastReadID, s.unreadFloor(ctx, project)) // within the unread window (#3875)
		return s.confirmedMention(ctx, project, subscriberID, sub.JobTitle, where, args, args[1].(int64), true)
	}
	var latest int64
	err := s.reader().GetContext(ctx, &latest, "SELECT COALESCE(MAX(m.id),0) FROM board_messages m WHERE "+where, args...)
	return latest, err
}

// HasUnreadHealthMessage reports whether the subscriber has an unread message
// emitted by the health monitor. Sender identity is authoritative so ordinary
// worker text cannot be classified as health.
func (s *Store) HasUnreadHealthMessage(ctx context.Context, project, subscriberID string) (bool, error) {
	return s.hasUnreadSenderMessage(ctx, project, subscriberID, "Coral Health Monitor", false)
}

// HasUnreadNonHealthMessage reports whether an eligible unread message was
// posted by anyone other than the health monitor. It lets worker sessions with
// receive-all mode retain ordinary notifications without being nudged solely
// for an Orchestrator health backlog.
// HasUnreadMention reports whether an unread message (from someone else, not the task queue)
// tags the subscriber: its id or job title, a numbered name of its own, @all or @notify-all,
// by containsExplicitTag, the rule the notifier and the reads use (#3578: the gate types a
// mention in mid-turn and holds a routine nudge until the turn ends). Its receive mode doesn't
// matter: an agent that receives everything is still mentioned by a message that tags it.
func (s *Store) HasUnreadMention(ctx context.Context, project, subscriberID string) (bool, error) {
	var sub struct {
		LastReadID int64  `db:"last_read_id"`
		JobTitle   string `db:"job_title"`
	}
	if err := s.reader().GetContext(ctx, &sub, "SELECT last_read_id, job_title FROM board_subscribers WHERE project=? AND subscriber_id=?", project, subscriberID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	from := max(sub.LastReadID, s.unreadFloor(ctx, project)) // within the unread window (#3875)
	id, err := s.confirmedMention(ctx, project, subscriberID, sub.JobTitle,
		"m.project=? AND m.id>? AND m.subscriber_id!=? AND m.subscriber_id!='Coral Task Queue'",
		[]interface{}{project, from, subscriberID}, from, false)
	return id > 0, err
}

func (s *Store) HasUnreadNonHealthMessage(ctx context.Context, project, subscriberID string) (bool, error) {
	return s.hasUnreadSenderMessage(ctx, project, subscriberID, "Coral Health Monitor", true)
}

// unreadSenderWhere is the condition of an unread message from (or, excluded, not from) one sender: the board, newer than what was read, not the
// reader's own, not the task queue's. With the sender as an equality, idx_board_messages_sender (project, subscriber_id, id) answers it with a seek (#5354).
func unreadSenderWhere(excludeSender bool) string {
	senderClause := "m.subscriber_id=?"
	if excludeSender {
		senderClause = "m.subscriber_id!=?"
	}
	return "m.project=? AND m.id>? AND m.subscriber_id!=? AND m.subscriber_id!='Coral Task Queue' AND " + senderClause
}

func (s *Store) hasUnreadSenderMessage(ctx context.Context, project, subscriberID, sender string, excludeSender bool) (bool, error) {
	var sub struct {
		LastReadID  int64  `db:"last_read_id"`
		JobTitle    string `db:"job_title"`
		ReceiveMode string `db:"receive_mode"`
		CanPeek     int    `db:"can_peek"`
	}
	if err := s.reader().GetContext(ctx, &sub, "SELECT last_read_id, job_title, receive_mode, can_peek FROM board_subscribers WHERE project=? AND subscriber_id=?", project, subscriberID); err != nil {
		return false, nil
	}
	if sub.ReceiveMode == "none" {
		return false, nil
	}
	args := []interface{}{project, sub.LastReadID, subscriberID, sender}
	where := unreadSenderWhere(excludeSender)
	if sub.ReceiveMode != "all" && !isOrchestrator(subscriberID, sub.JobTitle, sub.CanPeek) {
		args[1] = max(sub.LastReadID, s.unreadFloor(ctx, project)) // within the unread window (#3875)
		id, err := s.confirmedMention(ctx, project, subscriberID, sub.JobTitle, where, args, args[1].(int64), false)
		return id > 0, err
	}
	var found int
	err := s.reader().GetContext(ctx, &found, "SELECT 1 FROM board_messages m WHERE "+where+" LIMIT 1", args...)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return found == 1, err
}

// GetAllUnreadCounts returns unread counts for all subscribers, respecting each subscriber's receive_mode.
// Returns map keyed by session_name (tmux session identifier) for compatibility with live session lookups.
func (s *Store) GetAllUnreadCounts(ctx context.Context) (map[string]int, error) {
	return s.getUnreadCounts(ctx, nil)
}

// GetUnreadCountsForSessions excludes subscriptions unrelated to live refresh.
func (s *Store) GetUnreadCountsForSessions(ctx context.Context, sessionNames []string) (map[string]int, error) {
	if len(sessionNames) == 0 {
		return map[string]int{}, nil
	}
	return s.getUnreadCounts(ctx, sessionNames)
}

func (s *Store) getUnreadCounts(ctx context.Context, sessionNames []string) (map[string]int, error) {
	return s.countUnread(ctx, sessionNames, true)
}

func (s *Store) countUnread(ctx context.Context, sessionNames []string, retry bool) (map[string]int, error) {
	subs, err := s.activeUnreadSubscribers(ctx, sessionNames)
	if err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return map[string]int{}, nil
	}
	groups, err := s.groupMembers(ctx, subs)
	if err != nil {
		return nil, err
	}
	epoch := s.unreadCounts.epoch.Load()
	result, byProject := s.unreadCountStarts(subs, epoch)
	for project, projectSubs := range byProject {
		hi, err := s.countProjectUnread(ctx, project, projectSubs, groups)
		if err != nil {
			return nil, err
		}
		for _, sub := range projectSubs {
			result[sub.SessionName] += sub.count
			s.unreadCounts.storeSidebarUnread(sub.key, sidebarUnreadEntry{lastReadID: sub.LastReadID, jobTitle: sub.JobTitle, receiveMode: sub.ReceiveMode,
				scannedTo: max(hi, sub.start), count: sub.count, epoch: epoch})
		}
	}
	if retry && s.unreadCounts.epoch.Load() != epoch {
		// Messages were deleted or groups changed meanwhile: cached bases may
		// be stale, so count again.
		return s.countUnread(ctx, sessionNames, false)
	}
	return result, nil
}

// unreadSubscriber is an active subscription as the unread counts read it.
type unreadSubscriber struct {
	Project      string `db:"project"`
	SubscriberID string `db:"subscriber_id"`
	SessionName  string `db:"session_name"`
	JobTitle     string `db:"job_title"`
	LastReadID   int64  `db:"last_read_id"`
	ReceiveMode  string `db:"receive_mode"`
}

// activeUnreadSubscribers is every active subscription, or (sessionNames not nil) those of these sessions.
func (s *Store) activeUnreadSubscribers(ctx context.Context, sessionNames []string) ([]unreadSubscriber, error) {
	var subs []unreadSubscriber
	query := "SELECT project, subscriber_id, session_name, job_title, last_read_id, receive_mode FROM board_subscribers WHERE is_active = 1"
	var args []any
	if sessionNames != nil {
		var err error
		query, args, err = sqlx.In(query+" AND session_name IN (?)", sessionNames)
		if err != nil {
			return nil, err
		}
	}
	if err := s.reader().SelectContext(ctx, &subs, s.reader().Rebind(query), args...); err != nil {
		return nil, err
	}
	return subs, nil
}

type unreadGroupKey struct{ project, groupID string }

// groupMembers is each group's members, over the boards subs are on (a group receive mode counts its members' posts).
func (s *Store) groupMembers(ctx context.Context, subs []unreadSubscriber) (map[unreadGroupKey]map[string]bool, error) {
	projects := make([]string, 0, len(subs))
	seen := make(map[string]bool)
	for _, sub := range subs {
		if !seen[sub.Project] {
			seen[sub.Project] = true
			projects = append(projects, sub.Project)
		}
	}
	var groupRows []struct {
		Project      string `db:"project"`
		GroupID      string `db:"group_id"`
		SubscriberID string `db:"subscriber_id"`
	}
	query, args, err := sqlx.In("SELECT project, group_id, subscriber_id FROM board_groups WHERE project IN (?)", projects)
	if err != nil {
		return nil, err
	}
	if err := s.reader().SelectContext(ctx, &groupRows, s.reader().Rebind(query), args...); err != nil {
		return nil, err
	}
	groupsByKey := make(map[unreadGroupKey]map[string]bool)
	for _, gr := range groupRows {
		key := unreadGroupKey{gr.Project, gr.GroupID}
		if groupsByKey[key] == nil {
			groupsByKey[key] = make(map[string]bool)
		}
		groupsByKey[key][gr.SubscriberID] = true
	}
	return groupsByKey, nil
}

// unreadCount is one subscription being counted: where its count resumes (start) and what it holds so far.
type unreadCount struct {
	SubscriberID string
	SessionName  string
	JobTitle     string
	LastReadID   int64
	ReceiveMode  string
	key          sidebarUnreadKey
	start        int64
	count        int
}

// unreadCountStarts groups the subscriptions by board, each resuming at its cached watermark (see unread_cache.go), and
// gives every session a count of 0 to start from. A subscription receiving nothing ("none") is not counted; an unset
// mode is "mentions".
func (s *Store) unreadCountStarts(subs []unreadSubscriber, epoch uint64) (map[string]int, map[string][]*unreadCount) {
	result := make(map[string]int)
	byProject := make(map[string][]*unreadCount)
	for _, sub := range subs {
		result[sub.SessionName] += 0
		rm := sub.ReceiveMode
		if rm == "none" {
			continue
		}
		if rm == "" {
			rm = "mentions"
		}
		info := &unreadCount{SubscriberID: sub.SubscriberID, SessionName: sub.SessionName, JobTitle: sub.JobTitle, LastReadID: sub.LastReadID, ReceiveMode: rm,
			key: sidebarUnreadKey{sub.Project, sub.SubscriberID, sub.SessionName}}
		info.start, info.count = s.unreadCounts.sidebarUnreadStart(info.key, sub.LastReadID, sub.JobTitle, rm, epoch)
		byProject[sub.Project] = append(byProject[sub.Project], info)
	}
	return result, byProject
}

// countProjectUnread adds to each of one board's subscriptions its unread messages up to the board's newest, hi, which it
// returns. A subscriber that receives everything is counted by the index: streaming every
// message's content for it took minutes on a long board (#3875). The others are
// matched in Go, and a first count (nothing cached) looks back at most unreadWindow
// messages.
func (s *Store) countProjectUnread(ctx context.Context, project string, projectSubs []*unreadCount, groups map[unreadGroupKey]map[string]bool) (int64, error) {
	var hi int64
	if err := s.reader().GetContext(ctx, &hi, "SELECT COALESCE(MAX(id), 0) FROM board_messages WHERE project = ?", project); err != nil {
		return 0, err
	}
	var streamed []*unreadCount
	floor := int64(-1)
	for _, sub := range projectSubs {
		if sub.ReceiveMode == "all" {
			var n int
			if err := s.reader().GetContext(ctx, &n,
				"SELECT COUNT(*) FROM board_messages WHERE project = ? AND id > ? AND id <= ? AND subscriber_id != ? AND subscriber_id != 'Coral Task Queue'",
				project, sub.start, hi, sub.SubscriberID); err != nil {
				return 0, err
			}
			sub.count += n
			continue
		}
		if sub.start == sub.LastReadID {
			if floor < 0 {
				floor = s.unreadFloor(ctx, project)
			}
			sub.start = max(sub.start, floor)
		}
		streamed = append(streamed, sub)
	}
	if len(streamed) > 0 {
		if err := s.streamUnread(ctx, project, streamed, hi, groups); err != nil {
			return 0, err
		}
	}
	return hi, nil
}

// streamUnread counts, for each subscription in streamed, the board's messages after its start up to hi that reach it:
// not its own, and naming it (mentions) or from its group's members (a group mode).
func (s *Store) streamUnread(ctx context.Context, project string, streamed []*unreadCount, hi int64, groups map[unreadGroupKey]map[string]bool) error {
	minStart := streamed[0].start
	for _, sub := range streamed {
		minStart = min(minStart, sub.start)
	}
	numbered := s.numberedNamesCached(ctx, project)
	// Stream messages instead of retaining every unread body in memory.
	rows, err := s.reader().QueryxContext(ctx,
		"SELECT id, subscriber_id, content FROM board_messages WHERE project = ? AND id > ? AND id <= ? AND subscriber_id != 'Coral Task Queue' ORDER BY id",
		project, minStart, hi)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var sender, content string
		if err := rows.Scan(&id, &sender, &content); err != nil {
			rows.Close()
			return err
		}
		for _, sub := range streamed {
			if id <= sub.start || sender == sub.SubscriberID {
				continue
			}
			eligible := false
			switch sub.ReceiveMode {
			case "mentions":
				eligible = containsExplicitTag(content, sub.SubscriberID, sub.JobTitle, numbered)
			default:
				eligible = groups[unreadGroupKey{project, sub.ReceiveMode}][sender]
			}
			if eligible {
				sub.count++
			}
		}
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return readErr
	}
	return closeErr
}

// GetAllUnreadStates returns unread counts together with the latest eligible
// message ID for each active session. The notifier uses this pair when seeding
// after startup so a new same-count batch is not mistaken for the seeded one.
func (s *Store) GetAllUnreadStates(ctx context.Context) (map[string]UnreadState, error) {
	return s.getUnreadStates(ctx, nil)
}

// GetUnreadStatesFor is GetAllUnreadStates for these sessions only (the notifier seeds the
// agents that are running, #3875). An empty list gives an empty map.
func (s *Store) GetUnreadStatesFor(ctx context.Context, sessionNames []string) (map[string]UnreadState, error) {
	if len(sessionNames) == 0 {
		return map[string]UnreadState{}, nil
	}
	return s.getUnreadStates(ctx, sessionNames)
}

func (s *Store) getUnreadStates(ctx context.Context, sessionNames []string) (map[string]UnreadState, error) {
	counts, err := s.getUnreadCounts(ctx, sessionNames)
	if err != nil {
		return nil, err
	}
	states := make(map[string]UnreadState, len(counts))
	var subs []struct {
		Project      string `db:"project"`
		SubscriberID string `db:"subscriber_id"`
		SessionName  string `db:"session_name"`
		ReceiveMode  string `db:"receive_mode"`
		JobTitle     string `db:"job_title"`
		CanPeek      int    `db:"can_peek"`
	}
	query := `SELECT project, subscriber_id, session_name, COALESCE(receive_mode, '') AS receive_mode,
		        COALESCE(job_title, '') AS job_title, COALESCE(can_peek, 0) AS can_peek
		 FROM board_subscribers WHERE is_active = 1`
	var args []any
	if sessionNames != nil {
		if query, args, err = sqlx.In(query+" AND session_name IN (?)", sessionNames); err != nil {
			return nil, err
		}
	}
	if err := s.reader().SelectContext(ctx, &subs, s.reader().Rebind(query), args...); err != nil {
		return nil, err
	}
	for _, sub := range subs {
		// A mentions-mode subscriber with nothing unread has no unread message that tags it:
		// the count above and LatestUnreadMessageID's search confirm mentions by the same
		// rule over the same messages. The search would read every message after its cursor
		// and find none, which for a subscriber that never read is the board's whole history
		// (#3845: 40 such subscribers kept a start with 300,000 messages from serving for
		// minutes). A group-mode count is by sender, not mention, and an orchestrator's latest
		// is any message's, so those still search.
		if counts[sub.SessionName] == 0 && (sub.ReceiveMode == "" || sub.ReceiveMode == "mentions") &&
			!isOrchestrator(sub.SubscriberID, sub.JobTitle, sub.CanPeek) {
			states[sub.SessionName] = UnreadState{}
			continue
		}
		latest, err := s.LatestUnreadMessageID(ctx, sub.Project, sub.SubscriberID)
		if err != nil {
			return nil, err
		}
		states[sub.SessionName] = UnreadState{Count: counts[sub.SessionName], LatestID: latest}
	}
	return states, nil
}
