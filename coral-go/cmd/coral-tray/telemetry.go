package main

import (
	"sync"

	"github.com/subgentic/coral/internal/tracking"
)

// trayTelemetry is the supervisor's onTelemetry (#5184). The tray's own install and app_opened events used to go out from the tray process, which never
// reads the saved Usage analytics setting (only the server child's startup does), so they went out with it off. Now the tray's tracking is off until the
// child says its setting is on, and then send runs once (a later restart of the child does not count the tray's open twice). An "off", or a child that
// says nothing, sends nothing; an "off" after an "on" (the setting switched off in Settings while the tray runs) turns the tray's tracking off again and
// drops what it had queued, and a later "on" turns it back on without a second open.
func trayTelemetry(send func()) func(on bool) {
	var once sync.Once
	return func(on bool) {
		if !on {
			tracking.SetTelemetryEnabled(false)
			return
		}
		tracking.SetTelemetryEnabled(true)
		once.Do(send)
	}
}
