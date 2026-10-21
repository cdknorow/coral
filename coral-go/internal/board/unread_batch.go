package board

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
)

// SessionUnread is an agent session's active subscription (as GetSubscriptionBySessionName finds
// it) and its unread count (as CheckUnread counts it).
type SessionUnread struct {
	Sub    *Subscriber
	Unread int
	Err    error // the count failed; Sub is still the subscription
}

// UnreadForSessions is GetSubscriptionBySessionName and CheckUnread for many sessions at once, for
// the board notifier's pass (#4121): one query for the subscriptions, and for each board one read
// transaction that counts every mentions-mode subscriber's unread messages from one read of the
// board's new messages. Before, each agent cost about five statements (its subscription, its
// subscriber row again, the board's newest id, its candidate messages and the board's numbered
// names), so a pass over a big board was dominated by SQLite's per-statement locking. The counts
// are the same: the same cursor, cache, window, LIKE rule and explicit-tag confirmation.
// Subscribers in "all" or group mode are counted as CheckUnread counts them, one by one. A
// session with no active subscription is absent from the result.
func (s *Store) UnreadForSessions(ctx context.Context, sessionNames []string) (map[string]SessionUnread, error) {
	out := map[string]SessionUnread{}
	if len(sessionNames) == 0 {
		return out, nil
	}
	query, args, err := sqlx.In(`SELECT * FROM board_subscribers WHERE session_name IN (?) AND is_active = 1 ORDER BY subscribed_at DESC, rowid DESC`, sessionNames)
	if err != nil {
		return nil, err
	}
	var subs []Subscriber
	if err := s.reader().SelectContext(ctx, &subs, s.reader().Rebind(query), args...); err != nil {
		return nil, err
	}
	byProject := map[string][]*Subscriber{}
	for i := range subs {
		sub := &subs[i]
		if _, seen := out[sub.SessionName]; seen {
			continue // the newest subscription, as GetSubscriptionBySessionName's LIMIT 1
		}
		out[sub.SessionName] = SessionUnread{Sub: sub}
		switch receiveModeOf(sub) {
		case "none":
		case "mentions":
			byProject[sub.Project] = append(byProject[sub.Project], sub)
		default: // "all" and groups: as CheckUnread does
			n, err := s.CheckUnread(ctx, sub.Project, sub.SubscriberID)
			out[sub.SessionName] = SessionUnread{Sub: sub, Unread: n, Err: err}
		}
	}
	for project, list := range byProject {
		counts, err := s.countMentionsUnreadBatch(ctx, project, list)
		for _, sub := range list {
			out[sub.SessionName] = SessionUnread{Sub: sub, Unread: counts[sub.SubscriberID], Err: err}
		}
	}
	return out, nil
}

func receiveModeOf(sub *Subscriber) string {
	if sub.ReceiveMode == "" {
		return "mentions"
	}
	return sub.ReceiveMode
}

