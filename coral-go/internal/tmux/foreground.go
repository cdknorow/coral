package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// shellNames are the interactive shells an agent's terminal starts with. The
// agent CLI is typed into that shell at launch, so when the CLI exits the
// terminal is left at a plain shell prompt (#3219).
var shellNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "mksh": true,
	"tcsh": true, "csh": true, "ash": true, "nu": true, "xonsh": true, "elvish": true,
	"pwsh": true, "powershell": true, "cmd": true,
}

// IsShellCommand reports whether a process name is a plain shell (a login
// shell's leading '-' and any directory or .exe are ignored).
func IsShellCommand(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimPrefix(n, "-")
	n = strings.TrimSuffix(filepath.Base(n), ".exe")
	return shellNames[n]
}

// ShellIdle reports whether the process with this pid, a terminal's own shell,
// is a shell with no child processes: nothing (no agent CLI, no wrapper script
// running one) is running in it, so the agent has exited (#3219). Checking for
// children rather than the foreground name keeps a CLI started through a shell
// wrapper script (which shows as 'bash' or 'sh') from looking exited. Unix only:
// it uses ps and pgrep.
func ShellIdle(pid int) (idle bool, shell string, err error) {
	if pid <= 0 {
		return false, "", fmt.Errorf("no shell pid")
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false, "", fmt.Errorf("read process %d: %w", pid, err)
	}
	shell = strings.TrimSpace(string(out))
	if !IsShellCommand(shell) {
		return false, shell, nil // the terminal's process is the agent itself (it was exec'd)
	}
	children, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	var ee *exec.ExitError
	switch {
	case err == nil:
		// Children: busy only if one of them is in the foreground of the terminal. A
		// background job ('sleep &') left after the CLI exited is not the agent (#3268).
		if foregroundChild(strings.Fields(string(children))) {
			return false, shell, nil
		}
		return true, shell, nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return true, shell, nil // pgrep found no child
	default:
		return false, shell, fmt.Errorf("list children of %d: %w", pid, err)
	}
}

// foregroundChild reports whether any of these processes is in its terminal's
// foreground process group (a '+' in the ps state). When it cannot tell it says yes,
// the old, safe reading: a child is running.
func foregroundChild(pids []string) bool {
	if len(pids) == 0 {
		return false
	}
	out, err := exec.Command("ps", append([]string{"-o", "stat=", "-p"}, strings.Join(pids, ","))...).Output()
	if err != nil {
		// ps exits 1 when some pids are already gone: judge by the ones it printed.
		if len(out) == 0 {
			return err != nil && !isExit1(err)
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "+") {
			return true
		}
	}
	return false
}

func isExit1(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 1
}

// AgentShellIdle reports whether the agent's own pane is back at its idle shell
// prompt (ShellIdle on the pane's shell). It returns ErrTerminalGone when the
// pane is gone.
func (c *Client) AgentShellIdle(ctx context.Context, agentName, agentType, sessionID string) (bool, string, error) {
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return false, "", err
	}
	if target == "" {
		return false, "", fmt.Errorf("%w: pane %q not found in any tmux session", ErrTerminalGone, agentName)
	}
	out, err := c.DisplayMessage(ctx, target, "#{pane_pid}")
	if err != nil {
		return false, "", err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return false, "", fmt.Errorf("invalid pane pid %q: %w", out, err)
	}
	return ShellIdle(pid)
}
