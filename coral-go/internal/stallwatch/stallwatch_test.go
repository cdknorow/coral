package stallwatch

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secfile/secfiletest"
)

// a log output that stops reading, like the terminal in #3226.
// stuckWriter blocks every write until release is closed; entered (when set) is told that a
// write has started.
type stuckWriter struct{ release, entered chan struct{} }

func (s stuckWriter) Write(p []byte) (int, error) {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	<-s.release
	return len(p), nil
}

type clock struct{ t atomic.Int64 }

func (c *clock) now() time.Time      { return time.Unix(0, c.t.Load()) }
func (c *clock) add(d time.Duration) { c.t.Add(int64(d)) }
func newClock() *clock {
	c := &clock{}
	c.t.Store(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func dumpsIn(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, FilePrefix+"*.txt"))
	require.NoError(t, err)
	return m
}

// #3226 repro: a log write blocks (the terminal stopped reading), so every log call in the
// server would wait. The logger probe gets stuck too, and past the threshold the watchdog
// writes a goroutine dump that names the logger and shows the blocked write, without
// itself going through the logger.
func TestDumpWhenTheLoggerIsStuck(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	prev := log.Writer()
	entered := make(chan struct{}, 1)
	log.SetOutput(stuckWriter{release: release, entered: entered})
	defer func() {
		close(release)
		log.SetOutput(prev)
	}()
	go log.Print("this write never finishes") // holds the logger, as the stalled terminal did
	// The write must be blocked before the dump, or the dump can't show it: testify 1.12's
	// Eventually checks at once, without the tick that used to give this goroutine its turn.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the log write never started")
	}

	c := newClock()
	w := New(Config{Dir: dir, Threshold: 30 * time.Second, Probes: []Probe{LoggerProbe()}, now: c.now})
	require.Eventually(t, func() bool { return w.Tick(context.Background()) == "" }, time.Second, 10*time.Millisecond)
	c.add(10 * time.Second)
	assert.Empty(t, w.Tick(context.Background()), "not stuck long enough yet")
	c.add(25 * time.Second)
	path := w.Tick(context.Background())
	require.NotEmpty(t, path, "a dump after the threshold")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(b)
	assert.Contains(t, text, "Stuck: logger (no answer for 35s)")
	assert.Contains(t, text, "stuckWriter.Write", "the blocked write is in the dump")
	assert.Contains(t, text, "goroutine ", "full stacks")
	secfiletest.AssertPrivate(t, path)

	// One dump per stall: still stuck, no new dump.
	c.add(15 * time.Minute)
	assert.Empty(t, w.Tick(context.Background()))
	assert.Len(t, dumpsIn(t, dir), 1)
}

// Healthy probes never dump; a stuck probe is not started again (no pile-up); a new stall
// after recovery dumps again, but never closer than MinGap; only Keep dumps are kept.
func TestStallWatchRules(t *testing.T) {
	dir := t.TempDir()
	c := newClock()
	var healthyRuns, stuckRuns atomic.Int32
	var block atomic.Pointer[chan struct{}]
	first := make(chan struct{})
	block.Store(&first)
	stuck := atomic.Bool{}
	w := New(Config{Dir: dir, Threshold: 30 * time.Second, MinGap: 10 * time.Minute, Keep: 2, now: c.now, Probes: []Probe{
		{Name: "healthy", Check: func(context.Context) error { healthyRuns.Add(1); return nil }},
		{Name: "flaky", Check: func(context.Context) error {
			stuckRuns.Add(1)
			if stuck.Load() {
				<-*block.Load()
			}
			return nil
		}},
	}})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		assert.Empty(t, w.Tick(ctx))
		c.add(10 * time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, healthyRuns.Load(), int32(4))
	assert.Empty(t, dumpsIn(t, dir), "healthy: no dump")

	stall := func() string {
		stuck.Store(true)
		before := stuckRuns.Load()
		require.Eventually(t, func() bool { w.Tick(ctx); return stuckRuns.Load() > before }, time.Second, 10*time.Millisecond)
		n := stuckRuns.Load()
		var path string
		for i := 0; i < 5 && path == ""; i++ {
			c.add(10 * time.Second)
			path = w.Tick(ctx)
		}
		assert.Equal(t, n, stuckRuns.Load(), "a stuck probe is not started again")
		return path
	}
	recoverIt := func() {
		stuck.Store(false)
		next := make(chan struct{})
		close(*block.Swap(&next))
		time.Sleep(50 * time.Millisecond)
	}
	require.NotEmpty(t, stall())
	recoverIt()
	// Stuck again within MinGap: no dump (skipped, and not retried for this stall).
	assert.Empty(t, stall())
	recoverIt()
	c.add(11 * time.Minute)
	require.NotEmpty(t, stall(), "after MinGap a new stall dumps again")
	recoverIt()
	c.add(11 * time.Minute)
	require.NotEmpty(t, stall())
	recoverIt()
	assert.Len(t, dumpsIn(t, dir), 2, "only the newest Keep dumps")
	assert.Equal(t, 3, w.Dumps())
	for _, p := range dumpsIn(t, dir) {
		assert.True(t, strings.HasPrefix(filepath.Base(p), FilePrefix))
	}
}
