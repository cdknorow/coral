package tunnel

import "unicode/utf8"

// A text cut short (a chat page's trimmed tool output, a transcript field capped in size) must
// not leave the start of a secret behind: the redactor recognises a whole token only, so a token
// cut after its 39th character would pass it unredacted (#3723, Security's finding). A cut is
// moved back to where it can't split a token.

// tokenByte reports whether c can be part of a value the redactor recognises: the union of its
// rules' classes and a PEM key's body (TestTokenByteCoversEveryRule). isCredValue holds every
// token's; a URL's user name and password (#3756) take nearly any printable character, so a cut
// never falls inside "user:password@host" either.
func tokenByte(c byte) bool {
	return isCredValue(c) || isSecretValue(c) || urlUserByte(c) || urlRunByte(c)
}

// maxWideWalk bounds how far a cut moves back once it has crossed bytes that only the wide rules
// take (a URL's credentials, a secret-named value), so a long CSV or hex line is not cut to
// nothing (Security on #3756). Neither of those values runs longer than this past its start, so
// what is kept still holds each of them whole. A run of a token's own characters only (isCredValue:
// ghp_..., base64) still moves the cut back to its start, however long.
const maxWideWalk = maxURLUser + maxURLRun

// CutOutsideToken is where s may be cut at or before byte n: the largest p <= n that starts a
// UTF-8 character and doesn't fall between two bytes that could both belong to a token. A run
// of such bytes that crosses n is dropped whole; if it starts at 0, p is 0. Through bytes that
// only the wide rules take, the walk back stops after maxWideWalk. n >= len(s) gives len(s).
func CutOutsideToken(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	p := max(n, 0)
	wide := false // the walk has crossed a pair only the wide rules take
	for p > 0 && (!utf8.RuneStart(s[p]) || tokenByte(s[p-1]) && tokenByte(s[p])) {
		if utf8.RuneStart(s[p]) {
			wide = wide || !(isCredValue(s[p-1]) && isCredValue(s[p]))
			if wide && n-p >= maxWideWalk {
				break
			}
		}
		p--
	}
	return p
}
