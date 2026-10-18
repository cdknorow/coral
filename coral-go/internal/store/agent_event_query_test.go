package store

import (
	"context"
	"fmt"
	"math/rand"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4745: the agent events query. The parser's rules; then, on random event tables, the same answer as a plain read of every event with
// the filters applied in Go, page by page in both directions, with the total on every page.

func TestParseEventQuery(t *testing.T) {
	sid := "sess-1"
	ok := func(p EventQueryParams) *EventQuery {
		t.Helper()
		q, err := ParseEventQuery("agent", &sid, p)
		require.NoError(t, err, "%+v", p)
		return q
	}
	q := ok(EventQueryParams{})
	assert.Equal(t, EventQueryDefaultLimit, q.Limit)
	q = ok(EventQueryParams{Type: []string{"tool_use, stop", "stop"}, Q: []string{"  Read FILE "}, Since: []string{"2026-10-18T09:30:00+02:00"}, Until: []string{"2026-10-19T00:00:00Z"},
		Sort: []string{"oldest"}, Limit: []string{"200"}})
	assert.Equal(t, []string{"tool_use", "stop"}, q.Types)
	assert.Equal(t, "read file", q.Text)
	assert.Equal(t, "2026-10-18T07:30:00.000000+00:00", q.Since, "the layout the events are stored in")
	assert.Equal(t, "2026-10-19T00:00:00.000000+00:00", q.Until)
	assert.True(t, q.Oldest)
	for name, bad := range map[string]EventQueryParams{
		"type upper": {Type: []string{"Tool_Use"}}, "type space": {Type: []string{"tool use"}}, "type empty part": {Type: []string{"a,,b"}}, "type sql": {Type: []string{"x' OR 1=1 --"}},
		"type long": {Type: []string{strings.Repeat("a", 41)}}, "type many": {Type: []string{distinctEventTypes(eventQueryMaxTypes + 1)}},
		"q long": {Q: []string{strings.Repeat("a", EventQueryMaxTextLen+1)}}, "q control": {Q: []string{"a\x00b"}}, "q twice": {Q: []string{"a", "b"}},
		"since bad": {Since: []string{"yesterday"}}, "until twice": {Until: []string{"2026-10-18T00:00:00Z", "2026-10-19T00:00:00Z"}}, "sort": {Sort: []string{"name"}},
		"limit 0": {Limit: []string{"0"}}, "limit 201": {Limit: []string{"201"}}, "limit text": {Limit: []string{"ten"}}, "cursor junk": {Cursor: []string{"not base64!"}},
	} {
		_, err := ParseEventQuery("agent", &sid, bad)
		require.Error(t, err, name)
		assert.ErrorIs(t, err, ErrEventQuery, name)
	}
	// a cursor belongs to the filters it was made for, the agent and the session included
	cur := eventCursor{Finger: q.finger, At: "2026-10-18T08:00:00.000000+00:00", ID: 5}.encode()
	_, err := ParseEventQuery("agent", &sid, EventQueryParams{Type: []string{"tool_use,stop"}, Q: []string{"read file"}, Since: []string{"2026-10-18T07:30:00Z"}, Until: []string{"2026-10-19T00:00:00Z"},
		Sort: []string{"oldest"}, Cursor: []string{cur}})
	require.NoError(t, err, "the same filters in other words")
	other := "sess-2"
	_, err = ParseEventQuery("agent", &other, EventQueryParams{Type: []string{"tool_use,stop"}, Q: []string{"read file"}, Since: []string{"2026-10-18T07:30:00Z"}, Until: []string{"2026-10-19T00:00:00Z"},
		Sort: []string{"oldest"}, Cursor: []string{cur}})
	require.Error(t, err, "another session")
	_, err = ParseEventQuery("other-agent", &sid, EventQueryParams{Type: []string{"tool_use,stop"}, Q: []string{"read file"}, Since: []string{"2026-10-18T07:30:00Z"}, Until: []string{"2026-10-19T00:00:00Z"},
		Sort: []string{"oldest"}, Cursor: []string{cur}})
	require.Error(t, err, "another agent")
	assert.True(t, QueriesEvents(map[string][]string{"q": {"x"}}))
	assert.False(t, QueriesEvents(map[string][]string{"limit": {"5"}, "session_id": {"s"}, "_": {"1"}}), "the plain list's parameters do not make a query")
}

func distinctEventTypes(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("type_%d", i)
	}
	return strings.Join(parts, ",")
}

