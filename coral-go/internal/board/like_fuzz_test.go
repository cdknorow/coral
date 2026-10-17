package board

import (
	"strings"
	"sync"
	"testing"
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
var (
	likeDBOnce sync.Once
	likeDB     *sqlx.DB
)

func sqliteForLike(t testing.TB) *sqlx.DB {
	likeDBOnce.Do(func() {
		db, err := sqlx.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		likeDB = db
	})
	return likeDB
}

func FuzzLikeAgainstSQLite(f *testing.F) {
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
		if err := sqliteForLike(t).QueryRow(`SELECT ? LIKE ? COLLATE NOCASE`, content, pat).Scan(&want); err != nil {
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
