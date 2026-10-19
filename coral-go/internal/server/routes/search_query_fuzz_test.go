package routes

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParseSearchQuery (#5002): an agent's history search takes a free text, with "quoted phrases", from the URL. Whatever it is, the
// parser never panics and splits it into phrases and terms that are non-empty, lower case, trimmed, terms without white space, and
// together no longer than the text grown by lower-casing. Without a quote there are no phrases, and splitting the same text twice answers the same.
// go test -fuzz FuzzParseSearchQuery -fuzztime 10m ./internal/server/routes/ fuzzes.
func FuzzParseSearchQuery(f *testing.F) {
	for _, s := range []string{"", "  ", "hello world", `"exact phrase" other`, `"open phrase`, `a"b"c`, `""`, `" "`, `"a" "b" "c`, "\"\x00\"", "İİ \"İ\"", "\xff\"\xfe\"", "tab\tsep \u00a0nbsp", strings.Repeat(`"x`, 500)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		phrases, terms := parseSearchQuery(raw)
		p2, t2 := parseSearchQuery(raw)
		if strings.Join(phrases, "\x00") != strings.Join(p2, "\x00") || strings.Join(terms, "\x00") != strings.Join(t2, "\x00") {
			t.Fatalf("two parses differ for %q", raw)
		}
		total := 0
		for _, ph := range phrases {
			if ph == "" || ph != strings.TrimSpace(ph) || ph != strings.ToLower(ph) {
				t.Fatalf("phrase %q from %q", ph, raw)
			}
			total += len(ph)
		}
		for _, tm := range terms {
			if tm == "" || strings.ContainsAny(tm, " \t\n\r") || tm != strings.ToLower(tm) {
				t.Fatalf("term %q from %q", tm, raw)
			}
			total += len(tm)
		}
		if total > 3*len(raw) {
			t.Fatalf("%d bytes of terms from %d bytes", total, len(raw))
		}
		if !strings.Contains(raw, `"`) && len(phrases) != 0 {
			t.Fatalf("phrases %q from %q with no quote", phrases, raw)
		}
		if utf8.ValidString(raw) {
			for _, s := range append(append([]string{}, phrases...), terms...) {
				if !utf8.ValidString(s) {
					t.Fatalf("invalid UTF-8 %q from valid %q", s, raw)
				}
			}
		}
	})
}
