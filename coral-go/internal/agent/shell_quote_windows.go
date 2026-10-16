//go:build windows

package agent

import (
	"strings"

	"github.com/cdknorow/coral/internal/psquote"
)

// shellQuote returns s as one PowerShell word: unchanged when every byte is in a small
// safe set, otherwise single-quoted. PowerShell expands nothing inside single quotes
// ($var, $(…) and backticks are literal; a double-quoted string would still expand
// them, #3362); an embedded ' or curly single quote is doubled (psquote, #3904). An
// embedded " is written as \" so the C
// runtime's argv parser of the native executable keeps it (PowerShell 5.1 does not
// escape quotes when it builds a native command line).
func shellQuote(s string) string {
	if s == "" {
		return s
	}
	safe := true
	for i := 0; i < len(s); i++ {
		if !shellSafeByte(s[i]) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return psquote.Literal(strings.ReplaceAll(s, `"`, `\"`))
}

func shellSafeByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("-_./:=+,", c) >= 0
}

// ShellJoin builds a command line from parts. Every part is quoted as one word,
// except the parts in raw: shell syntax Coral built itself (a prompt file's
// $(Get-Content …), a $env: assignment), passed as they are. A part is raw only if
// the builder named it, never because of what it contains (#3362). The first quoted
// part that is the command gets PowerShell's call operator (&), since a bare quoted
// string is an expression, not a command.
func ShellJoin(parts []string, raw ...string) string {
	return joinQuoted(parts, raw, shellQuote, true)
}
