//go:build race

package routes

// raceEnabled: the race detector slows what a call does several times over, so a bound on the clock is widened under it (raceSlowdown); the bounds that do not depend on the clock are not.
const raceEnabled = true
