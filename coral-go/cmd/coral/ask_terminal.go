package main

import (
	"os"

	"golang.org/x/term"
)

// canAskAtTerminal reports whether a first-launch question can be asked and answered here
// (#4052). A terminal on stdin alone is not enough: on Windows a CI runner or a scheduled
// task can hand Coral a console nobody types in while its output goes to a pipe, and on
// Unix "coral &" keeps the terminal as stdin while a read would stop the process. So both
// stdin and stdout must be terminals, and on Unix Coral must be the terminal's foreground
// job. Otherwise nothing is asked: the safe default holds and the log says how to change it.
func canAskAtTerminal(stdin, stdout *os.File) bool {
	return term.IsTerminal(int(stdin.Fd())) && term.IsTerminal(int(stdout.Fd())) && inForeground(stdin)
}
