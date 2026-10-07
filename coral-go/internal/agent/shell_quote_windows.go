//go:build windows

package agent

import "strings"

// shellQuote wraps a string in double quotes for PowerShell if it contains
// metacharacters. Internal double quotes are escaped with the PowerShell
// backtick escape character.
func shellQuote(s string) string {
	if s == "" {
		return s
	}
	if !strings.ContainsAny(s, " \t[]*?{}$`\"\\!#&|;()<>~") {
		return s
	}
	escaped := strings.ReplaceAll(s, `"`, "`\"")
	return `"` + escaped + `"`
}

// ShellQuoteParts applies shellQuote to each part in a command parts slice,
// skipping parts that are already compound shell expressions (e.g.
// "$(Get-Content ...)", "$env:PATH=..."). On PowerShell, the first part
// (the executable) is prefixed with the call operator (&) when quoted,
// since PowerShell treats a bare quoted string as an expression rather
// than a command invocation.
func ShellQuoteParts(parts []string) []string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		if strings.Contains(p, "$(") || strings.HasPrefix(p, "$env:") {
			quoted[i] = p
		} else {
			q := shellQuote(p)
			if i == 0 && q != p {
				q = "& " + q
			}
			quoted[i] = q
		}
	}
	return quoted
}
