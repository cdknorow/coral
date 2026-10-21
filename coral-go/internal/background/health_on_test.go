package background

import "github.com/subgentic/coral/internal/board"

// healthOn is the default health settings with the check on: a team's check is off until it is turned on (#4962).
func healthOn() board.HealthSettings {
	h := board.DefaultHealthSettings()
	h.On = true
	return h
}
