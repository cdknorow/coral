package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3543: joining a board with a team template whose roles are already held by running agents
// gives each duplicate a numbered name ("Backend Dev 2"), so the two never share a
// subscription, mentions or read state. A role whose holder isn't running keeps its name.
func TestDuplicateRolesGetNumberedNames(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stubCLIPaths(t, store.NewSessionStore(db))
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	handler := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, newMockTerminal(), bs)
	ctx := context.Background()

	launchTeam := func(body map[string]any) (int, map[string]any) {
		body["working_dir"] = t.TempDir()
		body["agents"] = []map[string]any{{"name": "Orchestrator", "prompt": "lead"}, {"name": "Backend Dev", "prompt": "build"}}
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		handler.LaunchTeam(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(b)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	names := func(boardID string) []string {
		subs, err := bs.ListSubscribers(ctx, boardID)
		require.NoError(t, err)
		var out []string
		seen := map[string]bool{}
		for _, s := range subs {
			assert.False(t, seen[s.SessionName], "each agent has its own subscription")
			seen[s.SessionName] = true
			out = append(out, s.SubscriberID)
		}
		sort.Strings(out)
		return out
	}

	code, out := launchTeam(map[string]any{"board_name": "alpha"})
	require.Equal(t, http.StatusOK, code, out)
	id := out["board_id"].(string)
	assert.Equal(t, []string{"Backend Dev", "Orchestrator"}, names(id))

	code, out = launchTeam(map[string]any{"board_id": id})
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []string{"Backend Dev", "Backend Dev 2", "Orchestrator", "Orchestrator 2"}, names(id))
	agents, _ := out["agents"].([]any)
	require.Len(t, agents, 2)
	assert.Equal(t, "Orchestrator 2", agents[0].(map[string]any)["name"], "the answer names the agent as launched")

	code, _ = launchTeam(map[string]any{"board_id": id})
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, names(id), "Backend Dev 3")

	// A mention reaches only that agent, and read state is separate.
	_, err = bs.PostMessage(ctx, id, "Orchestrator", "@Backend Dev 2 please take #7", nil)
	require.NoError(t, err)
	n2, err := bs.CheckUnread(ctx, id, "Backend Dev 2")
	require.NoError(t, err)
	n1, err := bs.CheckUnread(ctx, id, "Backend Dev")
	require.NoError(t, err)
	assert.Equal(t, 1, n2, "the mention reaches Backend Dev 2")
	assert.Equal(t, 0, n1, "and not Backend Dev")
	_, err = bs.ReadMessages(ctx, id, "Backend Dev 2", 50)
	require.NoError(t, err)
	after, err := bs.CheckUnread(ctx, id, "Backend Dev 2")
	require.NoError(t, err)
	assert.Equal(t, 0, after)
	_, err = bs.PostMessage(ctx, id, "Orchestrator", "@Backend Dev and @Backend Dev 2 sync up", nil)
	require.NoError(t, err)
	n1, _ = bs.CheckUnread(ctx, id, "Backend Dev")
	n2, _ = bs.CheckUnread(ctx, id, "Backend Dev 2")
	assert.Equal(t, 1, n1)
	assert.Equal(t, 1, n2, "reading as Backend Dev 2 left Backend Dev's state alone, and vice versa")

	// A number that isn't a numbered agent on this board doesn't take the mention away.
	before, _ := bs.CheckUnread(ctx, id, "Backend Dev")
	_, err = bs.PostMessage(ctx, id, "Orchestrator", "@Backend Dev 9 files changed", nil)
	require.NoError(t, err)
	n1, _ = bs.CheckUnread(ctx, id, "Backend Dev")
	assert.Equal(t, before+1, n1, "no 'Backend Dev 9' on the board: Backend Dev is tagged")

	// Concurrent launches of one role get distinct names (the reservation covers the gap
	// before the subscription is written).
	results := make(chan string, 4)
	for i := 0; i < 4; i++ {
		go func() { results <- handler.uniqueBoardRole(ctx, id, "QA", "claude") }()
	}
	got := map[string]bool{}
	for i := 0; i < 4; i++ {
		got[<-results] = true
	}
	assert.Len(t, got, 4, "four concurrent picks, four names: %v", got)

	// An explicit numbered name that's taken is renumbered from its base role.
	assert.Equal(t, "Backend Dev 4", handler.uniqueBoardRole(ctx, id, "Backend Dev 2", "claude"), "not 'Backend Dev 2 2'")

	// A role whose holder isn't running keeps its name (a relaunch or resurrect).
	_, err = bs.Subscribe(ctx, "beta", "QA", "QA", "claude-gone-session", nil, "all")
	require.NoError(t, err)
	assert.Equal(t, "QA", handler.uniqueBoardRole(ctx, "beta", "QA", "claude"))

	// The 409 shows live agents next to members.
	code, out = launchTeam(map[string]any{"board_name": "alpha"})
	require.Equal(t, http.StatusConflict, code, out)
	cand := out["candidates"].([]any)[0].(map[string]any)
	assert.EqualValues(t, 6, cand["members"])
	assert.EqualValues(t, 6, cand["live_agents"])
	_ = store.NewSessionStore
}

// A name's reservation goes once its subscription is written, so a team killed and launched
// again on its board right away keeps its names (no stale "2").
func TestRoleReservationReleased(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	handler := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, newMockTerminal(), bs)
	ctx := context.Background()
	assert.Equal(t, "Dev", handler.uniqueBoardRole(ctx, "release-board", "Dev", "claude"))
	handler.setupBoardAndPrompt("11111111-aaaa-bbbb-cccc-000000000001", "claude-x1", "claude", "release-board", "Dev")
	// The subscription's session isn't live (the agent was killed): the name is free again.
	assert.Equal(t, "Dev", handler.uniqueBoardRole(ctx, "release-board", "Dev", "claude"))
}

// #3549 (QA's #3543 follow-up): a reset that relaunches an agent under a numbered name
// answers with that name (not the one asked for) and advances that name's read cursor.
func TestResetTeamAnswersLaunchedNames(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stubCLIPaths(t, store.NewSessionStore(db))
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	handler := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, newMockTerminal(), bs)
	ctx := context.Background()

	b, _ := json.Marshal(map[string]any{"board_name": "reset-alpha", "working_dir": t.TempDir(),
		"agents": []map[string]any{{"name": "Orchestrator", "prompt": "lead"}, {"name": "Backend Dev", "prompt": "build"}}})
	rec := httptest.NewRecorder()
	handler.LaunchTeam(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(b)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	id := out["board_id"].(string)

	// Another launch has just picked "Backend Dev" on this board, so the reset's relaunch
	// gets "Backend Dev 2".
	handler.roleRes.mu.Lock()
	handler.roleRes.at[id+"\x00backend dev"] = time.Now()
	handler.roleRes.mu.Unlock()
	_, err = bs.PostMessage(ctx, id, "Orchestrator", "@all before the reset", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("boardName", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec = httptest.NewRecorder()
	handler.ResetTeam(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out = nil
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	var got []string
	for _, a := range out["agents"].([]any) {
		got = append(got, a.(map[string]any)["name"].(string))
	}
	sort.Strings(got)
	assert.Equal(t, []string{"Backend Dev 2", "Orchestrator"}, got, "the answer names the agents as launched")
	n, err := bs.CheckUnread(ctx, id, "Backend Dev 2")
	require.NoError(t, err)
	assert.Equal(t, 0, n, "the relaunched agent's cursor (its numbered name) is advanced")
}
