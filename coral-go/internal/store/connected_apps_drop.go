package store

import (
	"context"
	"fmt"
)

// connected_apps (OAuth client secrets, access and refresh tokens, account emails) belonged to
// a feature that was retired; no code reads or writes it, yet the schema still created it
// (#3628 F8, at-rest audit). DropConnectedApps removes it for real, as the webhook wipe does:
// its rows are deleted with secure_delete on (the freed pages are zeroed), the empty table is
// dropped, and the WAL is checkpointed and truncated.

// DropConnectedApps drops the connected_apps table and returns how many rows it held plus one
// for the table, or 0 when it is already gone (a later start changes nothing).
func DropConnectedApps(ctx context.Context, db *DB) (int, error) {
	conn, err := db.Connx(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var exists int
	if err := conn.GetContext(ctx, &exists, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'connected_apps'"); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA secure_delete = ON"); err != nil {
		return 0, fmt.Errorf("secure_delete: %w", err)
	}
	var n int
	if err := conn.GetContext(ctx, &n, "SELECT COUNT(*) FROM connected_apps"); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM connected_apps"); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, "DROP TABLE connected_apps"); err != nil {
		return 0, err
	}
	if err := truncateWAL(ctx, conn); err != nil {
		return n + 1, err
	}
	return n + 1, nil
}
