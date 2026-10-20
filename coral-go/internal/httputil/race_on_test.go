//go:build race

package httputil

// raceEnabled: the race detector inflates what a call allocates, so an allocation bound is not measured under it.
const raceEnabled = true
