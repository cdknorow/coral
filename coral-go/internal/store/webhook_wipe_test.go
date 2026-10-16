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

// #3646: webhook URLs left in sessions.db by an older Coral (webhook_configs, webhook_deliveries'
// errors, scheduled_runs.webhook_url) are found in no byte of the file or its WAL after the wipe;
// the webhook tables are gone and a second run changes nothing.
func TestWipeWebhookDataLeavesNoTraceInTheFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	require.NoError(t, os.WriteFile(path, migratedTemplate(t), 0o644))
	db, err := OpenWithKey(context.Background(), path, "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}
	// The tables an older Coral created.
	exec(`CREATE TABLE webhook_configs (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, platform TEXT NOT NULL, url TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)
	exec(`CREATE TABLE webhook_deliveries (id INTEGER PRIMARY KEY AUTOINCREMENT, webhook_id INTEGER NOT NULL, agent_name TEXT NOT NULL, event_type TEXT NOT NULL, event_summary TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', error_msg TEXT, created_at TEXT NOT NULL)`)

	markers := []string{"WHMARK-CONFIG-4c1d9e2a", "WHMARK-DELIVERY-8b7f3a10", "WHMARK-RUN-2e6c5d97"}
	pad := strings.Repeat("p", 12000) // overflow pages: freed whole, zeroed only by secure_delete
	exec(`INSERT INTO webhook_configs (name, platform, url, created_at, updated_at) VALUES ('h', 'slack', ?, 'x', 'x')`,
		"https://hooks.slack.com/services/"+pad+"/"+markers[0])
	exec(`INSERT INTO webhook_deliveries (webhook_id, agent_name, event_type, event_summary, error_msg, created_at) VALUES (1, 'a', 'e', 's', ?, 'x')`,
		`Post "https://hooks.slack.com/`+markers[1]+`": refused`)
	exec(`INSERT INTO scheduled_jobs (name, cron_expr, repo_path, prompt, created_at, updated_at) VALUES ('j', '* * * * *', '/tmp', 'p', 'x', 'x')`)
	exec(`INSERT INTO scheduled_runs (job_id, status, scheduled_at, created_at, trigger_type, webhook_url) VALUES (1, 'pending', 'x', 'x', 'api', ?)`,
		"https://example.com/"+markers[2])
	exec("PRAGMA wal_checkpoint(PASSIVE)")
	exec(`UPDATE scheduled_jobs SET description = 'd' WHERE id = 1`)

	n, err := WipeWebhookData(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, 5, n, "two rows and two tables dropped, one run cleared")

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
	var tables int
	require.NoError(t, db.GetContext(ctx, &tables, "SELECT COUNT(*) FROM sqlite_master WHERE name IN ('webhook_configs', 'webhook_deliveries')"))
	assert.Zero(t, tables, "the webhook tables are gone")

	n, err = WipeWebhookData(ctx, db)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left: a later start changes nothing")
}
