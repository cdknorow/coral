package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board/boardtest"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4602: an agent token may not start more live agents than the ceiling (default 64, 0 = none): 429 agent_limit, nothing started.
// The Operator (a person at the dashboard or the command line) is never refused, sleeping agents are not counted, and a
// launch-team counts every agent it names.
func TestAgentLaunchCeiling(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	cfg := &config.Config{LogDir: t.TempDir()}
	cfg.UseCoralDir(t.TempDir())
	h := NewSessionsHandler(db, cfg, nil, newMockTerminal(), bs)
	ctx := context.Background()
	workDir := t.TempDir()

	seq := 0
	live := func(n int) {
		for i := 0; i < n; i++ {
			seq++
			sid := fmt.Sprintf("%08d-0000-4000-8000-000000000000", seq)
			require.NoError(t, h.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: fmt.Sprintf("a%d", seq), WorkingDir: workDir}))
		}
	}
	// running makes exactly k of the registered sessions awake and the rest asleep (an Operator launch above may have added some).
	running := func(k int) {
		all, err := h.ss.GetAllLiveSessions(ctx)
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(all), k)
		for i, s := range all {
			require.NoError(t, h.ss.SetSessionSleeping(ctx, s.SessionID, i >= k))
		}
		n, err := h.ss.CountActiveLiveSessions(ctx)
		require.NoError(t, err)
		require.Equal(t, k, n)
	}
	token := Actor{Kind: ActorToken, Name: "Orchestrator", Session: "claude-orch"}
	operator := Actor{Kind: ActorOperator, Name: "Operator"}
	launch := func(a Actor) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"working_dir": workDir, "agent_type": "claude"})
		rec := httptest.NewRecorder()
		h.Launch(rec, withActor(httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(b)), a))
		return rec
	}
	team := func(a Actor, n int) *httptest.ResponseRecorder {
		agents := make([]map[string]any, n)
		for i := range agents {
			agents[i] = map[string]any{"name": fmt.Sprintf("T%d", i), "prompt": "work"}
		}
		b, _ := json.Marshal(map[string]any{"board_name": "ceiling", "working_dir": workDir, "agents": agents})
		rec := httptest.NewRecorder()
		h.LaunchTeam(rec, withActor(httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(b)), a))
		return rec
	}
	refused := func(rec *httptest.ResponseRecorder) bool {
		return rec.Code == http.StatusTooManyRequests && bytes.Contains(rec.Body.Bytes(), []byte(`"agent_limit"`))
	}

	// The default is 64 when nothing is set.
	assert.Equal(t, 64, h.agentLaunchLimit(httptest.NewRequest("GET", "/", nil)))
	require.NoError(t, h.ss.SetSetting(ctx, AgentLaunchLimitSetting, "3"))
	assert.Equal(t, 3, h.agentLaunchLimit(httptest.NewRequest("GET", "/", nil)))

	live(3)
	rec := launch(token)
	require.True(t, refused(rec), "3 live, ceiling 3: an agent token adding one is refused (got %d %s)", rec.Code, rec.Body.String())
	assert.Equal(t, "action", rec.Header().Get(httputil.ActionLimitHeader), "a limit on this caller, not an overloaded Coral")
	var reply map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reply))
	assert.Equal(t, float64(3), reply["live"])
	assert.Equal(t, float64(3), reply["max"])
	assert.Equal(t, true, reply["not_started"])
	assert.Contains(t, reply["message"], AgentLaunchLimitSetting, "the message names the setting and how to raise it")
	assert.Contains(t, reply["message"], "PUT /api/settings")
	assert.Equal(t, reply["error"], reply["message"])
	n, _ := h.ss.CountLiveSessions(ctx)
	assert.Equal(t, 3, n, "nothing was started")

	assert.False(t, refused(launch(operator)), "the Operator is never refused")
	assert.False(t, refused(launch(Actor{Kind: ActorClaimed, Name: "x"})), "only an agent token is limited")

	// A team counts every agent it names.
	running(3)
	assert.True(t, refused(team(token, 1)))
	// Sleeping agents are not running processes.
	running(1)
	assert.False(t, refused(launch(token)), "1 running of 3: room for one more")
	running(1)
	assert.True(t, refused(team(token, 3)), "1 running + 3 would pass the ceiling of 3")
	running(1)
	assert.False(t, refused(team(token, 2)), "1 running + 2 reaches it, which is allowed")

	// 0 turns it off; a bad value is the default.
	require.NoError(t, h.ss.SetSetting(ctx, AgentLaunchLimitSetting, "0"))
	running(3)
	assert.False(t, refused(team(token, 32)))
	require.NoError(t, h.ss.SetSetting(ctx, AgentLaunchLimitSetting, "banana"))
	assert.Equal(t, 64, h.agentLaunchLimit(httptest.NewRequest("GET", "/", nil)))
	require.NoError(t, h.ss.SetSetting(ctx, AgentLaunchLimitSetting, "-5"))
	assert.Equal(t, 64, h.agentLaunchLimit(httptest.NewRequest("GET", "/", nil)), "a negative stored value is not a ceiling that refuses everything")

	// A store that cannot be read is never a refusal: the ceiling is the default, and a failed count lets the launch through.
	require.NoError(t, h.ss.SetSetting(ctx, AgentLaunchLimitSetting, "1"))
	live(1)
	running(1)
	require.True(t, refused(launch(token)), "the ceiling of 1 is reached")
	require.NoError(t, db.Close())
	assert.Equal(t, 64, h.agentLaunchLimit(httptest.NewRequest("GET", "/", nil)), "unreadable settings: the default, not 0 (no ceiling)")
	rec = httptest.NewRecorder()
	assert.False(t, h.refuseAgentLaunchOverLimit(rec, withActor(httptest.NewRequest(http.MethodPost, "/x", nil), token), 1), "a failed count never refuses")
	assert.Equal(t, 200, rec.Code, "and wrote nothing")
}
