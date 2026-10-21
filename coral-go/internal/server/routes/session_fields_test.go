package routes

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

var listFieldsShared = []string{"branch", "git_state", "repo_name", "capabilities", "context_pct", "context_window",
	"summary", "status", "display_name", "icon"}

// sessionFieldsFixture is one live agent with git state, capabilities and a
// recorded turn, so all six fields have values.
func sessionFieldsFixture(t *testing.T) (*SessionsHandler, string) {
	t.Helper()
	ctx := context.Background()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	term := newMockTerminal()
	h := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, term, nil)
	h.SetTokenStore(store.NewTokenUsageStore(db))

	sid := "11111111-2222-3333-4444-555555555555"
	term.addSession("claude-"+sid, "/tmp/proj")
	model := "claude-sonnet-4-5"
	caps := `{"allow":["Read","Edit"],"deny":["Bash"]}`
	require.NoError(t, h.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "proj", WorkingDir: "/tmp/proj", Model: &model, Capabilities: &caps}))
	remote := "git@github.com:acme/widgets.git"
	ts := "2026-10-11T00:00:00Z"
	require.NoError(t, store.NewGitStore(db).UpsertGitSnapshot(ctx, &store.GitSnapshot{
		AgentName: "proj", AgentType: "claude", WorkingDirectory: "/tmp/proj", Branch: "feature/x",
		CommitHash: "abc123", CommitSubject: "wip", CommitTimestamp: &ts, SessionID: &sid, RemoteURL: &remote,
		Worktree: true, BaseBranch: "main", Ahead: 2, Behind: 1, DirtyCount: 3,
	}))
	require.NoError(t, store.NewTokenUsageStore(db).RecordUsage(ctx, &store.TokenUsage{
		SessionID: sid, AgentName: "proj", AgentType: "claude", InputTokens: 40_000, OutputTokens: 500,
		Source: "jsonl", Model: model, RecordedAt: time.Now().UTC().Format(time.RFC3339),
	}))
	return h, sid
}

func pickFields(t *testing.T, entry map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, k := range listFieldsShared {
		v, ok := entry[k]
		if !ok {
			out[k] = "<absent>"
			continue
		}
		b, err := json.Marshal(v)
		require.NoError(t, err)
		out[k] = string(b)
	}
	return out
}

// For the same session state, /ws/coral carries the six fields with the same
// names, types and values as /api/sessions/live.
func TestSessionFields_WSMatchesList(t *testing.T) {
	h, sid := sessionFieldsFixture(t)

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest("GET", "/api/sessions/live", nil))
	require.Equal(t, 200, w.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))

	ws, err := h.buildSessionListForWS(httptest.NewRequest("GET", "/ws/coral", nil))
	require.NoError(t, err)
	// Compare as the client receives them: JSON round trip.
	b, err := json.Marshal(ws)
	require.NoError(t, err)
	var wsDecoded []map[string]any
	require.NoError(t, json.Unmarshal(b, &wsDecoded))

	find := func(entries []map[string]any) map[string]any {
		for _, e := range entries {
			if e["session_id"] == sid {
				return e
			}
		}
		t.Fatalf("session %s not in list", sid)
		return nil
	}
	listFields, wsFields := pickFields(t, find(list)), pickFields(t, find(wsDecoded))
	require.Equal(t, listFields, wsFields)
	// And every field is populated, so the comparison is meaningful.
	require.Equal(t, `"feature/x"`, wsFields["branch"])
	require.Equal(t, `"acme/widgets"`, wsFields["repo_name"])
	require.JSONEq(t, `{"is_worktree":true,"base_branch":"main","ahead":2,"behind":1,"dirty":3}`, wsFields["git_state"])
	require.JSONEq(t, `{"allow":["Read","Edit"],"deny":["Bash"]}`, wsFields["capabilities"])
	require.Equal(t, `200000`, wsFields["context_window"])
	require.Equal(t, `20`, wsFields["context_pct"])
	// An agent with no goal yet: summary is null on both (#3004), not "".
	require.Equal(t, `null`, wsFields["summary"])
	require.Equal(t, `null`, wsFields["status"])
	require.Equal(t, `""`, wsFields["display_name"])
	require.Equal(t, `null`, wsFields["icon"])
}

// An idle agent's six fields do not change between ticks, so they cause no
// coral_diff.
func TestSessionFields_IdleAgentNoDiff(t *testing.T) {
	h, sid := sessionFieldsFixture(t)
	req := httptest.NewRequest("GET", "/ws/coral", nil)
	snapshot := func() map[string]string {
		sessions, err := h.buildSessionListForWS(req)
		require.NoError(t, err)
		for _, s := range sessions {
			if s["session_id"] == sid {
				return pickFields(t, s)
			}
		}
		t.Fatal("session missing")
		return nil
	}
	first := snapshot()
	time.Sleep(1100 * time.Millisecond)
	require.Equal(t, first, snapshot())
}