var eventOracleTypes = []string{"tool_use", "stop", "prompt_submit", "notification", "permission_request", "auto_accept", "thinking"}

var eventOracleSummaries = []string{"Read internal/store/tasks.go", "Bash: go test ./...", "Edit sessions.go", "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD in the env", "100% sure", "a_b done",
	"ÉCOLE publique", "Straße", "' OR 1=1 --", "[redacted token] note", "Waiting for input", "UPPER lower Mixed", "日本語", ""}

var eventOracleQueries = []string{"read", "READ", "go test", "%", "_", "100%", "a_b", "école", "straße", "ghp_", "ghp_abcdefghijklmnopqrstuvwxyz0123456789abcd", "redacted", "' or 1=1", "bash", "zzz-none", "sessions"}

var eventShownTest = func() func(string) string {
	re := regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`)
	return func(s string) string { return re.ReplaceAllString(s, "[redacted token]") }
}()

type eventRow struct {
	ID                                  int64
	Agent, Session, Type, Tool, Summary string
	At                                  string
}

func seedEvents(t *testing.T, db *DB, rng *rand.Rand, n int) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	agents := []string{"alpha", "beta"}
	for i := 0; i < n; i++ {
		// times repeat (ties are broken on id), in the stored layout
		at := base.Add(time.Duration(rng.Intn(n/3+1)) * 1500 * time.Millisecond).Format(ISOFormat)
		var tool any
		if rng.Intn(2) == 0 {
			tool = []string{"Read", "Bash", "Edit", "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD"}[rng.Intn(4)]
		}
		_, err := db.ExecContext(ctx, `INSERT INTO agent_events (agent_name, session_id, event_type, tool_name, summary, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			agents[rng.Intn(2)], fmt.Sprintf("sess-%d", rng.Intn(3)), eventOracleTypes[rng.Intn(len(eventOracleTypes))], tool, eventOracleSummaries[rng.Intn(len(eventOracleSummaries))], at)
		require.NoError(t, err)
	}
}

func expectedEventIDs(t *testing.T, db *DB, q *EventQuery) []int64 {
	t.Helper()
	var rows []struct {
		ID      int64   `db:"id"`
		Agent   string  `db:"agent_name"`
		Session *string `db:"session_id"`
		Type    string  `db:"event_type"`
		Tool    *string `db:"tool_name"`
		Summary string  `db:"summary"`
		At      string  `db:"created_at"`
	}
	require.NoError(t, db.SelectContext(context.Background(), &rows, `SELECT id, agent_name, session_id, event_type, tool_name, summary, created_at FROM agent_events ORDER BY created_at ASC, id ASC`))
	shown := func(s string) string {
		if q.Shown != nil {
			return q.Shown(s)
		}
		return s
	}
	var ids []int64
	for _, r := range rows {
		if q.AgentName != "" && r.Agent != q.AgentName || q.SessionID != nil && (r.Session == nil || *r.Session != *q.SessionID) {
			continue
		}
		if len(q.Types) > 0 && !slices.Contains(q.Types, r.Type) {
			continue
		}
		if q.Since != "" && r.At < q.Since || q.Until != "" && r.At >= q.Until {
			continue
		}
		if q.Text != "" {
			tool := ""
			if r.Tool != nil {
				tool = *r.Tool
			}
			if !strings.Contains(strings.ToLower(shown(r.Summary)), q.Text) && !strings.Contains(strings.ToLower(shown(tool)), q.Text) {
				continue
			}
		}
		ids = append(ids, r.ID)
	}
	if !q.Oldest {
		slices.Reverse(ids)
	}
	return ids
}

