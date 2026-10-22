//go:build !windows

package ptymanager

import "github.com/subgentic/coral/internal/cmdhandoff"

// inlineCommandLimit is the longest command typed into a starting shell as it is; a longer one is
// delivered through a temporary script so the shell only has to receive a short path
// (cmdhandoff.InlineLimit).
const inlineCommandLimit = cmdhandoff.InlineLimit

// commandInvocation returns a short shell command that sources the complete command from a private
// temporary script (cmdhandoff.ShellScript). The script removes itself before running the command;
// callers should remove it on delivery failure.
func commandInvocation(command string, quiet bool) (invocation string, cleanup func(), err error) {
	if quiet { // an agent's launch line: a script that first erases the screen and scrollback (#5911)
		return cmdhandoff.QuietShellScript(command)
	}
	return cmdhandoff.ShellScript(command)
}