// With a goal, a display name and an icon, the text fields still match.
func TestSessionFields_WSMatchesListWithGoalAndName(t *testing.T) {
	h, sid := sessionFieldsFixture(t)
	ctx := context.Background()
	_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "proj", SessionID: &sid, EventType: "goal", Summary: "Ship the feed parity fix"})
	require.NoError(t, err)
	require.NoError(t, h.ss.SetDisplayName(ctx, sid, "Builder"))
	icon := "🛠"
	require.NoError(t, h.ss.SetIcon(ctx, sid, &icon))

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest("GET", "/api/sessions/live", nil))
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	ws, err := h.buildSessionListForWS(httptest.NewRequest("GET", "/ws/coral", nil))
	require.NoError(t, err)
	b, err := json.Marshal(ws)
	require.NoError(t, err)
	var wsDecoded []map[string]any
	require.NoError(t, json.Unmarshal(b, &wsDecoded))
	find := func(entries []map[string]any) map[string]any {
		for _, e := range entries {
			if e["session_id"] == sid {
				return e
			}
		}
		t.Fatalf("session %s not in list", sid)
		return nil
	}
	wsFields := pickFields(t, find(wsDecoded))
	require.Equal(t, pickFields(t, find(list)), wsFields)
	require.Equal(t, `"Ship the feed parity fix"`, wsFields["summary"])
	require.Equal(t, `"Builder"`, wsFields["display_name"])
	require.Equal(t, `"🛠"`, wsFields["icon"])
}

// An archived agent is a sleeping placeholder row with archived=true on both
// /api/sessions/live and /ws/coral; an awake or plain sleeping one says false.
// Archived rows come last (a capped reader keeps live rows) and never report
// waiting_for_input.
func TestSessionFields_ArchivedOnListAndWS(t *testing.T) {
	h, awake := sessionFieldsFixture(t)
	ctx := context.Background()
	asleep, archived := "22222222-2222-3333-4444-555555555555", "33333333-2222-3333-4444-555555555555"
	// The archived agent is registered first: only the ordering puts it last.
	for _, sid := range []string{archived, asleep} {
		require.NoError(t, h.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "proj-" + sid[:4], WorkingDir: "/tmp/proj"}))
	}
	require.NoError(t, h.ss.SetSessionSleeping(ctx, asleep, true))
	require.NoError(t, h.ss.SetSessionArchived(ctx, archived, true))

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest("GET", "/api/sessions/live", nil))
	require.Equal(t, 200, w.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	ws, err := h.buildSessionListForWS(httptest.NewRequest("GET", "/ws/coral", nil))
	require.NoError(t, err)
	b, err := json.Marshal(ws)
	require.NoError(t, err)
	var wsDecoded []map[string]any
	require.NoError(t, json.Unmarshal(b, &wsDecoded))

	want := map[string][2]any{awake: {false, false}, asleep: {true, false}, archived: {true, true}} // sleeping, archived
	for name, entries := range map[string][]map[string]any{"list": list, "ws": wsDecoded} {
		got := map[string][2]any{}
		for _, e := range entries {
			got[e["session_id"].(string)] = [2]any{e["sleeping"], e["archived"]}
			if e["archived"] == true {
				require.Equal(t, false, e["waiting_for_input"], "%s: an archived agent is never waiting for input", name)
			}
		}
		require.Equal(t, want, got, name)
		require.Equal(t, archived, entries[len(entries)-1]["session_id"], "%s: archived rows come last", name)
	}
}

