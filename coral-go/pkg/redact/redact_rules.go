package redact

import (
	"bytes"
)

type tokenRule struct {
	prefixes []string                 // literal starts (case variants listed explicitly)
	glue     func([]byte) (int, bool) // after the prefix, the separator before the value and whether it named a scheme (Authorization); nil: none
	class    func(byte) bool          // the value's characters
	min, max int                      // the value's length; a longer run is still redacted (max only bounds what is held back)
	exact    bool                     // the value is exactly min long (AKIA)
	keep     bool                     // keep the prefix and glue, replace only the value (Bearer, Authorization)
	notAfter func(byte) bool          // a character that may not come right before the prefix
	params   bool                     // the value may be an auth-param list (Digest's key="value", ...): redacted to its end
	bare     func([]byte) bool        // when glue reports no scheme: whether the value is a credential (nil: mixedValue)
	toQuote  bool                     // a value after an opening quote runs to its closing quote, spaces and all (#3733)
	// value, when set, measures the value itself (in place of class, min and max): its length, or
	// need when b ends before that can be told (never when final). A URL's password ends at the
	// last '@' before its host.
	value func(b []byte, final bool) (n int, need bool)
}

// prefixLen is the length of the longest of the rule's prefixes b starts with (SECRET_KEY, not
// SECRET; credentials, not credential), 0 when none. The secret names are a set, looked up per
// length (#3733: about a hundred spellings).
func (ru *tokenRule) prefixLen(b []byte) int {
	if ru == secretRule {
		for _, n := range secretNameLens {
			if n <= len(b) && secretNameSet[string(b[:n])] {
				return n
			}
		}
		return 0
	}
	at := 0
	for _, pre := range ru.prefixes {
		if len(pre) > at && bytes.HasPrefix(b, []byte(pre)) {
			at = len(pre)
		}
	}
	return at
}

// bareOK: an unqualified value (no scheme, not quoted) counts as a credential.
func (ru *tokenRule) bareOK(v []byte) bool {
	if ru.bare != nil {
		return ru.bare(v)
	}
	return mixedValue(v)
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
func isWord(c byte) bool       { return isAlnum(c) || c == '_' }
func isUpperDigit(c byte) bool { return c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' }
func isB64URL(c byte) bool     { return isAlnum(c) || c == '_' || c == '-' }
func isAlnumDash(c byte) bool  { return isAlnum(c) || c == '-' }
func isCredValue(c byte) bool {
	return isAlnum(c) || c == '.' || c == '_' || c == '~' || c == '+' || c == '/' || c == '=' || c == '-'
}

var tokenRules = []tokenRule{
	{prefixes: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}, class: isAlnum, min: 36, max: 255, notAfter: isWord},
	{prefixes: []string{"github_pat_"}, class: isWord, min: 22, max: 255, notAfter: isWord},
	{prefixes: []string{"sk-"}, class: isB64URL, min: 20, max: 255, notAfter: isB64URL},
	{prefixes: []string{"AKIA", "ASIA"}, class: isUpperDigit, min: 16, max: 16, exact: true, notAfter: isAlnum},
	{prefixes: []string{"xoxa-", "xoxb-", "xoxp-", "xoxr-", "xoxs-"}, class: isAlnumDash, min: 10, max: 255, notAfter: isWord},
	{prefixes: []string{"Bearer", "bearer", "BEARER"}, glue: bearerGlue, class: isCredValue, min: 20, max: 512, keep: true, notAfter: isWord},
	{prefixes: []string{"Authorization", "authorization", "AUTHORIZATION"}, glue: authGlue, class: isCredValue, min: 8, max: 512, keep: true, notAfter: isWord, params: true},
	{prefixes: secretNamePrefixes, glue: secretGlue, class: isSecretValue, min: 1, max: 512, keep: true, notAfter: isAlnum, bare: secretBare, toQuote: true},
	// A URL with a password, scheme://user:password@host (#3756): postgres, mysql, redis, mongodb(+srv),
	// amqp, https, git+https and the rest. The scheme, the user and the host stay; the password goes.
	{prefixes: []string{"://"}, glue: urlUserGlue, class: urlRunByte, min: 1, max: maxURLRun, keep: true, notAfter: never, value: urlPassword},
}

// match reports the token at b, which starts with one of the rule's prefixes: its length so far,
// where its value starts, whether it is a token, whether more bytes are needed to tell (never
// when final: the body ends there), and whether it runs past the rule's ceiling (it is then a
// token whose rest is dropped as it arrives; the ceiling only bounds what is held back).
func (ru *tokenRule) match(b []byte, final bool) (n, valueAt int, ok, need, long bool) {
	at := ru.prefixLen(b)
	scheme := true
	if ru.glue != nil {
		g, sch := ru.glue(b[at:])
		switch {
		case g == -2:
			return 0, 0, false, !final, false
		case g < 0:
			return 0, 0, false, false, false
		}
		at, scheme = at+g, sch
	}
	if ru.toQuote {
		if form := openingQuote(b, at); form != nil {
			return quotedValue(b, at, form, ru.max, final)
		}
	}
	if ru.params {
		// Digest (and any scheme with parameters): username="bob", nonce="...", response="..."
		// is one credential, redacted to the list's end (#3731).
		switch n, k := paramList(b[at:], final); {
		case n == -2:
			return 0, 0, false, true, false
		case n > 0 && k >= 2 || n > 0 && bytes.IndexByte(b[at:at+n], '"') >= 0:
			return at + n, at, true, false, false
		}
	}
	if ru.value != nil {
		n, need := ru.value(b[at:], final)
		switch {
		case need:
			return 0, 0, false, true, false
		case n < ru.min:
			return 0, 0, false, false, false
		}
		return at + n, at, true, false, false
	}
	run := 0
	for at+run < len(b) && ru.class(b[at+run]) {
		run++
		if run > ru.max {
			if ru.exact {
				return 0, 0, false, false, false // longer than the fixed length: not this kind
			}
			if !scheme && !ru.bareOK(b[at:at+run]) {
				return 0, 0, false, false, false
			}
			return at + run, at, true, false, true
		}
	}
	if at+run == len(b) && !final {
		return 0, 0, false, true, false // the value may go on in the next chunk
	}
	if run < ru.min || (ru.exact && run != ru.min) {
		return 0, 0, false, false, false
	}
	if !scheme && !ru.bareOK(b[at:at+run]) {
		// "Authorization: unavailable": a bare value of letters only is a word, not a credential.
		return 0, 0, false, false, false
	}
	return at + run, at, true, false, false
}
