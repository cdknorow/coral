package tunnel

import "unicode/utf8"

// A text cut short (a chat page's trimmed tool output, a transcript field capped in size) must
// not leave the start of a secret behind: the redactor recognises a whole token only, so a token
// cut after its 39th character would pass it unredacted (#3723, Security's finding). A cut is
// moved back to where it can't split a token.

// tokenByte reports whether c can be part of a value the redactor recognises: the union of its
// rules' classes and a PEM key's body (isCredValue holds them all; TestTokenByteCoversEveryRule).
func tokenByte(c byte) bool { return isCredValue(c) }

// CutOutsideToken is where s may be cut at or before byte n: the largest p <= n that starts a
// UTF-8 character and doesn't fall between two bytes that could both belong to a token. A run
// of such bytes that crosses n is dropped whole; if it starts at 0, p is 0. n >= len(s) gives
// len(s).
func CutOutsideToken(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	p := max(n, 0)
	for p > 0 && (!utf8.RuneStart(s[p]) || tokenByte(s[p-1]) && tokenByte(s[p])) {
		p--
	}
	return p
}
