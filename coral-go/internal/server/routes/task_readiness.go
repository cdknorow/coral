package routes

import (
	"context"
	"sync"

	"github.com/cdknorow/coral/internal/safego"
)

// taskReadiness runs the check "did this task's finish unblock others" at most once at a time for a task id (#4602, L5). Each
// update used to start its own goroutine, so a client that updated one task in a loop started as many at once, each asking the
// store the same question. A request that arrives while one runs for the same task does not start another: it asks for one
// more run after the current one, so an update that lands mid-check is still seen, and any number of requests in that time are
// one more run, not one each.
type taskReadiness struct {
	mu      sync.Mutex
	running map[int64]*readinessRun
	// run is the check itself; nil is notifyPersonalTaskReadiness (tests replace it).
	run func(ctx context.Context, id int64)
}

type readinessRun struct{ again bool }

// schedule starts the check for taskID in the background unless one runs for it, in which case it asks for one more run.
func (h *SessionsHandler) scheduleTaskReadiness(taskID int64) {
	tr := &h.readiness
	tr.mu.Lock()
	if cur, busy := tr.running[taskID]; busy {
		cur.again = true
		tr.mu.Unlock()
		return
	}
	if tr.running == nil {
		tr.running = map[int64]*readinessRun{}
	}
	cur := &readinessRun{}
	tr.running[taskID] = cur
	tr.mu.Unlock()
	run := tr.run
	if run == nil {
		run = h.notifyPersonalTaskReadiness
	}
	go func() {
		defer safego.Recover("server/routes/task_readiness:schedule")
		// A check that panics ends this loop: the task must not stay marked as running, or no later update would ever run it.
		defer func() {
			tr.mu.Lock()
			if tr.running[taskID] == cur {
				delete(tr.running, taskID)
			}
			tr.mu.Unlock()
		}()
		for {
			run(context.Background(), taskID)
			tr.mu.Lock()
			if !cur.again {
				delete(tr.running, taskID)
				tr.mu.Unlock()
				return
			}
			cur.again = false
			tr.mu.Unlock()
		}
	}()
}
