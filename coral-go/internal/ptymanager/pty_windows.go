//go:build windows

package ptymanager

import (
	"context"
	"fmt"
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
	// ConPTY takes a single command line string.
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

	p := &windowsPTY{
		cpty: cpty,
		done: make(chan struct{}),
	}

	go func() {
		p.cpty.Wait(context.Background())
		close(p.done)
	}()

	return p, nil
}

func (p *windowsPTY) Read(b []byte) (int, error)  { return p.cpty.Read(b) }
func (p *windowsPTY) Write(b []byte) (int, error) { return p.cpty.Write(b) }
func (p *windowsPTY) Done() <-chan struct{}        { return p.done }

func (p *windowsPTY) Resize(cols, rows uint16) error {
	return p.cpty.Resize(int(cols), int(rows))
}

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
// (e.g. $(Get-Content -Raw ...)) which cmd.exe cannot interpret.
func shellWrap(cmd string) []string {
	return []string{"powershell.exe", "-NoProfile", "-Command", cmd}
}
