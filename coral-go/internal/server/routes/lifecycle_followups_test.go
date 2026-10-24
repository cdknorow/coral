package routes

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #5213, the follow-ups to #5171: Launch and LaunchTeam run to their end when the request is dropped, the bound of a wake of many grows with the number, and a
// restart asked for twice starts one agent.

func TestLaunchRunsToItsEndWhenTheRequestIsDropped(t *testing.T) {
	for _, at := range []string{"CreateSession", "StartLogging", "SendToTarget"} {
		t.Run(at, func(t *testing.T) {
			_, h, term, _ := setupSessionsTestServer(t)
			req := dropAt(t, term, at, http.MethodPost, "/api/sessions/launch", `{"working_dir":`+jsonString(t.TempDir())+`,"agent_type":"claude"}`, nil)
			rec := httptest.NewRecorder()
			h.Launch(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code, "the launch finished although the request ended at %s: %s", at, rec.Body.String())
			assert.NotEmpty(t, term.sentCommands(), "the agent's start command was typed")
		})
	}
}

func TestLaunchTeamRunsToItsEndWhenTheRequestIsDropped(t *testing.T) {
	// the spaced starts are typed at once: the package default (main_test.go)
	for _, at := range []string{"CreateSession", "StartLogging", "SendToTarget"} {
		t.Run(at, func(t *testing.T) {
			_, h, term, _ := setupSessionsTestServer(t)
			body := `{"board_name":"drop-team-` + strings.ToLower(at) + `","working_dir":` + jsonString(t.TempDir()) + `,"agents":[{"name":"A","prompt":"work"},{"name":"B","prompt":"work"},{"name":"C","prompt":"work"}]}`
			req := dropAt(t, term, at, http.MethodPost, "/api/sessions/launch-team", body, nil)
			rec := httptest.NewRecorder()
			h.LaunchTeam(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, "the team launch finished although the request ended at %s: %s", at, rec.Body.String())
			assert.Len(t, term.sentCommands(), 3, "every agent's start command was typed")
		})
	}
}

// The bound of a wake or a team launch grows with the sessions in it.
func TestLifecycleBoundGrowsWithTheSessions(t *testing.T) {
	prevT, prevP := lifecycleTimeout, lifecyclePerSession
	lifecycleTimeout, lifecyclePerSession = 2*time.Second, time.Second
	t.Cleanup(func() { lifecycleTimeout, lifecyclePerSession = prevT, prevP })
	for n, want := range map[int]time.Duration{0: 2 * time.Second, 1: 3 * time.Second, 10: 12 * time.Second} {
		ctx, cancel := lifecycleCtxN(context.Background(), n)
		dl, ok := ctx.Deadline()
		require.True(t, ok)
		left := time.Until(dl)
		assert.True(t, left <= want && left > want-500*time.Millisecond, "n=%d: %v left, want about %v", n, left, want)
		cancel()
	}
	// The handlers that work on many ask for it by the number; Launch and LaunchTeam run on the lifecycle context too.
	for file, names := range map[string][]string{"sessions_sleep.go": {"Wake", "WakeAll"}, "sessions_launch_team.go": {"LaunchTeam"}} {
		b, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, f := range names {
			i := strings.Index(string(b), "func (h *SessionsHandler) "+f+"(")
			require.GreaterOrEqual(t, i, 0)
			end := strings.Index(string(b)[i+10:], "\nfunc ")
			body := string(b)[i:]
			if end >= 0 {
				body = string(b)[i : i+10+end]
			}
			assert.Contains(t, body, "lifecycleCtxN(", "%s sizes its bound by the number of sessions", f)
		}
	}
	b, err := os.ReadFile("sessions_launch.go")
	require.NoError(t, err)
	assert.Contains(t, string(b), "lifecycleCtx(r.Context())")
}

// A restart asked for twice while the first runs (a double click, a client that retries) starts one agent: the second is refused with 409, and once the
// first is done the agent can be restarted again.
func TestADoubleRestartStartsOneAgent(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	_, h, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const oldID = "double-restart-0001"
	oldName := "claude-" + oldID
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: oldID, AgentType: "claude", AgentName: "coral-go", WorkingDir: t.TempDir()}))
	term.addSession(oldName, t.TempDir())

	hold, release := make(chan struct{}), make(chan struct{})
	var held atomic.Bool // not a sync.Once: a second caller of Once.Do waits for the first's function, so a restart that got past a missing guard would hang here, not fail
	var releaseOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }
	defer letGo() // whatever happens below, the first restart is not left standing
	term.mu.Lock()
	term.onCall = func(step string) {
		if step == "RestartPane" && held.CompareAndSwap(false, true) {
			close(hold) // the first restart stands at its first change to the terminal
			<-release
		}
	}
	term.mu.Unlock()
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+oldName+"/restart", bytes.NewBufferString(`{"session_id":"`+oldID+`","agent_type":"claude"}`))
		req = dropAt2(req, map[string]string{"name": oldName})
		rec := httptest.NewRecorder()
		h.Restart(rec, req)
		return rec
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- post() }()
	select {
	case <-hold:
	case <-time.After(10 * time.Second):
		t.Fatal("the first restart never reached its first change to the terminal")
	}
	// A short deadline on the second request: refused at once when the turn is kept, and a regression (no turn) fails in seconds, not at the package's timeout.
	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { secondDone <- post() }()
	select {
	case second := <-secondDone:
		assert.Equal(t, http.StatusConflict, second.Code, "the second restart is refused while the first runs: %s", second.Body.String())
	case <-time.After(5 * time.Second):
		t.Fatal("the second restart neither ran nor was refused within 5 s: the session's turn is not being kept")
	}
	letGo()
	rec := <-first
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, term.sentCommands(), 1, "one agent was started")
	require.NotNil(t, replaced(t, ss, oldID))
	// The turn is given back: the session (under its new name) can be restarted again.
	_, free := h.beginRestart(oldName)
	require.True(t, free, "the first restart gave its turn back")
}
