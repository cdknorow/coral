package board

import (
	"context"
	"fmt"
	"time"
)

// WipeWebhookURLs clears every subscription's webhook_url (#3646: Coral doesn't send webhooks,
// and the URLs are usually secrets) and removes them for real: secure_delete on for the
// connection that clears them, then the WAL is checkpointed and truncated. It returns how many
// subscriptions it cleared; with none left it changes nothing and doesn't checkpoint. The column
// stays, unused.
func (s *Store) WipeWebhookURLs(ctx context.Context) (int, error) {
	conn, err := s.db.Connx(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA secure_delete = ON"); err != nil {
		return 0, fmt.Errorf("secure_delete: %w", err)
	}
	res, err := conn.ExecContext(ctx, "UPDATE board_subscribers SET webhook_url = NULL WHERE webhook_url IS NOT NULL")
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return 0, nil
	}
	for attempt := 0; ; attempt++ {
		var busy, logFrames, done int
		if err := conn.QueryRowxContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &done); err != nil {
			return int(n), fmt.Errorf("wal checkpoint: %w", err)
		}
		if busy == 0 {
			return int(n), nil
		}
		if attempt == 20 {
			return int(n), fmt.Errorf("wal checkpoint: still busy after %d tries; the cleared values stay in the WAL until the next checkpoint", attempt+1)
		}
		select {
		case <-ctx.Done():
			return int(n), ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
