// Package psquote quotes a word for PowerShell.
package psquote

import "strings"

// SingleQuotes are the characters PowerShell's tokenizer reads as a single quote: the ASCII
// apostrophe and the curly ones (U+2018 left, U+2019 right, U+201A low-9, U+201B high-reversed-9;
// about_Quoting_Rules). Each of them ends a single-quoted string unless doubled (#3904, sweep
// #3903 item 1): doubling only ' let a word with a curly apostrophe close its string early and
// the rest run as PowerShell.
const SingleQuotes = "'\u2018\u2019\u201a\u201b"

// IsSingleQuote reports whether r is one of SingleQuotes.
func IsSingleQuote(r rune) bool {
	return strings.ContainsRune(SingleQuotes, r)
}

// Literal returns s as a PowerShell single-quoted string: nothing inside expands ($var, $(...)
// and backticks are literal), and every single-quote character is doubled.
func Literal(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for _, r := range s {
		if IsSingleQuote(r) {
			b.WriteRune(r)
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}
