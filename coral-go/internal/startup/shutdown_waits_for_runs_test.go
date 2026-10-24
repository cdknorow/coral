package startup

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// closeSpy is a terminal backend that only records that it was closed, and whether a run goroutine was still going when it was.
type closeSpy struct {
	ptymanager.TerminalBackend
	runStillGoing      func() bool
	closedWhileRunning atomic.Bool
	closed             atomic.Bool
}

func (c *closeSpy) Close() error {
	c.closedWhileRunning.Store(c.runStillGoing())
	c.closed.Store(true)
	return nil
}

// Shutdown waits for the job scheduler's run goroutines before it closes the backend (#6918, #6927: Security's #6930 point 5, QA's LOW): a run goroutine never acts on a closed backend, and the wait is a call
// Shutdown makes, which nothing else pinned.
func TestShutdownWaitsForRunGoroutinesBeforeClosingTheBackend(t *testing.T) {
	db, err := store.OpenWithKey(context.Background(), filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	sched := background.NewJobScheduler(store.NewScheduleStore(db), time.Minute)

	var running, entered atomic.Bool
	sched.SetLaunchFn(func(ctx context.Context, _ store.ScheduledJob, _ int64) error {
		running.Store(true)
		entered.Store(true)
		<-ctx.Done()                       // Coral stops
		time.Sleep(300 * time.Millisecond) // sleep-ok: a run goroutine still busy when the server starts to shut down
		running.Store(false)
		return errors.New("cancelled by shutdown")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)
	time.Sleep(200 * time.Millisecond) // sleep-ok: Run records its context before a run starts
	_, err = sched.FireOneshot(ctx, map[string]interface{}{"prompt": "p", "agent_type": "claude", "max_duration_s": float64(60)})
	require.NoError(t, err)
	require.Eventually(t, entered.Load, 5*time.Second, 10*time.Millisecond)

	spy := &closeSpy{runStillGoing: running.Load}
	rs := &RunningServer{HTTPServer: &http.Server{}, Backend: spy, scheduler: sched}
	cancel() // as in coral, coral-tray and launch-coral: the context ends, then Shutdown
	rs.Shutdown(10 * time.Second)

	require.True(t, spy.closed.Load(), "the backend was closed")
	assert.False(t, spy.closedWhileRunning.Load(), "after the run goroutine had returned")
}
