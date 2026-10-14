//go:build !windows

package ptymanager

// ShellPID is the pid of the process the terminal started: the shell the agent
// CLI is typed into (or 'sh -c' for a launch command), for tmux.ShellIdle (#3219).
func (p *unixPTY) ShellPID() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
