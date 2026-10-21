package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// Resume keeps the agent's identity as restart does (#3216): its display name,
// its tags and its board subscription (job title, receive mode, peek).
// A sleeping agent comes back awake (#3273), and the old session's tag rows go
// when no history entry shows them.
func TestResumeAndRestartKeepTheAgentsIdentity(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	for _, op := range []string{"resume", "restart"} {
		t.Run(op, func(t *testing.T) {
			server, handler, term, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			cur := "aaaaaaaa-2222-3333-4444-555555555555"
			name := naming.SessionName("claude", cur)
			dir := t.TempDir()
			term.addSession(name, dir)
			board := "team"
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: cur, AgentType: "claude", AgentName: "proj", WorkingDir: dir, BoardName: &board}))
			require.NoError(t, ss.SetDisplayName(ctx, cur, "Builder"))
			tag, err := ss.CreateTag(ctx, "frontend", "")
			require.NoError(t, err)
			require.NoError(t, ss.AddSessionTag(ctx, cur, tag.ID))
			_, err = handler.bs.Subscribe(ctx, board, "Builder", "Builder", name, nil, "all", true)
			require.NoError(t, err)
			require.NoError(t, ss.SetSessionSleeping(ctx, cur, true))

			body := map[string]any{"agent_type": "claude", "session_id": cur}
			if op == "resume" {
				body = map[string]any{"agent_type": "claude", "session_id": "bbbbbbbb-2222-3333-4444-555555555555", "current_session_id": cur}
			}
			b, _ := json.Marshal(body)
			resp, err := http.Post(server.URL+"/api/sessions/live/"+name+"/"+op, "application/json", bytes.NewReader(b))
			require.NoError(t, err)
			var out struct {
				SessionID   string `json:"session_id"`
				SessionName string `json:"session_name"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
			resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.NotEqual(t, cur, out.SessionID)

			dn, err := ss.GetDisplayName(ctx, out.SessionID)
			require.NoError(t, err)
			require.NotNil(t, dn, "the new session has a display name")
			assert.Equal(t, "Builder", *dn)
			tags, err := ss.GetSessionTags(ctx, out.SessionID)
			require.NoError(t, err)
			require.Len(t, tags, 1)
			assert.Equal(t, "frontend", tags[0].Name)
			sub, err := handler.bs.GetSubscriptionBySessionName(ctx, out.SessionName)
			require.NoError(t, err)
			require.NotNil(t, sub, "subscribed under the new session")
			assert.Equal(t, "Builder", sub.SubscriberID)
			assert.Equal(t, "Builder", sub.JobTitle)
			assert.Equal(t, "all", sub.ReceiveMode)
			assert.Equal(t, 1, sub.CanPeek)

			ls, err := ss.GetLiveSession(ctx, out.SessionID)
			require.NoError(t, err)
			require.NotNil(t, ls)
			assert.Equal(t, 0, ls.IsSleeping, "the agent runs again: not asleep (#3273)")
			old, err := ss.GetSessionTags(ctx, cur)
			require.NoError(t, err)
			assert.Empty(t, old, "no history entry shows the old session: its tag rows are gone")
		})
	}
}
