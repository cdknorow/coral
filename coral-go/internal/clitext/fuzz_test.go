package clitext

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Fuzz targets for the cleaners a commit subject and a branch name go through (#6832, #6878): Clean for a subject, CleanName for a branch. The properties are the ones that make them safe to store and
// show: no terminal control or text-reordering character survives, the output is valid UTF-8 and never longer than its input, and cleaning a second time changes nothing.

var cleanSeeds = []string{
	"", "plain subject", "fix: handle \x1b[31mred\x1b[0m text", "\x1b]52;c;aGVsbG8=\x07 clipboard", "\x1bP1$r\x1b\\ dcs", "a\u202eb\u2066c\u2069d", "zero\u200bwidth\u2060joiner", "emoji \U0001F468\u200d\U0001F469",
	"\x9b31m c1 csi", "\xc2\x9b31m utf8 c1", "\xff\xfe stray", "tab\there\nnew\rline", "\x1b\x1b[31m\x1b[m nested", "\x1b[", "\x1b", "\x1b\x1b\x1b", "x\x1b[3\x1b[1m1mz", "tag\U000E0041\U000E007F",
	"feature/\u202ex", "my branch  name", "a\u00a0b", "\u2800\u3164", "\ufe0f variation", "a\u2028b\u2029c", "\x7f del", "\x00 nul", "\ufeffbom",
}

// forbiddenInClean reports a rune Clean must never leave in its output.
func forbiddenInClean(r rune) bool {
	return (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) || isBidiOrFormat(r) || isInvisible(r)
}

func FuzzClean(f *testing.F) {
	for _, s := range cleanSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := Clean(s)
		if len(out) > len(s) { // never grows: a removal only
			t.Fatalf("Clean(%q) = %q is longer than its input", s, out)
		}
		for _, r := range out {
			if forbiddenInClean(r) {
				t.Fatalf("Clean(%q) = %q keeps U+%04X", s, out, r)
			}
		}
		// Clean keeps a stray byte that is not a C1 control, by design (clean_test: "invalid UTF-8 that isn't C1 stays"), so only valid text must stay valid text.
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("Clean(%q) = %q turned valid UTF-8 into invalid", s, out)
		}
		if again := Clean(out); again != out {
			t.Fatalf("Clean is not idempotent: Clean(%q) = %q, then %q", s, out, again)
		}
	})
}

func FuzzCleanName(f *testing.F) {
	for _, s := range cleanSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := CleanName(s)
		// A stray byte becomes U+FFFD (three bytes), so growth is only checked for valid input.
		if utf8.ValidString(s) && len(out) > len(s) {
			t.Fatalf("CleanName(%q) = %q is longer than its input", s, out)
		}
		for _, r := range out {
			if forbiddenInClean(r) || unicode.Is(unicode.Cf, r) || invisibleInName(r) {
				t.Fatalf("CleanName(%q) = %q keeps U+%04X", s, out, r)
			}
			if r == '\n' || r == '\t' || (unicode.IsSpace(r) && r != ' ') {
				t.Fatalf("CleanName(%q) = %q keeps white space U+%04X: a name is on one line", s, out, r)
			}
		}
		if out != strings.TrimSpace(out) || strings.Contains(out, "  ") {
			t.Fatalf("CleanName(%q) = %q has loose spaces", s, out)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("CleanName(%q) = %q is not valid UTF-8", s, out)
		}
		if again := CleanName(out); again != out {
			t.Fatalf("CleanName is not idempotent: CleanName(%q) = %q, then %q", s, out, again)
		}
	})
}

// Removing a control byte can join the bytes around it into a character Clean removes (#6878, found by FuzzClean): a byte-order mark must not come out of a cleaner that ran once. (The other characters
// it removes need a continuation byte in 0x80-0x9f, which Clean drops as a stray C1 byte first, so they cannot be joined; the cases below check that too.)
func TestCleanRemovesWhatItsOwnRemovalsJoin(t *testing.T) {
	if got := Clean("a\xef\xbb\x13\xbfb"); got != "ab" {
		t.Errorf("a byte-order mark joined around a control byte stays: Clean = %q, want %q", got, "ab")
	}
	for _, in := range []string{"a\xe2\x80\x13\xaeb", "\xe2\x80\x1b[0m\xae", "\xe2\x80\x1b[0m\x1b[0m\xaex", "\xf3\xa0\x13\x80\x81", "\xef\xbb\x1b[0m\xbf", "\xef\xbb\x1b[0m\x1b[0m\xbf"} {
		got := Clean(in)
		for _, r := range got {
			if forbiddenInClean(r) {
				t.Errorf("Clean(%q) = %q keeps U+%04X", in, got, r)
			}
		}
		if Clean(got) != got {
			t.Errorf("Clean(%q) = %q is not stable", in, got)
		}
	}
}
