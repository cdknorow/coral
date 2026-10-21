package store

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/querypage"
)

// FuzzParseEventQuery (#5002): the agent events' search parameters come from the URL. The parser never panics, answers the same twice,
// and what it accepts is bounded and clean: type words in the lower-case form, at most eventQueryMaxTypes, a text within its cap without
// control characters, times in the stored layout, a limit in range.
// go test -fuzz FuzzParseEventQuery -fuzztime 10m ./internal/store/ fuzzes.
func FuzzParseEventQuery(f *testing.F) {
	f.Add("tool_use,stop", "hello", "2026-10-18T00:00:00Z", "2026-10-19T00:00:00Z", "newest", "50", "")
	f.Add("tool_use", "  Mixed  ", "", "", "oldest", "200", "")
	f.Add("Tool Use", "\x00", "bad", "", "up", "0", "xx")
	f.Add(strings.Repeat("a,", 30), strings.Repeat("q", 201), "", "", "", "-5", "")
	f.Add("", "İİİİ", "2026-10-18T00:00:00+02:00", "2026-10-18T00:00:00.5Z", "", "9223372036854775808", "e30=")
	f.Fuzz(func(t *testing.T, typ, q, since, until, sort, limit, cursor string) {
		p := EventQueryParams{Type: []string{typ}, Q: []string{q}, Since: []string{since}, Until: []string{until}, Sort: []string{sort}, Limit: []string{limit}, Cursor: []string{cursor}}
		sid := "sess"
		got, err := ParseEventQuery("agent", &sid, p)
		again, err2 := ParseEventQuery("agent", &sid, p)
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
		if len(got.Types) > eventQueryMaxTypes {
			t.Fatalf("%d types", len(got.Types))
		}
		for _, ty := range got.Types {
			if !eventTypeRe.MatchString(ty) {
				t.Fatalf("type %q accepted", ty)
			}
		}
		if querypage.HasControl(got.Text) || len(got.Text) > 2*EventQueryMaxTextLen || got.Text != strings.TrimSpace(got.Text) {
			t.Fatalf("text %q accepted", got.Text)
		}
		for _, v := range []string{got.Since, got.Until} {
			if v == "" {
				continue
			}
			if _, perr := time.Parse(ISOFormat, v); perr != nil {
				t.Fatalf("time %q accepted: %v", v, perr)
			}
		}
		if got.Limit < 1 || got.Limit > EventQueryMaxLimit {
			t.Fatalf("limit %d accepted", got.Limit)
		}
	})
}
