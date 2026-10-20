package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/cdknorow/coral/internal/board/boardtest"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4779: the history list's date_to is the end of that day for a board too: a board whose first message came at 23:59:59.5Z on the date_to day
// is listed (it was compared with "T23:59:59" as text and dropped), and one whose first message came the next day is not.
func TestHistoryBoardDateToIncludesTheWholeDay(t *testing.T) {
	ctx := context.Background()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	boardPath := filepath.Join(t.TempDir(), "board.db")
	bs, err := boardtest.Open(boardPath)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	raw, err := sqlx.Open("sqlite", boardPath+"?_pragma=busy_timeout(30000)")
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })

	first := map[string]string{
		"b-noon":      "2026-10-18T12:00:00Z",
		"b-end":       "2026-10-18T23:59:59Z",
		"b-frac":      "2026-10-18T23:59:59.500Z",
		"b-next":      "2026-10-19T00:00:00Z",
		"b-next-frac": "2026-10-19T00:00:00.001Z",
		"b-earlier":   "2026-10-17T23:59:59.999Z",
	}
	for project, at := range first {
		_, err := bs.Subscribe(ctx, project, "poster", "Poster", "", nil, "all")
		require.NoError(t, err)
		m, err := bs.PostMessage(ctx, project, "poster", "hello", nil)
		require.NoError(t, err)
		_, err = raw.ExecContext(ctx, `UPDATE board_messages SET created_at = ? WHERE id = ?`, at, m.ID)
		require.NoError(t, err)
	}

	h := NewHistoryHandler(db, &config.Config{}, bs)
	r := chi.NewRouter()
	r.Get("/api/sessions/history", h.ListSessions)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/api/sessions/history?type=group&date_to=2026-10-18&page_size=50")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, 200, resp.StatusCode, string(body))
	var out struct {
		Sessions []struct {
			Title string `json:"title"`
		} `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	var got []string
	for _, s := range out.Sessions {
		got = append(got, s.Title)
	}
	require.ElementsMatch(t, []string{"b-noon", "b-end", "b-frac", "b-earlier"}, got)
}
