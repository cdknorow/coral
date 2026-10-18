package board

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4745: q on the task query. It is a plain, case-insensitive substring of the title, the body or the completion message, matched in
// Go on what a viewer sees; the parser refuses what a text search should not take.

func TestParseTaskQueryText(t *testing.T) {
	ok := func(raw []string, want string) {
		t.Helper()
		q, err := ParseTaskQuery(TaskQueryParams{Q: raw})
		require.NoError(t, err, "%q", raw)
		assert.Equal(t, want, q.Text, "%q", raw)
	}
	ok(nil, "")
	ok([]string{""}, "")
	ok([]string{"   "}, "")
	ok([]string{"  Hello World  "}, "hello world")
	ok([]string{"100%_done"}, "100%_done")
	ok([]string{"ÉCOLE"}, "école")
	ok([]string{strings.Repeat("a", TaskQueryMaxTextLen)}, strings.Repeat("a", TaskQueryMaxTextLen))
	for _, bad := range [][]string{{strings.Repeat("a", TaskQueryMaxTextLen+1)}, {"a\x00b"}, {"line\nbreak"}, {"a\rb"}, {"tab\there"}, {"x", "y"}} {
		_, err := ParseTaskQuery(TaskQueryParams{Q: bad})
		require.Error(t, err, "%q", bad)
		assert.ErrorIs(t, err, ErrTaskQuery)
	}
}

var textOracleVocab = []string{"Alpha", "BETA gamma", "100% sure", "a_b", "ÉCOLE publique", "Straße", "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD", "' OR 1=1 --",
	"line one", "x", "Delta force", "[redacted token] note", "plain words here", "UPPER lower Mixed", "日本語のテキスト", ""}

var textOracleQueries = []string{"alpha", "BETA", "beta gamma", "%", "_", "100%", "a_b", "école", "ÉCOLE", "strasse", "straße", "ghp_", "ghp_abcdefghijklmnopqrstuvwxyz0123456789abcd",
	"redacted", "' or 1=1", "one", "x", "delta", "mixed", "日本", "zzz-no-such-text", "task 1", " alpha "}

