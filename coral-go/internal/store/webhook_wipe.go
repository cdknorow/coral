package store

import (
	"context"
	"fmt"
)

// Coral doesn't send webhooks (#3646, the operator's decision). Their URLs are usually secrets
// (the token is in the path or query), and they were stored in sessions.db: webhook_configs.url,
// webhook_deliveries (whose old error messages could hold the URL) and scheduled_runs.webhook_url.
// WipeWebhookData removes them for real, as WipeMCPConfig does: secure_delete on for the
// connection that drops and clears them, then the WAL is checkpointed and truncated. The
// messageboard.db copy (board_subscribers.webhook_url) is board.Store.WipeWebhookURLs.

// WipeWebhookData drops the webhook tables and clears every run's webhook_url, returning how many
// rows it removed or cleared. It is safe at every start: with nothing left it changes nothing and
// doesn't checkpoint.
func WipeWebhookData(ctx context.Context, db *DB) (int, error) {
	conn, err := db.Connx(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA secure_delete = ON"); err != nil {
		return 0, fmt.Errorf("secure_delete: %w", err)
	}
	changed := 0
	for _, table := range []string{"webhook_deliveries", "webhook_configs"} {
		var exists int
		if err := conn.GetContext(ctx, &exists, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table); err != nil {
			return changed, err
		}
		if exists == 0 {
			continue
		}
		var n int
		if err := conn.GetContext(ctx, &n, "SELECT COUNT(*) FROM "+table); err != nil {
			return changed, err
		}
		// Rows are deleted first, so secure_delete zeroes them; the empty table is then dropped.
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return changed, err
		}
		if _, err := conn.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			return changed, err
		}
		changed += n + 1
	}
	res, err := conn.ExecContext(ctx, "UPDATE scheduled_runs SET webhook_url = NULL WHERE webhook_url IS NOT NULL")
	if err != nil {
		return changed, err
	}
	if n, err := res.RowsAffected(); err == nil {
		changed += int(n)
	}
	if changed > 0 {
		if err := truncateWAL(ctx, conn); err != nil {
			return changed, err
		}
	}
	return changed, nil
}
