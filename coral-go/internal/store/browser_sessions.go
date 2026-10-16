package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/cdknorow/coral/internal/auth"
)

// Browser sessions (#3949) live in sessions.db so a sign-in survives a restart. The auth
// package keeps the cookie and its rules; this is only where the rows are kept (it implements
// auth.SessionBackend). Only the SHA-256 of a cookie value is stored.

type browserSessionRow struct {
	IDHash     string `db:"id_hash"`
	Label      string `db:"label"`
	Method     string `db:"method"`
	Persistent bool   `db:"persistent"`
	CreatedAt  int64  `db:"created_at"`
	LastSeen   int64  `db:"last_seen"`
	ExpiresAt  int64  `db:"expires_at"`
}

func (r browserSessionRow) record() auth.SessionRecord {
	return auth.SessionRecord{
		IDHash: r.IDHash, Label: r.Label, Method: r.Method, Persistent: r.Persistent,
		CreatedAt: time.Unix(r.CreatedAt, 0).UTC(), LastSeen: time.Unix(r.LastSeen, 0).UTC(), ExpiresAt: time.Unix(r.ExpiresAt, 0).UTC(),
	}
}

// BrowserSessions is the sessions.db side of auth.SessionBackend.
type BrowserSessions struct{ db *DB }

// NewBrowserSessions keeps browser sessions in db.
func NewBrowserSessions(db *DB) *BrowserSessions { return &BrowserSessions{db: db} }

func (b *BrowserSessions) PutSession(ctx context.Context, s auth.SessionRecord) error {
	_, err := b.db.ExecContext(ctx, `INSERT OR REPLACE INTO browser_sessions
		(id_hash, label, method, persistent, created_at, last_seen, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.IDHash, s.Label, s.Method, s.Persistent, s.CreatedAt.Unix(), s.LastSeen.Unix(), s.ExpiresAt.Unix())
	return err
}

func (b *BrowserSessions) GetSession(ctx context.Context, idHash string) (auth.SessionRecord, bool, error) {
	var row browserSessionRow
	err := b.db.Reader().GetContext(ctx, &row, `SELECT id_hash, label, method, persistent, created_at, last_seen, expires_at
		FROM browser_sessions WHERE id_hash = ?`, idHash)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.SessionRecord{}, false, nil
	}
	if err != nil {
		return auth.SessionRecord{}, false, err
	}
	return row.record(), true, nil
}

func (b *BrowserSessions) TouchSession(ctx context.Context, idHash string, at time.Time) error {
	_, err := b.db.ExecContext(ctx, `UPDATE browser_sessions SET last_seen = ? WHERE id_hash = ?`, at.Unix(), idHash)
	return err
}

func (b *BrowserSessions) DeleteSession(ctx context.Context, idHash string) error {
	_, err := b.db.ExecContext(ctx, `DELETE FROM browser_sessions WHERE id_hash = ?`, idHash)
	return err
}

func (b *BrowserSessions) DeleteSessionsExcept(ctx context.Context, keep string) (int, error) {
	res, err := b.db.ExecContext(ctx, `DELETE FROM browser_sessions WHERE id_hash != ?`, keep)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (b *BrowserSessions) ListSessions(ctx context.Context) ([]auth.SessionRecord, error) {
	var rows []browserSessionRow
	if err := b.db.Reader().SelectContext(ctx, &rows, `SELECT id_hash, label, method, persistent, created_at, last_seen, expires_at
		FROM browser_sessions ORDER BY last_seen DESC`); err != nil {
		return nil, err
	}
	out := make([]auth.SessionRecord, len(rows))
	for i, r := range rows {
		out[i] = r.record()
	}
	return out, nil
}

// SweepSessions deletes expired rows, and the least recently used beyond max.
func (b *BrowserSessions) SweepSessions(ctx context.Context, now time.Time, max int) error {
	if _, err := b.db.ExecContext(ctx, `DELETE FROM browser_sessions WHERE expires_at <= ?`, now.Unix()); err != nil {
		return err
	}
	_, err := b.db.ExecContext(ctx, `DELETE FROM browser_sessions WHERE id_hash NOT IN
		(SELECT id_hash FROM browser_sessions ORDER BY last_seen DESC LIMIT ?)`, max)
	return err
}
