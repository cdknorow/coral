package routes

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// #5372: cleanAnchor takes the text a viewer selected in an artifact and keeps it in the database and on replies, so it validates sizes and lines
// and puts the quote and its context through the redactor. Whatever the anchor: no panic, a whole-document anchor carries no text, an accepted one is
// valid UTF-8 within its own rules, and a token planted in the quote never survives.
//
//	go test -tags fts5 ./internal/server/routes -run '^$' -fuzz FuzzCleanAnchor -fuzztime 10m
func FuzzCleanAnchor(f *testing.F) {
	f.Add("selected text", "before ", " after", 3, 5, false)
	f.Add("", "", "", 0, 0, true)
	f.Add("   ", "", "", 0, 0, false)
	f.Add("quote\x00nul", "", "", 1, 1, false)
	f.Add("bad \xff utf8", "", "", 1, 1, false)
	f.Add("q", "p", "s", -1, 4, false)
	f.Add("q", "p", "s", 9, 4, false)
	f.Add("q", "p", "s", 10_000_001, 0, false)
	f.Add(strings.Repeat("x", 3000), "", "", 1, 1, false)
	f.Fuzz(func(t *testing.T, quote, prefix, suffix string, start, end int, whole bool) {
		const planted = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
		a := &commentAnchor{Quote: quote + " " + planted + " " + quote, Prefix: prefix, Suffix: suffix, StartLine: start, EndLine: end, Whole: whole}
		before := *a
		msg, unchecked := cleanAnchor(a)
		if whole {
			if (*a != commentAnchor{Whole: true}) {
				t.Fatalf("a whole-document anchor kept text: %+v", *a)
			}
			if msg != "" || unchecked {
				t.Fatalf("a whole-document anchor was refused: %q %v", msg, unchecked)
			}
			return
		}
		if msg != "" || unchecked {
			return
		}
		for _, s := range []string{a.Quote, a.Prefix, a.Suffix} {
			if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
				t.Fatalf("an accepted anchor holds invalid text: %+v", *a)
			}
		}
		if a.StartLine < 0 || a.EndLine < 0 || (a.EndLine != 0 && a.EndLine < a.StartLine) {
			t.Fatalf("an accepted anchor has lines %d..%d (was %d..%d)", a.StartLine, a.EndLine, before.StartLine, before.EndLine)
		}
		if !strings.Contains(quote+prefix+suffix, planted[4:20]) && (strings.Contains(a.Quote, planted) || strings.Contains(a.Prefix, planted) || strings.Contains(a.Suffix, planted)) {
			t.Fatalf("the planted token survived the anchor: %+v", *a)
		}
	})
}
