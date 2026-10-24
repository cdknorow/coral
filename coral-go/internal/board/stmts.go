package board

import (
	"context"
	"errors"
	"sync"

	"github.com/jmoiron/sqlx"
)

// The board store's hot fixed queries, prepared once (#4463, from the #4446 survey: SQLite parsed every
// query again on every call, 23% of a busy server's CPU). They are prepared on the read pool after the
// schema is in place, and closed before the store's databases. A *sqlx.Stmt on a *sqlx.DB is safe for
// concurrent use and prepares itself on each pool connection as needed; SQLite re-prepares a statement
// whose schema changed. Only fixed SQL is here: a query built per call stays unprepared.
const (
	sqlSubscriptionBySessionName = "SELECT * FROM board_subscribers WHERE session_name = ? AND is_active = 1 ORDER BY subscribed_at DESC, rowid DESC LIMIT 1"
	sqlBoardByID                 = `SELECT id, label, created_at, created_by, board_uid FROM boards WHERE id = ?`
	sqlBoardsLabelled            = `SELECT b.id, b.label, b.created_at, b.created_by, b.board_uid FROM boards b WHERE b.label = ?
		AND (EXISTS (SELECT 1 FROM board_subscribers WHERE project = b.id)
		  OR EXISTS (SELECT 1 FROM board_messages WHERE project = b.id)
		  OR EXISTS (SELECT 1 FROM board_tasks WHERE board_id = b.id))
		ORDER BY b.created_at, b.id`
	sqlOwnMaxMessageID = "SELECT COALESCE(MAX(id), 0) FROM board_messages WHERE project = ? AND subscriber_id = ?"

	// The mention queries (#4483 follow-up; they are fixed text since the mention index, #4465): ReadMessages' candidates,
	// the catch-up's check for new messages, CheckUnread's count from the index, and, on the write pool, the catch-up's
	// own statements.
	sqlNewestMessageID = "SELECT COALESCE(MAX(id), 0) FROM board_messages"
	sqlMentionCount    = `SELECT content FROM board_messages WHERE subscriber_id != ? AND subscriber_id NOT IN ('Coral Task Queue') AND ` +
		mentionIndexClauseNoAlias + ` AND project = ? AND id > ? AND id <= ?`
	sqlMentionChunk   = "SELECT id, content FROM board_messages WHERE id > ? ORDER BY id ASC LIMIT ?"
	sqlMentionInsert  = "INSERT OR IGNORE INTO board_message_mentions (token, message_id) VALUES (?, ?)"
	sqlMentionSetMark = "UPDATE board_mention_index_state SET indexed_to = ? WHERE id = 1"
	sqlMentionGetMark = "SELECT indexed_to FROM board_mention_index_state WHERE id = 1"
)

// maxCachedStmts bounds the statements prepared lazily for the few fixed shapes of confirmedMention's query
// (its WHERE is one of four constants); a shape past the bound runs unprepared.
const maxCachedStmts = 16

// hotStmts are the prepared statements; a nil field (or a nil *hotStmts) means the query runs unprepared.
type hotStmts struct {
	subscriptionBySessionName *sqlx.Stmt
	boardByID                 *sqlx.Stmt
	boardsLabelled            *sqlx.Stmt
	ownMaxMessageID           *sqlx.Stmt

	// The mention index's (read pool: candidates, newest id, count; write pool: the catch-up's).
	mentionCandidates *sqlx.Stmt
	newestMessageID   *sqlx.Stmt
	mentionCount      *sqlx.Stmt
	mentionChunk      *sqlx.Stmt
	mentionInsert     *sqlx.Stmt
	mentionSetMark    *sqlx.Stmt
	mentionGetMark    *sqlx.Stmt

	// cached are the fixed query shapes prepared on first use (confirmedMention's), bounded by maxCachedStmts.
	cacheMu sync.Mutex
	cached  map[string]*sqlx.Stmt
}

