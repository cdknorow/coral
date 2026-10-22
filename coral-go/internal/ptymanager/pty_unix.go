//go:build !windows

package ptymanager

import (
	"github.com/subgentic/coral/internal/agentprio"
	"github.com/subgentic/coral/internal/safego"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"github.com/shirou/gopsutil/v4/process"
)

// unixPTY wraps creack/pty for Unix systems (macOS, Linux).
type unixPTY struct {
	cmd     *exec.Cmd
	ptyFile *os.File
	done    chan struct{}
	root    member // the shell as it was when it started: pid and start time (zero if they could not be read)
	seen    seen   // the tree as Terminate found it
	tag     string // CORAL_PTY_RUN in the environment of everything this terminal runs: how its whole tree is found (proc_tree_unix.go)
}

// Pid is the process started in the terminal.
func (p *unixPTY) Pid() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// terminateTypesCtrlC is false on Unix: Terminate sends SIGTERM, not input.
const terminateTypesCtrlC = false

func startPTYProcess(name string, args []string, dir string, env []string, cols, rows uint16) (ptyProcess, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	tag := newRunTag()
	cmd.Env = append(env[:len(env):len(env)], runTagEnv+"="+tag)
	// No Setpgid: pty.StartWithSize makes the child a session leader (Setsid + Setctty),
	// which is already the leader of its own process group, and setpgid on a session
	// leader fails with EPERM, so no PTY could be spawned on macOS (#3229).
	// Terminate and ForceKill signal that group (kill(-pgid)).
	cmd.SysProcAttr = &syscall.SysProcAttr{}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, err
	}
	// The agent and what it starts run at the agents' lower priority (#3437).
	agentprio.Apply(cmd.Process.Pid)

	p := &unixPTY{
		cmd:     cmd,
		ptyFile: ptmx,
		done:    make(chan struct{}),
		tag:     tag,
	}
	if cp, err := process.NewProcess(int32(cmd.Process.Pid)); err == nil {
		if created, err := cp.CreateTime(); err == nil {
			p.root = member{cmd.Process.Pid, created}
		}
	}

	guardian.add(p.root, tag) // Coral dying without cleaning up ends this terminal's tree (#6129)
	go func() {
		defer safego.Recover("ptymanager/pty_unix:55")
		cmd.Wait()
		close(p.done)
		guardian.remove(p.root.pid) // ended: nothing left for the guardian to end (#6129)
	}()

	return p, nil
}

func (p *unixPTY) Read(b []byte) (int, error)  { return p.ptyFile.Read(b) }
func (p *unixPTY) Write(b []byte) (int, error) { return p.ptyFile.Write(b) }
func (p *unixPTY) Close() error                { return p.ptyFile.Close() }
func (p *unixPTY) Done() <-chan struct{}       { return p.done }

func (p *unixPTY) Resize(cols, rows uint16) error {
	return pty.Setsize(p.ptyFile, &pty.Winsize{Rows: rows, Cols: cols})
}

// Terminate is what closing a terminal does, then the polite stop: SIGHUP to the process group, then SIGTERM. An interactive shell ignores SIGTERM
// but exits on SIGHUP, so a terminal agent's kill waited the whole grace (5 s) for nothing (#4984); an agent that handles SIGTERM still gets it at
// once. The caller force-kills after its grace.
func (p *unixPTY) Terminate() error {
	if p.cmd.Process == nil {
		return nil
	}
	tree := treeMembers(p.root, p.tag) // before the shell goes: what descends from it, and what is tagged, is found while it is still the parent (#6125)
	p.seen.add(tree)
	pgid, err := syscall.Getpgid(p.cmd.Process.Pid)
	if err == nil && pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGHUP)
		err = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = p.cmd.Process.Signal(syscall.SIGHUP)
		err = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	signalTree(tree, syscall.SIGHUP)
	signalTree(tree, syscall.SIGTERM)
	return err
}

func (p *unixPTY) ForceKill() error {
	if p.cmd.Process == nil {
		return nil
	}
	killTree(p.seen.all(), p.root, p.tag) // the whole tree, not only the shell's group (#6125)
	pgid, err := syscall.Getpgid(p.cmd.Process.Pid)
	if err == nil && pgid > 0 {
		return syscall.Kill(-pgid, syscall.SIGKILL)
	}
	return p.cmd.Process.Kill()
}

// shellWrap wraps a command string for execution via the Unix shell.
func shellWrap(cmd string) []string {
	return []string{"sh", "-c", cmd}
}
