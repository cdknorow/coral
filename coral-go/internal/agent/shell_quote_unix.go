//go:build !windows

package agent

import "strings"

// shellQuote returns s as one word for bash/zsh/sh: unchanged when every byte is in
// a small safe set, otherwise single-quoted (no expansion of any kind inside), with
// embedded single quotes escaped. An allowlist, not a list of dangerous characters,
// so newlines, quotes and anything unforeseen are always quoted (#3362).
func shellQuote(s string) string {
	if s == "" {
		return s
	}
	// Safe inside a word, not at its start: in zsh "=curl" expands to curl's path (EQUALS),
	// and "%1" or "@x" can be read specially (#3930, Security's #3927 finding 3).
	if leadingSpecial(s) {
		return singleQuote(s)
	}
	for i := 0; i < len(s); i++ {
		if !shellSafeByte(s[i]) {
			return singleQuote(s)
		}
	}
	return s
}

// leadingSpecial: s starts with a byte the safe set allows elsewhere but a shell may read at the
// start of a word.
func leadingSpecial(s string) bool { return s != "" && strings.IndexByte("=%@", s[0]) >= 0 }

func shellSafeByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("-_./:=@%+,", c) >= 0
}

// ShellJoin builds a command line from parts. Every part is quoted as one word,
// except the parts in raw: those are shell syntax Coral built itself (a prompt file's
// $(cat '…'), an export prefix) and are passed as they are. A part is raw only if
// the builder named it: never because of what it contains (#3362).
func ShellJoin(parts []string, raw ...string) string {
	return joinQuoted(parts, raw, shellQuote, false)
}
