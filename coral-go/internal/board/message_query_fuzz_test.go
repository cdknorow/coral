package board

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/querypage"
)

// FuzzParseMessageQuery (#5002): the message search's raw parameters come from the URL. Whatever they are, the parser never panics,
// answers the same twice, and what it accepts is bounded and clean: no control characters, a text and values within their caps (a
// lower-cased copy can be longer than its input by at most a character's growth), times that are RFC 3339 in UTC, a limit in range.
// Accepted text is stable: parsing it again answers the same text.
// go test -fuzz FuzzParseMessageQuery -fuzztime 10m ./internal/board/ fuzzes.
func FuzzParseMessageQuery(f *testing.F) {
	f.Add("hello", "a,b", "lead", "2026-10-18T00:00:00Z", "2026-10-19T00:00:00Z", "newest", "50", "")
	f.Add("  Mixed Case  ", "A, a ,B", "", "", "", "oldest", "200", "")
	f.Add("\x01x", "x", "", "", "", "", "", "")
	f.Add("x", ",", "", "not a time", "", "sideways", "0", "garbage")
	f.Add(strings.Repeat("a", 201), strings.Repeat("b", 201), strings.Repeat("c", 201), "", "", "", "9999999999999999999", "")
	f.Add("İİİİ", "İ", "İ", "2026-10-18T00:00:00+02:00", "2026-10-18T00:00:00.123456789Z", "", "-1", "e30=")
	f.Fuzz(func(t *testing.T, q, from, mentions, since, until, sort, limit, cursor string) {
		p := MessageQueryParams{Q: []string{q}, From: []string{from}, Mentions: []string{mentions}, Since: []string{since}, Until: []string{until},
			Sort: []string{sort}, Limit: []string{limit}, Cursor: []string{cursor}}
		got, err := ParseMessageQuery(p)
		again, err2 := ParseMessageQuery(p)
		if (err == nil) != (err2 == nil) {
			t.Fatalf("answers differ between two parses: %v / %v", err, err2)
		}
		if err != nil {
			if got != nil {
				t.Fatalf("an error with a query")
			}
			return
		}
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("two parses differ:\n %+v\n %+v", got, again)
		}
		for name, v := range map[string]string{"text": got.Text, "mentions": got.Mentions} {
			if querypage.HasControl(v) || len(v) > 2*MessageQueryMaxTextLen {
				t.Fatalf("%s %q accepted", name, v)
			}
		}
		if len(got.From) > messageQueryMaxValues {
			t.Fatalf("%d from values", len(got.From))
		}
		for _, v := range got.From {
			if v == "" || querypage.HasControl(v) || len(v) > 2*200 || v != strings.TrimSpace(v) {
				t.Fatalf("from value %q accepted", v)
			}
		}
		for _, v := range []string{got.Since, got.Until} {
			if v == "" {
				continue
			}
			tm, perr := time.Parse(time.RFC3339, v)
			if perr != nil || tm.Location() != time.UTC || tm.Format(time.RFC3339) != v {
				t.Fatalf("time %q accepted", v)
			}
		}
		if got.Limit < 1 || got.Limit > MessageQueryMaxLimit {
			t.Fatalf("limit %d accepted", got.Limit)
		}
		if got.Text != "" {
			p2 := MessageQueryParams{Q: []string{got.Text}}
			if again2, err3 := ParseMessageQuery(p2); err3 != nil || again2.Text != got.Text {
				t.Fatalf("the accepted text %q does not parse to itself (%v)", got.Text, err3)
			}
		}
	})
}
