package routes

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
)

var listFieldsShared = []string{"branch", "git_state", "repo_name", "capabilities", "context_pct", "context_window"}

// sessionFieldsFixture is one live agent with git state, capabilities and a
// recorded turn, so all six fields have values.
func sessionFieldsFixture(t *testing.T) (*SessionsHandler, string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
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
