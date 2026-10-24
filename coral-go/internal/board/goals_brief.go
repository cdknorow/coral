package board

import "context"

// GoalBriefs is the goals brief of every board that has any (a goals change counter row), for the live feed (#7116): one query, no text. A board that never had a goal is not in it.
func (s *Store) GoalBriefs(ctx context.Context) (map[string]GoalsBrief, error) {
	var rows []struct {
		Board string `db:"board_id"`
		Seq   int64  `db:"seq"`
		Open  int    `db:"open"`
	}
	if err := s.reader().SelectContext(ctx, &rows, `SELECT m.board_id, m.seq,
		(SELECT COUNT(*) FROM board_goals g WHERE g.board_id = m.board_id AND g.state IN ('proposed', 'active', 'blocked')) AS open
		FROM board_goal_meta m`); err != nil {
		return nil, err
	}
	out := make(map[string]GoalsBrief, len(rows))
	for _, r := range rows {
		out[r.Board] = GoalsBrief{Open: r.Open, Seq: r.Seq}
	}
	return out, nil
}
