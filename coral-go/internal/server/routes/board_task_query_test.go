package routes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/pkg/tunnel"
)

type tqEnv struct {
	t   *testing.T
	srv *httptest.Server
	bs  *board.Store
	raw *sql.DB
	ids map[string]int64 // by title
}

func newTQEnv(t *testing.T) *tqEnv {
	t.Helper()
	dbPath := t.TempDir() + "/b.db"
	bs, err := board.NewStoreWithKey(dbPath, "")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	h := NewBoardHandler(bs)
	t.Cleanup(h.WaitBackground)
	r := chi.NewRouter()
	r.Get("/api/board/tasks", h.ListAllTasks)
	r.Get("/api/board/{project}/tasks/{taskID}", h.GetTask)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })
	return &tqEnv{t: t, srv: srv, bs: bs, raw: raw, ids: map[string]int64{}}
}

// add makes a task, then sets its timestamps and optional columns directly.
func (e *tqEnv) add(boardID, title, body, priority, by, assignee string, set string, args ...any) int64 {
	e.t.Helper()
	var as []string
	if assignee != "" {
		as = append(as, assignee)
	}
	task, err := e.bs.CreateTask(context.Background(), boardID, title, body, priority, by, as...)
	require.NoError(e.t, err)
	if set != "" {
		_, err = e.raw.Exec("UPDATE board_tasks SET "+set+" WHERE id = ?", append(args, task.ID)...)
		require.NoError(e.t, err)
	}
	e.ids[title] = task.ID
	return task.ID
}

func (e *tqEnv) get(q string) (int, map[string]any, string) {
	e.t.Helper()
	resp, err := http.Get(e.srv.URL + "/api/board/tasks?" + q)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out, string(b)
}

func (e *tqEnv) titles(q string) []string {
	e.t.Helper()
	code, out, body := e.get(q)
	require.Equal(e.t, 200, code, q+" "+body)
	var ts []string
	for _, r := range out["tasks"].([]any) {
		ts = append(ts, r.(map[string]any)["title"].(string))
	}
	return ts
}

// seed: five tasks over three boards, with distinct times. created_at: a<b<c<d<e.
func (e *tqEnv) seed() {
	e.add("alpha", "a", "BODY-A", "high", "Orchestrator", "Operator", "created_at = '2026-10-10T00:00:00Z', parked_reason = 'waiting', parked_until = '2999-01-01T00:00:00Z'")
	e.add("alpha", "b", "BODY-B", "low", "Orchestrator", "Backend Dev", "created_at = '2026-10-11T00:00:00Z', claimed_at = '2026-10-15T00:00:00Z'")
	e.add("beta", "c", "BODY-C", "critical", "Operator", "Operator", "created_at = '2026-10-12T00:00:00Z', completion_message = 'DONE-C', status = 'completed', completed_at = '2026-10-12T05:00:00Z', completed_by = 'Operator'")
	e.add("beta", "d", "BODY-D", "medium", "Security Reviewer", "", "created_at = '2026-10-13T00:00:00Z', last_activity_at = '2026-10-17T00:00:00Z'")
	e.add("gamma", "e", "BODY-E", "high", "Orchestrator", "QA Engineer", "created_at = '2026-10-14T00:00:00Z', status = 'blocked', parked_reason = 'old', parked_until = '2000-01-01T00:00:00Z'")
}

