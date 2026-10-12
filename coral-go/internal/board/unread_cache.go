package board

import (
	"context"
	"sync"
	"sync/atomic"
)

// Mentions-mode unread counts are incremental. Counting from last_read_id
// matches about fourteen LIKE patterns against every unread message, and the
// board notifier asks for every live subscriber on every pass. A worker that is
// rarely mentioned keeps an old cursor, so that scan grows with the whole board
// history (~13ms per subscriber per pass at 10k messages).
//
// The count for (project, subscriber) is a function of the cursor, the job
// title (which feeds the mention terms) and the messages. Message ids are
// AUTOINCREMENT and message content is never updated, so once messages up to
// id X have been counted, a later call only needs to count id > X. Deleting
// messages is the one change that can lower an already-counted total; both
// delete paths bump messagesEpoch, which invalidates every entry.

type mentionCountKey struct{ project, subscriberID string }

type mentionCountEntry struct {
	lastReadID int64
	jobTitle   string
	scannedTo  int64 // highest message id already counted
	count      int
	epoch      uint64
}

// unreadCountCache holds both incremental unread counts: CheckUnread's
// (SQL LIKE, mentions mode) and getUnreadCounts' (Go mention matching, every
// mode, for the session list badges). The two match mentions differently, so
// they are cached separately.
type unreadCountCache struct {
	mu      sync.Mutex
	entries map[mentionCountKey]mentionCountEntry
	sidebar map[sidebarUnreadKey]sidebarUnreadEntry
	epoch   atomic.Uint64
}

// invalidate drops every cached count. Call it before and after deleting
// messages (before, so a count racing the delete cannot reuse a base that
// includes the deleted rows; after, so none computed meanwhile survives), and
// after group membership changes (group-mode eligibility depends on it). The maps
// hold one small entry per subscriber ever counted; they are emptied here, so
// deleted boards do not leave entries behind.
func (c *unreadCountCache) invalidate() {
	c.epoch.Add(1)
	c.mu.Lock()
	c.entries = nil
	c.sidebar = nil
	c.mu.Unlock()
}

// forget drops a subscriber's cached counts, e.g. when it unsubscribes, so the
// maps hold only subscribers that still exist.
func (c *unreadCountCache) forget(project, subscriberID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, mentionCountKey{project, subscriberID})
	for key := range c.sidebar {
		if key.project == project && key.subscriberID == subscriberID {
			delete(c.sidebar, key)
		}
	}
}

type sidebarUnreadKey struct{ project, subscriberID, sessionName string }

type sidebarUnreadEntry struct {
	lastReadID  int64
	jobTitle    string
	receiveMode string
	scannedTo   int64
	count       int
	epoch       uint64
}

// sidebarUnreadStart returns where a subscriber's count must resume and the
// count already accumulated up to there.
func (c *unreadCountCache) sidebarUnreadStart(key sidebarUnreadKey, lastReadID int64, jobTitle, receiveMode string, epoch uint64) (int64, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.sidebar[key]; ok && e.epoch == epoch && e.lastReadID == lastReadID && e.jobTitle == jobTitle && e.receiveMode == receiveMode {
		return e.scannedTo, e.count
	}
	return lastReadID, 0
}

func (c *unreadCountCache) storeSidebarUnread(key sidebarUnreadKey, e sidebarUnreadEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch.Load() != e.epoch {
		return
	}
	if c.sidebar == nil {
		c.sidebar = make(map[sidebarUnreadKey]sidebarUnreadEntry)
	}
	c.sidebar[key] = e
}

// countMentionsUnread returns the number of unread messages after lastReadID
// matching filter (a boolean SQL expression over board_messages with filterArgs).
func (s *Store) countMentionsUnread(ctx context.Context, project, subscriberID, jobTitle string, lastReadID int64, filter string, filterArgs []any) (int, error) {
	c := &s.unreadCounts
	key := mentionCountKey{project, subscriberID}
	for attempt := 0; ; attempt++ {
		epoch := c.epoch.Load()
		start, base := lastReadID, 0
		if attempt == 0 {
			c.mu.Lock()
			if e, ok := c.entries[key]; ok && e.epoch == epoch && e.lastReadID == lastReadID && e.jobTitle == jobTitle {
				start, base = e.scannedTo, e.count
			}
			c.mu.Unlock()
		}

		// One statement, so the count and the watermark come from one snapshot.
		var row struct {
			N  int   `db:"n"`
			Hi int64 `db:"hi"`
		}
		args := append([]any{}, filterArgs...)
		args = append(args, start, project, start)
		if err := s.db.GetContext(ctx, &row,
			`SELECT COALESCE(SUM(CASE WHEN `+filter+` THEN 1 ELSE 0 END), 0) AS n, COALESCE(MAX(id), ?) AS hi
			 FROM board_messages WHERE project = ? AND id > ?`, args...); err != nil {
			return 0, err
		}

		if c.epoch.Load() != epoch {
			// Messages were deleted meanwhile; the cached base may count them.
			if attempt == 0 {
				continue
			}
			return base + row.N, nil
		}
		c.mu.Lock()
		if c.entries == nil {
			c.entries = make(map[mentionCountKey]mentionCountEntry)
		}
		c.entries[key] = mentionCountEntry{lastReadID: lastReadID, jobTitle: jobTitle, scannedTo: row.Hi, count: base + row.N, epoch: epoch}
		c.mu.Unlock()
		return base + row.N, nil
	}
}
