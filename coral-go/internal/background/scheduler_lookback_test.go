package background

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// A daily 09:00 UTC job, evaluated at chosen times. updated is the job's last
// change (creation, edit or pause/resume); lastRun, when set, is a finished
// earlier run. fired reports whether evaluateJob started a run.
func evaluateAt(t *testing.T, updated time.Time, lastRun *time.Time, now time.Time) (fired bool) {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	ss := store.NewScheduleStore(db)
	job, err := ss.CreateScheduledJob(ctx, &store.ScheduledJob{Name: "daily", CronExpr: "0 9 * * *", Timezone: "UTC", RepoPath: "/tmp", Prompt: "p", Enabled: 1})
	require.NoError(t, err)
	stamp := updated.UTC().Format(store.ISOFormat)
	_, err = db.ExecContext(ctx, `UPDATE scheduled_jobs SET created_at = ?, updated_at = ? WHERE id = ?`, stamp, stamp, job.ID)
	require.NoError(t, err)
	if lastRun != nil {
		id, err := ss.CreateScheduledRun(ctx, job.ID, lastRun.UTC().Format(store.ISOFormat), "completed")
		require.NoError(t, err)
		require.NotZero(t, id)
	}
	j, err := ss.GetScheduledJob(ctx, job.ID)
	require.NoError(t, err)

	s := NewJobScheduler(ss, time.Minute) // no launchFn: a due job only records its run
	s.SetNextFireTimeFn(NextFireTime)
	require.NoError(t, s.evaluateJob(ctx, *j, now))
	last, err := ss.GetLastRunForJob(ctx, job.ID)
	require.NoError(t, err)
	return last != nil && (lastRun == nil || last.ScheduledAt != lastRun.UTC().Format(store.ISOFormat))
}

func utcAt(day, hour, min int) time.Time { return time.Date(2026, 10, day, hour, min, 0, 0, time.UTC) }

// A job created after today's slot does not fire it: it waits for the next one.
func TestNewJobDoesNotFireASlotBeforeItExisted(t *testing.T) {
	created := utcAt(13, 10, 0) // today's 09:00 has passed
	assert.False(t, evaluateAt(t, created, nil, utcAt(13, 10, 1)), "created at 10:00: today's 09:00 is not due")
	assert.False(t, evaluateAt(t, created, nil, utcAt(13, 23, 59)), "nor later today")
	assert.True(t, evaluateAt(t, created, nil, utcAt(14, 9, 0).Add(30*time.Second)), "tomorrow's 09:00 fires")
}

// A job created before the slot fires at the slot, as before.
func TestNewJobFiresItsFirstSlot(t *testing.T) {
	created := utcAt(13, 8, 0)
	assert.False(t, evaluateAt(t, created, nil, utcAt(13, 8, 59)), "not before 09:00")
	assert.True(t, evaluateAt(t, created, nil, utcAt(13, 9, 0).Add(30*time.Second)), "09:00 fires")
	// Coral was down at 09:00 and starts at 11:00: the slot it missed after the
	// job existed is still caught up once.
	assert.True(t, evaluateAt(t, created, nil, utcAt(13, 11, 0)), "a slot missed after creation is caught up")
}

// A job that has run keeps the catch-up behaviour: a slot missed while Coral
// was down fires once on the next tick.
func TestJobWithRunsCatchesUpAMissedSlot(t *testing.T) {
	updated := utcAt(10, 12, 0)
	yesterday := utcAt(12, 9, 0)
	assert.True(t, evaluateAt(t, updated, &yesterday, utcAt(13, 11, 0)), "today's missed 09:00 fires once")
	today := utcAt(13, 9, 0)
	assert.False(t, evaluateAt(t, updated, &today, utcAt(13, 11, 0)), "already ran today: nothing due")
}

// Resuming (or editing) a job starts it fresh: the slots it missed while
// paused are not fired; the next slot is.
func TestResumedJobDoesNotBackfill(t *testing.T) {
	ran := utcAt(10, 9, 0)
	resumed := utcAt(13, 10, 0)
	assert.False(t, evaluateAt(t, resumed, &ran, utcAt(13, 10, 5)), "slots missed while paused are not fired")
	assert.True(t, evaluateAt(t, resumed, &ran, utcAt(14, 9, 0).Add(30*time.Second)), "the next slot fires")
}

// The floor itself: the later of the last run (or 24h ago) and the last change.
func TestLookbackStart(t *testing.T) {
	now := utcAt(13, 10, 0)
	iso := func(tm time.Time) string { return tm.UTC().Format(store.ISOFormat) }
	old := store.ScheduledJob{UpdatedAt: iso(utcAt(1, 0, 0))}
	assert.Equal(t, now.Add(-24*time.Hour), lookbackStart(old, nil, now), "an old job without runs looks back 24h")
	fresh := store.ScheduledJob{UpdatedAt: iso(utcAt(13, 9, 30))}
	assert.Equal(t, utcAt(13, 9, 30), lookbackStart(fresh, nil, now), "a new job looks back to its creation")
	run := &store.ScheduledRun{ScheduledAt: iso(utcAt(12, 9, 0))}
	assert.Equal(t, utcAt(12, 9, 0), lookbackStart(old, run, now), "the last run, for a job unchanged since")
	assert.Equal(t, utcAt(13, 9, 30), lookbackStart(fresh, run, now), "a change after the last run wins")
	legacy := store.ScheduledJob{CreatedAt: iso(utcAt(13, 9, 45))}
	assert.Equal(t, utcAt(13, 9, 45), lookbackStart(legacy, nil, now), "created_at when updated_at is empty")
	bad := store.ScheduledJob{UpdatedAt: "garbage"}
	assert.Equal(t, now.Add(-24*time.Hour), lookbackStart(bad, nil, now), "an unreadable stamp does not move the floor")
}
