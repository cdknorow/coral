package ptymanager

import "strings"

// psLiteral is s as a PowerShell single-quoted string: nothing inside expands, and an
// apostrophe is doubled (#3690: a profile folder like C:\Users\O'Brien ended the string early).
func psLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// psHandoff is the Windows command handoff for a script at path: the script's text (it removes
// itself, then runs command) and the short line that runs it.
func psHandoff(path, command string) (script, invocation string) {
	return "Remove-Item -Force " + psLiteral(path) + "\n" + command + "\n", "& " + psLiteral(path)
}
