package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// QA acceptance for the Agent state contract (tasks #165/#166).
//
// Events go through TaskStore.InsertAgentEvent and come back through both
// payload builders, so the store query, the derive function, and HTTP/WS
// parity are covered together. Summaries use the exact strings the hook
// writes ("Notification: <message>").

const (
	permissionNote = "Notification: Claude needs your permission to use Bash"
	idleNote       = "Notification: Claude is waiting for your input"
	loginNote      = "Notification: Claude Code login successful"
	mcpInputNote   = "Notification: github needs your input"
	unknownNote    = "Notification: A new version is available, ask a question about permission settings"
)

type stateStep struct {
	eventType string
	summary   string
}

type wantState struct {
	needs, awaiting bool
}

func stateFields(t *testing.T, entry map[string]any) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, k := range []string{"waiting_for_input", "awaiting_user", "working", "not_started", "sleeping", "stuck", "done", "waiting_reason", "waiting_summary", "board_is_orchestrator"} {
		v, ok := entry[k]
		require.Truef(t, ok, "payload must carry %q explicitly (client merge keeps omitted fields)", k)
		out[k] = v
	}
	return out
}

func httpAndWSState(t *testing.T, server *httptest.Server, handler *SessionsHandler, sid string) (map[string]any, map[string]any) {
	t.Helper()
	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	var list []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	var httpEntry map[string]any
	for _, s := range list {
		if s["session_id"] == sid {
			httpEntry = s
		}
	}
	require.NotNil(t, httpEntry, "session missing from HTTP list")

	req := httptest.NewRequest(http.MethodGet, "/ws/coral", nil)
	wsList, err := handler.buildSessionListForWS(req)
	require.NoError(t, err)
	var wsEntry map[string]any
	for _, s := range wsList {
		if s["session_id"] == sid {
			// Round-trip through JSON so types compare like the wire format.
			raw, _ := json.Marshal(s)
			require.NoError(t, json.Unmarshal(raw, &wsEntry))
		}
	}
	require.NotNil(t, wsEntry, "session missing from WS list")
	return stateFields(t, httpEntry), stateFields(t, wsEntry)
}

func TestSessionStateSequences_StoreToPayload_HTTPAndWSParity(t *testing.T) {
	cases := []struct {
		name  string
		steps []stateStep
		want  wantState
	}{
		{"no events is idle", nil, wantState{false, false}},
		{"stop is your turn", []stateStep{{"stop", "Agent stopped: end_turn"}}, wantState{false, true}},
		{"permission notification needs input", []stateStep{{"notification", permissionNote}}, wantState{true, false}},
		// A permission dialog blocks the turn, so a stop proves the request was
		// answered (denied, or approved and the tool failed: neither records a
		// tool_use). A finished, idle turn must never show Needs input (#174).
		{"permission denied then stop is your turn", []stateStep{{"prompt_submit", ""}, {"notification", permissionNote}, {"stop", "Agent stopped: unknown"}}, wantState{false, true}},
		{"approved tool fails then stop is your turn", []stateStep{{"prompt_submit", ""}, {"tool_use", "Ran: ls"}, {"notification", permissionNote}, {"stop", "Agent stopped: unknown"}}, wantState{false, true}},
		{"permission then stop then idle reminder is your turn", []stateStep{{"notification", permissionNote}, {"stop", ""}, {"notification", idleNote}}, wantState{false, true}},
		{"new request after a new prompt is pending again", []stateStep{{"notification", permissionNote}, {"stop", ""}, {"prompt_submit", ""}, {"notification", permissionNote}}, wantState{true, false}},
		{"MCP input dialog needs input", []stateStep{{"prompt_submit", ""}, {"notification", mcpInputNote}}, wantState{true, false}},
		{"unknown notification mentioning permission and question changes nothing", []stateStep{{"stop", ""}, {"notification", unknownNote}}, wantState{false, true}},
		{"tool_use resolves needs input", []stateStep{{"notification", permissionNote}, {"stop", ""}, {"tool_use", "Ran: ls"}}, wantState{false, false}},
		{"prompt_submit resolves your turn", []stateStep{{"stop", ""}, {"prompt_submit", "User submitted prompt"}}, wantState{false, false}},
		{"session_reset clears durable needs input", []stateStep{{"notification", permissionNote}, {"session_reset", "Session reset: /clear"}}, wantState{false, false}},
		{"session_reset clears your turn", []stateStep{{"stop", ""}, {"session_reset", "Session reset: /clear"}}, wantState{false, false}},
		{"work then stop again is your turn", []stateStep{{"stop", ""}, {"prompt_submit", ""}, {"tool_use", "Ran: ls"}, {"stop", ""}}, wantState{false, true}},
		// Claude Code fires this about a minute after every turn end. It is
		// the old "rewritten to stop" message and must stay the quiet state.
		{"idle notification after stop stays your turn", []stateStep{{"stop", ""}, {"notification", idleNote}}, wantState{false, true}},
		{"idle notification alone is your turn", []stateStep{{"notification", idleNote}}, wantState{false, true}},
		{"idle notification alone never downgrades a pending request", []stateStep{{"prompt_submit", ""}, {"notification", permissionNote}, {"notification", idleNote}}, wantState{true, false}},
		{"informational notification changes nothing", []stateStep{{"stop", ""}, {"notification", loginNote}}, wantState{false, true}},
		{"informational notification alone is idle", []stateStep{{"notification", loginNote}}, wantState{false, false}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, handler, terminal, sessStore := setupSessionsTestServer(t)
			sid := "00000000-0000-4000-8000-0000000005a1"
			terminal.addSession("claude-"+sid, t.TempDir())
			ctx := context.Background()
			require.NoError(t, sessStore.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "shared-folder", WorkingDir: t.TempDir()}))
			for _, step := range tc.steps {
				id := sid
				_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "shared-folder", SessionID: &id, EventType: step.eventType, Summary: step.summary})
				require.NoError(t, err)
			}

			httpState, wsState := httpAndWSState(t, server, handler, sid)
			assert.Equal(t, httpState, wsState, "HTTP and WS must derive identical state fields")
			assert.Equal(t, tc.want.needs, httpState["waiting_for_input"], "waiting_for_input")
			assert.Equal(t, tc.want.awaiting, httpState["awaiting_user"], "awaiting_user")
			assert.Equal(t, false, httpState["done"], "done is deprecated and always false")
			assert.Equal(t, false, httpState["stuck"], "stuck has no authoritative source")
			assert.Nil(t, httpState["board_is_orchestrator"], "no board subscription means null")
			if tc.want.needs {
				assert.Equal(t, "notification", httpState["waiting_reason"])
				wantSummary := permissionNote
				if tc.name == "MCP input dialog needs input" {
					wantSummary = mcpInputNote
				}
				assert.Equal(t, wantSummary, httpState["waiting_summary"], "summary must be the unresolved request, not later noise")
			} else {
				assert.Nil(t, httpState["waiting_reason"])
				assert.Nil(t, httpState["waiting_summary"])
			}
			// The single-session endpoints must agree with the list row on the
			// same database: the popout merges /resolve over the live row.
			for _, path := range []string{"/resolve", "/status"} {
				resp, err := http.Get(server.URL + "/api/sessions/" + sid + path)
				require.NoError(t, err)
				var single map[string]any
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&single))
				resp.Body.Close()
				require.Equal(t, http.StatusOK, resp.StatusCode, path)
				for _, k := range []string{"waiting_for_input", "awaiting_user", "waiting_reason", "waiting_summary"} {
					require.Containsf(t, single, k, "%s must emit %q explicitly", path, k)
					assert.Equalf(t, httpState[k], single[k], "%s %s must equal the list row", path, k)
				}
				if path == "/status" && !tc.want.needs {
					assert.NotEqual(t, "waiting_for_input", single["state"], "/status must not report waiting for an agent that is not blocked")
				}
			}
			// Needs input and Your turn are mutually exclusive by contract.
			assert.False(t, httpState["waiting_for_input"] == true && httpState["awaiting_user"] == true)
		})
	}
}

