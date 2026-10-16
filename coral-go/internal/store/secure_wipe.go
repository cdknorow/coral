package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// truncateWAL checkpoints the WAL into the database file and truncates it, so cleared values
// that still sat in its frames are gone (#3644, #3646). The checkpoint is busy while a reader
// holds an older snapshot: it is retried briefly, and an error says so if it never completes.
func truncateWAL(ctx context.Context, conn *sqlx.Conn) error {
	for attempt := 0; ; attempt++ {
		var busy, logFrames, done int
		if err := conn.QueryRowxContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &done); err != nil {
			return fmt.Errorf("wal checkpoint: %w", err)
		}
		if busy == 0 {
			return nil
		}
		if attempt == 20 {
			return fmt.Errorf("wal checkpoint: still busy after %d tries; the cleared values stay in the WAL until the next checkpoint", attempt+1)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
