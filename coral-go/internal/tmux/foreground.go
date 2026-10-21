package tmux

import (
	"context"
	"fmt"
	"github.com/subgentic/coral/internal/executil"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
// it reads the process table from ps, one snapshot shared by the checks of every
// agent made within procTableTTL (#3728: three ps/pgrep runs per agent every 30 s).
func ShellIdle(pid int) (idle bool, shell string, err error) {
	if pid <= 0 {
		return false, "", fmt.Errorf("no shell pid")
	}
	procs, err := readProcTable()
	if err != nil {
		return false, "", fmt.Errorf("read process %d: %w", pid, err)
	}
	p, ok := procs.byPID[pid]
	if !ok && time.Since(procs.at) > 20*time.Millisecond && freshLookAllowed(pid) { // (a snapshot taken by this call is as fresh as it gets)
		// A shell started since the snapshot (a restart's new terminal) is not in it: look again, now, before an unknown shell counts as a running
		// agent and automated text is typed into a bare shell (#5171, #5170).
		if procs, err = readProcTableFresh(); err != nil {
			return false, "", fmt.Errorf("read process %d: %w", pid, err)
		}
		p, ok = procs.byPID[pid]
	}
	if !ok {
		return false, "", fmt.Errorf("read process %d: no such process", pid)
	}
	shell = p.comm
	if !IsShellCommand(shell) {
		return false, shell, nil // the terminal's process is the agent itself (it was exec'd)
	}
	// Children: busy only if one of them is in the foreground of the terminal (a '+' in
	// its ps state). A background job ('sleep &') left after the CLI exited is not the
	// agent (#3268).
	for _, c := range procs.children[pid] {
		if strings.Contains(procs.byPID[c].stat, "+") {
			return false, shell, nil
		}
	}
	return true, shell, nil
}

type procEntry struct {
	ppid int
	stat string
	comm string
}

type procTable struct {
	byPID    map[int]procEntry
	children map[int][]int
	at       time.Time
}

// freshLookAllowed lets one fresh process-table read per pid per second through: a pid that is not there (a pane that is gone) must not make every check
// of it run ps.
func freshLookAllowed(pid int) bool {
	freshLookMu.Lock()
	defer freshLookMu.Unlock()
	now := time.Now()
	if t, ok := freshLooks[pid]; ok && now.Sub(t) < time.Second {
		return false
	}
	if len(freshLooks) > 1024 { // forget the old ones
		for k, t := range freshLooks {
			if now.Sub(t) >= time.Second {
				delete(freshLooks, k)
			}
		}
	}
	freshLooks[pid] = now
	return true
}

var (
	freshLookMu sync.Mutex
	freshLooks  = map[int]time.Time{}
)

// procTableTTL is how long a process-table snapshot answers: long enough to serve one
// reconciler pass over every agent, short enough that an exit is seen on the next pass.
const procTableTTL = time.Second

var (
	procTableMu   sync.Mutex
	procTableLast *procTable
	// procTableRuns counts the ps runs, for tests.
	procTableRuns atomic.Int64
)

// readProcTable returns a snapshot of every process (pid, parent, state, command) no older
// than procTableTTL, from one 'ps' run.
// psTimeout bounds the ps call: readProcTable holds procTableMu while it runs, so a stuck ps would
// block every caller for good (#3999, the #3994 audit finding 4).
var psTimeout = 5 * time.Second

func readProcTable() (*procTable, error) { return readProcTableMaxAge(procTableTTL) }

// readProcTableFresh is a snapshot taken now.
func readProcTableFresh() (*procTable, error) { return readProcTableMaxAge(0) }

func readProcTableMaxAge(maxAge time.Duration) (*procTable, error) {
	procTableMu.Lock()
	defer procTableMu.Unlock()
	if t := procTableLast; t != nil && maxAge > 0 && time.Since(t.at) < maxAge {
		return t, nil
	}
	procTableRuns.Add(1)
	out, err := executil.Output(psTimeout, "ps", "-A", "-o", "pid=,ppid=,stat=,comm=")
	if err != nil {
		return nil, err
	}
	t := parseProcTable(string(out))
	t.at = time.Now()
	procTableLast = t
	return t, nil
}

// parseProcTable reads 'ps -A -o pid=,ppid=,stat=,comm=': the command is the rest of the
// line (a macOS command is a path, which may hold spaces).
func parseProcTable(out string) *procTable {
	t := &procTable{byPID: map[int]procEntry{}, children: map[int][]int{}}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		rest := strings.TrimLeft(line, " \t")
		for k := 0; k < 3; k++ { // past pid, ppid and stat
			rest = strings.TrimLeft(rest[strings.IndexAny(rest, " \t"):], " \t")
		}
		t.byPID[pid] = procEntry{ppid: ppid, stat: f[2], comm: strings.TrimSpace(rest)}
		t.children[ppid] = append(t.children[ppid], pid)
	}
	return t
}

// AgentShellIdle reports whether the agent's own pane is back at its idle shell
// prompt (ShellIdle on the pane's shell). It returns ErrTerminalGone when the
// pane is gone.
func (c *Client) AgentShellIdle(ctx context.Context, agentName, agentType, sessionID string) (bool, string, error) {
	if st, ok := c.agentPaneState(ctx, agentName, agentType, sessionID); ok && st.pid > 0 {
		return ShellIdle(st.pid) // the shared pane list (#3847)
	}
	out, err := c.DisplayForAgent(ctx, agentName, agentType, sessionID, "#{pane_pid}")
	if err != nil {
		return false, "", err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return false, "", fmt.Errorf("invalid pane pid %q: %w", out, err)
	}
	return ShellIdle(pid)
}

// foregroundIsShell says whether the program in the foreground of the pane is a plain shell (pane_current_command).
// An unreadable answer is not a shell.
func (c *Client) foregroundIsShell(ctx context.Context, target string) bool {
	out, err := c.DisplayMessage(ctx, target, "#{pane_current_command}")
	return err == nil && IsShellCommand(strings.TrimSpace(out))
}
