package main

import (
	"sync"

	"github.com/subgentic/coral/internal/tracking"
)

// trayTelemetry is the supervisor's onTelemetry (#5184). The tray's own install and app_opened events used to go out from the tray process, which never
// reads the saved Usage analytics setting (only the server child's startup does), so they went out with it off. Now the tray's tracking is off until the
// child says its setting is on, and then send runs once (a later restart of the child does not count the tray's open twice). An "off", or a child that
// says nothing, sends nothing.
func trayTelemetry(send func()) func(on bool) {
	var once sync.Once
	return func(on bool) {
		if !on {
			return
		}
		once.Do(func() {
			tracking.SetTelemetryEnabled(true)
			send()
		})
	}
}