// prepareHotStmts prepares the statements on db. A statement that can't be prepared is left nil, and its
// query runs unprepared as before: preparing is an optimisation, never a reason for the store to fail.
func prepareHotStmts(ctx context.Context, db *sqlx.DB) *hotStmts {
	prep := func(q string) *sqlx.Stmt {
		st, err := db.PreparexContext(ctx, q)
		if err != nil {
			return nil
		}
		return st
	}
	return &hotStmts{
		subscriptionBySessionName: prep(sqlSubscriptionBySessionName),
		boardByID:                 prep(sqlBoardByID),
		boardsLabelled:            prep(sqlBoardsLabelled),
		ownMaxMessageID:           prep(sqlOwnMaxMessageID),
		mentionCandidates:         prep(mentionCandidateQuery),
		newestMessageID:           prep(sqlNewestMessageID),
		mentionCount:              prep(sqlMentionCount),
	}
}

// prepareWriter prepares the statements the mention index's catch-up runs on the write pool (inside its
// transaction, through tx.Stmtx); one that can't be prepared is left nil and runs unprepared.
func (h *hotStmts) prepareWriter(ctx context.Context, db *sqlx.DB) {
	if h == nil {
		return
	}
	prep := func(q string) *sqlx.Stmt {
		st, err := db.PreparexContext(ctx, q)
		if err != nil {
			return nil
		}
		return st
	}
	h.mentionChunk, h.mentionInsert, h.mentionSetMark, h.mentionGetMark = prep(sqlMentionChunk), prep(sqlMentionInsert), prep(sqlMentionSetMark), prep(sqlMentionGetMark)
}

// cachedStmt is the statement for a fixed query shape, prepared on first use on db; nil when the cache is full or
// the query can't be prepared (the caller then runs it unprepared).
func (h *hotStmts) cachedStmt(ctx context.Context, db *sqlx.DB, query string) *sqlx.Stmt {
	if h == nil {
		return nil
	}
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	if st, ok := h.cached[query]; ok {
		return st
	}
	if len(h.cached) >= maxCachedStmts {
		return nil
	}
	st, err := db.PreparexContext(ctx, query)
	if err != nil {
		st = nil
	}
	if h.cached == nil {
		h.cached = map[string]*sqlx.Stmt{}
	}
	h.cached[query] = st
	return st
}

// close closes every prepared statement; the store's queries run unprepared afterwards.
func (h *hotStmts) close() error {
	if h == nil {
		return nil
	}
	var errs []error
	for _, st := range []**sqlx.Stmt{&h.subscriptionBySessionName, &h.boardByID, &h.boardsLabelled, &h.ownMaxMessageID,
		&h.mentionCandidates, &h.newestMessageID, &h.mentionCount, &h.mentionChunk, &h.mentionInsert, &h.mentionSetMark, &h.mentionGetMark} {
		if *st != nil {
			errs = append(errs, (*st).Close())
			*st = nil
		}
	}
	h.cacheMu.Lock()
	for q, st := range h.cached {
		if st != nil {
			errs = append(errs, st.Close())
		}
		delete(h.cached, q)
	}
	h.cacheMu.Unlock()
	return errors.Join(errs...)
}

// getHot runs query through st when it was prepared, otherwise on db.
func getHot(ctx context.Context, st *sqlx.Stmt, db *sqlx.DB, dest any, query string, args ...any) error {
	if st != nil {
		return st.GetContext(ctx, dest, args...)
	}
	return db.GetContext(ctx, dest, query, args...)
}

// selectHot is getHot for a query that returns rows.
func selectHot(ctx context.Context, st *sqlx.Stmt, db *sqlx.DB, dest any, query string, args ...any) error {
	if st != nil {
		return st.SelectContext(ctx, dest, args...)
	}
	return db.SelectContext(ctx, dest, query, args...)
}

// hot is the store's prepared statements, or an empty set when there are none.
func (s *Store) hot() *hotStmts {
	if s.stmts == nil {
		return &hotStmts{}
	}
	return s.stmts
}

// queryxHot is selectHot for a query read row by row.
func queryxHot(ctx context.Context, st *sqlx.Stmt, db *sqlx.DB, query string, args ...any) (*sqlx.Rows, error) {
	if st != nil {
		return st.QueryxContext(ctx, args...)
	}
	return db.QueryxContext(ctx, query, args...)
}
