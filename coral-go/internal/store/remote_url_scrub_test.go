package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3558: a remote URL recorded with its credentials (or a ?query or #fragment) before this
// release is cleaned at startup; other rows are untouched. The scrub runs once: after it, a
// store_meta row marks it done and a later call scans nothing (Security's NIT 3).
func TestScrubRemoteURLCredentials(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	insert := func(u string) {
		_, err := db.ExecContext(ctx, `INSERT INTO git_snapshots (agent_name, agent_type, working_directory, branch, commit_hash, remote_url, recorded_at) VALUES ('a', 'claude', '/w', 'main', ?, ?, '2026-01-01T00:00:00Z')`, u, u)
		require.NoError(t, err)
	}
	urls := func() []string {
		var out []string
		require.NoError(t, db.SelectContext(ctx, &out, `SELECT remote_url FROM git_snapshots ORDER BY id`))
		return out
	}
	// The test template was opened (and so scrubbed) already; start as a pre-#3558 store.
	_, err := db.ExecContext(ctx, `DELETE FROM store_meta WHERE key = ?`, remoteURLScrubKey)
	require.NoError(t, err)
	for _, u := range []string{"https://user:ghp_SECRET3558@github.com/o/r.git", "git@github.com:o/r.git", "https://github.com/o/clean.git", "https://host/o/q.git?access_token=X"} {
		insert(u)
	}
	scrubRemoteURLCredentials(ctx, db)
	assert.Equal(t, []string{"https://github.com/o/r.git", "git@github.com:o/r.git", "https://github.com/o/clean.git", "https://host/o/q.git"}, urls())
	var marks int
	require.NoError(t, db.GetContext(ctx, &marks, `SELECT COUNT(*) FROM store_meta WHERE key = ?`, remoteURLScrubKey))
	assert.Equal(t, 1, marks)

	// Once done, a later start doesn't scan again.
	insert("https://user:LATER@host/o/r.git")
	scrubRemoteURLCredentials(ctx, db)
	assert.Equal(t, "https://user:LATER@host/o/r.git", urls()[4])
}
