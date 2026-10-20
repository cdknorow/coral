package secrets

import (
	"runtime"
	"runtime/debug"
	"strings"
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

// measureLinear times f over mk(small) and mk(large) and says whether it is linear: no more than 9 times as long (a fast small run is floored at 5 ms) in at least one
// of two measurements, or finished within 150 ms (too quick to read a ratio from).
func measureLinear(mk func(n int) string, f func(string), small, large int) (ds, dl time.Duration, ok bool) {
	text, big := mk(small), mk(large)
	for attempt := 0; attempt < 2; attempt++ {
		ds, dl = bestOfThree(f, text), bestOfThree(f, big)
		if dl <= 150*time.Millisecond || float64(dl)/float64(max(ds, 5*time.Millisecond)) < 9.0 {
			return ds, dl, true
		}
	}
	return ds, dl, false
}

// requireLinear fails when measureLinear says the pass is not linear, or when it takes more than 10 s at all (a floor that only catches a blow-up: a shared
// runner is several times slower than a laptop).
func requireLinear(t *testing.T, name string, mk func(n int) string, f func(string), small, large int) {
	t.Helper()
	ds, dl, ok := measureLinear(mk, f, small, large)
	require.True(t, ok, "%s: %v for %d bytes, %v for %d: more than 9 times as long for 4 times the text", name, ds, small, dl, large)
	require.Less(t, dl, 10*time.Second, "%s: %v for %d bytes", name, dl, large)
}

// The guard still guards: a pass that does work for every 500th byte over the whole text (quadratic) is refused, a single pass is accepted.
func TestTheLinearGuardRefusesAQuadraticPassAndAcceptsALinearOne(t *testing.T) {
	mk := func(n int) string { return strings.Repeat("a", n) }
	var sink byte
	quadratic := func(text string) {
		for i := 0; i < len(text); i += 500 {
			for j := 0; j < len(text); j++ {
				sink += text[j]
			}
		}
	}
	linear := func(text string) {
		for j := 0; j < len(text); j++ {
			sink += text[j]
		}
	}
	_, _, ok := measureLinear(mk, quadratic, 100_000, 400_000)
	require.False(t, ok, "a quadratic pass must fail the guard")
	_, _, ok = measureLinear(mk, linear, 100_000, 400_000)
	require.True(t, ok, "a single pass must pass it")
	_ = sink
}
