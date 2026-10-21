package board

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/naming"
)

// What the orchestrator's health check needs to tell the agents apart (#4583).

// SleepingSubscribers is the set of subscriber ids on project whose session is asleep (an archived session is asleep too: the
// store sets is_sleeping with is_archived). Empty when the sessions database is not attached.
func (s *Store) SleepingSubscribers(ctx context.Context, project string) (map[string]bool, error) {
	out := map[string]bool{}
	if s.sessionsDB == nil {
		return out, nil
	}
	var subs []struct {
		SubscriberID string  `db:"subscriber_id"`
		SessionName  *string `db:"session_name"`
	}
	if err := s.reader().SelectContext(ctx, &subs, `SELECT subscriber_id, session_name FROM board_subscribers WHERE project = ? AND is_active = 1`, project); err != nil {
		return nil, err
	}
	byID := map[string][]string{} // session uuid -> subscriber ids
	var uuids []string
	for _, sub := range subs {
		if sub.SessionName == nil || strings.TrimSpace(*sub.SessionName) == "" {
			continue
		}
		id := naming.SessionIDFromName(*sub.SessionName)
		if _, seen := byID[id]; !seen {
			uuids = append(uuids, id)
		}
		byID[id] = append(byID[id], sub.SubscriberID)
	}
	for start := 0; start < len(uuids); start += idChunkSize {
		chunk := uuids[start:min(start+idChunkSize, len(uuids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		var asleep []string
		if err := s.sessionsDB.SelectContext(ctx, &asleep, `SELECT session_id FROM live_sessions WHERE is_sleeping = 1 AND session_id IN (`+placeholders(len(chunk))+`)`, args...); err != nil {
			return nil, err
		}
		for _, id := range asleep {
			for _, sub := range byID[id] {
				out[sub] = true
			}
		}
	}
	return out, nil
}

// UnreadTagged is how many messages tagged to subscriberID (its name or its role, as MentionsOf counts them) were posted after the
// last message it read, and when the oldest was posted (zero when there are none). Coral's own lines and its own posts don't count.
func (s *Store) UnreadTagged(ctx context.Context, project, subscriberID string) (int, time.Time, error) {
	var sub struct {
		LastReadID   int64  `db:"last_read_id"`
		JobTitle     string `db:"job_title"`
		SubscribedAt string `db:"subscribed_at"`
	}
	if err := s.reader().GetContext(ctx, &sub, `SELECT last_read_id, job_title, subscribed_at FROM board_subscribers WHERE project = ? AND subscriber_id = ?`, project, subscriberID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, time.Time{}, nil
		}
		return 0, time.Time{}, err
	}
	// Counted by message id (a message in the same second as the last one read still counts); the time only narrows the scan.
	since, _ := time.Parse(time.RFC3339, sub.SubscribedAt)
	since = since.Add(-time.Second)
	if sub.LastReadID > 0 {
		var at sql.NullString
		if err := s.reader().GetContext(ctx, &at, `SELECT created_at FROM board_messages WHERE id = ? AND project = ?`, sub.LastReadID, project); err == nil && at.Valid {
			if t, perr := time.Parse(time.RFC3339, at.String); perr == nil && t.After(since) {
				since = t.Add(-time.Second)
			}
		}
	}
	all, err := s.MentionsOf(ctx, project, subscriberID, sub.JobTitle, since)
	if err != nil {
		return 0, time.Time{}, err
	}
	var mentions []MentionOf
	for _, m := range all {
		if m.ID > sub.LastReadID {
			mentions = append(mentions, m)
		}
	}
	if len(mentions) == 0 {
		return 0, time.Time{}, nil
	}
	first, _ := time.Parse(time.RFC3339, mentions[0].CreatedAt)
	return len(mentions), first, nil
}

// MentionedBy is when (the first time after since) a message from sender mentioned the member: the check calls a wait STALE when the
// party it waits on has tagged it since the wait began and it has not woken. Zero when there is none. sender is a subscriber id or a
// role; a mention carries its sender's subscriber id.
func (s *Store) MentionedBy(ctx context.Context, project, name, title string, since time.Time, senders ...string) (time.Time, error) {
	mentions, err := s.MentionsOf(ctx, project, name, title, since)
	if err != nil {
		return time.Time{}, err
	}
	for _, m := range mentions {
		for _, sender := range senders {
			if sender != "" && SameIdentity(m.Sender, sender) {
				at, _ := time.Parse(time.RFC3339, m.CreatedAt)
				return at, nil
			}
		}
	}
	return time.Time{}, nil
}
