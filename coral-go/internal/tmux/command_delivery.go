package tmux

import "github.com/cdknorow/coral/internal/cmdhandoff"

// inlineCommandLimit is the longest command typed into a fresh shell as it is; a longer one is
// delivered through a script (cmdhandoff.InlineLimit).
const inlineCommandLimit = cmdhandoff.InlineLimit

// commandInvocation writes an oversized command to a private script and returns a short source
// command for the target shell (cmdhandoff.ShellScript).
func commandInvocation(command string) (invocation string, cleanup func(), err error) {
	return cmdhandoff.ShellScript(command)
}

// shellQuote is value as one single-quoted shell word.
func shellQuote(value string) string { return cmdhandoff.Quote(value) }
