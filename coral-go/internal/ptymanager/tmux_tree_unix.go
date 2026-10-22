//go:build !windows

package ptymanager

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/subgentic/coral/internal/logparse"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/tmux"
)

// tmux only hangs up the pane's own process group when a session is killed or a pane respawned, so an agent in a group of its own (job control) and the helpers it started in sessions of their own were left
// running, reparented to PID 1 (#6141: checked with a real tmux and real children). The same as the built-in terminal had before #6125, and fixed the same way: the tree of the pane's shell is found before the
// kill (pid and start time, the same exclusions), sent the polite stop with the kill, and what is still there after a short grace is killed.

// tmuxTreeGrace is how long what the agent started is given to end on the hangup and SIGTERM before it is killed.
const tmuxTreeGrace = 3 * time.Second

// beginTreeKill finds the tree of the pane whose pid is paneOut (the answer of tmux's #{pane_pid}) and finds its members; the returned function, called after tmux's own kill, sends them the stop, waits for them to end
// within the grace and kills what is left. A pane whose pid cannot be read is a pane with nothing to do: the function does nothing.
//
// paneOut is tmux's "#{pane_pid} #{session_name}": the session's name says which agent it is, and so which tag its processes carry (RunTagLookup); sessionID, when the caller knows it, is used instead.
func beginTreeKill(paneOut, sessionID string) (finish func()) {
	fields := strings.Fields(paneOut)
	if len(fields) == 0 {
		slog.Debug("[tmux] the pane's pid is not known: nothing of its tree is ended", "answer", strings.TrimSpace(paneOut))
		return func() {}
	}
	pid, err := strconv.Atoi(fields[0])
	if sessionID == "" && len(fields) > 1 {
		_, sessionID = logparse.ParseSessionName(fields[1])
	}
	if err != nil || pid <= 1 {
		slog.Debug("[tmux] the pane's pid is not known: nothing of its tree is ended", "answer", strings.TrimSpace(paneOut))
		return func() {}
	}
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		slog.Debug("[tmux] the pane's process is gone before its kill: nothing of its tree is ended", "pid", pid, "error", err)
		return func() {}
	}
	created, err := p.CreateTime()
	if err != nil {
		slog.Debug("[tmux] the pane's start time cannot be read: nothing of its tree is ended", "pid", pid, "error", err)
		return func() {}
	}
	root := member{pid, created}
	tag := ""
	if f := runTagLookup.Load(); f != nil && sessionID != "" {
		tag = (*f)(sessionID) // "" for an agent started before the tag existed: descent only, as before
	}
	known := treeMembers(root, tag) // found now, while they are still its descendants; signalled only after tmux's own kill, which would otherwise find its session already ended by a signal and fail
	return func() {
		// In the background: the person's Kill, sleep or Restart is answered at once and the tree ends while the dashboard shows the result, not after the grace. It is registered while it runs, so a Coral that quits
		// meanwhile can finish it (finishPendingTreeKills) instead of leaving the helpers running.
		job := &treeKillJob{known: known, root: root, done: make(chan struct{})}
		pendingTreeKills.add(job)
		safego.Go("ptymanager/tmux-tree", func() {
			defer func() { pendingTreeKills.remove(job); close(job.done) }()
			signalTree(known, syscall.SIGHUP) // what closing the terminal does, then the polite stop
			signalTree(known, syscall.SIGTERM)
			deadline := time.Now().Add(tmuxTreeGrace)
			for anyAlive(known) && time.Now().Before(deadline) {
				time.Sleep(25 * time.Millisecond)
			}
			if !anyAlive(known) {
				return
			}
			left := killTree(known, root, tag)
			for end := time.Now().Add(time.Second); anyAlive(left) && time.Now().Before(end); {
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// runTagLookup finds the tag a tmux agent's processes carry (the sessions database's live_sessions.run_tag), set once at startup by SetRunTagLookup.
var runTagLookup atomic.Pointer[func(sessionID string) string]

// SetRunTagLookup tells the tmux Kill, sleep and Restart where to find an agent's tag by its session id (#6141).
func SetRunTagLookup(f func(sessionID string) string) { runTagLookup.Store(&f) }

// treeKillJob is one tmux tree kill that is waiting out its grace in the background.
type treeKillJob struct {
	known []member
	root  member
	done  chan struct{}
}

var pendingTreeKills pendingJobs

type pendingJobs struct {
	mu   sync.Mutex
	jobs map[*treeKillJob]struct{}
}

func (p *pendingJobs) add(j *treeKillJob) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.jobs == nil {
		p.jobs = map[*treeKillJob]struct{}{}
	}
	p.jobs[j] = struct{}{}
}

func (p *pendingJobs) remove(j *treeKillJob) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.jobs, j)
}

func (p *pendingJobs) snapshot() []*treeKillJob {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*treeKillJob, 0, len(p.jobs))
	for j := range p.jobs {
		out = append(out, j)
	}
	return out
}

// finishPendingTreeKills is for a Coral that is quitting: a tree kill still waiting out its grace would die with the process and leave the helpers running, so each pending one is finished now with the
// same stop, sweep and SIGKILL, and waited for, within a second.
func finishPendingTreeKills() {
	for _, j := range pendingTreeKills.snapshot() {
		killTree(j.known, j.root, "")
		select {
		case <-j.done:
		case <-time.After(time.Second):
		}
	}
}

// paneTreeForAgent starts the tree kill of an agent's pane (a no-op when the pane cannot be found).
func paneTreeForAgent(ctx context.Context, c *tmux.Client, name, agentType, sessionID string) func() {
	out, err := c.DisplayForAgent(ctx, name, agentType, sessionID, "#{pane_pid} #{session_name}")
	if err != nil {
		return func() {}
	}
	return beginTreeKill(out, sessionID)
}

// paneTreeForTarget starts the tree kill of the pane at a tmux target.
func paneTreeForTarget(ctx context.Context, c *tmux.Client, target string) func() {
	out, err := c.DisplayMessage(ctx, target, "#{pane_pid} #{session_name}")
	if err != nil {
		return func() {}
	}
	return beginTreeKill(out, "")
}
