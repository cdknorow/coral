package ptymanager

import "github.com/cdknorow/coral/internal/psquote"

// psLiteral is s as a PowerShell single-quoted string: nothing inside expands, and every
// single-quote character is doubled (#3690: a profile folder like C:\Users\O'Brien ended the
// string early; #3904: so did a curly apostrophe).
func psLiteral(s string) string {
	return psquote.Literal(s)
}

// psHandoff is the Windows command handoff for a script at path: the script's text (it removes
// itself, then runs command) and the short line that runs it.
func psHandoff(path, command string) (script, invocation string) {
	return "Remove-Item -Force -LiteralPath " + psLiteral(path) + "\n" + command + "\n", "& " + psLiteral(path)
}
