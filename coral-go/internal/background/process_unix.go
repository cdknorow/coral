//go:build !windows

package background

import (
	"context"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/cdknorow/coral/internal/safego"
)

// configureChildProcess puts cmd in its own process group and makes a context
// cancel or timeout kill that whole group, not only the direct child: by default
// exec.CommandContext kills just the process it started, so `sh -c 'sleep 600 &'`
// left the sleep running after a step's timeout (audit HIGH-03).
func configureChildProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		killChildProcess(cmd)
		return nil
	}
}

// childKillGrace is how long a process group has to end after SIGTERM before it gets SIGKILL.
var childKillGrace = 5 * time.Second

// killEscalations holds the SIGKILL escalations under way (each a channel closed when it ends), so a shutdown can wait for them: Coral exiting before one fires left a step
// that ignores SIGTERM running (#5366). A set and not a sync.WaitGroup: a step's cancel can start an escalation while a shutdown is waiting, and a WaitGroup's Add must not
// race with its Wait.
var (
	killEscalationsMu sync.Mutex
	killEscalations   = map[chan struct{}]struct{}{}
)

// killChildProcess sends SIGTERM to cmd's process group, then SIGKILL to whatever is left of it
// after childKillGrace. The escalation ends as soon as the group is gone.
func killChildProcess(cmd *exec.Cmd) {
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		pgid = cmd.Process.Pid
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	ended := make(chan struct{})
	killEscalationsMu.Lock()
	killEscalations[ended] = struct{}{}
	killEscalationsMu.Unlock()
	go func() {
		defer safego.Recover("background/process_unix:31")
		defer func() {
			killEscalationsMu.Lock()
			delete(killEscalations, ended)
			killEscalationsMu.Unlock()
			close(ended)
		}()
		for deadline := time.Now().Add(childKillGrace); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
			if syscall.Kill(-pgid, 0) != nil {
				return
			}
		}
		if err := syscall.Kill(-pgid, 0); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}()
}

// waitChildKills waits for the SIGKILL escalations of killChildProcess, or for ctx: a shutdown calls it so that a step ignoring SIGTERM is killed before Coral exits.
func waitChildKills(ctx context.Context) {
	killEscalationsMu.Lock()
	pending := make([]chan struct{}, 0, len(killEscalations))
	for c := range killEscalations {
		pending = append(pending, c)
	}
	killEscalationsMu.Unlock()
	for _, c := range pending {
		select {
		case <-c:
		case <-ctx.Done():
			return
		}
	}
}

// trackChildProcess and releaseChildProcess are Windows' job object (process_windows.go); the
// process group set by configureChildProcess does their work here.
func trackChildProcess(*exec.Cmd)   {}
func releaseChildProcess(*exec.Cmd) {}
