package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
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
	return token, tx.Commit()
}

// revokeSessionTokensTx revokes every live token of a session.
func revokeSessionTokensTx(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, sessionID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE session_tokens SET revoked_at = ? WHERE session_id = ? AND revoked_at IS NULL`, nowUTC(), sessionID)
	return err
}

// SessionTokenOwnerOf returns the session a live (unrevoked) token belongs to.
func (db *DB) SessionTokenOwnerOf(ctx context.Context, token string) (*SessionTokenOwner, error) {
	var o SessionTokenOwner
	err := db.GetContext(ctx, &o, `SELECT session_id, session_name FROM session_tokens WHERE token_hash = ? AND revoked_at IS NULL`, hashSessionToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnknownSessionToken
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}
