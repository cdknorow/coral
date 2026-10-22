//go:build !windows

package ptymanager

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/process"

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
func beginTreeKill(paneOut string) (finish func()) {
	pid, err := strconv.Atoi(strings.TrimSpace(paneOut))
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
	known := treeMembers(root, "") // found now, while they are still its descendants; signalled only after tmux's own kill, which would otherwise find its session already ended by a signal and fail
	return func() {
		// In the background: the person's Kill, sleep or Restart is answered at once and the tree ends while the dashboard shows the result, not after the grace.
		safego.Go("ptymanager/tmux-tree", func() {
			signalTree(known, syscall.SIGHUP) // what closing the terminal does, then the polite stop
			signalTree(known, syscall.SIGTERM)
			deadline := time.Now().Add(tmuxTreeGrace)
			for anyAlive(known) && time.Now().Before(deadline) {
				time.Sleep(25 * time.Millisecond)
			}
			if !anyAlive(known) {
				return
			}
			left := killTree(known, root, "")
			for end := time.Now().Add(time.Second); anyAlive(left) && time.Now().Before(end); {
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// paneTreeForAgent starts the tree kill of an agent's pane (a no-op when the pane cannot be found).
func paneTreeForAgent(ctx context.Context, c *tmux.Client, name, agentType, sessionID string) func() {
	out, err := c.DisplayForAgent(ctx, name, agentType, sessionID, "#{pane_pid}")
	if err != nil {
		return func() {}
	}
	return beginTreeKill(out)
}

// paneTreeForTarget starts the tree kill of the pane at a tmux target.
func paneTreeForTarget(ctx context.Context, c *tmux.Client, target string) func() {
	out, err := c.DisplayMessage(ctx, target, "#{pane_pid}")
	if err != nil {
		return func() {}
	}
	return beginTreeKill(out)
}