func TestTaskQueryFieldSelection(t *testing.T) {
	e := newTQEnv(t)
	e.seed()

	// fields=summary: the minimal set, no long text.
	code, out, body := e.get("fields=summary")
	require.Equal(t, 200, code, body)
	rows := out["tasks"].([]any)
	require.Len(t, rows, 5)
	first := rows[0].(map[string]any)
	var keys []string
	for k := range first {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"assigned_to", "board_id", "board_label", "created_at", "id", "parked_reason", "parked_until", "priority", "status", "title", "updated_at"}, keys)
	for _, banned := range []string{"BODY-", "DONE-C", `"body"`, `"completion_message"`} {
		assert.NotContains(t, body, banned)
	}
	assert.Equal(t, "e", first["title"]) // newest created first
	assert.Equal(t, "2026-10-14T00:00:00Z", first["updated_at"])

	// A filter without fields: the whole task rows, as the plain list has them.
	_, out, body = e.get("board=alpha")
	assert.Contains(t, body, "BODY-A")
	assert.Contains(t, body, `"workflow"`)
	assert.Len(t, out["tasks"], 2)

	// Explicit: exactly those, in any order, and the long ones only when named.
	_, out, body = e.get("fields=id,title&fields=body")
	for _, r := range out["tasks"].([]any) {
		m := r.(map[string]any)
		assert.Len(t, m, 3)
		assert.Contains(t, m, "body")
	}
	assert.Contains(t, body, "BODY-A")
	assert.NotContains(t, body, "DONE-C")
	_, _, body = e.get("fields=completion_message,id")
	assert.Contains(t, body, "DONE-C")
	assert.NotContains(t, body, "BODY-")

	// Derived fields.
	_, out, _ = e.get("fields=id,updated_at,completed_at,claimed_at,revision,created_by,completed_by&sort=created_at&order=asc")
	byID := map[string]map[string]any{}
	for _, r := range out["tasks"].([]any) {
		m := r.(map[string]any)
		byID[strconv.Itoa(int(m["id"].(float64)))] = m
	}
	assert.Equal(t, "2026-10-15T00:00:00Z", byID[strconv.Itoa(int(e.ids["b"]))]["updated_at"], "claim is later than creation")
	assert.Equal(t, "2026-10-12T05:00:00Z", byID[strconv.Itoa(int(e.ids["c"]))]["updated_at"], "completion")
	assert.Equal(t, "2026-10-17T00:00:00Z", byID[strconv.Itoa(int(e.ids["d"]))]["updated_at"], "last activity")
	assert.Equal(t, "Operator", byID[strconv.Itoa(int(e.ids["c"]))]["completed_by"])
}

func TestTaskQueryFilters(t *testing.T) {
	e := newTQEnv(t)
	e.seed()
	// Newest created first: e, d, c, b, a.
	for q, want := range map[string][]string{
		"status=completed":                                             {"c"},
		"status=pending,blocked":                                       {"e", "d", "b", "a"},
		"status=blocked&status=completed":                              {"e", "c"},
		"assigned_to=Operator":                                         {"c", "a"},
		"assigned_to=Operator,QA%20Engineer":                           {"e", "c", "a"},
		"assigned_to=Nobody":                                           {},
		"board=alpha":                                                  {"b", "a"},
		"board=alpha&board=gamma":                                      {"e", "b", "a"},
		"priority=high":                                                {"e", "a"},
		"priority=critical,low":                                        {"c", "b"},
		"created_by=Operator":                                          {"c"},
		"created_by=Orchestrator,Security%20Reviewer":                  {"e", "d", "b", "a"},
		"parked=true":                                                  {"a"}, // e's park time has passed
		"parked=false":                                                 {"e", "d", "c", "b"},
		"created_since=2026-10-12T00:00:00Z":                           {"e", "d", "c"},
		"created_since=2026-10-12T02:00:00%2B02:00":                    {"e", "d", "c"}, // an offset is read as the same instant
		"updated_since=2026-10-16T00:00:00Z":                           {"d"},
		"board=beta&assigned_to=Operator":                              {"c"},
		"status=pending&priority=high&parked=true":                     {"a"},
		"board=alpha&created_since=2026-10-11T00:00:00Z":               {"b"},
		"assigned_to=Operator&status=completed&board=beta":             {"c"},
		"board=alpha&board=beta&priority=low,medium":                   {"d", "b"},
		"order=asc":                                                    {"a", "b", "c", "d", "e"},
		"updated_since=2026-10-15T00:00:00Z&sort=updated_at":           {"d", "b"},
		"updated_since=2026-10-15T00:00:00Z&sort=updated_at&order=asc": {"b", "d"},
	} {
		got := e.titles(q)
		if got == nil {
			got = []string{}
		}
		assert.Equal(t, want, got, q)
	}
}

func TestTaskQuerySortByUpdated(t *testing.T) {
	e := newTQEnv(t)
	e.seed()
	// updated_at: a 10-10, b 10-15, c 10-12T05, d 10-17, e 10-14
	assert.Equal(t, []string{"d", "b", "e", "c", "a"}, e.titles("sort=updated_at"))
	assert.Equal(t, []string{"a", "c", "e", "b", "d"}, e.titles("sort=updated_at&order=asc"))
}

