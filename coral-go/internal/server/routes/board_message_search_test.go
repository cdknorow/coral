package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/board/boardtest"
	"github.com/cdknorow/coral/pkg/tunnel"
)

type msEnv struct {
	t   *testing.T
	srv *httptest.Server
	bs  *board.Store
}

func newMSEnv(t *testing.T) *msEnv {
	t.Helper()
	bs, err := boardtest.Open(t.TempDir() + "/b.db")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	h := NewBoardHandler(bs)
	t.Cleanup(h.WaitBackground)
	r := chi.NewRouter()
	r.Get("/api/board/{project}/messages/search", h.SearchMessages)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &msEnv{t: t, srv: srv, bs: bs}
}

func (e *msEnv) post(sender, title, text string) int64 {
	e.t.Helper()
	ctx := context.Background()
	_, err := e.bs.Subscribe(ctx, "proj", sender, title, "tmux-"+sender, nil, "")
	require.NoError(e.t, err)
	m, err := e.bs.PostMessage(ctx, "proj", sender, text, nil)
	require.NoError(e.t, err)
	return m.ID
}

type msReply struct {
	Messages   []map[string]any `json:"messages"`
	Total      int              `json:"total"`
	Limit      int              `json:"limit"`
	NextCursor string           `json:"next_cursor"`
	PrevCursor string           `json:"prev_cursor"`
	Truncated  bool             `json:"truncated"`
	Error      string           `json:"error"`
}

