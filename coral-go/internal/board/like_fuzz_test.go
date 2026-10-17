package board

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
)

// FuzzLikeAgainstSQLite (#4147): likeAny, the Go copy of SQLite's LIKE that counts @-mentions for the
// notifier's batch (#4121), against SQLite itself, asked the way CheckUnread asks (content LIKE
// "%term%" COLLATE NOCASE). For text without a NUL byte and valid UTF-8 (the two inputs where Go and
// SQLite read the bytes differently, and which a message cannot carry: it arrives through JSON) the
// answers are equal, whatever the term holds: % and _ wildcards, ASCII case, non-ASCII letters,
// combining marks, spaces. A plain go test runs the seeds; go test -fuzz FuzzLikeAgainstSQLite
// -fuzztime 10m ./internal/board/ fuzzes.
func FuzzLikeAgainstSQLite(f *testing.F) {
	// One in-memory SQLite for every input, closed when the fuzz target ends (#4167's leak guard
	// found its connection opener still running when it was opened once per process).
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		f.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	f.Cleanup(func() { db.Close() })
	for _, s := range [][2]string{
		{"@Backend Dev please look", "@Backend Dev"}, {"@BACKENDDEV case", "@backenddev"}, {"@Lead Developerx not a tag", "@Lead Developer"},
		{"50% done", "50%"}, {"a_b", "a_b"}, {"aXb", "a_b"}, {"axxb", "a%b"}, {"cafÉ", "café"}, {"CAFE", "café"}, {"ßa", "SSA"},
		{"trailing space ", "space "}, {"", "x"}, {"x", ""}, {"@all heads up", "@all"}, {"@notify_all now", "@notify_all"}, {"@notifyXall", "@notify_all"},
		{"\U0001f600 @dev", "@dev"}, {"é", "e"}, {"%", "%"}, {"_", "_"}, {"a\nb", "a_b"}, {"K", "k"}, {"İ", "i"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, content, term string) {
		if !utf8.ValidString(content) || !utf8.ValidString(term) || strings.ContainsRune(content, 0) || strings.ContainsRune(term, 0) {
			t.Skip()
		}
		pat := "%" + term + "%"
		var want bool
		if err := db.QueryRow(`SELECT ? LIKE ? COLLATE NOCASE`, content, pat).Scan(&want); err != nil {
			t.Fatal(err)
		}
		if got := likeAny(content, []string{pat}); got != want {
			t.Fatalf("content %q term %q: SQLite says %v, likeAny says %v", content, term, want, got)
		}
		// likeMatch on its own (the general pattern path) agrees too.
		if got := likeMatch(content, pat); got != want {
			t.Fatalf("content %q pattern %q: SQLite says %v, likeMatch says %v", content, pat, want, got)
		}
	})
}

// A term with several wildcards over a long message is answered in time proportional to the product of
// their lengths, not a power of the message length (FuzzLikeAgainstSQLite stalled on this input).
func TestLikeMatchManyWildcardsOverLongTextIsBounded(t *testing.T) {
	long := "0000000000" + strings.Repeat("\x13", 60000) + "0"
	start := time.Now()
	for _, p := range []string{"%0%_%1%", "%0%_%_%_%1%", "%a%b%c%d%e%f%g%", "%_%_%_%_%_%_%x"} {
		if likeMatch(long, p) {
			t.Fatalf("%q should not match", p)
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("matching took %s", d)
	}
}