// The hub feed (/ws/coral?feed=hub) carries 'archived' like /api/sessions/live,
// and archiving or unarchiving an agent reaches it as a coral_diff (#3030:
// Coral Hub reconciles the feed against the list).
func TestWSCoral_HubFeedCarriesArchivedFlips(t *testing.T) {
	server, handler := setupTestServer(t)
	ctx := context.Background()
	ss := store.NewSessionStore(handler.db)
	sid := "archive-session-001"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: sid, AgentType: "claude", AgentName: "old-reviewer", WorkingDir: "/tmp/test",
		IsSleeping: 1, CreatedAt: "2026-01-01T00:00:00Z",
	}))

	wsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(wsCtx, "ws"+server.URL[4:]+"/ws/coral?feed=hub", nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	var first struct {
		Type     string           `json:"type"`
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, wsjson.Read(wsCtx, conn, &first))
	require.Equal(t, "coral_update", first.Type)
	archivedIn := func(entries []map[string]any) (any, bool) {
		for _, e := range entries {
			if e["session_id"] == sid {
				v, ok := e["archived"]
				return v, ok
			}
		}
		return nil, false
	}
	v, ok := archivedIn(first.Sessions)
	require.True(t, ok, "archived is present on the hub feed's first update")
	require.Equal(t, false, v)

	listArchived := func() any {
		w := httptest.NewRecorder()
		handler.List(w, httptest.NewRequest("GET", "/api/sessions/live", nil))
		require.Equal(t, 200, w.Code)
		var list []map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
		v, ok := archivedIn(list)
		require.True(t, ok)
		return v
	}
	// Wait for the coral_diff that carries this session's new archived value.
	awaitDiff := func(want bool) {
		t.Helper()
		for {
			var msg struct {
				Type    string           `json:"type"`
				Changed []map[string]any `json:"changed"`
			}
			require.NoError(t, wsjson.Read(wsCtx, conn, &msg), "no coral_diff with archived=%v", want)
			if msg.Type != "coral_diff" {
				continue
			}
			if v, ok := archivedIn(msg.Changed); ok {
				require.Equal(t, want, v)
				return
			}
		}
	}
	require.NoError(t, ss.SetSessionArchived(ctx, sid, true))
	awaitDiff(true)
	require.Equal(t, true, listArchived(), "the list agrees with the hub feed")
	require.NoError(t, ss.SetSessionArchived(ctx, sid, false))
	awaitDiff(false)
	require.Equal(t, false, listArchived(), "the list agrees with the hub feed")
}

// An archived agent whose terminal outlived the archive (a failed kill, or a
// tmux pane that survived a restart) is still archived on the list and the
// feed, and a pending permission request does not make it waiting_for_input.
func TestSessionFields_ArchivedWithLivePaneNeverWaits(t *testing.T) {
	h, sid := sessionFieldsFixture(t) // a running agent with a live pane
	ctx := context.Background()
	_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "proj", SessionID: &sid, EventType: "notification", Summary: permissionNote})
	require.NoError(t, err)
	// Archive it in the store only: the pane is left running.
	require.NoError(t, h.ss.SetSessionArchived(ctx, sid, true))

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest("GET", "/api/sessions/live", nil))
	require.Equal(t, 200, w.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	ws, err := h.buildSessionListForWS(httptest.NewRequest("GET", "/ws/coral", nil))
	require.NoError(t, err)
	b, err := json.Marshal(ws)
	require.NoError(t, err)
	var wsDecoded []map[string]any
	require.NoError(t, json.Unmarshal(b, &wsDecoded))

	for name, entries := range map[string][]map[string]any{"list": list, "ws": wsDecoded} {
		var row map[string]any
		for _, e := range entries {
			if e["session_id"] == sid {
				row = e
			}
		}
		require.NotNil(t, row, name)
		require.Equal(t, true, row["archived"], name)
		require.Equal(t, false, row["waiting_for_input"], "%s: archived never waits", name)
	}
}

// The feed's rows are the list's rows (#4599: they came from two builders, and the feed's had drifted: no prompt,
// model or cache tokens, its own team-name query, a sleeping agent's lingering terminal shown awake). Every field,
// with an awake agent, a prompt, a sleeping placeholder and an archived one.
func TestFeedRowsAreTheListRows(t *testing.T) {
	h, sid := sessionFieldsFixture(t)
	ctx := context.Background()
	prompt, board := "build the widget", "widgets"
	require.NoError(t, h.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "22222222-2222-3333-4444-555555555555", AgentType: "codex", AgentName: "sleeper", WorkingDir: "/tmp/s", Prompt: &prompt, BoardName: &board, IsSleeping: 1}))
	require.NoError(t, h.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "33333333-2222-3333-4444-555555555555", AgentType: "claude", AgentName: "archived", WorkingDir: "/tmp/a", IsSleeping: 1, IsArchived: 1}))
	require.NoError(t, h.ss.SetDisplayName(ctx, sid, "Widget Dev"))

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest("GET", "/api/sessions/live", nil))
	require.Equal(t, 200, w.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	ws, err := h.buildSessionListForWS(httptest.NewRequest("GET", "/ws/coral", nil))
	require.NoError(t, err)
	b, err := json.Marshal(ws)
	require.NoError(t, err)
	var feed []map[string]any
	require.NoError(t, json.Unmarshal(b, &feed))
	require.Len(t, list, 3)
	assert.Equal(t, list, feed)
	assert.Equal(t, "claude-sonnet-4-5", feed[0]["model"], "the fields the feed's own builder left out are there")
}
