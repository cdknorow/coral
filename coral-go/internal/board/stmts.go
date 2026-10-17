package board

import (
	"context"
	"errors"

	"github.com/jmoiron/sqlx"
)

// The board store's hot fixed queries, prepared once (#4463, from the #4446 survey: SQLite parsed every
// query again on every call, 23% of a busy server's CPU). They are prepared on the read pool after the
// schema is in place, and closed before the store's databases. A *sqlx.Stmt on a *sqlx.DB is safe for
// concurrent use and prepares itself on each pool connection as needed; SQLite re-prepares a statement
// whose schema changed. Only fixed SQL is here: a query built per call stays unprepared.
const (
	sqlSubscriptionBySessionName = "SELECT * FROM board_subscribers WHERE session_name = ? AND is_active = 1 ORDER BY subscribed_at DESC LIMIT 1"
	sqlBoardByID                 = `SELECT id, label, created_at, created_by FROM boards WHERE id = ?`
	sqlBoardsLabelled            = `SELECT b.id, b.label, b.created_at, b.created_by FROM boards b WHERE b.label = ?
		AND (EXISTS (SELECT 1 FROM board_subscribers WHERE project = b.id)
		  OR EXISTS (SELECT 1 FROM board_messages WHERE project = b.id)
		  OR EXISTS (SELECT 1 FROM board_tasks WHERE board_id = b.id))
		ORDER BY b.created_at, b.id`
	sqlOwnMaxMessageID = "SELECT COALESCE(MAX(id), 0) FROM board_messages WHERE project = ? AND subscriber_id = ?"
)

// hotStmts are the prepared statements; a nil field (or a nil *hotStmts) means the query runs unprepared.
type hotStmts struct {
	subscriptionBySessionName *sqlx.Stmt
	boardByID                 *sqlx.Stmt
	boardsLabelled            *sqlx.Stmt
	ownMaxMessageID           *sqlx.Stmt
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
	}
}

// close closes every prepared statement; the store's queries run unprepared afterwards.
func (h *hotStmts) close() error {
	if h == nil {
		return nil
	}
	var errs []error
	for _, st := range []**sqlx.Stmt{&h.subscriptionBySessionName, &h.boardByID, &h.boardsLabelled, &h.ownMaxMessageID} {
		if *st != nil {
			errs = append(errs, (*st).Close())
			*st = nil
		}
	}
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
