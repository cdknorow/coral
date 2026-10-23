package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaimClaudeArtifactSeenIsOncePerKeyAndForgetsAfterAMonth(t *testing.T) {
	ss := NewSessionStore(openTestDB(t))
	ctx := context.Background()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	first, err := ss.ClaimClaudeArtifactSeen(ctx, "link:abcdefgh1", "s1", now)
	require.NoError(t, err)
	assert.True(t, first)
	again, err := ss.ClaimClaudeArtifactSeen(ctx, "link:abcdefgh1", "s2", now.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, again, "the same key, whoever asks")
	other, err := ss.ClaimClaudeArtifactSeen(ctx, "tool:s1:/a/b.html", "s1", now.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, other)

	later, err := ss.ClaimClaudeArtifactSeen(ctx, "link:abcdefgh1", "s1", now.Add(ClaudeArtifactNudgeKeep+time.Hour))
	require.NoError(t, err)
	assert.True(t, later, "a row older than 30 days is forgotten")

	var keys []string
	require.NoError(t, ss.db.SelectContext(ctx, &keys, `SELECT key FROM claude_artifact_nudges`))
	for _, k := range keys {
		assert.NotContains(t, k, "abcdefgh1", "an id is stored hashed")
		assert.NotContains(t, k, "/a/b.html", "a path is stored hashed")
		assert.Regexp(t, `^(link|tool):[0-9a-f]{64}$`, k)
	}
	long := "tool:s1:/" + string(make([]byte, 5000))
	first, err = ss.ClaimClaudeArtifactSeen(ctx, long, "s1", now)
	require.NoError(t, err)
	assert.True(t, first)
	again, err = ss.ClaimClaudeArtifactSeen(ctx, long+"more", "s1", now)
	require.NoError(t, err)
	assert.False(t, again, "only the first KiB of a key is read")
}
