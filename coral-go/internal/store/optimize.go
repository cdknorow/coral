package store

import "context"

// JournalSizeLimit is the size the WAL is truncated back to after a checkpoint (#4142): a WAL
// never shrinks on its own, so one large transaction (a migration, a call-metrics rollup after an
// upgrade) would otherwise leave it at that size for good.
const JournalSizeLimit = 16 << 20

// Optimize runs SQLite's PRAGMA optimize on the writer (#4142): it refreshes the query planner's
// statistics where they are missing or out of date, bounded by analysis_limit so a large table
// costs no more than a small one. first asks for the fuller pass SQLite recommends once per
// long-lived connection (0x10002); later passes, every few hours, are the plain form.
func (d *DB) Optimize(ctx context.Context, first bool) error {
	if _, err := d.ExecContext(ctx, `PRAGMA analysis_limit=400`); err != nil {
		return err
	}
	q := `PRAGMA optimize`
	if first {
		q = `PRAGMA optimize=0x10002`
	}
	_, err := d.ExecContext(ctx, q)
	return err
}
