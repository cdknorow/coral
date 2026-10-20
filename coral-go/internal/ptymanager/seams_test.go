package ptymanager

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

// setWaits replaces the gate's and the prompt's waits for the test: f edits a copy of the ones in force, and the cleanup puts them back.
// Race-free against a goroutine an earlier test left running (see waitTimings). The gate's retry loop is stopped before they are put
// back, so the loop of this test does not outlive its timings into the next test.
func setWaits(t *testing.T, f func(w *waitTimings)) {
	t.Helper()
	old := waitsP.Load()
	n := *old
	f(&n)
	waitsP.Store(&n)
	t.Cleanup(func() {
		StopGate(5 * time.Second)
		waitsP.Store(old)
	})
}

// setNow replaces the gate's clock for the test (the cleanup puts the old one back, after the retry loop is stopped).
func setNow(t *testing.T, g *inputGate, f func() time.Time) {
	t.Helper()
	old := g.nowFn.Load()
	g.nowFn.Store(&f)
	t.Cleanup(func() {
		if g == gate {
			StopGate(5 * time.Second)
		}
		g.nowFn.Store(old)
	})
}

// setSpacer swaps the process's spacer for the test; the cleanup puts the old one back.
func setSpacer(t *testing.T, s *teamSpacer) {
	t.Helper()
	old := spacerP.Load()
	spacerP.Store(s)
	t.Cleanup(func() { spacerP.Store(old) })
}

// logBuf is a log destination a test reads while the gate's loop, or a goroutine of an earlier test, may still write to it: a bytes.Buffer
// alone is a data race under -race (#5266). Use it, not &bytes.Buffer, for log.SetOutput and a slog handler (the census checks).
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
