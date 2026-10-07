//go:build !windows

package agent

import "strings"

// shellQuote wraps a string in single quotes if it contains shell metacharacters
// that bash/zsh would interpret. Single quotes inside the string are escaped.
func shellQuote(s string) string {
	if s == "" {
		return s
	}
	if !strings.ContainsAny(s, " \t[]*?{}$`\"\\!#&|;()<>~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellQuoteParts applies shellQuote to each part in a command parts slice,
// skipping parts that are already compound shell expressions (e.g. "$(cat ...)",
// "export VAR=... &&") which are already properly formatted.
func ShellQuoteParts(parts []string) []string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		if strings.Contains(p, "$(") || strings.HasPrefix(p, "export ") {
			quoted[i] = p
		} else {
			quoted[i] = shellQuote(p)
		}
	}
	return quoted
}
