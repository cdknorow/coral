package board

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// The mention index (#4465). ReadMessages found a subscriber's mentions with an OR of LIKE patterns over
// every message after its cursor, a different query text for every subscriber's names and a scan of every
// message's text on every poll: 9 microseconds a message, 560 ms for a 60,000-message tail. The index
// narrows that to the messages that can mention the subscriber, found by key.
//
// board_message_mentions holds, for every message, a few short tokens cut from its lowercased text where a
// mention of someone could be: the three bytes after every "@", and the three bytes before every ":" and
// before every em dash. A mention term (mentionTerms) is "@name", "name:", "name —" or "name—", so any
// message that contains one has the token its first three bytes (after the "@") or its last three bytes
// (before the ":" or "—") name. The tokens are a superset: they never miss a message that mentions the
// subscriber, and the rule that decides whether it does (containsExplicitTag, and LIKE for exactly the old
// candidate set) runs on what the index finds, as before. A message with too many tokens, and a term too
// short to have three bytes, are not indexed that way: the first is stored as the token "*", which every
// query includes; the second makes the caller use the LIKE query as before.
//
// Messages are indexed by catching up: mentionIndexedTo is the highest message id indexed, ids are never
// reused, and every write path inserts into board_messages, so ReadMessages indexes whatever is above it
// before it reads. The first catch-up on an existing database is the backfill.

// maxMentionTokens bounds the tokens of one message; past it the message is indexed as "*".
const maxMentionTokens = 128

// mentionWild is the token of a message that is always a candidate.
const mentionWild = "*"

// mentionTokenBytes is how many bytes of a name a token holds.
const mentionTokenBytes = 3

