package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3644, Security's requirement 1: every stored MCP configuration is removed, and removed for
// real. Unique markers seeded in all four places are found in no byte of sessions.db or its WAL
// afterwards, and the rest of each document is kept.
func TestWipeMCPConfigLeavesNoTraceInTheFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	require.NoError(t, os.WriteFile(path, migratedTemplate(t), 0o644))
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	markers := []string{"MCPMARK-LIVE-7f3a9c1e", "MCPMARK-TEAM-2b8d0f44", "MCPMARK-MEMBER-91c7e6aa", "MCPMARK-STEP-5d1e02bb", "MCPMARK-DEFAGENT-c4f9a713"}
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO live_sessions (session_id, agent_type, agent_name, working_dir, mcp_servers, status, created_at) VALUES ('s1', 'claude', 'claude-s1', '/tmp', ?, 'active', '2026-01-01T00:00:00Z')`,
		// Big enough for overflow pages: clearing it frees whole pages, which keep their old
		// bytes unless secure_delete zeroes them.
		`{"github":{"command":"npx","args":["`+strings.Repeat("p", 12000)+`"],"env":{"GITHUB_TOKEN":"`+markers[0]+`"}}}`)
	exec(`INSERT INTO teams (id, name, config_json, working_dir, created_at, updated_at) VALUES (1, 'team', ?, '/tmp', 'x', 'x')`,
		`{"agents":[{"name":"Dev","prompt":"keep-this-prompt","mcpServers":{"s":{"env":{"T":"`+markers[1]+`"}}}}]}`)
	exec(`INSERT INTO team_members (team_id, agent_name, agent_config_json, created_at) VALUES (1, 'Dev', ?, 'x')`,
		`{"name":"Dev","mcpServers":{"s":{"headers":{"Authorization":"`+markers[2]+`"}}}}`)
	exec(`INSERT INTO workflows (name, steps_json, default_agent_json, created_at, updated_at) VALUES ('wf', ?, ?, 'x', 'x')`,
		`[{"name":"one","type":"agent","agent":{"agent_type":"claude","mcpServers":{"s":{"env":{"T":"`+markers[3]+`"}}}}}]`,
		`{"agent_type":"claude","mcp_servers":{"s":{"env":{"T":"`+markers[4]+`"}}}}`)
	// Some of it reaches the main file, some stays in the WAL, as in a running Coral.
	exec("PRAGMA wal_checkpoint(PASSIVE)")
	exec(`UPDATE teams SET updated_at = 'y' WHERE id = 1`)

	n, err := WipeMCPConfig(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, 5, n, "the session, the team, the member and both workflow columns")

	for _, f := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		for _, m := range markers {
			assert.False(t, bytes.Contains(b, []byte(m)), "%s still holds %s", filepath.Base(f), m)
		}
	}

	var team, steps string
	require.NoError(t, db.GetContext(ctx, &team, "SELECT config_json FROM teams WHERE id = 1"))
	require.NoError(t, db.GetContext(ctx, &steps, "SELECT steps_json FROM workflows WHERE name = 'wf'"))
	assert.Contains(t, team, "keep-this-prompt", "the rest of the document is kept")
	assert.NotContains(t, team, "mcpServers")
	assert.Contains(t, steps, `"agent_type":"claude"`)
	var mcp *string
	require.NoError(t, db.GetContext(ctx, &mcp, "SELECT mcp_servers FROM live_sessions WHERE session_id = 's1'"))
	assert.Nil(t, mcp)

	n, err = WipeMCPConfig(ctx, db)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left: a later start changes nothing")
}
