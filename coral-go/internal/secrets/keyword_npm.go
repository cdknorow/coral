package secrets

import (
	"regexp"
	"strings"

	"github.com/cdknorow/coral/pkg/redact"
)

// Round five of the hub's keyword rules (#5242), found by coral-hub's rerun on v1.4.122: the npm and yarn way of writing a registry token or password, and a bare "pwd" name. The
// entity-encoded equals (password&#61;VALUE) is in plainSpellings (keyword_more.go). Same shape as round four: found by moreKeywordSpans behind a cheap test of the text.

var (
	// npm config set [-g] //registry.npmjs.org/:_authToken VALUE, npm config set _authToken VALUE, yarn config set npmAuthToken VALUE (also pnpm): the name is the last word before the value, with no
	// '=' between them. The prefix before the name ends in nothing but the registry's host and path.
	npmConfigRe = regexp.MustCompile(`(?i)\b(?:npm|pnpm|yarn)\b[^\n]*?\bconfig\s+(?:-{1,2}[a-z][a-z-]*(?:=[^\s"']+)?\s+)*set\s+(?:-{1,2}[a-z][a-z-]*(?:=[^\s"']+)?\s+)*[^\s"']*?(_authtoken|_auth|_password|npmauthtoken|npmauthident|npmpassword|authtoken|auth-token|token|password|secret)\s+["']?([^\s"'\-][^\s"']{7,})`)
	// A line of an .npmrc: //registry.npmjs.org/:_authToken=VALUE (already read by the exact names), _auth=BASE64, //host/:_password=BASE64 (anywhere in the text, not only at the start of a line: a JSON reply has no line start).
	npmrcRe = regexp.MustCompile(`(?i)` + nameBoundary + `(?://[^\s=]*:)?(_authtoken|_auth|_password)[ \t]*=[ \t]*["']?([^\s"']{8,})`)
	// A "pwd" name, as an environment variable, a key or a parameter (PGPWD, db_pwd, "pwd": "..."), with the same separators as the other names.
	pwdRe = regexp.MustCompile(`(?i)` + nameBoundary + nameChain + `pwd)` + sepAndQuote + `(` + valueEndPassword + `{8,})`)
)

func npmConfigSpans(text string) []redact.Span {
	var out []redact.Span
	for _, m := range npmConfigRe.FindAllStringSubmatchIndex(text, -1) {
		name, value := strings.ToLower(text[m[2]:m[3]]), text[m[4]:m[5]]
		if npmValueOK(name, value) {
			out = append(out, redact.Span{Start: m[4], End: m[5]})
		}
	}
	for _, m := range npmrcRe.FindAllStringSubmatchIndex(text, -1) {
		if npmValueOK(strings.ToLower(text[m[2]:m[3]]), text[m[4]:m[5]]) {
			out = append(out, redact.Span{Start: m[4], End: m[5]})
		}
	}
	return out
}

// npmValueOK: a value is delimited by the command (or the line), so a plain word counts, but not a placeholder; the base64 of a login (_auth, _password) is read as a password (8 characters), the rest as a key (16).
func npmValueOK(name, value string) bool {
	if value == "" || placeholderValue(value) {
		return false
	}
	switch name {
	case "_auth", "_password", "npmauthident", "npmpassword", "password":
		return len(value) >= minPasswordLen
	}
	return len(value) >= minKeyLen
}

// pwdSpans: the value after a name ending in pwd, unless it is a path (pwd=/home/me/src, PWD=C:\work, pwd=~/x, pwd=./y: the shell's working directory) or a placeholder.
func pwdSpans(text string) []redact.Span {
	var out []redact.Span
	for _, m := range pwdRe.FindAllStringSubmatchIndex(text, -1) {
		end := trimEntityQuotes(text, m[6], m[7])
		name, sep, value := text[m[2]:m[3]], text[m[4]:m[5]], text[m[6]:end]
		if value == "" || looksLikePath(value) {
			continue
		}
		if keywordValueOK(keywordRule{}, name, sep, value) {
			out = append(out, redact.Span{Start: m[6], End: end})
		}
	}
	return out
}

// looksLikePath: starts like a file path (/, ~, ./, ../, a drive letter and a separator, a UNC path).
func looksLikePath(v string) bool {
	switch v[0] {
	case '/', '~':
		return true
	case '.':
		return strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../") || strings.HasPrefix(v, `.\`)
	case '\\':
		return strings.HasPrefix(v, `\\`)
	}
	return len(v) >= 3 && (v[0]|0x20) >= 'a' && (v[0]|0x20) <= 'z' && v[1] == ':' && (v[2] == '\\' || v[2] == '/')
}