func TestTaskQueryPaging(t *testing.T) {
	e := newTQEnv(t)
	// 23 tasks with many equal created_at values, so the id breaks the ties.
	for i := 0; i < 23; i++ {
		e.add("p", fmt.Sprintf("t%02d", i), "", "medium", "Orchestrator", "", fmt.Sprintf("created_at = '2026-10-1%dT00:00:00Z'", i%4))
	}
	for _, sortBy := range []string{"created_at", "updated_at"} {
		for _, order := range []string{"asc", "desc"} {
			base := "sort=" + sortBy + "&order=" + order
			_, _, _ = e.get(base)
			full := e.titles(base + "&limit=100")
			require.Len(t, full, 23)
			var paged []string
			cursor := ""
			for pages := 0; ; pages++ {
				require.Less(t, pages, 20, "paging does not end")
				q := base + "&limit=5"
				if cursor != "" {
					q += "&cursor=" + url.QueryEscape(cursor)
				}
				code, out, body := e.get(q)
				require.Equal(t, 200, code, body)
				for _, r := range out["tasks"].([]any) {
					paged = append(paged, r.(map[string]any)["title"].(string))
				}
				next, _ := out["next_cursor"].(string)
				if next == "" {
					assert.LessOrEqual(t, len(out["tasks"].([]any)), 5)
					break
				}
				assert.Len(t, out["tasks"], 5)
				cursor = next
			}
			assert.Equal(t, full, paged, base)
		}
	}
	// A task made between pages does not shift the next page.
	_, out, _ := e.get("order=desc&limit=5")
	cursor := out["next_cursor"].(string)
	e.add("p", "late", "", "medium", "Orchestrator", "", "")
	_, out, _ = e.get("order=desc&limit=5&cursor=" + url.QueryEscape(cursor))
	assert.Len(t, out["tasks"], 5)
	// Exactly a page worth: no cursor.
	_, out, _ = e.get("order=desc&limit=100")
	assert.NotContains(t, out, "next_cursor")
}

func TestTaskQueryRefusals(t *testing.T) {
	e := newTQEnv(t)
	e.seed()
	_, out, _ := e.get("order=desc&limit=1")
	cur := out["next_cursor"].(string)
	for q, wantMsg := range map[string]string{
		"nope=1":                              "unknown parameter nope",
		"nope=1&zip=2":                        "unknown parameter nope, zip",
		"fields=id,secret":                    "unknown field secret",
		"fields=session_id":                   "unknown field session_id",
		"fields=cost_usd,input_tokens":        "unknown field cost_usd, input_tokens",
		"fields=workflow,blocked_by":          "unknown field workflow, blocked_by",
		"fields=id;DROP":                      "the query string is not valid",
		"fields=id%zz":                        "the query string is not valid",
		"fields=id&fields=DROP+TABLE":         "unknown field DROP TABLE",
		"fields=":                             "fields has an empty value",
		"status=pending,done":                 "unknown status done",
		"status=":                             "status has an empty value",
		"priority=urgent":                     "unknown priority urgent",
		"sort=title":                          "unknown sort title",
		"sort=created_at&sort=updated_at":     "sort is given more than once",
		"order=up":                            "unknown order up",
		"parked=yes":                          "parked must be true or false",
		"parked=true&parked=false":            "parked is given more than once",
		"created_since=yesterday":             "created_since must be an RFC 3339 time",
		"updated_since=2026-10-10":            "updated_since must be an RFC 3339 time",
		"order=desc&limit=0":                  "limit must be a whole number from 1 to 100",
		"order=desc&limit=101":                "limit must be a whole number from 1 to 100",
		"fields=summary&limit=501":            "limit must be a whole number from 1 to 500",
		"fields=summary&limit=-3":             "limit must be a whole number from 1 to 500",
		"fields=summary&limit=ten":            "limit must be a whole number from 1 to 500",
		"order=desc&limit=5&limit=6":          "limit is given more than once",
		"limit=101&fields=body":               "limit must be a whole number from 1 to 100",
		"fields=completion_message&limit=500": "limit must be a whole number from 1 to 100",
		"cursor=garbage":                      "cursor is not one this server gave",
		"cursor=" + url.QueryEscape(cur) + "&sort=updated_at": "cursor was made for a different sort or order",
		"cursor=" + url.QueryEscape(cur) + "&order=asc":       "cursor was made for a different sort or order",
	} {
		code, out, body := e.get(q)
		assert.Equal(t, 400, code, q+" "+body)
		assert.Contains(t, fmt.Sprint(out["error"]), wantMsg, q)
	}
	// 100 rows with a long field is the cap; 500 without.
	code, _, _ := e.get("fields=summary&limit=500")
	assert.Equal(t, 200, code)
	code, _, _ = e.get("limit=100&fields=body")
	assert.Equal(t, 200, code)
	// POSTed or odd values never reach the SQL: a quote in a value only matches nothing.
	assert.Empty(t, e.titles("assigned_to="+url.QueryEscape("x' OR '1'='1")))
	assert.Empty(t, e.titles("board="+url.QueryEscape("a') OR 1=1 --")))
	assert.Len(t, e.titles(""), 5, "the table is intact")
}

