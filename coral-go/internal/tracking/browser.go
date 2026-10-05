package tracking

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// BrowserEvents is the closed set of events the dashboard may report through
// POST /api/tracking/event. Server lifecycle events (launches, tasks, first_*
// milestones) are emitted only by the server and are rejected there.
var BrowserEvents = map[string]bool{
	EventSupporterCheckoutClicked: true,
	EventDashboardReady:           true,
	EventDashboardActiveDay:       true,
	EventDashboardFailed:          true,
	EventPromptSubmitRequested:    true,
}

const (
	// maxBrowserKeys bounds the in-memory dedupe sets so a misbehaving page
	// cannot grow them without limit.
	maxBrowserKeys = 1000
	// maxFailuresPerCode caps dashboard_failed per code per process run.
	maxFailuresPerCode = 5
)

var (
	browserMu       sync.Mutex
	seenPages       = map[string]bool{}
	failuresPerCode = map[string]int{}
)

// TrackBrowserEvent records an event reported by the dashboard, applying the
// server-side dedupe each event needs. The caller has already restricted event
// to BrowserEvents. Non-blocking.
//
//   - dashboard_ready: once per (run, page_id), so reloads and new tabs count
//     but a page cannot repeat itself. A missing page_id is dropped.
//   - dashboard_active_day: once per install per UTC day, retried until the
//     service accepts it.
//   - dashboard_failed: at most maxFailuresPerCode per code per run.
//   - prompt_submit_requested: once per install (a milestone; opt-out and
//     delivery failure do not consume it).
//   - supporter_checkout_clicked: every click.
func TrackBrowserEvent(event string, props map[string]string) {
	if !BrowserEvents[event] {
		return
	}
	// Dedupe state is consumed only when an event could actually be sent: with
	// telemetry off, or on a keyless build, nothing is counted or remembered.
	if event != EventDashboardActiveDay && event != EventPromptSubmitRequested && !(posthogKeyPresent() && telemetryEnabled()) {
		return
	}
	switch event {
	case EventDashboardReady:
		page := props["page_id"]
		if !slugRE.MatchString(page) || !firstSightingOfPage(page) {
			return
		}
		TrackEvent(event, map[string]string{"page_id": page})
	case EventDashboardActiveDay:
		asyncGo(trackActiveDay)
	case EventDashboardFailed:
		code := props["code"]
		if !allowFailure(code) {
			return
		}
		TrackEvent(event, map[string]string{"code": code})
	case EventPromptSubmitRequested:
		TrackOnce(event, map[string]string{"source": props["source"]})
	default:
		TrackEvent(event, props)
	}
}

func firstSightingOfPage(page string) bool {
	browserMu.Lock()
	defer browserMu.Unlock()
	if seenPages[page] {
		return false
	}
	if len(seenPages) >= maxBrowserKeys {
		return false // bounded: stop counting rather than grow
	}
	seenPages[page] = true
	return true
}

func allowFailure(code string) bool {
	if _, ok := eventSpecs[EventDashboardFailed]["code"]; !ok {
		return false
	}
	valid := false
	for _, v := range eventSpecs[EventDashboardFailed]["code"].enum {
		if v == code {
			valid = true
		}
	}
	if !valid {
		return false
	}
	browserMu.Lock()
	defer browserMu.Unlock()
	if failuresPerCode[code] >= maxFailuresPerCode {
		return false
	}
	failuresPerCode[code]++
	return true
}

// trackActiveDay sends dashboard_active_day at most once per install per UTC
// day. The day is marked sent only after the service accepts the event, under
// a uuid that is stable for the install and day.
func trackActiveDay() {
	gen := telemetryGen.Load()
	if !stateDirReady() || !posthogKeyPresent() || !telemetryValid(gen) {
		return
	}
	day := time.Now().UTC().Format("2006-01-02")
	name := EventDashboardActiveDay + "|" + day
	send, snap := reserveMilestone(name, true, nil)
	if !send {
		return
	}
	eventUUID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("coral-active-day|"+getInstallID()+"|"+day+"|"+snap.Timestamp)).String()
	if emitEvent(EventDashboardActiveDay, nil, eventUUID, snap.Timestamp, gen) {
		confirmMilestoneSent(name)
		pruneActiveDays(day)
		return
	}
	releaseMilestone(name)
}

// pruneActiveDays keeps the milestone file small: only today's active-day
// record is needed to dedupe, so older ones are removed.
func pruneActiveDays(today string) {
	milestoneMu.Lock()
	defer milestoneMu.Unlock()
	s := loadMilestones()
	changed := false
	stale := func(k string) bool {
		return len(k) > len(EventDashboardActiveDay)+1 && k[:len(EventDashboardActiveDay)+1] == EventDashboardActiveDay+"|" && k != EventDashboardActiveDay+"|"+today
	}
	for _, m := range []map[string]string{s.Fired, s.Reached} {
		for k := range m {
			if stale(k) {
				delete(m, k)
				changed = true
			}
		}
	}
	for k := range s.Pending {
		if stale(k) {
			delete(s.Pending, k)
			changed = true
		}
	}
	if changed {
		_ = saveMilestones(s)
	}
}
