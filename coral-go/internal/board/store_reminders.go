// Subscriber reminders (moved out of store.go, #4998; no behaviour change).

package board

import "context"

func (s *Store) UpsertSubscriberReminder(ctx context.Context, r SubscriberReminder) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO board_reminders(project,subscriber_id,message,interval_seconds) VALUES(?,?,?,?) ON CONFLICT(project,subscriber_id) DO UPDATE SET message=excluded.message, interval_seconds=excluded.interval_seconds`, r.Project, r.SubscriberID, r.Message, r.IntervalSeconds)
	return err
}

func (s *Store) DeleteSubscriberReminder(ctx context.Context, project, subscriber string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM board_reminders WHERE project=? AND subscriber_id=?`, project, subscriber)
	return err
}

func (s *Store) ListSubscriberReminders(ctx context.Context) ([]SubscriberReminder, error) {
	var out []SubscriberReminder
	err := s.reader().SelectContext(ctx, &out, `SELECT project,subscriber_id,message,interval_seconds FROM board_reminders`)
	return out, err
}
