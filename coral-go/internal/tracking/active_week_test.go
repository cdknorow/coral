package tracking

import (
	"os"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// everyKindOfEvent makes the server try to send one of each way an event is built: the install and upgrade
// lifecycle events, app_opened, a plain event, a launch attempt, a one-time milestone, and the
// dashboard's relayed events.
func everyKindOfEvent(t *testing.T) {
	t.Helper()
	browserMu.Lock()
	seenPages, failuresPerCode = map[string]bool{}, map[string]int{} // the dashboard's per-run dedupe belongs to this run
	browserMu.Unlock()
	prevVersion := config.Version
	config.Version = "1.2.3"
	t.Cleanup(func() { config.Version = prevVersion })
	require.NoError(t, os.Remove(coralDir+"/.install_id")) // the first start is a new install
	TrackInstallAsync()                                    // install, app_opened, installation_active_week
	waitForAsync()
	require.NoError(t, writeTestFile(coralDir+"/.install_version", "0.0.1")) // the next start is an upgrade
	TrackInstallAsync()
	waitForAsync()
	TrackEvent(EventSessionLaunched, nil)
	StartLaunch(KindAgent, "claude", "tmux", 1, false).Finish(OutcomeSuccess, "", 1, 0)
	TrackOnce(EventFirstAgentLaunched, nil)
	TrackBrowserEvent(EventDashboardReady, map[string]string{"page_id": "page-1"})
	TrackBrowserEvent(EventDashboardFailed, map[string]string{"code": "init_failed"})
	TrackBrowserEvent(EventDashboardActiveDay, nil)
	TrackBrowserEvent(EventPromptSubmitRequested, map[string]string{"source": "dashboard_composer"})
	waitForAsync()
}

func TestOptOutSendsNothingNotEvenTheActiveWeek(t *testing.T) {
	t.Run("setting", func(t *testing.T) {
		rec, _ := newTestTracking(t)
		SetTelemetryEnabled(false)
		t.Cleanup(func() { SetTelemetryEnabled(true) })
		everyKindOfEvent(t)
		assert.Empty(t, rec.all())
	})
	t.Run("CORAL_TELEMETRY_DISABLED", func(t *testing.T) {
		rec, _ := newTestTracking(t)
		t.Setenv("CORAL_TELEMETRY_DISABLED", "1")
		everyKindOfEvent(t)
		assert.Empty(t, rec.all())
	})
}

// ---- installation_active_week (#4935) ----

func TestActiveWeekIsSentOncePerISOWeek(t *testing.T) {
	rec, _ := newTestTracking(t)
	monday := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC) // 2026-W43
	prev := activeWeekNow
	t.Cleanup(func() { activeWeekNow = prev })
	activeWeekNow = func() time.Time { return monday }

	for i := 0; i < 5; i++ {
		trackActiveWeek()
	}
	assert.Equal(t, 1, rec.count(EventInstallationActiveWeek), "five triggers in one week send one event")

	resetActiveWeek() // a restart: the memory is gone, the saved record still dedupes
	trackActiveWeek()
	assert.Equal(t, 1, rec.count(EventInstallationActiveWeek), "a restart in the same week sends nothing more")

	activeWeekNow = func() time.Time { return monday.Add(6*24*time.Hour + 14*time.Hour) } // Sunday night, still W43
	trackActiveWeek()
	assert.Equal(t, 1, rec.count(EventInstallationActiveWeek))

	activeWeekNow = func() time.Time { return monday.Add(7 * 24 * time.Hour) } // W44
	trackActiveWeek()
	trackActiveWeek()
	assert.Equal(t, 2, rec.count(EventInstallationActiveWeek), "the next ISO week sends one more")
	assert.Equal(t, "2026-W43", isoWeek(monday))
	assert.Equal(t, "2026-W44", isoWeek(monday.Add(7*24*time.Hour)))
	assert.Equal(t, "2026-W53", isoWeek(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC))) // ISO year edges
	assert.Equal(t, "2027-W01", isoWeek(time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC)))

	s := loadMilestones()
	for k := range s.Fired {
		assert.NotEqual(t, EventInstallationActiveWeek+"|2026-W43", k, "the old week's record is pruned")
	}
	assert.Contains(t, s.Fired, EventInstallationActiveWeek+"|2026-W44")
}

func TestActiveWeekCarriesOnlyTheStandardProperties(t *testing.T) {
	rec, _ := newTestTracking(t)
	trackActiveWeek()
	events := rec.all()
	require.Len(t, events, 1)
	assert.Equal(t, "install-under-test", events[0].DistinctID)
	want := standardProps()
	require.Len(t, events[0].Properties, len(want), "no property beyond the standard ones: %v", events[0].Properties)
	for k := range want {
		assert.Contains(t, events[0].Properties, k)
	}
}

func TestActiveWeekIsNotConsumedByAFailedDeliveryOrAnOptOut(t *testing.T) {
	rec, _ := newTestTracking(t)
	prev := retryDelays
	retryDelays = nil
	t.Cleanup(func() { retryDelays = prev })

	rec.mu.Lock()
	rec.status = 500
	rec.mu.Unlock()
	trackActiveWeek()
	rec.mu.Lock()
	rec.status = 200
	rec.mu.Unlock()
	before := rec.count(EventInstallationActiveWeek) // the rejected attempt was recorded by the fake, but not accepted
	trackActiveWeek()
	assert.Equal(t, before+1, rec.count(EventInstallationActiveWeek), "a refused delivery is tried again at the next trigger")
	n := rec.count(EventInstallationActiveWeek)
	trackActiveWeek()
	assert.Equal(t, n, rec.count(EventInstallationActiveWeek), "an accepted one is not repeated")
}

func TestActiveWeekSendsNothingWhenOptedOut(t *testing.T) {
	rec, _ := newTestTracking(t)
	SetTelemetryEnabled(false)
	t.Cleanup(func() { SetTelemetryEnabled(true) })
	trackActiveWeek()
	TrackEvent(EventSessionLaunched, nil)
	TrackBrowserEvent(EventDashboardActiveDay, nil)
	waitForAsync()
	assert.Empty(t, rec.all())
	SetTelemetryEnabled(true)
	trackActiveWeek()
	assert.Equal(t, 1, rec.count(EventInstallationActiveWeek), "opting out did not use up the week")
}

func TestActiveWeekIsCountedByUseNotOnlyByStart(t *testing.T) {
	// A Coral that runs for weeks sends app_opened once. Using it must still count each week.
	for name, use := range map[string]func(){
		"session launched":       func() { TrackEvent(EventSessionLaunched, nil) },
		"team launched":          func() { TrackEvent(EventTeamLaunched, map[string]string{"agent_count": "2"}) },
		"task completed":         func() { TrackTaskCompleted("success") },
		"dashboard opened a day": func() { TrackBrowserEvent(EventDashboardActiveDay, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := newTestTracking(t)
			use()
			use()
			waitForAsync()
			assert.Equal(t, 1, rec.count(EventInstallationActiveWeek))
		})
	}
	t.Run("a dashboard page load alone is not use", func(t *testing.T) {
		rec, _ := newTestTracking(t)
		TrackBrowserEvent(EventDashboardReady, map[string]string{"page_id": "p"})
		waitForAsync()
		assert.Equal(t, 0, rec.count(EventInstallationActiveWeek))
	})
}
