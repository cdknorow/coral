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

// tooQuick is the run time under which the real guards read no ratio from a run: a few milliseconds of timer and cache noise would decide it.
const (
	tooQuick  = 150 * time.Millisecond
	smallBase = 5 * time.Millisecond // a faster small run is read as this long, so a few milliseconds of noise cannot make the ratio
)

// measureLinear times f over mk(small) and mk(large) and says whether it is linear: no more than 9 times as long (a fast small run is floored at 5 ms) in at least one
// of two measurements, or finished within quick (a run too quick to read a ratio from). The real guards pass tooQuick and smallBase; a test of the guard itself passes 0 and
// 1 microsecond, so that its verdict never depends on how fast the machine is.
func measureLinear(mk func(n int) string, f func(string), small, large int, quick, base time.Duration) (ds, dl time.Duration, ok bool) {
	text, big := mk(small), mk(large)
	for attempt := 0; attempt < 2; attempt++ {
		ds, dl = bestOfThree(f, text), bestOfThree(f, big)
		if dl <= quick || float64(dl)/float64(max(ds, base)) < 9.0 {
			return ds, dl, true
		}
	}
	return ds, dl, false
}

// requireLinear fails when measureLinear says the pass is not linear, or when it takes more than 10 s at all (a floor that only catches a blow-up: a shared
// runner is several times slower than a laptop).
func requireLinear(t *testing.T, name string, mk func(n int) string, f func(string), small, large int) {
	t.Helper()
	ds, dl, ok := measureLinear(mk, f, small, large, tooQuick, smallBase)
	require.True(t, ok, "%s: %v for %d bytes, %v for %d: more than 9 times as long for 4 times the text", name, ds, small, dl, large)
	require.Less(t, dl, 10*time.Second, "%s: %v for %d bytes", name, dl, large)
}

// The guard still guards: a pass that does work for every 50th byte over the whole text (quadratic) is refused, a single pass of the same weight is accepted. The "too
// quick" shortcut and the 5 ms floor are off (0 and 1 microsecond), so the ratio is always read and the verdict does not depend on how fast the machine is (a quadratic
// stand-in under the 150 ms shortcut was accepted on a fast runner, 132 gate); the input is small, so the test takes well under a second.
func TestTheLinearGuardRefusesAQuadraticPassAndAcceptsALinearOne(t *testing.T) {
	mk := func(n int) string { return strings.Repeat("a", n) }
	var sink byte
	quadratic := func(text string) {
		for i := 0; i < len(text); i += 50 {
			for j := 0; j < len(text); j++ {
				sink += text[j]
			}
		}
	}
	linear := func(text string) { // as many additions as the quadratic one makes at 20 KB, but in proportion to the length
		for rep := 0; rep < 400; rep++ {
			for j := 0; j < len(text); j++ {
				sink += text[j]
			}
		}
	}
	_, _, ok := measureLinear(mk, quadratic, 20_000, 80_000, 0, time.Microsecond)
	require.False(t, ok, "a quadratic pass must fail the guard")
	_, _, ok = measureLinear(mk, linear, 20_000, 80_000, 0, time.Microsecond)
	require.True(t, ok, "a single pass must pass it")
	_ = sink
}