// countMentionsUnreadBatch is countMentionsUnread for several subscribers of one board, in one
// read transaction: the newest id, the numbered names and the new messages are read once.
func (s *Store) countMentionsUnreadBatch(ctx context.Context, project string, subs []*Subscriber) (map[string]int, error) {
	c := &s.unreadCounts
	epoch := c.epoch.Load()
	type plan struct {
		sub  *Subscriber
		base int
		from int64
	}
	plans := make([]plan, 0, len(subs))
	floor, floorRead := int64(0), false
	minFrom := int64(-1)
	c.mu.Lock()
	for _, sub := range subs {
		p := plan{sub: sub, from: sub.LastReadID}
		key := mentionCountKey{project, sub.SubscriberID}
		if e, ok := c.entries[key]; ok && e.epoch == epoch && e.lastReadID == sub.LastReadID && e.jobTitle == sub.JobTitle {
			p.from, p.base = e.scannedTo, e.count
		}
		plans = append(plans, p)
	}
	c.mu.Unlock()
	for i := range plans {
		if plans[i].from == plans[i].sub.LastReadID {
			if !floorRead {
				floor, floorRead = s.unreadFloor(ctx, project), true
			}
			plans[i].from = max(plans[i].from, floor) // a first count looks back at most unreadWindow
		}
		if minFrom < 0 || plans[i].from < minFrom {
			minFrom = plans[i].from
		}
	}

	type msg struct {
		ID           int64          `db:"id"`
		SubscriberID sql.NullString `db:"subscriber_id"` // NULL never passes SQL's "!=": skipped
		Content      string         `db:"content"`
	}
	var newest sql.NullInt64
	var msgs []msg
	var numbered map[string]bool
	if err := func() error {
		tx, err := s.reader().BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := tx.GetContext(ctx, &newest, `SELECT MAX(id) FROM board_messages WHERE project = ?`, project); err != nil {
			return err
		}
		if newest.Valid && newest.Int64 > minFrom {
			if err := tx.SelectContext(ctx, &msgs,
				`SELECT id, subscriber_id, content FROM board_messages WHERE project = ? AND id > ? AND id <= ? ORDER BY id`,
				project, minFrom, newest.Int64); err != nil {
				return err
			}
		}
		numbered = s.numberedNames(ctx, tx, project)
		return nil
	}(); err != nil {
		return nil, err
	}

	counts := make(map[string]int, len(plans))
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range plans {
		sub := p.sub
		// countMentionsUnread's watermark: the newest id after its start, or the start itself.
		hi := p.from
		if newest.Valid && newest.Int64 > hi {
			hi = newest.Int64
		}
		patterns := mentionLikePatterns(sub.SubscriberID, sub.JobTitle)
		n := 0
		for _, m := range msgs {
			if m.ID <= p.from || m.ID > hi || !m.SubscriberID.Valid || m.SubscriberID.String == sub.SubscriberID || m.SubscriberID.String == "Coral Task Queue" {
				continue
			}
			if !likeAny(m.Content, patterns) || !containsExplicitTag(m.Content, sub.SubscriberID, sub.JobTitle, numbered) {
				continue
			}
			n++
		}
		counts[sub.SubscriberID] = p.base + n
		if c.epoch.Load() != epoch {
			continue // messages were deleted meanwhile: count, but keep nothing
		}
		if c.entries == nil {
			c.entries = make(map[mentionCountKey]mentionCountEntry)
		}
		c.entries[mentionCountKey{project, sub.SubscriberID}] = mentionCountEntry{lastReadID: sub.LastReadID, jobTitle: sub.JobTitle, scannedTo: hi, count: p.base + n, epoch: epoch}
	}
	return counts, nil
}

// mentionLikePatterns are CheckUnread's LIKE patterns for a mentions-mode subscriber: "%term%"
// for each mention term, matched as SQLite's LIKE matches them (likeMatch).
func mentionLikePatterns(subscriberID, jobTitle string) []string {
	terms := mentionTerms(subscriberID, jobTitle)
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = "%" + t + "%"
	}
	return out
}

func likeAny(s string, patterns []string) bool {
	for _, p := range patterns {
		if len(p) >= 2 && p[0] == '%' && p[len(p)-1] == '%' && !strings.ContainsAny(p[1:len(p)-1], "%_") {
			if asciiContainsFold(s, p[1:len(p)-1]) { // "%term%" with no wildcard inside: a substring
				return true
			}
		} else if likeMatch(s, p) {
			return true
		}
	}
	return false
}

// asciiContainsFold reports whether sub is in s, ASCII letters matched case-insensitively and
// everything else exactly (as LIKE "%sub%").
func asciiContainsFold(s, sub string) bool {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for j < n && asciiLowerByte(s[i+j]) == asciiLowerByte(sub[j]) {
			j++
		}
		if j == n {
			return true
		}
	}
	return false
}

func asciiLowerByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// likeMatch is SQLite's LIKE without ESCAPE: '%' matches any run of characters, '_' any one
// character, and other characters match case-insensitively for ASCII only (COLLATE NOCASE and
// LIKE's own folding are both ASCII-only). It remembers only the last '%' to return to, so the work
// is at most len(s)*len(p) however many wildcards the pattern holds (a term with several of them
// over a long message once cost a power of the message length: #4147).
func likeMatch(s, p string) bool {
	si, pi := 0, 0
	star, mark := -1, 0 // the pattern position after the last '%', and where in s its run now ends
	for si < len(s) {
		if pi < len(p) {
			pr, pw := utf8.DecodeRuneInString(p[pi:])
			if pr == '%' {
				pi += pw
				star, mark = pi, si
				continue
			}
			sr, sw := utf8.DecodeRuneInString(s[si:])
			if pr == '_' || asciiLower(sr) == asciiLower(pr) {
				si += sw
				pi += pw
				continue
			}
		}
		if star < 0 {
			return false
		}
		_, w := utf8.DecodeRuneInString(s[mark:])
		mark += w
		si, pi = mark, star
	}
	for pi < len(p) && p[pi] == '%' {
		pi++
	}
	return pi == len(p)
}

func asciiLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}
