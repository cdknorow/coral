package querypage

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

// The pager gives the page a plain slice gives: from every cursor position, forward and back, for every page size, over streams of
// every length, and says there are neighbours exactly when there are.
func TestPagerAgreesWithSlicing(t *testing.T) {
	rng := rand.New(rand.NewSource(4745))
	floors := map[string]int{}
	for trial := 0; trial < 3000; trial++ {
		n := rng.Intn(30)
		// ids ascend; the stream holds the matches only, so the cursor may name an id that is not a match
		ids := make([]int, n)
		next := 1
		for i := range ids {
			next += 1 + rng.Intn(3)
			ids[i] = next
		}
		limit := 1 + rng.Intn(8)
		cursor := 0
		if n > 0 && rng.Intn(3) != 0 {
			cursor = ids[rng.Intn(n)] - rng.Intn(2) // a match's id, or the id before it
		}
		for _, prev := range []bool{false, true} {
			if cursor == 0 && prev {
				continue
			}
			p := &Pager[int]{Limit: limit, Prev: prev}
			if cursor != 0 {
				c := cursor
				p.After = func(id int) bool { return id > c }
				p.AtCursor = func(id int) bool { return id == c }
			}
			for _, id := range ids {
				p.Add(id)
			}
			var want []int
			var wantNext, wantPrev bool
			switch {
			case cursor == 0:
				want = ids[:min(limit, n)]
				wantNext = n > limit
			case !prev:
				var after []int
				for _, id := range ids {
					if id > cursor {
						after = append(after, id)
					}
				}
				want = after[:min(limit, len(after))]
				wantNext = len(after) > limit
				wantPrev = len(after) < n
			default:
				var before []int
				for _, id := range ids {
					if id < cursor {
						before = append(before, id)
					}
				}
				start := max(0, len(before)-limit)
				want = before[start:]
				wantPrev = start > 0
				wantNext = len(want) > 0 && len(before) < n
			}
			require.Equal(t, len(ids), p.Total)
			require.Equal(t, append([]int(nil), want...), append([]int(nil), p.Page...), "ids %v limit %d cursor %d prev %v", ids, limit, cursor, prev)
			require.Equal(t, wantNext && len(want) > 0, p.HasNext(), "next: ids %v limit %d cursor %d prev %v", ids, limit, cursor, prev)
			require.Equal(t, wantPrev && len(want) > 0, p.HasPrev(), "prev: ids %v limit %d cursor %d prev %v", ids, limit, cursor, prev)
			switch {
			case cursor == 0:
				floors["first page"]++
			case prev:
				floors["previous page"]++
			default:
				floors["next page"]++
			}
			if len(want) == 0 {
				floors["empty page"]++
			}
			if wantNext && wantPrev {
				floors["both neighbours"]++
			}
		}
	}
	for name, floor := range map[string]int{"first page": 800, "previous page": 500, "next page": 500, "empty page": 100, "both neighbours": 200} {
		require.GreaterOrEqual(t, floors[name], floor, "%s: %v", name, floors)
	}
}
