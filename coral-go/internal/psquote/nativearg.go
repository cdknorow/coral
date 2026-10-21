package psquote

import (
	"path/filepath"
	"strings"
	"unicode"
)

// PowerShells are the shells Coral starts a Windows terminal with, in order of preference
// (ptymanager's pickShell). The launch line is written for the one that runs it (LegacyNativeArgs).
var PowerShells = []string{"pwsh.exe", "powershell.exe"}

// LegacyNativeArgs reports whether shell, a path or name, passes arguments to native programs
// the way Windows PowerShell 5.1 does, so a launch line must escape them itself (NativeArg):
// every shell but pwsh. pwsh 7.3 and later escape each argument for the program's argv parser
// ($PSNativeCommandArgumentPassing), so an argument escaped here would arrive with the escapes
// in it; pwsh 7.2 and earlier, out of support, are not told apart.
func LegacyNativeArgs(shell string) bool {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(shell, `\`, "/")))
	return strings.TrimSuffix(base, ".exe") != "pwsh"
}

// NativeArg returns the text that makes Windows PowerShell 5.1 hand a native program s as one
// argument, as the program's argv parser (the C runtime's, CommandLineToArgvW, Rust's and Go's
// alike) reads it. 5.1 writes each argument into the command line as it is, adding double
// quotes around it when it has whitespace outside the double quotes it counts (every ", even
// an escaped one), and escapes nothing. So:
//   - a run of n backslashes before a " becomes 2n+1 backslashes and the " (a literal quote);
//   - when 5.1 will add its quotes, the n backslashes at the end become 2n, so its closing
//     quote isn't escaped.
//
// An argument whose spaces all come after an odd number of quotes (a"b c) is one 5.1 won't
// quote while its argv parser would split it: it is quoted here instead, with each literal
// quote written "" (a quote inside quotes; an odd count of them would make 5.1 add its own),
// which the C runtime since 2008, Rust and Go read as one quote.
func NativeArg(s string) string {
	wraps := wrapsNativeArg(s)
	if !wraps && strings.ContainsAny(s, " \t") {
		return `"` + escapeQuotes(s, `""`, true) + `"`
	}
	return escapeQuotes(s, `\"`, wraps)
}

// escapeQuotes writes s with each " as quote, the backslashes before it doubled (so they stay
// literal), and the trailing backslashes doubled when a closing quote follows.
func escapeQuotes(s, quote string, closed bool) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		switch r {
		case '\\':
			n++
			continue
		case '"':
			b.WriteString(strings.Repeat(`\`, 2*n))
			b.WriteString(quote)
		default:
			b.WriteString(strings.Repeat(`\`, n))
			b.WriteRune(r)
		}
		n = 0
	}
	if closed {
		n *= 2
	}
	b.WriteString(strings.Repeat(`\`, n))
	return b.String()
}

// wrapsNativeArg reports whether Windows PowerShell 5.1 puts double quotes around s when it
// passes it to a native program: s has whitespace after an even number of " (its NeedQuotes).
func wrapsNativeArg(s string) bool {
	quotes := 0
	for _, r := range s {
		if r == '"' {
			quotes++
		} else if unicode.IsSpace(r) && quotes%2 == 0 {
			return true
		}
	}
	return false
}
