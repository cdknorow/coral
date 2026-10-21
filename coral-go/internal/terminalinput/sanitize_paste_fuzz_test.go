package terminalinput

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// #5372: SanitizePaste prepares every board message and prompt for a bracketed paste into an agent's terminal. Whatever the text, the result must
// carry nothing a terminal could act on (ESC, so the paste cannot be ended early with ESC [ 201 ~; any other control or C1 character; CR), must be
// stable under a second pass, and must keep ordinary text as it was.
//
//	go test ./internal/terminalinput -run '^$' -fuzz FuzzSanitizePaste -fuzztime 10m
func FuzzSanitizePaste(f *testing.F) {
	for _, s := range []string{
		"", "plain text", "line one\nline two\ttabbed", "a\r\nb\rc", "\x1b[201~ rm -rf /", "x\x1b[200~y", "\x00\x01\x7f", "é ü 日本語 🎉", "\xc2\x85next line", "\xc2\x9b31m",
		"bad \xff\xfe utf8", "\u202e bidi \u2066", strings.Repeat("\x1b", 100), "tab\there\x0bvertical\x0cform",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := SanitizePaste(in)
		for i, r := range out {
			switch {
			case r == '\n' || r == '\t':
			case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
				t.Fatalf("SanitizePaste(%q) kept the control character U+%04X at byte %d: %q", in, r, i, out)
			}
		}
		if strings.ContainsAny(out, "\x1b\r") {
			t.Fatalf("SanitizePaste(%q) kept an ESC or a CR: %q", in, out)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("SanitizePaste(%q) is not valid UTF-8: %q", in, out)
		}
		if again := SanitizePaste(out); again != out {
			t.Fatalf("SanitizePaste is not stable: %q then %q", out, again)
		}
		// Text that was already clean (valid UTF-8, no control character but newline and tab, no CR) comes back untouched.
		clean := utf8.ValidString(in)
		for _, r := range in {
			if r == '\r' || (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
				clean = false
			}
		}
		if clean && out != in {
			t.Fatalf("clean text changed: %q became %q", in, out)
		}
		// Nothing grows beyond the replacement of each bad byte by U+FFFD (3 bytes).
		if len(out) > 3*len(in) {
			t.Fatalf("SanitizePaste(%q) grew to %d bytes", in, len(out))
		}
	})
}
