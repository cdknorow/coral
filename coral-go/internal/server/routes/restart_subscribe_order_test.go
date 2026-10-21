package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// A restarted or resumed agent's first request carries the new session's token, and with no
// subscription under the new session name it is refused (identity_mismatch: not on the board).
// The identity harness saw a restart's first post refused 39 ms after its launch command, because
// the board subscription was moved to the new name after the launch. It must exist when the
// launch command is typed.
func TestRestartAndResumeSubscribeTheNewSessionBeforeTheLaunch(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	for _, op := range []string{"restart", "resume"} {
		t.Run(op, func(t *testing.T) {
			server, handler, term, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			oldSID := "22222222-2222-4222-8222-222222222222"
			oldName := naming.SessionName("claude", oldSID)
			dir := t.TempDir()
			term.addSession(oldName, dir)
			board := "order-team"
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: oldSID, AgentType: "claude", AgentName: "proj", WorkingDir: dir, BoardName: &board}))
			require.NoError(t, ss.SetDisplayName(ctx, oldSID, "Dev One"))
			_, err := handler.bs.Subscribe(ctx, board, "Dev One", "Dev One", oldName, nil, "all", true)
			require.NoError(t, err)

			var mu sync.Mutex
			sawNew := map[string]bool{} // a session name other than the old one -> it was subscribed when a send to it was made
			term.mu.Lock()
			term.onSend = func(target string) {
				name, _, _ := strings.Cut(target, ":")
				name = strings.TrimSuffix(name, ".0")
				if name == oldName {
					return
				}
				sub, err := handler.bs.GetSubscriptionBySessionName(ctx, name)
				mu.Lock()
				sawNew[name] = err == nil && sub != nil && sub.Project == board && sub.SubscriberID == "Dev One"
				mu.Unlock()
			}
			term.mu.Unlock()

			body := map[string]any{"agent_type": "claude", "session_id": oldSID}
			if op == "resume" {
				body = map[string]any{"agent_type": "claude", "session_id": "33333333-3333-4333-8333-333333333333", "current_session_id": oldSID}
			}
			b, _ := json.Marshal(body)
			resp, err := http.Post(server.URL+"/api/sessions/live/"+oldName+"/"+op, "application/json", bytes.NewReader(b))
			require.NoError(t, err)
			defer resp.Body.Close()
			var out struct {
				SessionName string `json:"session_name"`
			}
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))

			mu.Lock()
			defer mu.Unlock()
			require.NotEmpty(t, sawNew, "the launch command was typed into the new session")
			for name, subscribed := range sawNew {
				assert.True(t, subscribed, "%s: %s had no board subscription when its launch was typed", op, name)
			}
			assert.True(t, sawNew[out.SessionName], "%s: the launch went to the new session %s (saw %v)", op, out.SessionName, sawNew)
		})
	}
}

// When the launch command can't be typed, the subscription that moved to the new session name
// before it goes back to the old one: a failed restart or resume leaves the agent on its board.
func TestFailedRestartOrResumePutsTheSubscriptionBack(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	for _, op := range []string{"restart", "resume"} {
		t.Run(op, func(t *testing.T) {
			server, handler, term, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			oldSID := "44444444-4444-4444-8444-444444444444"
			oldName := naming.SessionName("claude", oldSID)
			dir := t.TempDir()
			term.addSession(oldName, dir)
			board := "back-team"
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: oldSID, AgentType: "claude", AgentName: "proj", WorkingDir: dir, BoardName: &board}))
			require.NoError(t, ss.SetDisplayName(ctx, oldSID, "Dev One"))
			_, err := handler.bs.Subscribe(ctx, board, "Dev One", "Dev One", oldName, nil, "all", true)
			require.NoError(t, err)

			term.mu.Lock()
			term.sendErr = errors.New("terminal gone")
			term.mu.Unlock()
			body := map[string]any{"agent_type": "claude", "session_id": oldSID}
			if op == "resume" {
				body = map[string]any{"agent_type": "claude", "session_id": "55555555-5555-4555-8555-555555555555", "current_session_id": oldSID}
			}
			b, _ := json.Marshal(body)
			resp, err := http.Post(server.URL+"/api/sessions/live/"+oldName+"/"+op, "application/json", bytes.NewReader(b))
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)

			subs, err := handler.bs.ListSubscribers(ctx, board)
			require.NoError(t, err)
			require.Len(t, subs, 1, "one subscription")
			assert.Equal(t, oldName, subs[0].SessionName, "%s failed: the agent is back on its old session", op)
			assert.Equal(t, "Dev One", subs[0].SubscriberID)
			assert.Equal(t, "all", subs[0].ReceiveMode)
			assert.Equal(t, 1, subs[0].CanPeek)
		})
	}
}
