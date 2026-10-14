package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3387: a token names its session; minting again (restart, resume) revokes the old one;
// tokens are unique and only their hash is stored.
func TestSessionTokens(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	a1, err := db.MintSessionToken(ctx, "sid-a", "claude-sid-a")
	require.NoError(t, err)
	b, err := db.MintSessionToken(ctx, "sid-b", "claude-sid-b")
	require.NoError(t, err)
	assert.NotEqual(t, a1, b)
	assert.GreaterOrEqual(t, len(a1), 43)

	o, err := db.SessionTokenOwnerOf(ctx, a1)
	require.NoError(t, err)
	assert.Equal(t, "sid-a", o.SessionID)
	assert.Equal(t, "claude-sid-a", o.SessionName)

	a2, err := db.MintSessionToken(ctx, "sid-a", "claude-sid-a")
	require.NoError(t, err)
	_, err = db.SessionTokenOwnerOf(ctx, a1)
	assert.ErrorIs(t, err, ErrUnknownSessionToken, "the restart revoked the old token")
	_, err = db.SessionTokenOwnerOf(ctx, a2)
	assert.NoError(t, err)
	_, err = db.SessionTokenOwnerOf(ctx, "made-up")
	assert.ErrorIs(t, err, ErrUnknownSessionToken)

	var raw int
	require.NoError(t, db.Get(&raw, `SELECT COUNT(*) FROM session_tokens WHERE token_hash = ? OR token_hash = ?`, a2, b))
	assert.Zero(t, raw, "the token itself is never stored")
}

// A session that ends, or is replaced by a restart or resume, loses its token.
func TestSessionTokensRevokedWithTheSession(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ss := NewSessionStore(db)
	require.NoError(t, ss.RegisterLiveSession(ctx, &LiveSession{SessionID: "old", AgentType: "claude", AgentName: "a", WorkingDir: "/tmp"}))
	tok, err := db.MintSessionToken(ctx, "old", "claude-old")
	require.NoError(t, err)
	require.NoError(t, ss.ReplaceLiveSession(ctx, "old", &LiveSession{SessionID: "new", AgentType: "claude", AgentName: "a", WorkingDir: "/tmp"}))
	_, err = db.SessionTokenOwnerOf(ctx, tok)
	assert.ErrorIs(t, err, ErrUnknownSessionToken, "restart/resume")

	tok2, err := db.MintSessionToken(ctx, "new", "claude-new")
	require.NoError(t, err)
	require.NoError(t, ss.UnregisterLiveSession(ctx, "new"))
	_, err = db.SessionTokenOwnerOf(ctx, tok2)
	assert.ErrorIs(t, err, ErrUnknownSessionToken, "the session ended")
}