func TestTaskQueryLeavesThePlainListAlone(t *testing.T) {
	e := newTQEnv(t)
	e.seed()
	read := func(q string) (int, string) {
		resp, err := http.Get(e.srv.URL + "/api/board/tasks" + q)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, plain := read("")
	require.Equal(t, 200, code)
	assert.Contains(t, plain, "BODY-A")
	assert.Contains(t, plain, "DONE-C")
	assert.Contains(t, plain, `"workflow"`)
	assert.NotContains(t, plain, "next_cursor")
	// Only limit: the same plain list, with limit as it always read (an unusable one is ignored, not a 400).
	for _, q := range []string{"?limit=100", "?limit=0", "?limit=junk", "?limit=100&limit=3"} {
		code, body := read(q)
		assert.Equal(t, 200, code, q)
		assert.Equal(t, plain, body, q)
	}
	_, one := read("?limit=1")
	assert.Equal(t, 1, strings.Count(one, `"board_id"`))
	// The whole rows of a filtered query are the plain list's rows, field for field.
	_, filtered := read("?order=desc&limit=100")
	var a, b struct{ Tasks []map[string]any }
	require.NoError(t, json.Unmarshal([]byte(plain), &a))
	require.NoError(t, json.Unmarshal([]byte(filtered), &b))
	assert.Equal(t, a.Tasks, b.Tasks)
	// An unknown parameter is now refused, by name.
	code, body := read("?cache=1")
	assert.Equal(t, 400, code)
	assert.Contains(t, body, "unknown parameter cache")
}

func TestTaskQueryBoardLabel(t *testing.T) {
	e := newTQEnv(t)
	e.seed()
	_, out, _ := e.get("fields=id,board_id,board_label&board=alpha&limit=1")
	m := out["tasks"].([]any)[0].(map[string]any)
	assert.Equal(t, "alpha", m["board_id"])
}

// A completed task's verification state (#4563) is on the plain list's rows, on task detail, as a field of the query and as a
// filter; the rest of the plain reply is unchanged.
func TestTaskVerificationOverHTTP(t *testing.T) {
	e := newTQEnv(t)
	ctx := context.Background()
	_, err := e.bs.Subscribe(ctx, "team", "lead", "Orchestrator", "lead-session", nil, "all")
	require.NoError(t, err)
	done := func(title string, arts []board.TaskArtifact) int64 {
		task, err := e.bs.CreateTask(ctx, "team", title, "BODY-"+title, "medium", "lead", "worker")
		require.NoError(t, err)
		_, err = e.bs.ClaimTask(ctx, "team", "worker", task.ID)
		require.NoError(t, err)
		_, err = e.bs.CompleteTaskWithArtifacts(ctx, "team", task.ID, "lead", nil, "success", arts)
		require.NoError(t, err)
		return task.ID
	}
	plain := done("plain", nil)
	evid := done("evid", []board.TaskArtifact{{Name: "r", Content: "x"}})
	open, err := e.bs.CreateTask(ctx, "team", "open", "", "medium", "lead", "worker")
	require.NoError(t, err)

	get := func(path string) (int, map[string]any) {
		resp, err := http.Get(e.srv.URL + path)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}
	rows := func(path string) map[int64]map[string]any {
		code, out := get(path)
		require.Equal(t, 200, code)
		m := map[int64]map[string]any{}
		for _, r := range out["tasks"].([]any) {
			row := r.(map[string]any)
			m[int64(row["id"].(float64))] = row
		}
		return m
	}

	// the plain list: whole rows with the new field on completed tasks, none on the open one
	list := rows("/api/board/tasks")
	assert.Equal(t, "unverified", list[plain]["verification"].(map[string]any)["state"])
	assert.Equal(t, "evidence", list[evid]["verification"].(map[string]any)["state"])
	assert.NotContains(t, list[open.ID], "verification")
	assert.Equal(t, []any{}, list[plain]["verification"].(map[string]any)["by"])
	assert.NotEmpty(t, list[plain]["verification"].(map[string]any)["at"])
	assert.Contains(t, list[plain], "body", "whole rows are otherwise as before")
	assert.Contains(t, list[plain], "workflow")

	// task detail
	code, detail := get("/api/board/team/tasks/" + fmt.Sprint(evid))
	require.Equal(t, 200, code)
	assert.Equal(t, "evidence", detail["verification"].(map[string]any)["state"])
	code, detail = get("/api/board/team/tasks/" + fmt.Sprint(open.ID))
	require.Equal(t, 200, code)
	assert.NotContains(t, detail, "verification")

	// the filter, alone and with the projection
	assert.Equal(t, []int64{evid}, keys(rows("/api/board/tasks?verification=evidence")))
	assert.Equal(t, []int64{plain}, keys(rows("/api/board/tasks?verification=unverified&fields=id,title")))
	assert.Equal(t, []int64{plain, evid}, keys(rows("/api/board/tasks?verification=unverified,evidence")))
	assert.Empty(t, keys(rows("/api/board/tasks?verification=verified")))
	proj := rows("/api/board/tasks?fields=id,verification&sort=created_at&order=asc")
	assert.Equal(t, "unverified", proj[plain]["verification"].(map[string]any)["state"])
	assert.Nil(t, proj[open.ID]["verification"])
	assert.Contains(t, proj[open.ID], "verification")

	// unknown value: a 400 that names it
	code, out := get("/api/board/tasks?verification=done")
	assert.Equal(t, 400, code)
	assert.Contains(t, fmt.Sprint(out["error"]), "unknown verification done")
}

func keys(m map[int64]map[string]any) []int64 {
	var ids []int64
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Through Coral Hub the reply is redacted after the filter has matched, so an exact filter on a name a token makes up would confirm a
// guess of the token. A filter value the redactor would change is refused when tunneled (it can match nothing a viewer can see); a value
// without a secret matches as before, and a local caller is not restricted (#4572 round 2: the same class, #4540's name filters).
func TestTaskQueryTunneledNameFiltersRefuseSecrets(t *testing.T) {
	const tok = "ghp_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	e := newTQEnv(t)
	e.seed()
	e.add("alpha", "secret owner", "", "high", tok, tok, "")
	get := func(query string, tunneled bool) (int, []string) {
		req, err := http.NewRequest("GET", e.srv.URL+"/api/board/tasks?"+query, nil)
		require.NoError(t, err)
		if tunneled {
			req.Header.Set(tunnel.ViaHeader, "1")
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out struct {
			Tasks []map[string]any `json:"tasks"`
			Error string           `json:"error"`
		}
		_ = json.Unmarshal(b, &out)
		var titles []string
		for _, r := range out.Tasks {
			titles = append(titles, r["title"].(string))
		}
		if resp.StatusCode != 200 {
			return resp.StatusCode, []string{out.Error}
		}
		return resp.StatusCode, titles
	}
	for _, q := range []string{"assigned_to=" + tok, "created_by=" + tok, "board=" + tok, "assigned_to=Operator," + tok, "created_by=" + url.QueryEscape("x "+tok+" y")} {
		code, msg := get(q+"&fields=id,title", true)
		assert.Equal(t, 400, code, q)
		assert.Contains(t, msg[0], "can't contain a secret", q)
	}
	// a value with no secret in it is matched as before, tunneled or not
	code, titles := get("assigned_to=Operator&fields=id,title", true)
	assert.Equal(t, 200, code)
	assert.ElementsMatch(t, []string{"c", "a"}, titles)
	// a local caller may filter by any name
	code, titles = get("assigned_to="+tok+"&fields=id,title", false)
	assert.Equal(t, 200, code)
	assert.Equal(t, []string{"secret owner"}, titles)
}

// #4745: q searches a task's title, body and completion message (a plain, case-insensitive substring), the reply has the total, and
// through Coral Hub the match is made on what the viewer sees: a token cannot be searched for, and the marker that stands in for it can.
func TestTaskQueryTextOverHTTP(t *testing.T) {
	const tok = "ghp_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	e := newTQEnv(t)
	e.seed()
	e.add("alpha", "deploy notes", "the key is "+tok+" 100% sure", "low", "Operator", "", "created_at = '2026-10-16T00:00:00Z'")
	total := func(q string) (int, []string, any) {
		e.t.Helper()
		code, out, body := e.get(q)
		require.Equal(e.t, 200, code, q+" "+body)
		var ts []string
		for _, r := range out["tasks"].([]any) {
			ts = append(ts, r.(map[string]any)["title"].(string))
		}
		return int(out["total"].(float64)), ts, out["next_cursor"]
	}
	// the body, the completion message and the title; any case; the whole row and the projection
	n, ts, _ := total("q=body-c&fields=id,title")
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"c"}, ts)
	n, ts, _ = total("q=done-C")
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"c"}, ts)
	n, ts, _ = total("q=DEPLOY&fields=id,title")
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"deploy notes"}, ts)
	// % and _ are ordinary characters, not patterns: each finds only the task whose text holds it (a LIKE "%" or "_" would find all)
	n, ts, _ = total("q=" + url.QueryEscape("100%") + "&fields=id,title")
	assert.Equal(t, []string{"deploy notes"}, ts)
	n, ts, _ = total("q=" + url.QueryEscape("%") + "&fields=id,title")
	assert.Equal(t, []string{"deploy notes"}, ts)
	n, ts, _ = total("q=_&fields=id,title")
	assert.Equal(t, []string{"deploy notes"}, ts, "the underscore in the token")
	// with another filter, and paging: the total counts every match, a page is limit rows
	n, ts, next := total("q=body&fields=id,title&limit=2&order=asc")
	assert.Equal(t, 5, n)
	assert.Equal(t, []string{"a", "b"}, ts)
	require.NotNil(t, next)
	n, ts, next = total("q=body&fields=id,title&limit=2&order=asc&cursor=" + next.(string))
	assert.Equal(t, 5, n)
	assert.Equal(t, []string{"c", "d"}, ts)
	require.NotNil(t, next)
	n, ts, next = total("q=body&fields=id,title&limit=2&order=asc&cursor=" + next.(string))
	assert.Equal(t, []string{"e"}, ts)
	assert.Nil(t, next)
	n, ts, _ = total("q=body&status=completed&fields=id,title")
	assert.Equal(t, 1, n)
	// a reply with no q has a total too: the tasks its filters match
	code, out, body := e.get("status=pending&fields=id,title&limit=1")
	require.Equal(t, 200, code, body)
	assert.Equal(t, float64(4), out["total"])
	// a q the parser refuses is a 400 naming it
	code, _, body = e.get("q=" + url.QueryEscape(strings.Repeat("x", 201)))
	assert.Equal(t, 400, code)
	assert.Contains(t, body, "q is at most 200 characters")

	tunneled := func(query string) (int, string, []string) {
		req, err := http.NewRequest("GET", e.srv.URL+"/api/board/tasks?"+query, nil)
		require.NoError(t, err)
		req.Header.Set(tunnel.ViaHeader, "1")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out struct {
			Tasks []map[string]any `json:"tasks"`
			Error string           `json:"error"`
		}
		_ = json.Unmarshal(b, &out)
		var titles []string
		for _, r := range out.Tasks {
			titles = append(titles, r["title"].(string))
		}
		return resp.StatusCode, out.Error, titles
	}
	// through the hub: a q holding a secret is refused; the marker finds the task; text without a secret is as before
	code2, msg, _ := tunneled("q=" + tok + "&fields=id,title")
	assert.Equal(t, 400, code2)
	assert.Contains(t, msg, "q can't contain a secret")
	code2, _, titles := tunneled("q=redacted&fields=id,title")
	assert.Equal(t, 200, code2)
	assert.Equal(t, []string{"deploy notes"}, titles, "the viewer sees [redacted token] in the body")
	code2, _, titles = tunneled("q=" + url.QueryEscape("100%") + "&fields=id,title")
	assert.Equal(t, 200, code2)
	assert.Equal(t, []string{"deploy notes"}, titles)
	// a local caller searches the stored text, secret and all
	n, ts, _ = total("q=" + tok + "&fields=id,title")
	assert.Equal(t, []string{"deploy notes"}, ts)
	n, _, _ = total("q=redacted&fields=id,title")
	assert.Equal(t, 0, n, "the stored text has no marker")
}
