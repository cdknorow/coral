package routes

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/cdknorow/coral/internal/store"
	"github.com/stretchr/testify/require"
)

func TestLiveListRejectsUnavailableIdentityMetadata(t *testing.T) {
	for _, failure := range []string{"sessions", "subscriptions", "display_names"} {
		t.Run(failure, func(t *testing.T) {
			_, h, terminal, ss := setupSessionsTestServer(t)
			sid := "00000000-0000-0000-0000-000000000081"
			display, team := "Team agent", "team"
			require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "fixture", WorkingDir: t.TempDir(), DisplayName: &display, BoardName: &team}))
			terminal.addSession("claude-"+sid, "/fixture")
			list := func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				h.List(w, httptest.NewRequest("GET", "/api/sessions/live", nil))
				return w
			}
			require.Equal(t, 200, list().Code)
			switch failure {
			case "sessions":
				require.NoError(t, h.db.Close())
			case "subscriptions":
				require.NoError(t, h.bs.Close())
			case "display_names":
				_, err := h.db.Exec("ALTER TABLE session_meta RENAME TO unavailable_meta")
				require.NoError(t, err)
			}
			w := list()
			require.Equal(t, 503, w.Code, w.Body.String())
			var result map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
			require.Equal(t, "agent_metadata_unavailable", result["code"])
			if failure == "display_names" {
				_, err := h.db.Exec("ALTER TABLE unavailable_meta RENAME TO session_meta")
				require.NoError(t, err)
				require.Equal(t, 200, list().Code, "a later healthy read must recover without restarting")
			}
		})
	}
}

func TestSendUnavailableMetadataDoesNotDeliver(t *testing.T) {
	server, h, terminal, _ := setupSessionsTestServer(t)
	sid := "00000000-0000-0000-0000-000000000082"
	terminal.addSession("claude-"+sid, "/fixture")
	require.NoError(t, h.db.Close())
	resp := postJSON(t, server.URL+"/api/sessions/live/fixture/send", map[string]string{"agent_type": "claude", "session_id": sid, "command": "do not send"})
	defer resp.Body.Close()
	require.Equal(t, 503, resp.StatusCode)
	var result struct {
		Code    string `json:"code"`
		NotSent bool   `json:"not_sent"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.Equal(t, "agent_metadata_unavailable", result.Code)
	require.True(t, result.NotSent)
	require.Empty(t, terminal.sentTo("claude-"+sid))
}
