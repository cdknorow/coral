//go:build windows

package agent

import "strings"

// shellQuote wraps a string in double quotes for PowerShell if it contains
// metacharacters. Internal double quotes are escaped as \`" which PowerShell
// evaluates to \" (literal backslash + literal quote). The \" survives to
// the native executable's command line where the C runtime's argv parser
// treats it as a literal double-quote. Plain `" escaping would work inside
// PowerShell but PS 5.1 doesn't re-escape the quotes when constructing the
// command line for native executables, so they get stripped.
func shellQuote(s string) string {
	if s == "" {
		return s
	}
	if !strings.ContainsAny(s, " \t[]*?{}$`\"\\!#&|;()<>~") {
		return s
	}
	escaped := strings.ReplaceAll(s, `"`, "\\`\"")
	return `"` + escaped + `"`
}

// ShellQuoteParts applies shellQuote to each part in a command parts slice,
// skipping parts that are already compound shell expressions (e.g.
// "$(Get-Content ...)", "$env:PATH=..."). On PowerShell, the first
// "command" part (the executable) is prefixed with the call operator (&)
// when quoted, since PowerShell treats a bare quoted string as an
// expression rather than a command invocation. Preamble parts like
// $env: assignments are not considered the command.
func ShellQuoteParts(parts []string) []string {
	quoted := make([]string, len(parts))
	seenCommand := false
	for i, p := range parts {
		if strings.Contains(p, "$(") || strings.HasPrefix(p, "$env:") {
			quoted[i] = p
		} else {
			q := shellQuote(p)
			if !seenCommand && q != p {
				q = "& " + q
			}
			seenCommand = true
			quoted[i] = q
		}
	}
	return quoted
}
