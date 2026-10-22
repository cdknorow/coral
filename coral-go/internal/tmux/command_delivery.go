package tmux

import "github.com/subgentic/coral/internal/cmdhandoff"

// inlineCommandLimit is the longest command typed into a fresh shell as it is; a longer one is
// delivered through a script (cmdhandoff.InlineLimit).
const inlineCommandLimit = cmdhandoff.InlineLimit

// commandInvocation writes an oversized command to a private script and returns a short source
// command for the target shell (cmdhandoff.ShellScript); quiet is an agent's launch line, which is always a script that first
// erases the screen and scrollback (cmdhandoff.QuietShellScript, #5911).
func commandInvocation(command string, quiet bool) (invocation string, cleanup func(), err error) {
	if quiet {
		return cmdhandoff.QuietShellScript(command)
	}
	return cmdhandoff.ShellScript(command)
}

// shellQuote is value as one single-quoted shell word.
func shellQuote(value string) string { return cmdhandoff.Quote(value) }
