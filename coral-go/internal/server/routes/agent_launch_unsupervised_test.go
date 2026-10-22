package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/store"
)

// #5901, Security's ruling on #4032: an agent never starts an agent that asks for less than itself. A type that never asks (agent.SkipsPermissionPrompts with no flags is true: Pi today)
// is refused when an agent starts it, alone or as a team member, with 403 agent_launch_unsupervised and nothing started; the Operator and a saved team template's member are unchanged.
func TestAnAgentCannotStartATypeThatNeverAsks(t *testing.T) {
	_, h, term, ss := setupSessionsTestServer(t)
	stubCLIPaths(t, ss)
	ctx := context.Background()
	const parent = "11111111-2222-4333-8444-555555555555"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: parent, AgentType: "claude", AgentName: "parent", WorkingDir: t.TempDir()}))
	asAgent := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: parent, SessionName: "claude-" + parent}))
	}
	same := func(r *http.Request) *http.Request { return r }
	launch := func(agentType string, req func(*http.Request) *http.Request) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"agent_type": agentType, "working_dir": t.TempDir()})
		rec := httptest.NewRecorder()
		h.Launch(rec, req(httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(b))))
		return rec
	}
	n := 0
	team := func(req func(*http.Request) *http.Request, members ...map[string]any) (code int, agents []map[string]any) {
		t.Helper()
		n++
		b, _ := json.Marshal(map[string]any{"board_name": "unsup" + string(rune('a'+n)), "new_board": true, "working_dir": t.TempDir(), "agent_type": "claude", "agents": members})
		rec := httptest.NewRecorder()
		h.LaunchTeam(rec, req(httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(b))))
		var out struct {
			Agents []map[string]any `json:"agents"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Agents
	}
	assert.True(t, agent.SkipsPermissionPrompts("pi", nil), "the rule derives from this, not from the name")

	// an agent's launch of Pi: refused, nothing started
	before := len(term.sentCommands())
	rec := launch("pi", asAgent)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var refusal map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refusal))
	assert.Equal(t, agentLaunchUnsupervisedCode, refusal["code"])
	assert.Contains(t, refusal["error"], "ask the Operator")
	assert.Equal(t, before, len(term.sentCommands()), "nothing started")

	// the types that ask are unchanged for an agent
	for _, typ := range []string{"claude", "codex", "agy"} {
		assert.Equal(t, http.StatusOK, launch(typ, asAgent).Code, typ)
	}
	// the Operator can launch Pi
	assert.Equal(t, http.StatusOK, launch("pi", same).Code, "the dashboard, command line or API key")

	// a team an agent launches: the Pi member is not started (a setup problem), the Claude member is
	code, agents := team(asAgent, map[string]any{"name": "A", "agent_type": "claude"}, map[string]any{"name": "B", "agent_type": "pi"})
	require.Equal(t, http.StatusOK, code)
	require.Len(t, agents, 2)
	byName := map[string]map[string]any{}
	for _, a := range agents {
		byName[a["name"].(string)] = a
	}
	assert.NotContains(t, byName["A"], "error", "the Claude member starts")
	assert.Equal(t, agentLaunchUnsupervisedCode, byName["B"]["code"])
	assert.Contains(t, byName["B"]["error"], "ask the Operator")
	// a team whose members take the team's type: Pi
	b, _ := json.Marshal(map[string]any{"board_name": "unsupteam", "new_board": true, "working_dir": t.TempDir(), "agent_type": "pi", "agents": []map[string]any{{"name": "P"}}})
	trec := httptest.NewRecorder()
	h.LaunchTeam(trec, asAgent(httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(b))))
	assert.NotEqual(t, http.StatusOK, trec.Code, "every member refused: the team is a failure, not a success")
	assert.Contains(t, trec.Body.String(), agentLaunchUnsupervisedCode)

	// the same team from the Operator starts both
	code, agents = team(same, map[string]any{"name": "A", "agent_type": "claude"}, map[string]any{"name": "B", "agent_type": "pi"})
	require.Equal(t, http.StatusOK, code)
	for _, a := range agents {
		assert.NotContains(t, a, "error", a["name"])
	}

	// a member that is exactly one of the owner's saved entries keeps the exception (the owner chose that team)
	require.NoError(t, ss.SetSetting(ctx, "saved_team_templates", `[{"name":"t","agents":[{"name":"S","agent_type":"pi"}]}]`))
	code, agents = team(asAgent, map[string]any{"name": "S", "agent_type": "pi"})
	require.Equal(t, http.StatusOK, code)
	require.Len(t, agents, 1)
	assert.NotContains(t, agents[0], "error", "a saved template's Pi member")
	// but not one that differs (here its prompt)
	code, agents = team(asAgent, map[string]any{"name": "S", "agent_type": "pi", "prompt": "something else"})
	require.Equal(t, http.StatusUnprocessableEntity, code, "the only member is refused: a setup problem")
	require.Len(t, agents, 1)
	assert.Equal(t, agentLaunchUnsupervisedCode, agents[0]["code"], "a member that is not exactly the saved entry")
}
