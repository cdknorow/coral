package board

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4745: the board message search. The parser's rules; then, on random boards, the same answer as a plain read of every message with
// the filters applied in Go, page by page in both directions, with the total on every page.

func TestParseMessageQuery(t *testing.T) {
	ok := func(p MessageQueryParams) *MessageQuery {
		t.Helper()
		q, err := ParseMessageQuery(p)
		require.NoError(t, err, "%+v", p)
		return q
	}
	q := ok(MessageQueryParams{})
	assert.Equal(t, MessageQueryDefaultLimit, q.Limit)
	assert.False(t, q.Oldest)
	q = ok(MessageQueryParams{Q: []string{"  Hello 100%_  "}, From: []string{"Backend Dev, QA", "qa"}, Mentions: []string{"Backend Dev"}, Since: []string{"2026-10-18T09:30:00+02:00"},
		Until: []string{"2026-10-19T00:00:00Z"}, Sort: []string{"oldest"}, Limit: []string{"200"}})
	assert.Equal(t, "hello 100%_", q.Text)
	assert.Equal(t, []string{"backend dev", "qa"}, q.From)
	assert.Equal(t, "backend dev", q.Mentions)
	assert.Equal(t, "2026-10-18T07:30:00Z", q.Since)
	assert.Equal(t, "2026-10-19T00:00:00Z", q.Until)
	assert.True(t, q.Oldest)
	assert.Equal(t, 200, q.Limit)
	for name, bad := range map[string]MessageQueryParams{
		"q too long": {Q: []string{strings.Repeat("a", MessageQueryMaxTextLen+1)}}, "q control": {Q: []string{"a\x00b"}}, "q twice": {Q: []string{"a", "b"}},
		"from empty part": {From: []string{"a,,b"}}, "from long": {From: []string{strings.Repeat("a", 201)}}, "from control": {From: []string{"a\nb"}},
		"from too many":  {From: []string{distinctValues(messageQueryMaxValues + 1)}},
		"mentions twice": {Mentions: []string{"a", "b"}}, "since bad": {Since: []string{"yesterday"}}, "until twice": {Until: []string{"2026-10-18T00:00:00Z", "2026-10-19T00:00:00Z"}},
		"sort unknown": {Sort: []string{"name"}}, "limit 0": {Limit: []string{"0"}}, "limit 201": {Limit: []string{"201"}}, "limit text": {Limit: []string{"ten"}}, "limit padded": {Limit: []string{" 5"}},
		"cursor junk": {Cursor: []string{"not base64!"}}, "cursor twice": {Cursor: []string{"a", "b"}},
	} {
		_, err := ParseMessageQuery(bad)
		require.Error(t, err, name)
		assert.ErrorIs(t, err, ErrMessageQuery, name)
	}
	// a cursor belongs to the filters and sort it was made for
	cur := messageCursor{Finger: q.finger, ID: 5}.encode()
	_, err := ParseMessageQuery(MessageQueryParams{Q: []string{"hello 100%_"}, From: []string{"backend dev,qa"}, Mentions: []string{"Backend Dev"}, Since: []string{"2026-10-18T07:30:00Z"},
		Until: []string{"2026-10-19T00:00:00Z"}, Sort: []string{"oldest"}, Cursor: []string{cur}})
	require.NoError(t, err, "the same filters in other words")
	_, err = ParseMessageQuery(MessageQueryParams{Q: []string{"other"}, Cursor: []string{cur}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "other filters")
	_, err = ParseMessageQuery(MessageQueryParams{Q: []string{"hello 100%_"}, From: []string{"backend dev,qa"}, Mentions: []string{"Backend Dev"}, Since: []string{"2026-10-18T07:30:00Z"},
		Until: []string{"2026-10-19T00:00:00Z"}, Cursor: []string{cur}}) // the sort differs
	require.Error(t, err)
}

var msgOracleSenders = []struct{ id, title string }{{"lead", "Orchestrator"}, {"dev", "Backend Dev"}, {"dev2", "Backend Dev 2"}, {"qa", "QA Engineer"}, {"sec", "Security Reviewer"}, {"ux", "UX Engineer"}}

var msgOracleTimes = []string{"2026-10-10T09:00:00Z", "2026-10-10T09:00:00Z", "2026-10-11T10:30:00Z", "2026-10-12T08:15:00Z", "2026-10-12T08:15:00Z", "2026-10-14T12:00:00Z", "2026-10-16T18:45:00Z", "2026-10-18T07:00:00Z"}

var msgOracleLines = []string{"Alpha ready", "BETA gamma landed", "100% sure", "a_b done", "ÉCOLE publique", "Straße", "key ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD here", "' OR 1=1 --",
	"@Backend Dev please look", "@QA Engineer ok?", "@all heads up", "@notify-all release", "@Backend Dev 2 take it", "email dev@example.invalid", "plain words", "UPPER lower Mixed", "日本語のテキスト", "mydev: not a tag"}

func seedMessageBoard(t *testing.T, rng *rand.Rand, n int) (*Store, context.Context) {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	for _, sd := range msgOracleSenders {
		_, err := sub(s, ctx, "proj", sd.id, sd.title)
		require.NoError(t, err)
	}
	sub(s, ctx, "other", "x", "Elsewhere")
	for i := 0; i < n; i++ {
		sender := msgOracleSenders[rng.Intn(len(msgOracleSenders))]
		parts := make([]string, 1+rng.Intn(3))
		for j := range parts {
			parts[j] = msgOracleLines[rng.Intn(len(msgOracleLines))]
		}
		m, err := s.PostMessage(ctx, "proj", sender.id, strings.Join(parts, " "), nil)
		require.NoError(t, err)
		_, err = s.db.ExecContext(ctx, `UPDATE board_messages SET created_at = ? WHERE id = ?`, msgOracleTimes[min(len(msgOracleTimes)-1, i*len(msgOracleTimes)/n)], m.ID)
		require.NoError(t, err)
		if rng.Intn(10) == 0 { // another board's message in between: never returned
			_, err = s.PostMessage(ctx, "other", "x", "Alpha elsewhere", nil)
			require.NoError(t, err)
		}
	}
	return s, ctx
}

// expectedMessageIDs: every message of the board, in the query's order, that passes the filters, applied here in Go from a plain read.
func expectedMessageIDs(t *testing.T, s *Store, ctx context.Context, q *MessageQuery) []int64 {
	t.Helper()
	all, err := s.ListMessages(ctx, "proj", 100000, 0, 0) // oldest first
	require.NoError(t, err)
	subs, err := s.ListSubscribers(ctx, "proj")
	require.NoError(t, err)
	numbered := s.numberedNames(ctx, s.reader(), "proj")
	mid, mtitle := q.Mentions, q.Mentions
	for _, sb := range subs {
		if strings.EqualFold(sb.SubscriberID, q.Mentions) || strings.EqualFold(sb.JobTitle, q.Mentions) {
			mid, mtitle = sb.SubscriberID, sb.JobTitle
			break
		}
	}
	shown := func(s string) string {
		if q.Shown != nil {
			return q.Shown(s)
		}
		return s
	}
	var ids []int64
	for _, m := range all {
		if q.Since != "" && m.CreatedAt < q.Since || q.Until != "" && m.CreatedAt >= q.Until {
			continue
		}
		if len(q.From) > 0 {
			ok := false
			for _, f := range q.From {
				ok = ok || strings.ToLower(shown(m.SubscriberID)) == f || strings.ToLower(shown(m.JobTitle)) == f
			}
			if !ok {
				continue
			}
		}
		if q.Mentions != "" && !containsExplicitTag(shown(m.Content), mid, mtitle, numbered) {
			continue
		}
		if q.Text != "" && !strings.Contains(strings.ToLower(shown(m.Content)), q.Text) {
			continue
		}
		ids = append(ids, m.ID)
	}
	if !q.Oldest {
		slices.Reverse(ids)
	}
	return ids
}

func idsOf(ms []Message) []int64 {
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func randomMessageParams(rng *rand.Rand) MessageQueryParams {
	var p MessageQueryParams
	if rng.Intn(3) != 0 {
		p.Q = []string{textOracleQueries[rng.Intn(len(textOracleQueries))]}
	}
	if rng.Intn(3) == 0 {
		var from []string
		for _, i := range rng.Perm(len(msgOracleSenders))[:1+rng.Intn(3)] {
			if rng.Intn(2) == 0 {
				from = append(from, msgOracleSenders[i].id)
			} else {
				from = append(from, strings.ToUpper(msgOracleSenders[i].title))
			}
		}
		p.From = []string{strings.Join(from, ",")}
	}
	if rng.Intn(4) == 0 {
		names := []string{"Backend Dev", "qa engineer", "dev2", "Orchestrator", "Nobody"}
		p.Mentions = []string{names[rng.Intn(len(names))]}
	}
	if rng.Intn(4) == 0 {
		p.Since = []string{msgOracleTimes[rng.Intn(len(msgOracleTimes))]}
	}
	if rng.Intn(4) == 0 {
		p.Until = []string{msgOracleTimes[rng.Intn(len(msgOracleTimes))]}
	}
	if rng.Intn(2) == 0 {
		p.Sort = []string{[]string{"newest", "oldest"}[rng.Intn(2)]}
	}
	return p
}

// pagesForward follows next_cursor from the first page; pagesBackward follows prev_cursor from the last. Each returns the pages.
func walkMessages(t *testing.T, s *Store, ctx context.Context, p MessageQueryParams, shown func(string) string, size int, start string, backward bool) []*MessageQueryResult {
	t.Helper()
	p.Limit = []string{fmt.Sprint(size)}
	var pages []*MessageQueryResult
	cursor := start
	for {
		pp := p
		if cursor != "" {
			pp.Cursor = []string{cursor}
		}
		q, err := ParseMessageQuery(pp)
		require.NoError(t, err, "%+v", pp)
		q.Project, q.Shown = "proj", shown
		res, err := s.QueryMessages(ctx, q)
		require.NoError(t, err)
		pages = append(pages, res)
		require.Less(t, len(pages), 3000, "paging does not end")
		next := res.NextCursor
		if backward {
			next = res.PrevCursor
		}
		if next == "" {
			return pages
		}
		require.NotEmpty(t, res.Messages)
		cursor = next
	}
}

func TestMessageQueryAgreesWithAPlainReadOfEveryMessage(t *testing.T) {
	seeds, queries := 3, 80
	if testing.Short() {
		seeds, queries = 2, 40
	}
	floors := map[string]int{}
	for seed := int64(1); seed <= int64(seeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		s, ctx := seedMessageBoard(t, rng, 160+rng.Intn(80))
		for i := 0; i < queries; i++ {
			p := randomMessageParams(rng)
			var shown func(string) string
			if rng.Intn(2) == 0 {
				shown = shownForTest
				floors["shown"]++
			}
			ref, err := ParseMessageQuery(p)
			require.NoError(t, err, "%+v", p)
			ref.Shown = shown
			want := expectedMessageIDs(t, s, ctx, ref)
			size := []int{1, 3, 7, 25, 200}[rng.Intn(5)]
			fwd := walkMessages(t, s, ctx, p, shown, size, "", false)
			var got []int64
			for pi, pg := range fwd {
				got = append(got, idsOf(pg.Messages)...)
				require.Equal(t, len(want), pg.Total, "the total is every match, on every page: %+v", p)
				if pi == 0 {
					require.Empty(t, pg.PrevCursor, "the first page has nothing before it")
				} else {
					require.NotEmpty(t, pg.PrevCursor)
				}
				if pi == len(fwd)-1 {
					require.Empty(t, pg.NextCursor)
				}
			}
			require.Equal(t, want, got, "seed %d query %d size %d: %+v shown=%v", seed, i, size, p, shown != nil)
			// and back again from the last page: the same pages in the other order
			if len(fwd) > 1 {
				last := fwd[len(fwd)-1]
				back := walkMessages(t, s, ctx, p, shown, size, last.PrevCursor, true)
				require.Len(t, back, len(fwd)-1, "%+v size %d", p, size)
				for bi, pg := range back {
					wantPage := fwd[len(fwd)-2-bi]
					require.Equal(t, idsOf(wantPage.Messages), idsOf(pg.Messages), "going back, page %d: %+v size %d", bi, p, size)
					require.Equal(t, len(want), pg.Total)
				}
				floors["paged back"]++
			}
			floors["queries"]++
			switch {
			case len(want) == 0:
				floors["no match"]++
			case len(fwd) > 1:
				floors["several pages"]++
			}
			add := func(on bool, name string) {
				if on && len(want) > 0 {
					floors[name]++
				}
			}
			add(ref.Text != "", "q")
			add(len(ref.From) > 0, "from")
			add(ref.Mentions != "", "mentions")
			add(ref.Since != "", "since")
			add(ref.Until != "", "until")
			add(ref.Oldest, "oldest")
			add(ref.Text == "%" || ref.Text == "_" || ref.Text == "100%", "a literal % or _")
		}
	}
	for name, floor := range map[string]int{"queries": 120, "no match": 10, "several pages": 40, "paged back": 40, "q": 40, "from": 15, "mentions": 8, "since": 8, "until": 8, "oldest": 30, "a literal % or _": 3, "shown": 60} {
		assert.GreaterOrEqual(t, floors[name], floor, "%s: %v", name, floors)
	}
}

// A cursor outlives its message: pages go on from where the id was, though the message was deleted, and a message that arrives
// meanwhile does not move the page behind the cursor.
func TestMessageQueryCursorSurvivesDeletionAndArrivals(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sub(s, ctx, "proj", "dev", "Backend Dev")
	var ids []int64
	for i := 0; i < 9; i++ {
		m, err := s.PostMessage(ctx, "proj", "dev", fmt.Sprintf("needle %d", i), nil)
		require.NoError(t, err)
		ids = append(ids, m.ID)
	}
	page := func(cursor string) *MessageQueryResult {
		p := MessageQueryParams{Q: []string{"needle"}, Limit: []string{"3"}, Sort: []string{"oldest"}}
		if cursor != "" {
			p.Cursor = []string{cursor}
		}
		q, err := ParseMessageQuery(p)
		require.NoError(t, err)
		q.Project = "proj"
		res, err := s.QueryMessages(ctx, q)
		require.NoError(t, err)
		return res
	}
	p1 := page("")
	assert.Equal(t, ids[0:3], idsOf(p1.Messages))
	found, err := s.DeleteMessage(ctx, "proj", ids[2])
	require.NoError(t, err)
	require.True(t, found)
	_, err = s.PostMessage(ctx, "proj", "dev", "needle new", nil)
	require.NoError(t, err)
	p2 := page(p1.NextCursor)
	assert.Equal(t, ids[3:6], idsOf(p2.Messages), "the page after the deleted message's place")
	assert.Equal(t, 9, p2.Total, "8 left of the first nine and the new one")
	require.NotEmpty(t, p2.PrevCursor)
	back := page(p2.PrevCursor)
	assert.Equal(t, []int64{ids[0], ids[1]}, idsOf(back.Messages), "back from the second page: what is before it, the deleted message gone")
	assert.Empty(t, back.PrevCursor)
}

// What a viewer cannot see is not matched: sender names, the text and the member a message tags are all tested as shown.
func TestMessageQueryMatchesWhatTheViewerSees(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sub(s, ctx, "proj", "dev", "Backend Dev")
	token := "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD"
	_, err := s.PostMessage(ctx, "proj", "dev", "deploy key "+token+" for @Backend Dev", nil)
	require.NoError(t, err)
	count := func(p MessageQueryParams, shown func(string) string) int {
		q, err := ParseMessageQuery(p)
		require.NoError(t, err)
		q.Project, q.Shown = "proj", shown
		res, err := s.QueryMessages(ctx, q)
		require.NoError(t, err)
		return res.Total
	}
	assert.Equal(t, 1, count(MessageQueryParams{Q: []string{"ghp_abcdefgh"}}, nil))
	assert.Equal(t, 0, count(MessageQueryParams{Q: []string{"ghp_abcdefgh"}}, shownForTest), "the token is not there to match through the hub")
	assert.Equal(t, 1, count(MessageQueryParams{Q: []string{"[redacted token]"}}, shownForTest))
	assert.Equal(t, 0, count(MessageQueryParams{Q: []string{"[redacted token]"}}, nil))
	assert.Equal(t, 1, count(MessageQueryParams{Mentions: []string{"Backend Dev"}}, shownForTest), "a tag the redaction leaves is still a tag")
	assert.Equal(t, 1, count(MessageQueryParams{From: []string{"backend dev"}, Q: []string{"deploy"}}, shownForTest))
	assert.Equal(t, 0, count(MessageQueryParams{From: []string{"someone else"}}, nil))
	// a sender whose id is a token: filtered by the marker the viewer sees, never by the token
	sub(s, ctx, "proj", token, token)
	_, err = s.PostMessage(ctx, "proj", token, "from the token holder", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, count(MessageQueryParams{From: []string{token}}, nil))
	assert.Equal(t, 0, count(MessageQueryParams{From: []string{token}}, shownForTest))
	assert.Equal(t, 1, count(MessageQueryParams{From: []string{"[redacted token]"}}, shownForTest))
	assert.Equal(t, 0, count(MessageQueryParams{From: []string{"[redacted token]"}}, nil))
}

// "@Backend Dev 2" tags the numbered agent, not "Backend Dev", when the board has both (the rule unread counts use).
func TestMessageQueryMentionsAreNumberAware(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sub(s, ctx, "proj", "Backend Dev", "Backend Dev") // an agent's id is its name, numbered when there are several
	sub(s, ctx, "proj", "Backend Dev 2", "Backend Dev 2")
	sub(s, ctx, "proj", "Orchestrator", "Orchestrator")
	for _, text := range []string{"@Backend Dev 2 take it", "@Backend Dev look", "@all hello"} {
		_, err := s.PostMessage(ctx, "proj", "Orchestrator", text, nil)
		require.NoError(t, err)
	}
	texts := func(member string) []string {
		q, err := ParseMessageQuery(MessageQueryParams{Mentions: []string{member}, Sort: []string{"oldest"}})
		require.NoError(t, err)
		q.Project = "proj"
		res, err := s.QueryMessages(ctx, q)
		require.NoError(t, err)
		var out []string
		for _, m := range res.Messages {
			out = append(out, m.Content)
		}
		return out
	}
	assert.Equal(t, []string{"@Backend Dev look", "@all hello"}, texts("Backend Dev"))
	assert.Equal(t, []string{"@Backend Dev 2 take it", "@all hello"}, texts("Backend Dev 2"))
}

// The scan is bounded: past its row bound the search stops and says so.
func TestMessageQueryScanIsBounded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sub(s, ctx, "proj", "dev", "Backend Dev")
	for i := 0; i < 30; i++ {
		_, err := s.PostMessage(ctx, "proj", "dev", fmt.Sprintf("needle %d", i), nil)
		require.NoError(t, err)
	}
	run := func() *MessageQueryResult {
		q, err := ParseMessageQuery(MessageQueryParams{Q: []string{"needle"}, Limit: []string{"100"}})
		require.NoError(t, err)
		q.Project = "proj"
		res, err := s.QueryMessages(ctx, q)
		require.NoError(t, err)
		return res
	}
	res := run()
	assert.Equal(t, 30, res.Total)
	assert.False(t, res.Truncated)
	rows, bytes := messageScanRows, messageScanBytes
	t.Cleanup(func() { messageScanRows, messageScanBytes = rows, bytes })
	messageScanRows = 10
	res = run()
	assert.True(t, res.Truncated)
	assert.Equal(t, 10, res.Total)
	messageScanRows = rows
	messageScanBytes = 20
	res = run()
	assert.True(t, res.Truncated)
	assert.Less(t, res.Total, 30)
}
