package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// #4943: restarting or resuming an agent gives it a new session id; it keeps its place in the agent list. The list is served in the order
// the agents' places were taken (list_at), which a replacement session inherits, so a team (the earliest of its agents' places) keeps its
// place among the teams too, and a new agent or team comes last whatever order the terminal lists them in.

func servedRows(t *testing.T, serverURL string) []struct {
	SessionID string `json:"session_id"`
	ListAt    string `json:"list_at"`
} {
	t.Helper()
	resp, err := http.Get(serverURL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	var rows []struct {
		SessionID string `json:"session_id"`
		ListAt    string `json:"list_at"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
	return rows
}

// servedOrder is the agent list as GET /api/sessions/live serves it: the ids, top to bottom (the dashboard shows it in this order).
func servedOrder(t *testing.T, serverURL string) []string {
	t.Helper()
	var ids []string
	for _, r := range servedRows(t, serverURL) {
		ids = append(ids, r.SessionID)
	}
	return ids
}

func restartOrResume(t *testing.T, serverURL, op, id string) string {
	t.Helper()
	body := map[string]any{"agent_type": "claude", "session_id": id}
	if op == "resume" {
		body = map[string]any{"agent_type": "claude", "session_id": "dddddddd-2222-3333-4444-555555555555", "current_session_id": id}
	}
	b, _ := json.Marshal(body)
	resp, err := http.Post(serverURL+"/api/sessions/live/"+naming.SessionName("claude", id)+"/"+op, "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	defer resp.Body.Close()
	var out struct {
		SessionID string `json:"session_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotEqual(t, id, out.SessionID)
	return out.SessionID
}

func TestRestartAndResumeKeepTheAgentsPlaceInTheList(t *testing.T) {
	ids := []string{"cccccccc-2222-3333-4444-555555555555", "aaaaaaaa-2222-3333-4444-555555555555", "bbbbbbbb-2222-3333-4444-555555555555"} // creation order is not alphabetical
	for _, op := range []string{"restart", "resume"} {
		t.Run(op, func(t *testing.T) {
			server, handler, term, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			dir := t.TempDir()
			board := "team-one"
			for _, id := range ids {
				term.addSession(naming.SessionName("claude", id), dir)
				require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "proj", WorkingDir: dir, BoardName: &board}))
				time.Sleep(2 * time.Millisecond) // sleep-ok: distinct creation times
			}
			require.NoError(t, ss.SetSetting(ctx, "session_order", `["bbbbbbbb-2222-3333-4444-555555555555","cccccccc-2222-3333-4444-555555555555","aaaaaaaa-2222-3333-4444-555555555555"]`))
			require.NoError(t, ss.SetSetting(ctx, "group_order", `["team-two","team-one"]`))
			assert.Equal(t, ids, servedOrder(t, server.URL), "the served list is the creation order, whatever order the terminal lists them in")

			fresh := restartOrResume(t, server.URL, op, ids[1]) // the middle agent
			want := []string{ids[0], fresh, ids[2]}
			assert.Equal(t, want, servedOrder(t, server.URL), "the middle agent is still in the middle of the served list")
			for i := 0; i < 3; i++ {
				assert.Equal(t, want, servedOrder(t, server.URL), "and stays there on every refresh")
			}
			wsRows, err := handler.buildSessionListForWS(httptest.NewRequest(http.MethodGet, "/ws/coral", nil))
			require.NoError(t, err)
			var wsIDs []string
			for _, row := range wsRows {
				wsIDs = append(wsIDs, row["session_id"].(string))
			}
			assert.Equal(t, want, wsIDs, "and in the feed's list, which the page's diffs and full updates come from")

			rows := servedRows(t, server.URL)
			require.Len(t, rows, 3)
			assert.NotEmpty(t, rows[1].ListAt, "each row says when its place was taken, so the page can place a session a feed diff adds")
			assert.Less(t, rows[0].ListAt, rows[1].ListAt)
			assert.Less(t, rows[1].ListAt, rows[2].ListAt)

			settings, err := ss.GetSettings(ctx)
			require.NoError(t, err)
			assert.JSONEq(t, `["`+ids[2]+`","`+ids[0]+`","`+fresh+`"]`, settings["session_order"], "the agent's place in the saved order (within its group) moves to the new id")
			assert.JSONEq(t, `["team-two","team-one"]`, settings["group_order"], "the teams keep their saved order")
		})
	}
}

// Teams are placed by their earliest agent: the team of a restarted agent keeps its place among the teams, and a new team comes last.
func TestTeamsKeepTheirOrderAcrossRestartsAndLaunches(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	const (
		t1a = "11111111-2222-3333-4444-555555555555"
		t1b = "22222222-2222-3333-4444-555555555555"
		t2a = "33333333-2222-3333-4444-555555555555"
		t2b = "44444444-2222-3333-4444-555555555555"
		t3a = "55555555-2222-3333-4444-555555555555"
	)
	server, _, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	dir := t.TempDir()
	reg := func(id, board string) {
		term.addSession(naming.SessionName("claude", id), dir)
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "proj", WorkingDir: dir, BoardName: &board}))
		time.Sleep(2 * time.Millisecond) // sleep-ok: distinct creation times
	}
	reg(t1a, "team-one")
	reg(t2a, "team-two")
	reg(t1b, "team-one") // joined later: team one's place is still its first agent's
	reg(t2b, "team-two")
	assert.Equal(t, []string{t1a, t2a, t1b, t2b}, servedOrder(t, server.URL))

	fresh := restartOrResume(t, server.URL, "restart", t1a) // team one's first agent: its team must not drop below team two
	assert.Equal(t, []string{fresh, t2a, t1b, t2b}, servedOrder(t, server.URL), "team one keeps its place among the teams")
	fresh2 := restartOrResume(t, server.URL, "restart", t2a)
	assert.Equal(t, []string{fresh, fresh2, t1b, t2b}, servedOrder(t, server.URL))

	reg(t3a, "team-three") // a new team
	assert.Equal(t, []string{fresh, fresh2, t1b, t2b, t3a}, servedOrder(t, server.URL), "a new team comes last")
	fresh3 := restartOrResume(t, server.URL, "restart", fresh)
	assert.Equal(t, []string{fresh3, fresh2, t1b, t2b, t3a}, servedOrder(t, server.URL), "and a repeated restart changes nothing")
}
