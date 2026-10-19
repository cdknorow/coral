package tracking

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// activityEvents are the server-side events that mean the install is being used, so each also counts the
// week as active (#4935). A Coral that runs for weeks sends app_opened only when it starts.
var activityEvents = map[string]bool{
	EventSessionLaunched: true,
	EventTeamLaunched:    true,
	EventTaskCompleted:   true,
}

// activeWeekNow is the clock trackActiveWeek reads; a test moves it to the next week.
var activeWeekNow = time.Now

var (
	activeWeekMu   sync.Mutex
	activeWeekSent string // the ISO week the service last accepted, so a launch costs no disk read once it is sent
)

// resetActiveWeek forgets the in-memory "already sent" week, as a new process would; tests that start from fresh state call it.
func resetActiveWeek() {
	activeWeekMu.Lock()
	activeWeekSent = ""
	activeWeekMu.Unlock()
}

// isoWeek names the ISO week of t in UTC, such as "2026-W43".
func isoWeek(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

// trackActiveWeek sends installation_active_week at most once per install per ISO week, deduplicated the way
// dashboard_active_day is: the week is marked sent only after the service accepts the event, under a uuid that
// is stable for the install and week. It carries the standard properties only.
func trackActiveWeek() {
	gen := telemetryGen.Load()
	if !stateDirReady() || !posthogKeyPresent() || !telemetryValid(gen) {
		return
	}
	week := isoWeek(activeWeekNow())
	activeWeekMu.Lock()
	done := activeWeekSent == week
	activeWeekMu.Unlock()
	if done {
		return
	}
	name := EventInstallationActiveWeek + "|" + week
	send, snap := reserveMilestone(name, true, nil)
	if !send {
		return
	}
	eventUUID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("coral-active-week|"+getInstallID()+"|"+week+"|"+snap.Timestamp)).String()
	if emitEvent(EventInstallationActiveWeek, nil, eventUUID, snap.Timestamp, gen) {
		confirmMilestoneSent(name)
		activeWeekMu.Lock()
		activeWeekSent = week
		activeWeekMu.Unlock()
		pruneDatedMilestones(EventInstallationActiveWeek, week)
		return
	}
	releaseMilestone(name)
}
