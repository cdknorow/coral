package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// The resolver and status endpoints kept their own "latest event is a
// notification" rule after the list and WebSocket rows moved to the shared
// derivation. The popout merges the resolver record over the live row, so the
// idle reminder showed "Needs input" there. Both endpoints must emit the same
// explicit state fields as the list row.
func TestSingleSessionEndpointsUseTheSharedDerivation(t *testing.T) {
	type step struct{ eventType, summary string }
	cases := []struct {
		name         string
		steps        []step
		needs        bool
		awaiting     bool
		statusState  string
		wantsSummary bool
	}{
		{"pending permission request", []step{{"prompt_submit", ""}, {"notification", permissionNote}}, true, false, "waiting_for_input", true},
		{"turn ended then idle reminder", []step{{"prompt_submit", ""}, {"tool_use", "Ran: ls"}, {"stop", "Agent stopped: unknown"}, {"notification", idleNote}}, false, true, "done", false},
		{"idle reminder alone", []step{{"notification", idleNote}}, false, true, "done", false},
		{"permission denied then turn ended", []step{{"notification", permissionNote}, {"stop", "Agent stopped: unknown"}}, false, true, "done", false},
		{"login notification", []step{{"notification", loginNote}}, false, false, "active", false},
		{"working", []step{{"prompt_submit", ""}, {"tool_use", "Ran: ls"}}, false, false, "active", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, handler, _, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			sid := "00000000-0000-4000-8000-0000000006a1"
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
				SessionID: sid, AgentType: "claude", AgentName: "shared-folder", WorkingDir: t.TempDir(),
			}))
			for _, s := range tc.steps {
				id := sid
				_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "shared-folder", SessionID: &id, EventType: s.eventType, Summary: s.summary})
				require.NoError(t, err)
			}

			get := func(path string) map[string]any {
				resp, err := http.Get(server.URL + path)
				require.NoError(t, err)
				defer resp.Body.Close()
				require.Equal(t, http.StatusOK, resp.StatusCode)
				var got map[string]any
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
				return got
			}

			resolved := get("/api/sessions/" + sid + "/resolve")
			status := get("/api/sessions/" + sid + "/status")
			for name, got := range map[string]map[string]any{"resolve": resolved, "status": status} {
				for _, key := range []string{"waiting_for_input", "awaiting_user", "waiting_reason", "waiting_summary"} {
					require.Containsf(t, got, key, "%s must carry %q explicitly: the popout merges it over the live row", name, key)
				}
				assert.Equalf(t, tc.needs, got["waiting_for_input"], "%s waiting_for_input", name)
				assert.Equalf(t, tc.awaiting, got["awaiting_user"], "%s awaiting_user", name)
				if tc.wantsSummary {
					assert.Equalf(t, "notification", got["waiting_reason"], "%s waiting_reason", name)
					assert.Equalf(t, permissionNote, got["waiting_summary"], "%s waiting_summary", name)
				} else {
					assert.Nilf(t, got["waiting_reason"], "%s waiting_reason", name)
					assert.Nilf(t, got["waiting_summary"], "%s waiting_summary", name)
				}
			}
			// The resolver matches the list row: done is deprecated there.
			assert.Equal(t, false, resolved["done"])
			// The status endpoint keeps its own vocabulary for a finished turn.
			assert.Equal(t, tc.statusState, status["state"])
			assert.Equal(t, tc.awaiting, status["done"])
		})
	}
}

func TestSingleSessionEndpointsNeverWaitWhileSleeping(t *testing.T) {
	server, handler, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-0000000006b1"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: sid, AgentType: "claude", AgentName: "shared-folder", WorkingDir: t.TempDir(),
	}))
	id := sid
	_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "shared-folder", SessionID: &id, EventType: "notification", Summary: permissionNote})
	require.NoError(t, err)
	_, err = handler.db.ExecContext(ctx, "UPDATE live_sessions SET is_sleeping = 1 WHERE session_id = ?", sid)
	require.NoError(t, err)

	for _, path := range []string{"/resolve", "/status"} {
		resp, err := http.Get(server.URL + "/api/sessions/" + sid + path)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		resp.Body.Close()
		assert.Equalf(t, false, got["waiting_for_input"], "%s", path)
		assert.Equalf(t, false, got["awaiting_user"], "%s", path)
		assert.Equalf(t, "sleeping", got["state"], "%s", path)
	}
}
