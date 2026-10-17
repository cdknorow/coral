package board

import "context"

// Optimize is store.DB.Optimize for the board database (#4142).
func (s *Store) Optimize(ctx context.Context, first bool) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA analysis_limit=400`); err != nil {
		return err
	}
	q := `PRAGMA optimize`
	if first {
		q = `PRAGMA optimize=0x10002`
	}
	_, err := s.db.ExecContext(ctx, q)
	return err
}