var textOracleSecret = regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`)

// shownForTest is a viewer's redaction: a token becomes a marker.
func shownForTest(s string) string { return textOracleSecret.ReplaceAllString(s, "[redacted token]") }

// seedTexts gives random tasks of the oracle board texts from the vocabulary in the title, the body and the completion message.
func seedTexts(t *testing.T, e *verEnv, rng *rand.Rand) {
	t.Helper()
	var ids []int64
	require.NoError(t, e.s.db.SelectContext(e.ctx, &ids, `SELECT id FROM board_tasks ORDER BY id`))
	pick := func() string {
		n := 1 + rng.Intn(3)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = textOracleVocab[rng.Intn(len(textOracleVocab))]
		}
		return strings.Join(parts, " ")
	}
	for _, id := range ids {
		var title string
		require.NoError(t, e.s.db.GetContext(e.ctx, &title, `SELECT title FROM board_tasks WHERE id = ?`, id))
		if rng.Intn(3) == 0 {
			title += " " + pick()
		}
		_, err := e.s.db.ExecContext(e.ctx, `UPDATE board_tasks SET title = ?, body = ?, completion_message = ? WHERE id = ?`, title, pick(), pick(), id)
		require.NoError(t, err)
	}
}

type textRow struct{ title, body, completion string }

// expectedTextMatches is, in the query's order, the ids the same filters (without q) give whose title, body or completion message hold
// q (lower-cased) in what shown makes of them: the reference, from the plain query and a plain read of the texts.
func expectedTextMatches(t *testing.T, e *verEnv, p TaskQueryParams, text string, shown func(string) string) []int64 {
	t.Helper()
	p.Q, p.Cursor, p.Fields, p.Limit = nil, nil, []string{"id"}, []string{fmt.Sprint(TaskQueryMaxLimit)}
	var all []int64
	for {
		q, err := ParseTaskQuery(p)
		require.NoError(t, err, "%+v", p)
		res, err := e.s.QueryTasks(e.ctx, q)
		require.NoError(t, err)
		for _, m := range res.Tasks {
			all = append(all, m["id"].(int64))
		}
		if res.NextCursor == "" {
			break
		}
		p.Cursor = []string{res.NextCursor}
	}
	texts := map[int64]textRow{}
	var rows []struct {
		ID         int64   `db:"id"`
		Title      string  `db:"title"`
		Body       *string `db:"body"`
		Completion *string `db:"completion_message"`
	}
	require.NoError(t, e.s.db.SelectContext(e.ctx, &rows, `SELECT id, title, body, completion_message FROM board_tasks`))
	for _, r := range rows {
		tr := textRow{title: r.Title}
		if r.Body != nil {
			tr.body = *r.Body
		}
		if r.Completion != nil {
			tr.completion = *r.Completion
		}
		texts[r.ID] = tr
	}
	var want []int64
	for _, id := range all {
		for _, f := range []string{texts[id].title, texts[id].body, texts[id].completion} {
			if f != "" && strings.Contains(strings.ToLower(shown(f)), text) {
				want = append(want, id)
				break
			}
		}
	}
	return want
}

// pagesOf follows the query's cursors with the given page size and returns the ids in order, each page's total, and the number of pages.
func pagesOf(t *testing.T, e *verEnv, p TaskQueryParams, shown func(string) string, size int) (ids []int64, totals map[int]bool, pages int) {
	t.Helper()
	p.Fields, p.Limit, p.Cursor = []string{"id"}, []string{fmt.Sprint(size)}, nil
	totals = map[int]bool{}
	for {
		q, err := ParseTaskQuery(p)
		require.NoError(t, err, "%+v", p)
		q.Shown = shown
		res, err := e.s.QueryTasks(e.ctx, q)
		require.NoError(t, err)
		pages++
		totals[res.Total] = true
		assert.LessOrEqual(t, len(res.Tasks), size)
		for _, m := range res.Tasks {
			ids = append(ids, m["id"].(int64))
		}
		if res.NextCursor == "" {
			return ids, totals, pages
		}
		require.NotEmpty(t, res.Tasks, "a next cursor with no rows")
		p.Cursor = []string{res.NextCursor}
		require.Less(t, pages, 2000, "paging does not end")
	}
}

// With any other filters, a q query answers the plain query's rows that hold q (case-folded, % and _ ordinary, matched on the shown
// text), in the same order, page after page, with the total of every match on every page. Floors: every kind of outcome is reached.
func TestTaskQueryTextAgreesWithAPlainReadOfTheTexts(t *testing.T) {
	seeds := 4
	if testing.Short() {
		seeds = 2
	}
	floors := map[string]int{}
	for seed := int64(1); seed <= int64(seeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		e := seedTaskQueryOracle(t, rng)
		seedTexts(t, e, rng)
		for i := 0; i < 120; i++ {
			p := randomValidTaskQuery(rng)
			p.Q = nil
			text := strings.ToLower(strings.TrimSpace(textOracleQueries[rng.Intn(len(textOracleQueries))]))
			var shown func(string) string
			if rng.Intn(2) == 0 {
				shown = shownForTest
				floors["shown"]++
			}
			want := expectedTextMatches(t, e, p, text, func(s string) string {
				if shown != nil {
					return shown(s)
				}
				return s
			})
			size := []int{1, 3, 7, 50, 100}[rng.Intn(5)]
			p.Q = []string{text}
			got, totals, pages := pagesOf(t, e, p, shown, size)
			require.Equal(t, want, got, "seed %d query %d q=%q size %d shown=%v: %+v", seed, i, text, size, shown != nil, p)
			require.Equal(t, map[int]bool{len(want): true}, totals, "the total is every match, on every page: q=%q", text)
			floors["queries"]++
			switch {
			case len(want) == 0:
				floors["no match"]++
			case pages > 1:
				floors["several pages"]++
			default:
				floors["one page"]++
			}
			switch text {
			case "%", "_", "100%", "a_b":
				floors["a literal % or _"]++
				if len(want) > 0 {
					floors["a literal % or _ that matched"]++
				}
			case "école", "straße", "日本":
				floors["unicode"]++
			case "ghp_", "redacted":
				floors["token or marker"]++
			}
		}
	}
	for name, floor := range map[string]int{"queries": 200, "no match": 15, "several pages": 40, "one page": 15, "a literal % or _": 15, "a literal % or _ that matched": 3, "unicode": 8, "token or marker": 8, "shown": 80} {
		assert.GreaterOrEqual(t, floors[name], floor, "%s: %v", name, floors)
	}
}

// What a viewer cannot see is not matched: a task holding a token is found by the marker the viewer sees in its place, never by the token.
func TestTaskQueryTextMatchesWhatTheViewerSees(t *testing.T) {
	e := newVerEnv(t)
	token := "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD"
	_, err := e.s.db.ExecContext(e.ctx, `INSERT INTO board_tasks (board_id, title, body, status, priority, created_by, created_at) VALUES ('team', 'deploy key', ?, 'pending', 'low', 'lead', '2026-10-01T09:00:00Z')`, "the token is "+token)
	require.NoError(t, err)
	find := func(text string, shown func(string) string) int {
		q, err := ParseTaskQuery(TaskQueryParams{Q: []string{text}, Fields: []string{"id"}})
		require.NoError(t, err)
		q.Shown = shown
		res, err := e.s.QueryTasks(e.ctx, q)
		require.NoError(t, err)
		return res.Total
	}
	assert.Equal(t, 1, find("ghp_abcdefgh", nil), "stored text, as a local caller sees it")
	assert.Equal(t, 0, find("ghp_abcdefgh", shownForTest), "through the hub the token is not there to match")
	assert.Equal(t, 1, find("[redacted token]", shownForTest), "the marker a viewer sees is")
	assert.Equal(t, 0, find("[redacted token]", nil), "and a local caller's text has none")
}

// The scan is bounded: past its row bound the search stops and says so, and what it counted is what it read.
func TestTaskQueryTextScanIsBounded(t *testing.T) {
	e := newVerEnv(t)
	for i := 0; i < 30; i++ {
		_, err := e.s.db.ExecContext(e.ctx, `INSERT INTO board_tasks (board_id, title, body, status, priority, created_by, created_at) VALUES ('team', ?, 'needle', 'pending', 'low', 'lead', ?)`,
			fmt.Sprintf("task %d", i), fmt.Sprintf("2026-10-01T09:00:%02dZ", i))
		require.NoError(t, err)
	}
	run := func() *TaskQueryResult {
		q, err := ParseTaskQuery(TaskQueryParams{Q: []string{"needle"}, Fields: []string{"id"}, Limit: []string{"100"}})
		require.NoError(t, err)
		res, err := e.s.QueryTasks(e.ctx, q)
		require.NoError(t, err)
		return res
	}
	res := run()
	assert.Equal(t, 30, res.Total)
	assert.False(t, res.Truncated)
	rows, bytes := taskTextScanRows, taskTextScanBytes
	t.Cleanup(func() { taskTextScanRows, taskTextScanBytes = rows, bytes })
	taskTextScanRows = 10
	res = run()
	assert.True(t, res.Truncated)
	assert.Equal(t, 10, res.Total, "the matches among the 10 rows read")
	taskTextScanRows = rows
	taskTextScanBytes = 12 // title and body of the first row are already past it
	res = run()
	assert.True(t, res.Truncated)
	assert.Less(t, res.Total, 30)
}

// Without q the query answers as before, and CountTaskQuery is what its filters match.
func TestCountTaskQueryIsTheFiltersTotal(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	e := seedTaskQueryOracle(t, rng)
	for i := 0; i < 60; i++ {
		p := randomValidTaskQuery(rng)
		p.Cursor = nil
		q, err := ParseTaskQuery(p)
		require.NoError(t, err)
		n, err := e.s.CountTaskQuery(e.ctx, q)
		require.NoError(t, err)
		ids, _, _ := pagesOf(t, e, p, nil, 500)
		assert.Equal(t, len(ids), n, "%+v", p)
	}
}
