package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Per-session agent tokens (#3387, identity phase 1). Each agent session gets a random
// token at launch, restart, resume or wake; the agent's tools send it back
// (X-Coral-Session-Token) so the server knows which session a call really comes from.
// Only a token's SHA-256 is stored, and minting for a session revokes its previous one.

// SessionTokenHeader carries an agent's session token.
const SessionTokenHeader = "X-Coral-Session-Token"

// SessionTokenEnv is the environment variable an agent's tools read the token from.
const SessionTokenEnv = "CORAL_SESSION_TOKEN"

// ErrUnknownSessionToken is a token Coral never minted, or one that was revoked.
var ErrUnknownSessionToken = errors.New("unknown or revoked session token")

// SessionTokenOwner is the session a token was minted for.
type SessionTokenOwner struct {
	SessionID   string `db:"session_id"`
	SessionName string `db:"session_name"`
}

func hashSessionToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// MintSessionToken returns a fresh token for the session and revokes any earlier one.
func (db *DB) MintSessionToken(ctx context.Context, sessionID, sessionName string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := nowUTC()
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE session_tokens SET revoked_at = ? WHERE session_id = ? AND revoked_at IS NULL`, now, sessionID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_tokens (token_hash, session_id, session_name, created_at) VALUES (?, ?, ?, ?)`,
		hashSessionToken(token), sessionID, sessionName, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	db.InvalidateSessionTokens() // the session's earlier token is revoked now
	return token, nil
}

// revokeSessionTokensTx revokes every live token of a session.
func revokeSessionTokensTx(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, sessionID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE session_tokens SET revoked_at = ? WHERE session_id = ? AND revoked_at IS NULL`, nowUTC(), sessionID)
	return err
}

// tokenOwnerTTL is the longest a cached owner is trusted without a write of this process
// invalidating it: the backstop for a change this process did not see (another process on the
// same database file).
const tokenOwnerTTL = 10 * time.Second

// tokenOwnerMax bounds the cache; past it the cache is emptied rather than grown.
const tokenOwnerMax = 4096

// tokenOwnerCache remembers which session a LIVE token belongs to, so an agent request does not
// query the database for every call (#4303: it was the costliest statement on the write
// connection). Only a positive answer is kept. Every write that changes a token (mint, which
// revokes the session's earlier one; unregister and replace of a session) invalidates it AFTER the
// write commits, and a lookup that began before an invalidation never stores its answer (gen), so
// a revoked token is not accepted from the cache on the next request. An error is never cached
// (a lookup that fails refuses the token).
type tokenOwnerCache struct {
	mu      sync.Mutex
	entries map[string]tokenOwnerEntry
	gen     atomic.Uint64

	hits, lookups atomic.Int64 // for tests and measurement
}

type tokenOwnerEntry struct {
	owner   SessionTokenOwner
	expires time.Time
}

func (c *tokenOwnerCache) get(hash string) (SessionTokenOwner, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[hash]
	if !ok || time.Now().After(e.expires) {
		delete(c.entries, hash)
		return SessionTokenOwner{}, false
	}
	return e.owner, true
}

// put stores an answer unless an invalidation happened since gen was read.
func (c *tokenOwnerCache) put(hash string, o SessionTokenOwner, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen.Load() != gen {
		return
	}
	if c.entries == nil || len(c.entries) >= tokenOwnerMax {
		c.entries = map[string]tokenOwnerEntry{}
	}
	c.entries[hash] = tokenOwnerEntry{owner: o, expires: time.Now().Add(tokenOwnerTTL)}
}

func (c *tokenOwnerCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen.Add(1)
	c.entries = nil
}

// InvalidateSessionTokens drops every cached token owner. A write that changes a token calls it
// after the write commits.
func (db *DB) InvalidateSessionTokens() { db.tokenOwners.invalidate() }

// SessionTokenCacheStats is how many owner lookups were asked for and how many the cache answered.
func (db *DB) SessionTokenCacheStats() (lookups, hits int64) {
	return db.tokenOwners.lookups.Load(), db.tokenOwners.hits.Load()
}

// SessionTokenOwnerOf returns the session a live (unrevoked) token belongs to. The answer is
// cached (tokenOwnerCache); a miss reads the read pool, not the write connection.
func (db *DB) SessionTokenOwnerOf(ctx context.Context, token string) (*SessionTokenOwner, error) {
	hash := hashSessionToken(token)
	db.tokenOwners.lookups.Add(1)
	if o, ok := db.tokenOwners.get(hash); ok {
		db.tokenOwners.hits.Add(1)
		return &o, nil
	}
	gen := db.tokenOwners.gen.Load()
	var o SessionTokenOwner
	err := db.Reader().GetContext(ctx, &o, `SELECT session_id, session_name FROM session_tokens WHERE token_hash = ? AND revoked_at IS NULL`, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnknownSessionToken
	}
	if err != nil {
		return nil, err
	}
	db.tokenOwners.put(hash, o, gen)
	return &o, nil
}
