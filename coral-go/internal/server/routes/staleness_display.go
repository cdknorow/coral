package routes

import "math"

// listStaleness rounds staleness_seconds down to the unit the dashboard
// displays it in (formatStaleness in render.js: whole seconds under a minute,
// whole minutes under an hour, then whole hours), so the label is unchanged.
// The exact value changed on every tick, so every session differed from the
// previous coral_update and each tick sent a full coral_diff; on the hub it
// also changed every remote's session signature every poll. Rounded, an idle
// agent's entry changes once a minute, then once an hour. State derivation
// keeps using the exact value.
func listStaleness(v any) any {
	f, ok := v.(float64)
	if !ok {
		return v
	}
	switch {
	case f < 60:
		return math.Floor(f)
	case f < 3600:
		return math.Floor(f/60) * 60
	default:
		return math.Floor(f/3600) * 3600
	}
}