func (e *msEnv) get(query string, tunneled bool) (int, msReply) {
	e.t.Helper()
	req, err := http.NewRequest("GET", e.srv.URL+"/api/board/proj/messages/search?"+query, nil)
	require.NoError(e.t, err)
	if tunneled {
		req.Header.Set(tunnel.ViaHeader, "1")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out msReply
	require.NoError(e.t, json.Unmarshal(b, &out), string(b))
	return resp.StatusCode, out
}

func (r msReply) contents() []string {
	var out []string
	for _, m := range r.Messages {
		out = append(out, m["content"].(string))
	}
	return out
}

// #4745: GET /api/board/{project}/messages/search: q, from, mentions, since, until, sort, limit and cursor, the reply {messages, total,
// next_cursor, prev_cursor}, paging both ways, and the Coral Hub rules.
func TestBoardMessageSearchOverHTTP(t *testing.T) {
	e := newMSEnv(t)
	e.post("Orchestrator", "Orchestrator", "Alpha plan for @Backend Dev")
	e.post("Backend Dev", "Backend Dev", "alpha built, 100% sure")
	e.post("QA Engineer", "QA Engineer", "BETA checks for @all")
	e.post("Backend Dev", "Backend Dev", "Alpha again")
	e.post("QA Engineer", "QA Engineer", "nothing here")
	// text, case, a literal %
	code, r := e.get("q=ALPHA&sort=oldest", false)
	require.Equal(t, 200, code)
	assert.Equal(t, []string{"Alpha plan for @Backend Dev", "alpha built, 100% sure", "Alpha again"}, r.contents())
	assert.Equal(t, 3, r.Total)
	assert.Equal(t, 50, r.Limit)
	assert.Empty(t, r.NextCursor)
	assert.Empty(t, r.PrevCursor)
	assert.NotNil(t, r.Messages[0]["id"])
	assert.Equal(t, "Orchestrator", r.Messages[0]["job_title"], "the rows are the ones /messages/all gives")
	code, r = e.get("q="+url.QueryEscape("100%"), false)
	assert.Equal(t, []string{"alpha built, 100% sure"}, r.contents())
	// from (a name or an id, any case), mentions, newest first by default
	code, r = e.get("from=backend+dev", false)
	assert.Equal(t, []string{"Alpha again", "alpha built, 100% sure"}, r.contents())
	code, r = e.get("mentions=Backend+Dev", false)
	assert.Equal(t, []string{"BETA checks for @all", "Alpha plan for @Backend Dev"}, r.contents(), "@all tags everyone")
	code, r = e.get("q=alpha&from=QA+Engineer", false)
	assert.Equal(t, 0, r.Total)
	assert.Equal(t, []string{}, append([]string{}, r.contents()...))
	// paging both ways, with the total on every page
	code, r = e.get("limit=2&sort=oldest", false)
	require.Equal(t, 200, code)
	assert.Equal(t, 5, r.Total)
	assert.Equal(t, []string{"Alpha plan for @Backend Dev", "alpha built, 100% sure"}, r.contents())
	require.NotEmpty(t, r.NextCursor)
	assert.Empty(t, r.PrevCursor)
	code, r2 := e.get("limit=2&sort=oldest&cursor="+r.NextCursor, false)
	assert.Equal(t, []string{"BETA checks for @all", "Alpha again"}, r2.contents())
	assert.Equal(t, 5, r2.Total)
	require.NotEmpty(t, r2.NextCursor)
	require.NotEmpty(t, r2.PrevCursor)
	code, r3 := e.get("limit=2&sort=oldest&cursor="+r2.NextCursor, false)
	assert.Equal(t, []string{"nothing here"}, r3.contents())
	assert.Empty(t, r3.NextCursor)
	code, back := e.get("limit=2&sort=oldest&cursor="+r3.PrevCursor, false)
	assert.Equal(t, r2.contents(), back.contents(), "back from the last page is the page before it")
	code, back = e.get("limit=2&sort=oldest&cursor="+back.PrevCursor, false)
	assert.Equal(t, r.contents(), back.contents())
	assert.Empty(t, back.PrevCursor, "the first page again")
	// jump to the first match
	code, r = e.get("q=beta&sort=oldest&limit=1", false)
	assert.Equal(t, []string{"BETA checks for @all"}, r.contents())
	// refusals name the problem
	for query, want := range map[string]string{
		"nope=1": "unknown parameter nope", "q=" + strings.Repeat("x", 201): "q is at most 200 characters", "since=yesterday": "since must be an RFC 3339 time",
		"sort=name": "unknown sort name", "limit=500": "limit must be a whole number from 1 to 200", "cursor=junk": "cursor is not one this server gave",
		"q=a;b": "the query string is not valid", "limit=2&limit=3": "limit is given more than once",
	} {
		code, r = e.get(query, false)
		assert.Equal(t, 400, code, query)
		assert.Contains(t, r.Error, want, query)
	}
	// a cursor made for other filters is refused
	code, r = e.get("q=alpha&limit=1", false)
	require.NotEmpty(t, r.NextCursor)
	code, r = e.get("q=beta&limit=1&cursor="+r.NextCursor, false)
	assert.Equal(t, 400, code)
	assert.Contains(t, r.Error, "other filters")
	// another board's messages are not here
	code, r = e.get("q=alpha", false)
	assert.Equal(t, 3, r.Total)
}

// Through Coral Hub the match is made on what the viewer sees: a token cannot be searched for (a q, from or mentions holding one is
// refused) and the marker that stands in for it can; text with no secret works as ever.
func TestBoardMessageSearchThroughTheHub(t *testing.T) {
	const tok = "ghp_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	e := newMSEnv(t)
	e.post("Backend Dev", "Backend Dev", "deploy key "+tok+" for @Backend Dev")
	e.post(tok, tok, "from the token holder")
	e.post("QA Engineer", "QA Engineer", "plain deploy note")
	for _, q := range []string{"q=" + tok, "from=" + tok, "mentions=" + tok, "q=" + url.QueryEscape("x "+tok+" y")} {
		code, r := e.get(q, true)
		assert.Equal(t, 400, code, q)
		assert.Contains(t, r.Error, "can't contain a secret", q)
	}
	code, r := e.get("q=redacted&sort=oldest", true)
	require.Equal(t, 200, code)
	assert.Equal(t, 1, r.Total, "the viewer sees [redacted token] in the text")
	code, r = e.get("q=deploy&sort=oldest", true)
	assert.Equal(t, 2, r.Total)
	code, r = e.get("from="+url.QueryEscape("[redacted token]"), true)
	assert.Equal(t, 1, r.Total, "the sender the viewer sees")
	code, r = e.get("mentions=Backend+Dev", true)
	assert.Equal(t, 1, r.Total)
	// a local caller searches the stored text, secret and all
	code, r = e.get("q="+tok, false)
	assert.Equal(t, 1, r.Total)
	code, r = e.get("from="+tok, false)
	assert.Equal(t, 1, r.Total)
	code, r = e.get("q=redacted", false)
	assert.Equal(t, 0, r.Total, "the stored text has no marker")
}

// A big board costs one bounded scan, not one per page: 300 messages, 7 pages, the same total on each.
func TestBoardMessageSearchPagesAreConsistentOnABigBoard(t *testing.T) {
	e := newMSEnv(t)
	for i := 0; i < 300; i++ {
		e.post("Backend Dev", "Backend Dev", fmt.Sprintf("needle %03d", i))
	}
	got := 0
	cursor := ""
	for pages := 0; pages < 20; pages++ {
		q := "q=needle&limit=50"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		code, r := e.get(q, false)
		require.Equal(t, 200, code)
		assert.Equal(t, 300, r.Total)
		got += len(r.Messages)
		if r.NextCursor == "" {
			break
		}
		cursor = r.NextCursor
	}
	assert.Equal(t, 300, got)
}
