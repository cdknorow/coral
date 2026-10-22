package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/serverproof"
)

// #6103 (Security's LOW): the proof route is public, so a flood of challenges for made-up hashes must cost no database reads. The live sessions' keys are kept in memory, read again only after a
// write that changes a token, or after the TTL.
func TestAFloodOfUnknownProofHashesReadsNothingFromTheDatabase(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tok, err := db.MintSessionToken(ctx, "sid-p", "claude-sid-p")
	require.NoError(t, err)
	hash := serverproof.TokenHash(tok)

	_, ok := db.SessionProofKey(ctx, hash)
	require.True(t, ok, "a live session's key is found")
	before := db.ProofKeyLoads()
	for i := 0; i < 5000; i++ {
		_, ok := db.SessionProofKey(ctx, fmt.Sprintf("%064x", i))
		require.False(t, ok)
	}
	_, ok = db.SessionProofKey(ctx, hash)
	assert.True(t, ok, "and still found during the flood")
	assert.Equal(t, before, db.ProofKeyLoads(), "5000 made-up hashes read nothing from the database")
}

func TestTheProofKeySetFollowsMintAndRevokeAndTheTTL(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	first, err := db.MintSessionToken(ctx, "sid-q", "claude-sid-q")
	require.NoError(t, err)
	_, ok := db.SessionProofKey(ctx, serverproof.TokenHash(first))
	require.True(t, ok)

	second, err := db.MintSessionToken(ctx, "sid-q", "claude-sid-q") // a restart: the first token is revoked
	require.NoError(t, err)
	_, ok = db.SessionProofKey(ctx, serverproof.TokenHash(first))
	assert.False(t, ok, "a revoked token's key is gone at once, not after the TTL")
	_, ok = db.SessionProofKey(ctx, serverproof.TokenHash(second))
	assert.True(t, ok, "the new token's key is found at once")

	// a change this process did not see (another process on the file) is picked up when the set expires
	_, err = db.ExecContext(ctx, `UPDATE session_tokens SET proof_key = NULL WHERE token_hash = ?`, serverproof.TokenHash(second))
	require.NoError(t, err)
	_, ok = db.SessionProofKey(ctx, serverproof.TokenHash(second))
	assert.True(t, ok, "still the set's answer within the TTL")
	db.proofKeys.mu.Lock()
	db.proofKeys.until = time.Now().Add(-time.Second)
	db.proofKeys.mu.Unlock()
	_, ok = db.SessionProofKey(ctx, serverproof.TokenHash(second))
	assert.False(t, ok, "read again once the TTL has passed")
}
