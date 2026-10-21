//go:build windows

package ptymanager

import (
	"context"
	"fmt"
	"github.com/subgentic/coral/internal/agentprio"
	"github.com/subgentic/coral/internal/safego"
	"strings"
	"sync"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
)

// windowsPTY wraps ConPTY for Windows systems.
type windowsPTY struct {
	cpty   *conpty.ConPty
	done   chan struct{}
	closed sync.Once
	job    windows.Handle // ends the whole process tree (0: none)
}

func startPTYProcess(name string, args []string, dir string, env []string, cols, rows uint16) (ptyProcess, error) {
	// ConPTY takes a single command line string. Joining with spaces, unquoted, is right only for
	// what newSession passes (#3690, #3689 finding 5): defaultShell (one word), shellWrap
	// (powershell.exe -NoProfile -Command <cmd>: PowerShell reads everything after -Command as the
	// command) and strings.Fields of a command with no shell metacharacters or quotes. Any other
	// caller must quote its arguments itself.
	cmdLine := name
	if len(args) > 0 {
		cmdLine = name + " " + strings.Join(args, " ")
	}

	opts := []conpty.ConPtyOption{
		conpty.ConPtyDimensions(int(cols), int(rows)),
	}
	if dir != "" {
		opts = append(opts, conpty.ConPtyWorkDir(dir))
	}

	cpty, err := conpty.Start(cmdLine, opts...)
	if err != nil {
		return nil, fmt.Errorf("conpty.Start: %w", err)
	}
	// BELOW_NORMAL priority for the agent (#3437); its children inherit it.
	agentprio.Apply(cpty.Pid())

	p := &windowsPTY{
		cpty: cpty,
		done: make(chan struct{}),
		job:  newKillJob(cpty.Pid()),
	}

	go func() {
		defer safego.Recover("ptymanager/pty_windows:52")
		p.cpty.Wait(context.Background())
		close(p.done)
	}()

	return p, nil
}

func (p *windowsPTY) Read(b []byte) (int, error)  { return p.cpty.Read(b) }
func (p *windowsPTY) Write(b []byte) (int, error) { return p.cpty.Write(b) }
func (p *windowsPTY) Done() <-chan struct{}       { return p.done }
func (p *windowsPTY) Pid() int                    { return p.cpty.Pid() }

func (p *windowsPTY) Resize(cols, rows uint16) error {
	return p.cpty.Resize(int(cols), int(rows))
}

// terminateTypesCtrlC: Terminate stops the program by typing Ctrl-C into its
// console, so the session records it in the input debug log (#3256).
const terminateTypesCtrlC = true

func (p *windowsPTY) Terminate() error {
	// Ctrl+C into the console now; politeStop adds the second one a raw-mode CLI needs, and "exit" once the agent has gone
	// and its shell is idle. The force kill after the grace is the backstop.
	_, err := p.cpty.Write([]byte{0x03}) // Ctrl+C
	if err == nil {
		pid := p.cpty.Pid()
		go func() {
			defer safego.Recover("ptymanager/pty_windows:terminate")
			politeStop(func(b []byte) error { _, err := p.cpty.Write(b); return err }, p.done,
				func() (bool, error) { return hasChildProcesses(pid) }, func() bool { return rootIsShell(pid) }, defaultStopTimings)
		}()
	}
	return err
}

// ForceKill ends the whole process tree through the job object, then the pseudo console.
func (p *windowsPTY) ForceKill() error {
	var err error
	p.closed.Do(func() {
		p.endTree()
		err = p.cpty.Close()
	})
	return err
}

func (p *windowsPTY) Close() error {
	var err error
	p.closed.Do(func() {
		p.endTree()
		err = p.cpty.Close()
	})
	return err
}

// endTree terminates every process of the terminal's tree and releases the job.
func (p *windowsPTY) endTree() {
	if p.job != 0 {
		_ = windows.TerminateJobObject(p.job, 1)
		_ = windows.CloseHandle(p.job)
		p.job = 0
	}
}

// shellWrap wraps a command string for execution via PowerShell on Windows.
// PowerShell is used because agent launch commands use PowerShell syntax
// (e.g. $(Get-Content -Raw ...)) which cmd.exe cannot interpret.
func shellWrap(cmd string) []string {
	return []string{"powershell.exe", "-NoProfile", "-Command", cmd}
}
