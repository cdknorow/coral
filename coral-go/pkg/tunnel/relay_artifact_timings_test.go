package tunnel

import (
	"testing"
	"time"
)

// setArtifactTimings changes the artifact request's timings for one test and puts them back after it. They are an atomic pointer
// because a request started by an earlier test can still be reading them (#5148).
func setArtifactTimings(t testing.TB, change func(*artifactTimings)) {
	t.Helper()
	old := artifactCfg.Load()
	n := *old
	change(&n)
	artifactCfg.Store(&n)
	t.Cleanup(func() { artifactCfg.Store(old) })
}

// setArtifactIdle changes the idle time in the middle of a test (the oracle changes it per scenario).
func setArtifactIdle(d time.Duration) {
	n := artifactNow()
	n.idleFor = d
	artifactCfg.Store(&n)
}