func eventIDs(es []AgentEvent) []int64 {
	out := make([]int64, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func randomEventParams(rng *rand.Rand) EventQueryParams {
	var p EventQueryParams
	if rng.Intn(2) == 0 {
		var types []string
		for _, i := range rng.Perm(len(eventOracleTypes))[:1+rng.Intn(3)] {
			types = append(types, eventOracleTypes[i])
		}
		p.Type = []string{strings.Join(types, ",")}
	}
	if rng.Intn(3) != 0 {
		p.Q = []string{eventOracleQueries[rng.Intn(len(eventOracleQueries))]}
	}
	base := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	if rng.Intn(3) == 0 {
		p.Since = []string{base.Add(time.Duration(rng.Intn(60)) * time.Second).Format(time.RFC3339)}
	}
	if rng.Intn(3) == 0 {
		p.Until = []string{base.Add(time.Duration(rng.Intn(120)) * time.Second).Format(time.RFC3339)}
	}
	if rng.Intn(2) == 0 {
		p.Sort = []string{[]string{"newest", "oldest"}[rng.Intn(2)]}
	}
	return p
}

func walkEvents(t *testing.T, s *TaskStore, scope func() (string, *string), p EventQueryParams, shown func(string) string, size int, start string, backward bool) []*EventQueryResult {
	t.Helper()
	p.Limit = []string{fmt.Sprint(size)}
	var pages []*EventQueryResult
	cursor := start
	for {
		pp := p
		if cursor != "" {
			pp.Cursor = []string{cursor}
		}
		name, sid := scope()
		q, err := ParseEventQuery(name, sid, pp)
		require.NoError(t, err, "%+v", pp)
		q.Shown = shown
		res, err := s.QueryAgentEvents(context.Background(), q)
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
		require.NotEmpty(t, res.Events)
		cursor = next
	}
}

func TestEventQueryAgreesWithAPlainReadOfEveryEvent(t *testing.T) {
	seeds, queries := 3, 90
	if testing.Short() {
		seeds, queries = 2, 40
	}
	floors := map[string]int{}
	for seed := int64(1); seed <= int64(seeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		db := openTestDB(t)
		seedEvents(t, db, rng, 240+rng.Intn(80))
		s := NewTaskStore(db)
		for i := 0; i < queries; i++ {
			p := randomEventParams(rng)
			scope := func() (string, *string) { return "", nil }
			switch rng.Intn(3) {
			case 0:
				scope = func() (string, *string) { return "alpha", nil }
			case 1:
				sid := fmt.Sprintf("sess-%d", rng.Intn(3))
				scope = func() (string, *string) { return "beta", &sid }
			}
			var shown func(string) string
			if rng.Intn(2) == 0 {
				shown = eventShownTest
				floors["shown"]++
			}
			name, sid := scope()
			ref, err := ParseEventQuery(name, sid, p)
			require.NoError(t, err, "%+v", p)
			ref.Shown = shown
			want := expectedEventIDs(t, db, ref)
			size := []int{1, 3, 7, 25, 200}[rng.Intn(5)]
			fwd := walkEvents(t, s, scope, p, shown, size, "", false)
			var got []int64
			for pi, pg := range fwd {
				got = append(got, eventIDs(pg.Events)...)
				require.Equal(t, len(want), pg.Total, "the total is every match, on every page: %+v", p)
				if pi == 0 {
					require.Empty(t, pg.PrevCursor)
				} else {
					require.NotEmpty(t, pg.PrevCursor)
				}
				if pi == len(fwd)-1 {
					require.Empty(t, pg.NextCursor)
				}
			}
			require.Equal(t, want, got, "seed %d query %d size %d scope %q %v: %+v shown=%v", seed, i, size, name, sid, p, shown != nil)
			if len(fwd) > 1 {
				last := fwd[len(fwd)-1]
				back := walkEvents(t, s, scope, p, shown, size, last.PrevCursor, true)
				require.Len(t, back, len(fwd)-1, "%+v size %d", p, size)
				for bi, pg := range back {
					require.Equal(t, eventIDs(fwd[len(fwd)-2-bi].Events), eventIDs(pg.Events), "going back, page %d: %+v size %d", bi, p, size)
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
			add(len(ref.Types) > 0, "type")
			add(ref.Text != "", "q")
			add(ref.Since != "", "since")
			add(ref.Until != "", "until")
			add(ref.Oldest, "oldest")
			add(name != "", "an agent")
			add(sid != nil, "a session")
			add(ref.Text == "%" || ref.Text == "_" || ref.Text == "100%", "a literal % or _")
		}
	}
	for name, floor := range map[string]int{"queries": 150, "no match": 10, "several pages": 40, "paged back": 30, "type": 20, "q": 40, "since": 10, "until": 10, "oldest": 30, "an agent": 20, "a session": 10, "a literal % or _": 3, "shown": 60} {
		assert.GreaterOrEqual(t, floors[name], floor, "%s: %v", name, floors)
	}
}

// since is inclusive and until exclusive, to the microsecond, in the layout the events are stored in: an event half a second after a whole-second
// since is in, and one at the until instant is out.
func TestEventQueryBounds(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, at := range []string{"2026-10-18T10:00:00.499999+00:00", "2026-10-18T10:00:00.500000+00:00", "2026-10-18T10:00:01.000000+00:00", "2026-10-18T10:00:01.000001+00:00"} {
		_, err := db.ExecContext(ctx, `INSERT INTO agent_events (agent_name, event_type, summary, created_at) VALUES ('a', 'tool_use', 's', ?)`, at)
		require.NoError(t, err)
	}
	s := NewTaskStore(db)
	times := func(p EventQueryParams) []string {
		q, err := ParseEventQuery("a", nil, p)
		require.NoError(t, err)
		res, err := s.QueryAgentEvents(ctx, q)
		require.NoError(t, err)
		var out []string
		for _, e := range res.Events {
			out = append(out, e.CreatedAt[17:26])
		}
		return out
	}
	assert.Equal(t, []string{"00.500000", "01.000000", "01.000001"}, times(EventQueryParams{Since: []string{"2026-10-18T10:00:00.5Z"}, Sort: []string{"oldest"}}))
	assert.Equal(t, []string{"00.499999", "00.500000", "01.000000", "01.000001"}, times(EventQueryParams{Since: []string{"2026-10-18T10:00:00Z"}, Sort: []string{"oldest"}}), "a whole-second since keeps the events inside that second")
	assert.Equal(t, []string{"00.499999", "00.500000"}, times(EventQueryParams{Until: []string{"2026-10-18T10:00:01Z"}, Sort: []string{"oldest"}}), "until is exclusive")
}

// What a viewer cannot see is not matched: the summary and the tool name are tested as shown.
func TestEventQueryMatchesWhatTheViewerSees(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	token := "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD"
	_, err := db.ExecContext(ctx, `INSERT INTO agent_events (agent_name, event_type, tool_name, summary, created_at) VALUES ('a', 'tool_use', ?, ?, '2026-10-18T10:00:00.000000+00:00')`, "Bash", "export TOKEN="+token)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO agent_events (agent_name, event_type, tool_name, summary, created_at) VALUES ('a', 'tool_use', ?, 'plain', '2026-10-18T10:00:01.000000+00:00')`, token)
	require.NoError(t, err)
	s := NewTaskStore(db)
	count := func(text string, shown func(string) string) int {
		q, err := ParseEventQuery("a", nil, EventQueryParams{Q: []string{text}})
		require.NoError(t, err)
		q.Shown = shown
		res, err := s.QueryAgentEvents(ctx, q)
		require.NoError(t, err)
		return res.Total
	}
	assert.Equal(t, 2, count("ghp_abcdefgh", nil), "the summary and the tool name, as stored")
	assert.Equal(t, 0, count("ghp_abcdefgh", eventShownTest), "through the hub neither is there to match")
	assert.Equal(t, 2, count("[redacted token]", eventShownTest))
	assert.Equal(t, 0, count("[redacted token]", nil))
}

// A page carries the whole event, detail included, though the scan reads only the short columns; and the scan is bounded.
func TestEventQueryPagesCarryTheDetailAndTheScanIsBounded(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		_, err := db.ExecContext(ctx, `INSERT INTO agent_events (agent_name, event_type, summary, detail_json, created_at) VALUES ('a', 'tool_use', ?, ?, ?)`,
			fmt.Sprintf("needle %d", i), fmt.Sprintf(`{"n":%d}`, i), fmt.Sprintf("2026-10-18T10:00:%02d.000000+00:00", i))
		require.NoError(t, err)
	}
	s := NewTaskStore(db)
	run := func() *EventQueryResult {
		q, err := ParseEventQuery("a", nil, EventQueryParams{Q: []string{"needle"}, Limit: []string{"100"}, Sort: []string{"oldest"}})
		require.NoError(t, err)
		res, err := s.QueryAgentEvents(ctx, q)
		require.NoError(t, err)
		return res
	}
	res := run()
	require.Len(t, res.Events, 30)
	assert.Equal(t, `{"n":7}`, *res.Events[7].DetailJSON)
	assert.False(t, res.Truncated)
	rows, bytes := eventScanRows, eventScanBytes
	t.Cleanup(func() { eventScanRows, eventScanBytes = rows, bytes })
	eventScanRows = 10
	res = run()
	assert.True(t, res.Truncated)
	assert.Equal(t, 10, res.Total)
	eventScanRows = rows
	eventScanBytes = 20
	res = run()
	assert.True(t, res.Truncated)
	assert.Less(t, res.Total, 30)
}
