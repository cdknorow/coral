// Package ptymanager provides terminal session management via native PTY
// or tmux backends. It replaces direct tmux dependency for Mac App Store compatibility.
package ptymanager

import "io"

// ptyProcess abstracts a process running inside a pseudo-terminal.
// Unix uses creack/pty; Windows uses ConPTY.
type ptyProcess interface {
	io.ReadWriteCloser

	// Resize changes the terminal dimensions.
	Resize(cols, rows uint16) error

	// Terminate sends a graceful stop signal (SIGTERM on Unix, TerminateProcess on Windows).
	Terminate() error

	// ForceKill forcefully kills the process tree (SIGKILL on Unix, job object on Windows).
	ForceKill() error

	// Done returns a channel that is closed when the process exits.
	Done() <-chan struct{}

	// Pid is the process started in the terminal (0 when not known).
	Pid() int
}

// startPTYProcess starts a command inside a new pseudo-terminal.
// Implemented per-platform in pty_unix.go and pty_windows.go.
//
//	func startPTYProcess(name string, args []string, dir string, env []string, cols, rows uint16) (ptyProcess, error)

// shellWrap returns a command line that executes cmd via the platform shell.
// Implemented per-platform in pty_unix.go and pty_windows.go.
//
//	func shellWrap(cmd string) []string
