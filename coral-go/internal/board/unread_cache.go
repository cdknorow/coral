package board

import (
	"context"
	"database/sql"
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
// they are cached separately. It also holds each board's message total, kept
// the same way (messageTotal).
type unreadCountCache struct {
	mu      sync.Mutex
	entries map[mentionCountKey]mentionCountEntry
	sidebar map[sidebarUnreadKey]sidebarUnreadEntry
	totals  map[string]messageTotalEntry
	epoch   atomic.Uint64
}

// messageTotalEntry is a board's message count up to scannedTo.
type messageTotalEntry struct {
	scannedTo int64
	count     int
	epoch     uint64
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
	c.totals = nil
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
		if start == lastReadID {
			start = max(start, s.unreadFloor(ctx, project)) // a first count looks back at most unreadWindow
		}

		// One read transaction, so the count and the watermark come from one snapshot. The
		// SQL filter (LIKE) finds candidates; each is confirmed with the same rule
		// ReadMessages uses (containsExplicitTag: word boundaries, and "@Backend Dev 2" isn't
		// "@Backend Dev", #3543).
		var row struct {
			N  int
			Hi int64
		}
		if err := func() error {
			tx, err := s.reader().BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := tx.GetContext(ctx, &row.Hi, `SELECT COALESCE(MAX(id), ?) FROM board_messages WHERE project = ? AND id > ?`, start, project, start); err != nil {
				return err
			}
			args := append([]any{}, filterArgs...)
			args = append(args, project, start, row.Hi)
			var contents []string
			if err := tx.SelectContext(ctx, &contents,
				`SELECT content FROM board_messages WHERE `+filter+` AND project = ? AND id > ? AND id <= ?`, args...); err != nil {
				return err
			}
			numbered := s.numberedNames(ctx, tx, project)
			for _, c := range contents {
				if containsExplicitTag(c, subscriberID, jobTitle, numbered) {
					row.N++
				}
			}
			return nil
		}(); err != nil {
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

// messageTotal returns how many messages the board has, and the highest message id counted
// (0 when it has none). The board chat's poll asks for the total every few seconds; counting
// every row each time cost about 19 ms at 300,000 messages and was half the dashboard's CPU
// (#3845, perf pass #3833 item 1). Like the unread counts, a total once counted only needs
// the messages after the id it reached, and deleting messages invalidates it.
func (s *Store) messageTotal(ctx context.Context, project string) (int, int64, error) {
	c := &s.unreadCounts
	for attempt := 0; ; attempt++ {
		epoch := c.epoch.Load()
		var start int64
		base := 0
		if attempt == 0 {
			c.mu.Lock()
			if e, ok := c.totals[project]; ok && e.epoch == epoch {
				start, base = e.scannedTo, e.count
			}
			c.mu.Unlock()
		}
		var hi int64
		var n int
		if err := func() error {
			tx, err := s.reader().BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := tx.GetContext(ctx, &hi, messageTotalHighSQL, start, project, start); err != nil {
				return err
			}
			return tx.GetContext(ctx, &n, messageTotalCountSQL, project, start, hi)
		}(); err != nil {
			return 0, 0, err
		}
		if c.epoch.Load() != epoch {
			// Messages were deleted meanwhile; the cached base may count them. A second
			// attempt counts from the start and is not cached.
			if attempt == 0 {
				continue
			}
			return n, hi, nil
		}
		c.mu.Lock()
		if c.totals == nil {
			c.totals = make(map[string]messageTotalEntry)
		}
		c.totals[project] = messageTotalEntry{scannedTo: hi, count: base + n, epoch: epoch}
		c.mu.Unlock()
		return base + n, hi, nil
	}
}

// unreadWindow is how far back, in a board's own messages, a mention still counts as unread for
// a nudge or a badge when nothing has been counted yet (#3875). A subscriber that never read a
// long board (a new or reset cursor) made every first count and newest-mention search read the
// board's whole history through LIKE and the Go matcher: minutes at 300,000 messages, with no
// nudge until it finished. Reading is unchanged: coral-board read still returns every unread
// message.
const unreadWindow = 2000

// unreadFloor is the id just before the board's unreadWindow-th newest message, or 0 when the
// board has fewer: unread mentions are counted and searched after it. One index seek.
func (s *Store) unreadFloor(ctx context.Context, project string) int64 {
	var id int64
	if err := s.reader().GetContext(ctx, &id,
		"SELECT id FROM board_messages WHERE project = ? ORDER BY id DESC LIMIT 1 OFFSET ?", project, unreadWindow-1); err != nil {
		return 0
	}
	return id - 1
}
