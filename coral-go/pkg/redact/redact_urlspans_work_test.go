package redact

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The work of URLPasswordSpans, counted, not timed (#4908). A timing ratio of 1 MiB against 4 MiB failed under a load of 32 to 54 (713 ms for
// the large run against a limit of 38): a preempted run costs scheduler slices a short one escapes, so a clock cannot tell a load from a quadratic
// finder. The bytes the finder examines can: for a line of "://", of a user with no password, of an empty user and of whole URLs, the count is
// a small constant times the text, and 4 times the text is 4 times the count. A finder whose password run does not stop at the next "://" (each
// URL then scans up to 1 KiB) or one that rescans from each separator counts hundreds of times the text and fails this at once.
func urlWork(text string) (spans, work int) {
	n := 0
	urlScanned = &n
	defer func() { urlScanned = nil }()
	return len(URLPasswordSpans(text)), n
}

func TestURLPasswordSpansWorkIsLinear(t *testing.T) {
	for _, unit := range []string{"://", "a://u:", "x://:", "://u:p@", "https://u:pw@h/p ", "://u:" + strings.Repeat("p", 40) + "@h "} {
		small, large := strings.Repeat(unit, (1<<18)/len(unit)), strings.Repeat(unit, (1<<20)/len(unit))
		_, ws := urlWork(small)
		sp, wl := urlWork(large)
		require.NotZero(t, ws, "%q: the finder did some work", unit)
		assert.LessOrEqual(t, wl, 3*len(large), "%q: the finder examines at most 3 bytes per byte of text, it examined %d for %d", unit, wl, len(large))
		assert.LessOrEqual(t, float64(wl), 4.2*float64(ws), "%q: 4x the text is 4x the work (%d against %d)", unit, wl, ws)
		if strings.Contains(unit, "pw@") || strings.Contains(unit, "p@") || strings.Contains(unit, "@h") {
			assert.Positive(t, sp, "%q: the passwords are found", unit)
		}
	}
}

// A huge line finishes: the work count cannot see a cost outside the helpers (a regex, an extra scan), so the clock is a ceiling only, 20 s for 16 MiB
// of each shape against about 0.2 s: a load does not reach it, a finder that is quadratic in the line never ends.
func TestURLPasswordSpansFinishOnAHugeLine(t *testing.T) {
	for _, unit := range []string{"://", "a://u:", "://u:p@"} {
		text := strings.Repeat(unit, (1<<24)/len(unit))
		done := make(chan struct{})
		go func() { URLPasswordSpans(text); close(done) }()
		select {
		case <-done:
		case <-timeAfter20s():
			t.Fatalf("%q: 16 MiB took more than 20 s", unit)
		}
	}
}

func timeAfter20s() <-chan time.Time { return time.After(20 * time.Second) }
