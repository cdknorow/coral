package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3558: a remote URL recorded with its credentials before this release is cleaned at
// startup; other rows are untouched.
func TestScrubRemoteURLCredentials(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, u := range []string{"https://user:ghp_SECRET3558@github.com/o/r.git", "git@github.com:o/r.git", "https://github.com/o/clean.git"} {
		_, err := db.ExecContext(ctx, `INSERT INTO git_snapshots (agent_name, agent_type, working_directory, branch, commit_hash, remote_url, recorded_at) VALUES ('a', 'claude', '/w', 'main', 'c', ?, '2026-01-01T00:00:00Z')`, u)
		require.NoError(t, err)
	}
	scrubRemoteURLCredentials(ctx, db)
	var urls []string
	require.NoError(t, db.SelectContext(ctx, &urls, `SELECT remote_url FROM git_snapshots ORDER BY id`))
	assert.Equal(t, []string{"https://github.com/o/r.git", "git@github.com:o/r.git", "https://github.com/o/clean.git"}, urls)
}
