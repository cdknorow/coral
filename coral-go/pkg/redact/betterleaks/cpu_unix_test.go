//go:build !windows

package betterleaks

import (
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/pkg/redact"
)

func cpuSeconds() float64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6 + float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
}

// Security's probe on #3808: callers that give up at the budget leave little work behind. Each
// detector call is given at most a chunk, and scan slots are freed only when betterleaks
// returns, so 16 callers of 1 MiB of keyword-dense text at a 100 ms budget leave a bounded
// amount of CPU (it was 80 CPU-seconds in the 8 s after they returned).
func TestAbandonedScansStayBounded(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("timing bound; not under -race or -short")
	}
	d := detector(t)
	text := []byte(strings.Repeat("password="+strings.Repeat("0", 100)+"\n", (1<<20)/110))
	s := &redact.Scanner{Detector: d, Budget: 100 * time.Millisecond}
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Text(text)
			assert.ErrorIs(t, err, redact.ErrUnchecked)
		}()
	}
	wg.Wait()
	assert.Less(t, time.Since(start), 2*time.Second, "callers return at the budget")
	atReturn := cpuSeconds()
	time.Sleep(3 * time.Second)
	assert.Less(t, cpuSeconds()-atReturn, 5.0, "CPU used by abandoned scans after the callers returned")
}
