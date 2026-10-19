package querypage

import (
	"slices"
	"testing"
	"unicode"
)

// Fuzz targets for what the search routes take from the outside (#5002). A plain go test runs the seeds;
// go test -fuzz FuzzHasControl -fuzztime 10m ./internal/querypage/ fuzzes.

// FuzzHasControl: HasControl is exactly "some rune of s is a control character", on any bytes (an invalid byte reads
// as U+FFFD, which is not one), and every control case the routes' tests use is refused.
func FuzzHasControl(f *testing.F) {
	for _, s := range append(ControlCases(), "", "a b", "tab\there", "\xff\xfe", "\u2028", "\u200b", "é", "\x00", "x\x7f", "\u0085") {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want := false
		for _, r := range s {
			if unicode.IsControl(r) {
				want = true
			}
		}
		if got := HasControl(s); got != want {
			t.Fatalf("HasControl(%q) = %v, want %v", s, got, want)
		}
		// Cutting or padding the text with white space never hides a control character (the routes check before they trim).
		if HasControl(s) != HasControl(" "+s+" ") {
			t.Fatalf("padding changed the answer for %q", s)
		}
	})
}

// FuzzPager: whatever the limit, the cursor and the direction, a page is at most Limit matches, in the stream's
// order and next to each other, Total counts every match, and Has{Next,Prev} say exactly whether a match lies beyond it.
func FuzzPager(f *testing.F) {
	f.Add(uint8(10), uint8(3), uint8(5), true, false)
	f.Add(uint8(0), uint8(1), uint8(0), false, false)
	f.Add(uint8(40), uint8(7), uint8(39), true, true)
	f.Add(uint8(40), uint8(7), uint8(255), true, true)
	f.Add(uint8(5), uint8(200), uint8(2), true, false)
	f.Fuzz(func(t *testing.T, n, limit, cur uint8, hasCursor, prev bool) {
		N, lim, c := int(n), int(limit)%12+1, int(cur)
		p := &Pager[int]{Limit: lim, Prev: prev && hasCursor}
		if hasCursor {
			p.After = func(x int) bool { return x > c }
			p.AtCursor = func(x int) bool { return x == c }
		}
		for i := 0; i < N; i++ {
			p.Add(i)
		}
		if p.Total != N {
			t.Fatalf("total %d of %d", p.Total, N)
		}
		if len(p.Page) > lim {
			t.Fatalf("a page of %d with limit %d", len(p.Page), lim)
		}
		for i := 1; i < len(p.Page); i++ {
			if p.Page[i] != p.Page[i-1]+1 {
				t.Fatalf("page %v is not a run of the stream", p.Page)
			}
		}
		if len(p.Page) == 0 {
			if p.HasNext() || p.HasPrev() {
				t.Fatalf("an empty page with neighbours")
			}
			return
		}
		first, last := p.Page[0], p.Page[len(p.Page)-1]
		// Matches before the page and after it, by the stream itself.
		if p.HasPrev() != (first > 0) {
			t.Fatalf("HasPrev %v for page %v (n %d, cursor %v %d, prev %v, limit %d)", p.HasPrev(), p.Page, N, hasCursor, c, p.Prev, lim)
		}
		if p.HasNext() != (last < N-1) {
			t.Fatalf("HasNext %v for page %v (n %d, cursor %v %d, prev %v, limit %d)", p.HasNext(), p.Page, N, hasCursor, c, p.Prev, lim)
		}
		// The page is the right one: the first Limit after the cursor (or from the start), or the last Limit before it.
		var want []int
		switch {
		case !hasCursor:
			want = seq(0, min(N, lim))
		case !p.Prev:
			want = seq(min(c+1, N), min(N, c+1+lim))
		default:
			hi := min(c, N)
			want = seq(max(0, hi-lim), hi)
		}
		if !slices.Equal(p.Page, want) {
			t.Fatalf("page %v, want %v (n %d, cursor %v %d, prev %v, limit %d)", p.Page, want, N, hasCursor, c, p.Prev, lim)
		}
	})
}

func seq(from, to int) []int {
	var out []int
	for i := from; i < to; i++ {
		out = append(out, i)
	}
	return out
}
