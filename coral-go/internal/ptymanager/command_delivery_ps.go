package ptymanager

import "github.com/subgentic/coral/internal/psquote"

// psLiteral is s as a PowerShell single-quoted string: nothing inside expands, and every
// single-quote character is doubled (#3690: a profile folder like C:\Users\O'Brien ended the
// string early; #3904: so did a curly apostrophe).
func psLiteral(s string) string {
	return psquote.Literal(s)
}

// utf8BOM starts the hand-off script: Windows PowerShell 5.1 reads a .ps1 without a byte-order
// mark in the ANSI code page, so a non-ASCII character in the script (an accented or
// curly-apostrophe profile folder in its own path, or text in the command) was misread: the
// script couldn't remove itself and the command could change (#3913, found by its round trip on
// windows-latest).
const utf8BOM = "\ufeff"

// psHandoff is the Windows command handoff for a script at path: the script's text (it removes
// itself, then runs command) and the short line that runs it. The line starts a child PowerShell (shell, a full
// path) with -ExecutionPolicy Bypass for that one process: a stock Windows client's policy is Restricted, which
// refuses every script, and the script (this user's and SYSTEM's only, removing itself) is all the child runs.
// The agent's interactive terminal keeps the machine's policy (#25 G3A, Security's review).
func psHandoff(shell, path, command string) (script, invocation string) {
	return utf8BOM + "Remove-Item -Force -LiteralPath " + psLiteral(path) + "\n" + command + "\n",
		"& " + psLiteral(shell) + " -NoProfile -ExecutionPolicy Bypass -File " + psLiteral(path)
}
