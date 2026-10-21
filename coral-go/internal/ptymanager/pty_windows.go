//go:build windows

package ptymanager

import (
	"context"
	"fmt"
	"github.com/subgentic/coral/internal/agentprio"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/winshell"
	"strings"
	"sync"

	"github.com/UserExistsError/conpty"
)

// windowsPTY wraps ConPTY for Windows systems.
type windowsPTY struct {
	cpty   *conpty.ConPty
	done   chan struct{}
	closed sync.Once
}

func startPTYProcess(name string, args []string, dir string, env []string, cols, rows uint16) (ptyProcess, error) {
	// ConPTY takes a single command line string. The program is quoted (windowsProgram): it is
	// often an absolute path with a space (C:\Program Files\PowerShell\7\pwsh.exe, #26 G1A), and
	// unquoted CreateProcessW would try C:\Program.exe first. The arguments are joined with
	// spaces, unquoted, which is right only for what newSession passes (#3690, #3689 finding 5):
	// shellWrap's (-NoProfile -Command <cmd>: PowerShell reads everything after -Command as the
	// command) and strings.Fields of a command with no shell metacharacters or quotes. Any other
	// caller must quote its arguments itself.
	cmdLine, err := windowsProgram(name)
	if err != nil {
		return nil, err
	}
	if len(args) > 0 {
		cmdLine += " " + strings.Join(args, " ")
	}

	// The agent gets env, newSession's filtered environment (#3389), as on Unix (cmd.Env): without
	// ConPtyEnv the process inherited Coral's whole environment, CORAL_SECRET_KEY and every other
	// server setting with it, and missed TERM, COLORTERM and CORAL_SESSION_NAME (#19).
	if len(env) == 0 {
		// conpty passes no block for an empty one, and no block is the parent's whole
		// environment: refuse rather than start an agent with the server's (Security's #20 F2).
		return nil, errEmptyEnv
	}
	opts := []conpty.ConPtyOption{
		conpty.ConPtyDimensions(int(cols), int(rows)),
		conpty.ConPtyEnv(windowsEnvBlock(env)),
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
	// Send Ctrl+C to the console to allow graceful shutdown
	_, err := p.cpty.Write([]byte{0x03}) // Ctrl+C
	return err
}

func (p *windowsPTY) ForceKill() error {
	var err error
	p.closed.Do(func() {
		err = p.cpty.Close()
	})
	return err
}

func (p *windowsPTY) Close() error {
	var err error
	p.closed.Do(func() {
		err = p.cpty.Close()
	})
	return err
}

// shellWrap wraps a command string for execution via PowerShell on Windows.
// PowerShell is used because agent launch commands use PowerShell syntax
// (e.g. $(Get-Content -Raw ...)) which cmd.exe cannot interpret. It is the PowerShell the
// launch line was written for (winshell.PowerShell, as the agent package's quoting): a line
// escaped for Windows PowerShell 5.1 run by pwsh, or the reverse, would split or keep its
// escapes (#29, Security's #20 F3).
func shellWrap(cmd string) []string {
	ps := winshell.PowerShell()
	if ps == "" {
		ps = "powershell.exe" // none on PATH: the launch fails as it did, naming it
	}
	return []string{ps, "-NoProfile", "-Command", cmd}
}
