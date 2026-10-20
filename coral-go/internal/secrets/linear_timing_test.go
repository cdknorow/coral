package secrets

import (
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The linear-time guards (#5018, #5242) compare a run over 250 KB with one over 1 MB. A linear pass makes the ratio about 4 and a quadratic one about 16, so the
// limit is 9, but a single collection landing in the one 1 MB run was enough to read 10.02 (16.6 ms and 166 ms on a shared runner, #131 gate): a noisy sample, not a
// slow pass. So each size is timed with the collector off and swept first, the fastest of three, and a ratio over the limit is measured once more before it fails: a
// pass that really is quadratic fails twice, a noisy sample does not.

// bestOfThree is the fastest of three runs of f over text, with garbage collection paused (and run beforehand) so a collection cannot land in the timing.
func bestOfThree(f func(string), text string) time.Duration {
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)
	best := time.Hour
	for i := 0; i < 3; i++ {
		runtime.GC()
		start := time.Now()
		f(text)
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

// requireLinear fails when f over mk(large) takes more than 9 times f over mk(small) (a fast small run is floored at 5 ms), in two measurements in a row,
// or more than 10 s at all (a floor that only catches a blow-up: a shared runner is several times slower than a laptop).
func requireLinear(t *testing.T, name string, mk func(n int) string, f func(string), small, large int) {
	t.Helper()
	text, big := mk(small), mk(large)
	var ds, dl time.Duration
	for attempt := 0; attempt < 2; attempt++ {
		ds, dl = bestOfThree(f, text), bestOfThree(f, big)
		if dl <= 150*time.Millisecond || float64(dl)/float64(max(ds, 5*time.Millisecond)) < 9.0 {
			break
		}
	}
	if dl > 150*time.Millisecond {
		require.Less(t, float64(dl)/float64(max(ds, 5*time.Millisecond)), 9.0, "%s: %v for %d bytes, %v for %d", name, ds, small, dl, large)
	}
	require.Less(t, dl, 10*time.Second, "%s: %v for %d bytes", name, dl, large)
}
