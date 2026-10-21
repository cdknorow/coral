package routes

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #5171 (Security's reproduction): the dashboard drops a restart request (a reload, a tab change) after the old terminal is touched, and the agent
// was left at a bare shell, neither the old agent nor the new. From the first change to a terminal the restart, resume and wake run to their end
// whatever the request does. Each test ends the request (its context) at one named step of the terminal and requires the agent to be launched.

// dropAt makes the request's context end at the first call of terminal step `at`, and returns the request to hand a handler.
func dropAt(t *testing.T, term *mockSessionTerminal, at, method, path, body string, params map[string]string) *http.Request {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var once sync.Once
	term.mu.Lock()
	term.ctxFails = true
	term.onCall = func(step string) {
		if step == at {
			once.Do(cancel)
		}
	}
	term.mu.Unlock()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rc))
}

func replaced(t *testing.T, ss *store.SessionStore, oldID string) *store.LiveSession {
	t.Helper()
	all, err := ss.GetAllLiveSessions(context.Background())
	require.NoError(t, err)
	for i := range all {
		if all[i].SessionID != oldID && all[i].ResumeFromID != nil && *all[i].ResumeFromID == oldID {
			return &all[i]
		}
	}
	return nil
}

func TestRestartRunsToItsEndWhenTheRequestIsDroppedAtEachStep(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	for _, tc := range []struct {
		name     string
		sleeping bool // no terminal: a new one is made
		at       string
	}{
		{"pane respawned: RestartPane", false, "RestartPane"}, {"pane respawned: RenameSession", false, "RenameSession"},
		{"pane respawned: ClearHistory", false, "ClearHistory"}, {"pane respawned: StartLogging", false, "StartLogging"},
		{"pane respawned: the launch typed", false, "SendToTarget"},
		{"new terminal: CreateSession", true, "CreateSession"}, {"new terminal: StartLogging", true, "StartLogging"},
		{"new terminal: the launch typed", true, "SendToTarget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, h, term, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			const oldID = "drop-restart-0001"
			oldName := "claude-" + oldID
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: oldID, AgentType: "claude", AgentName: "coral-go", WorkingDir: t.TempDir()}))
			if tc.sleeping {
				require.NoError(t, ss.SetSessionSleeping(ctx, oldID, true))
			} else {
				term.addSession(oldName, t.TempDir())
			}
			req := dropAt(t, term, tc.at, http.MethodPost, "/api/sessions/live/"+oldName+"/restart", `{"session_id":"`+oldID+`","agent_type":"claude"}`, map[string]string{"name": oldName})
			rec := httptest.NewRecorder()
			h.Restart(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code, "the restart finished although the request ended at %s: %s", tc.at, rec.Body.String())
			assert.NotEmpty(t, term.sentCommands(), "the agent's start command was typed")
			assert.NotNil(t, replaced(t, ss, oldID), "the new session replaced the old one in the store")
		})
	}
}

func TestWakeRunsToItsEndWhenTheRequestIsDropped(t *testing.T) {
	for _, at := range []string{"CreateSession", "StartLogging", "SendToTarget"} {
		t.Run(at, func(t *testing.T) {
			_, h, term, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			const id = "drop-wake-0001"
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "coral-go", WorkingDir: t.TempDir()}))
			require.NoError(t, ss.SetSessionSleeping(ctx, id, true))
			req := dropAt(t, term, at, http.MethodPost, "/api/sessions/live/"+id+"/wake", "", map[string]string{"sessionID": id})
			rec := httptest.NewRecorder()
			h.WakeSession(rec, req)
			assert.Contains(t, rec.Body.String(), `"ok":true`, "the wake finished although the request ended at %s: %s", at, rec.Body.String())
			assert.NotEmpty(t, term.sentCommands(), "the agent's start command was typed")
			ls, err := ss.GetLiveSession(ctx, id)
			require.NoError(t, err)
			require.NotNil(t, ls)
			assert.Zero(t, ls.IsSleeping, "awake")
		})
	}
}

// Every handler that ends a terminal or makes one and then starts an agent runs the rest on lifecycleCtx; a new one added without it fails here.
func TestLifecycleHandlersRunOnLifecycleCtx(t *testing.T) {
	for file, funcs := range map[string][]string{"sessions_lifecycle.go": {"Restart", "Resume"}, "sessions_sleep.go": {"Wake", "WakeSession", "WakeAll"}} {
		b, err := os.ReadFile(file)
		require.NoError(t, err)
		src := string(b)
		for _, f := range funcs {
			i := strings.Index(src, "func (h *SessionsHandler) "+f+"(")
			require.GreaterOrEqual(t, i, 0, f)
			end := regexp.MustCompile(`\nfunc `).FindStringIndex(src[i+10:])
			body := src[i:]
			if end != nil {
				body = src[i : i+10+end[0]]
			}
			assert.Contains(t, body, "lifecycleCtx(", "%s must run its terminal steps on lifecycleCtx", f)
		}
	}
	// A team reset relaunches on a background context of its own.
	b, err := os.ReadFile("sessions_team_reset.go")
	require.NoError(t, err)
	assert.Contains(t, string(b), "h.launchSession(bgCtx,")
}

// dropAt2 gives req the route parameters a handler reads (no cancellation).
func dropAt2(req *http.Request, params map[string]string) *http.Request {
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
}