func (s *Store) ensureMentionIndexSchema(ctx context.Context) error {
	s.mentionIndexedTo.Store(-1) // read from the table at the first catch-up
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS board_message_mentions (
			token      TEXT NOT NULL,
			message_id INTEGER NOT NULL,
			PRIMARY KEY (token, message_id)
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS board_mention_index_state (
			id         INTEGER PRIMARY KEY CHECK (id = 1),
			indexed_to INTEGER NOT NULL
		)`,
		`INSERT OR IGNORE INTO board_mention_index_state (id, indexed_to) VALUES (1, 0)`,
	} {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	// The backfill: everything already stored. It is bounded by the database, done in chunks, and a store
	// that cannot finish it still starts: ReadMessages catches up (or reads as it did) when it needs to.
	// A database with many messages to index says so (Security's #4465 INFO 1), so a slow start is explained.
	var pending int
	if err := s.db.GetContext(ctx, &pending, "SELECT COUNT(*) FROM board_messages WHERE id > (SELECT indexed_to FROM board_mention_index_state WHERE id = 1)"); err == nil && pending >= mentionBackfillLogMin {
		start := time.Now()
		log.Printf("[board] mention index: indexing %d messages (one time, at start)", pending)
		err := s.catchUpMentionIndex(ctx)
		if err != nil {
			log.Printf("[board] mention index: stopped after %s: %v (reads use the old query until it catches up)", time.Since(start).Round(time.Millisecond), err)
			return err
		}
		log.Printf("[board] mention index: %d messages indexed in %s", pending, time.Since(start).Round(time.Millisecond))
		return nil
	}
	return s.catchUpMentionIndex(ctx)
}

// mentionBackfillLogMin is how many messages to index at start before the store says it is doing so.
const mentionBackfillLogMin = 1000

// mentionTokens are the index tokens of a message's text (see the comment above): hex of the bytes, with
// a one-letter kind in front, so a token is plain ASCII whatever the bytes are.
func mentionTokens(content string) []string {
	lc := strings.ToLower(content)
	seen := map[string]struct{}{}
	add := func(kind byte, b string) bool {
		seen[string(kind)+hex.EncodeToString([]byte(b))] = struct{}{}
		return len(seen) <= maxMentionTokens
	}
	for i := 0; i < len(lc); i++ {
		switch c := lc[i]; {
		case c == '@' && i+1+mentionTokenBytes <= len(lc):
			if !add('@', lc[i+1:i+1+mentionTokenBytes]) {
				return []string{mentionWild}
			}
		case c == ':' && i >= mentionTokenBytes:
			if !add(':', lc[i-mentionTokenBytes:i]) {
				return []string{mentionWild}
			}
		case c == 0xE2 && strings.HasPrefix(lc[i:], emDash) && i >= mentionTokenBytes:
			if !add('-', lc[i-mentionTokenBytes:i]) {
				return []string{mentionWild}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	return out
}

const emDash = "—"

// mentionCandidateTokens are the tokens a message must have one of to mention a subscriber with these
// terms, plus the wild one; ok is false when a term cannot be indexed (shorter than three bytes, or of a
// shape the index does not know), and the LIKE query is used instead.
func mentionCandidateTokens(terms []string) (tokens []string, ok bool) {
	seen := map[string]struct{}{mentionWild: {}}
	tokens = []string{mentionWild}
	add := func(kind byte, b string) {
		t := string(kind) + hex.EncodeToString([]byte(b))
		if _, dup := seen[t]; !dup {
			seen[t] = struct{}{}
			tokens = append(tokens, t)
		}
	}
	for _, term := range terms {
		t := strings.ToLower(term)
		switch {
		case strings.HasPrefix(t, "@"):
			if len(t) < 1+mentionTokenBytes {
				return nil, false
			}
			add('@', t[1:1+mentionTokenBytes])
		case strings.HasSuffix(t, ":"):
			if pre := t[:len(t)-1]; len(pre) >= mentionTokenBytes {
				add(':', pre[len(pre)-mentionTokenBytes:])
			} else {
				return nil, false
			}
		case strings.HasSuffix(t, emDash):
			if pre := strings.TrimSuffix(t, emDash); len(pre) >= mentionTokenBytes {
				add('-', pre[len(pre)-mentionTokenBytes:])
			} else {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return tokens, true
}

// catchUpMentionIndex indexes the messages above mentionIndexedTo, a chunk at a time, each chunk and its new
// watermark in one transaction. It does nothing when the index is current.
func (s *Store) catchUpMentionIndex(ctx context.Context) error {
	s.mentionMu.Lock()
	defer s.mentionMu.Unlock()
	const chunk = 500
	for {
		n, err := s.indexMentionChunk(ctx, chunk)
		if err != nil {
			return err
		}
		if n < chunk {
			return nil
		}
	}
}

func (s *Store) indexMentionChunk(ctx context.Context, limit int) (int, error) {
	// The watermark is read from the table once, then kept here: only this store moves it (under mentionMu).
	from := s.mentionIndexedTo.Load()
	if from < 0 {
		if err := s.reader().GetContext(ctx, &from, sqlMentionGetMark); err != nil {
			return 0, err
		}
		s.mentionIndexedTo.Store(from)
	}
	var newest int64
	if err := getHot(ctx, s.hot().newestMessageID, s.reader(), &newest, sqlNewestMessageID); err != nil {
		return 0, err
	}
	if newest <= from {
		return 0, nil
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// The watermark read again inside the write transaction: nothing else moves it, but it is the writer's
	// view that decides which messages exist.
	h := s.hot()
	if err := txGet(ctx, tx, h.mentionGetMark, &from, sqlMentionGetMark); err != nil {
		return 0, err
	}
	s.mentionIndexedTo.Store(from)
	var rows []struct {
		ID      int64  `db:"id"`
		Content string `db:"content"`
	}
	if h.mentionChunk != nil {
		err = tx.StmtxContext(ctx, h.mentionChunk).SelectContext(ctx, &rows, from, limit)
	} else {
		err = tx.SelectContext(ctx, &rows, sqlMentionChunk, from, limit)
	}
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	var ins *sqlx.Stmt
	if h.mentionInsert != nil {
		ins = tx.StmtxContext(ctx, h.mentionInsert)
	} else if ins, err = tx.PreparexContext(ctx, sqlMentionInsert); err != nil {
		return 0, err
	}
	defer ins.Close() // closes the transaction's binding of a prepared statement, or the one prepared here
	for _, r := range rows {
		for _, tok := range mentionTokens(r.Content) {
			if _, err := ins.ExecContext(ctx, tok, r.ID); err != nil {
				return 0, err
			}
		}
	}
	if h.mentionSetMark != nil {
		_, err = tx.StmtxContext(ctx, h.mentionSetMark).ExecContext(ctx, rows[len(rows)-1].ID)
	} else {
		_, err = tx.ExecContext(ctx, sqlMentionSetMark, rows[len(rows)-1].ID)
	}
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.mentionIndexedTo.Store(rows[len(rows)-1].ID)
	return len(rows), nil
}

// mentionCandidateQuery is the one query text for every subscriber: the messages after a cursor, from others,
// that have one of the tokens (a JSON array of them), oldest first.
const mentionCandidateQuery = `SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system, m.created_at,
	        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title
	 FROM board_messages m
	 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
	 WHERE m.project = ? AND m.id > ? AND m.subscriber_id != ?
	   AND m.id IN (SELECT t.message_id FROM board_message_mentions t
	                WHERE t.token IN (SELECT value FROM json_each(?)) AND t.message_id > ?)
	 ORDER BY m.id ASC`

// indexedMentionCandidates returns the messages after lastReadID that can mention a subscriber with these
// terms and match the LIKE patterns the old query used (the first fetchLimit of them), oldest first. ok is
// false when the index cannot answer (an unindexable term, a catch-up that failed): the caller reads as it
// did before.
func (s *Store) indexedMentionCandidates(ctx context.Context, project, subscriberID string, lastReadID int64, terms []string, fetchLimit int) (out []Message, ok bool, err error) {
	tokens, can := mentionCandidateTokens(terms)
	if !can || s.noMentionIndex {
		return nil, false, nil
	}
	if err := s.catchUpMentionIndex(ctx); err != nil {
		return nil, false, nil
	}
	arr, _ := json.Marshal(tokens)
	patterns := make([]string, len(terms))
	for i, t := range terms {
		patterns[i] = "%" + t + "%"
	}
	rows, err := queryxHot(ctx, s.hot().mentionCandidates, s.reader(), mentionCandidateQuery, project, lastReadID, subscriberID, string(arr), lastReadID)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		if err := rows.StructScan(&m); err != nil {
			return nil, true, err
		}
		// LIKE, as SQLite matches it: the old query's candidate set, so what it would have cut off at
		// fetchLimit is cut off here.
		if !likeAny(m.Content, patterns) {
			continue
		}
		out = append(out, m)
		if len(out) >= fetchLimit {
			break
		}
	}
	return out, true, rows.Err()
}

// forgetMentionTokens drops the index rows of deleted messages.
func forgetMentionTokens(ctx context.Context, ex sqlx.ExecerContext, where string, args ...any) {
	_, _ = ex.ExecContext(ctx, "DELETE FROM board_message_mentions WHERE message_id IN (SELECT id FROM board_messages WHERE "+where+")", args...)
}

// mentionIndexClause is the SQL condition that narrows a messages query to the messages that can mention a
// subscriber: those with one of the tokens (a JSON array bound to the first ?) above a lower id (the second ?),
// or above the index's watermark, which are not indexed yet and so all stay candidates. alias is the messages
// table's prefix ("m." or ""). The rules that decide (likeAny, containsExplicitTag) run on what it finds.
func mentionIndexClause(alias string) string {
	return "(" + alias + "id IN (SELECT t.message_id FROM board_message_mentions t WHERE t.token IN (SELECT value FROM json_each(?)) AND t.message_id > ?) " +
		"OR " + alias + "id > (SELECT indexed_to FROM board_mention_index_state WHERE id = 1))"
}

// mentionIndexClauseNoAlias is mentionIndexClause("") as a constant, for the prepared count query.
const mentionIndexClauseNoAlias = "(id IN (SELECT t.message_id FROM board_message_mentions t WHERE t.token IN (SELECT value FROM json_each(?)) AND t.message_id > ?) " +
	"OR id > (SELECT indexed_to FROM board_mention_index_state WHERE id = 1))"

// mentionIndexTokensJSON is the bound JSON array of a subscriber's candidate tokens, after the index has been
// caught up; "" when the index cannot answer (an unindexable term, a failed catch-up, tests that read as before).
func (s *Store) mentionIndexTokensJSON(ctx context.Context, terms []string) string {
	tokens, ok := mentionCandidateTokens(terms)
	if !ok || s.noMentionIndex || s.catchUpMentionIndex(ctx) != nil {
		return ""
	}
	arr, _ := json.Marshal(tokens)
	return string(arr)
}

// txGet is getHot inside a transaction: a prepared statement is bound to the transaction with tx.Stmtx.
func txGet(ctx context.Context, tx *sqlx.Tx, st *sqlx.Stmt, dest any, query string, args ...any) error {
	if st != nil {
		return tx.StmtxContext(ctx, st).GetContext(ctx, dest, args...)
	}
	return tx.GetContext(ctx, dest, query, args...)
}
