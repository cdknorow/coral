package background

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/internal/board"
)

// The pieces of a notification pass (#4599 second ranking, item 1: split out of runPassWith, behaviour unchanged).

func TestNudgeDue(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	window := 15 * time.Minute
	recent := notifyState{count: 2, latestID: 10, at: now.Add(-time.Minute)}
	old := notifyState{count: 2, latestID: 10, at: now.Add(-20 * time.Minute)}
	worker := &board.Subscriber{ReceiveMode: "mentions"}
	all := &board.Subscriber{ReceiveMode: "all"}
	readPast := &board.Subscriber{ReceiveMode: "mentions", LastReadID: 10}
	for _, c := range []struct {
		name     string
		last     notifyState
		notified bool
		sub      *board.Subscriber
		unread   int
		latestID int64
		want     bool
	}{
		{"never nudged", notifyState{}, false, worker, 1, 5, true},
		{"nudged moments ago, same batch", recent, true, worker, 2, 10, false},
		{"nudged moments ago, a worker's batch grew: still one nudge per batch", recent, true, worker, 5, 12, false},
		{"the reminder is due", old, true, worker, 2, 10, true},
		{"exactly at the reminder window: due", notifyState{count: 2, latestID: 10, at: now.Add(-window)}, true, worker, 2, 10, true},
		{"a second before the window: not yet", notifyState{count: 2, latestID: 10, at: now.Add(-window + time.Second)}, true, worker, 2, 10, false},
		{"all-messages: a newer message is a new batch", recent, true, all, 2, 11, true},
		{"all-messages: more unread is new activity", recent, true, all, 3, 10, true},
		{"all-messages: the same batch", recent, true, all, 2, 10, false},
		{"read past the nudged batch: the new ones are a new batch (#3480)", recent, true, readPast, 1, 11, true},
		{"read past, nothing newer", recent, true, readPast, 1, 10, false},
	} {
		assert.Equal(t, c.want, nudgeDue(c.last, c.notified, c.sub, c.unread, c.latestID, now, window), c.name)
	}
}

func TestNudgeText(t *testing.T) {
	assert.Equal(t, "You have 1 unread message on the message board. Run 'coral-board read' to see them.", nudgeText("t", 1, false))
	assert.Equal(t, "You have 3 unread messages on the message board. Run 'coral-board read' to see them.", nudgeText("t", 3, false))
	assert.Equal(t, "[Coral health check] Team health check for team-a: 2 unread messages include a health-monitor summary. Review team status, assignments, progress, and blockers, then run 'coral-board read' to see the authoritative full content.", nudgeText("team-a", 2, true))
}
