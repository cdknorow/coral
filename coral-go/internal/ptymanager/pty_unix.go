//go:build !windows

package ptymanager

import (
	"github.com/cdknorow/coral/internal/agentprio"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// unixPTY wraps creack/pty for Unix systems (macOS, Linux).
type unixPTY struct {
	cmd     *exec.Cmd
	ptyFile *os.File
	done    chan struct{}
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
	cmd.Env = env
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
	}

	go func() {
		cmd.Wait()
		close(p.done)
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

func (p *unixPTY) Terminate() error {
	if p.cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(p.cmd.Process.Pid)
	if err == nil && pgid > 0 {
		return syscall.Kill(-pgid, syscall.SIGTERM)
	}
	return p.cmd.Process.Signal(syscall.SIGTERM)
}

func (p *unixPTY) ForceKill() error {
	if p.cmd.Process == nil {
		return nil
	}
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