// Events of other sessions in the same folder (same agent_name) must never
// leak into a session's state.
func TestSessionStateSequences_IsolatedPerSession(t *testing.T) {
	server, handler, terminal, _ := setupSessionsTestServer(t)
	blocked := "00000000-0000-0000-0000-0000000005b1"
	busy := "00000000-0000-0000-0000-0000000005b2"
	terminal.addSession("claude-"+blocked, t.TempDir())
	terminal.addSession("claude-"+busy, t.TempDir())
	ctx := context.Background()
	ins := func(sid, typ, summary string) {
		id := sid
		_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "shared-folder", SessionID: &id, EventType: typ, Summary: summary})
		require.NoError(t, err)
	}
	ins(blocked, "notification", permissionNote)
	ins(busy, "prompt_submit", "")
	ins(busy, "tool_use", "Ran: ls")

	b, _ := httpAndWSState(t, server, handler, blocked)
	w, _ := httpAndWSState(t, server, handler, busy)
	assert.Equal(t, true, b["waiting_for_input"])
	assert.Equal(t, false, w["waiting_for_input"])
	assert.Equal(t, false, w["awaiting_user"])
}

// InsertAgentEvent prunes to 500 rows per agent_name, and agent_name is the
// folder shared by every agent in a worktree. A blocked agent's unresolved
// request must survive its teammates' activity.
func TestSessionStateSequences_NeedsInputSurvivesTeammatePruning(t *testing.T) {
	server, handler, terminal, _ := setupSessionsTestServer(t)
	blocked := "00000000-0000-0000-0000-0000000005c1"
	busy := "00000000-0000-0000-0000-0000000005c2"
	terminal.addSession("claude-"+blocked, t.TempDir())
	terminal.addSession("claude-"+busy, t.TempDir())
	ctx := context.Background()
	id := blocked
	_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "shared-folder", SessionID: &id, EventType: "notification", Summary: permissionNote})
	require.NoError(t, err)
	for i := 0; i < 520; i++ {
		other := busy
		_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "shared-folder", SessionID: &other, EventType: "tool_use", Summary: "Ran: ls"})
		require.NoError(t, err)
	}
	b, _ := httpAndWSState(t, server, handler, blocked)
	assert.Equal(t, true, b["waiting_for_input"], "unresolved Needs input was pruned away by another session's events")
}
