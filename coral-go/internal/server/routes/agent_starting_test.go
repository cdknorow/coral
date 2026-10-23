package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #6381: the live list says an agent is "starting" while its CLI has not emitted its first event: a signal (the absence of the first event), bounded by the not-started window, never
// a timer of the page's own, and never true for a terminal, a sleeping or an archived session.
func TestAgentStartingIsTheAbsenceOfTheFirstEvent(t *testing.T) {
	for name, c := range map[string]struct {
		agentType, latest string
		age               float64
		sleeping, arch    bool
		want              bool
	}{
		"a new agent, no event yet":    {"claude", "", 2, false, false, true},
		"a Codex agent, no event yet":  {"codex", "", 40, false, false, true},
		"its first event arrived":      {"claude", "SessionStart", 2, false, false, false},
		"any event":                    {"claude", "Stop", 90, false, false, false},
		"a terminal is a shell":        {"terminal", "", 2, false, false, false},
		"asleep":                       {"claude", "", 2, true, false, false},
		"archived":                     {"claude", "", 2, false, true, false},
		"no creation time":             {"claude", "", 0, false, false, false},
		"past the window: not forever": {"claude", "", notStartedWindowSeconds + 1, false, false, false},
		"at the window's end":          {"claude", "", notStartedWindowSeconds, false, false, true},
	} {
		assert.Equal(t, c.want, agentStarting(c.agentType, c.latest, c.age, c.sleeping, c.arch), name)
	}
}

func TestTheLiveListCarriesStartingForANewAgent(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	now := time.Now().UTC().Format(time.RFC3339)
	for id, mod := range map[string]func(*store.LiveSession){
		"00000000-0000-0000-0000-0000000000a1": func(l *store.LiveSession) {},
		"00000000-0000-0000-0000-0000000000a2": func(l *store.LiveSession) { l.CreatedAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) },
		"00000000-0000-0000-0000-0000000000a3": func(l *store.LiveSession) { l.IsSleeping = 1 },
	} {
		terminal.addSession("claude-"+id, "/tmp/test")
		l := &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "agent", WorkingDir: "/tmp/test", CreatedAt: now}
		mod(l)
		require.NoError(t, ss.RegisterLiveSession(context.Background(), l))
	}
	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	var rows []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
	got := map[string]any{}
	for _, r := range rows {
		got[r["session_id"].(string)] = r["starting"]
	}
	assert.Equal(t, true, got["00000000-0000-0000-0000-0000000000a1"], "a new agent with no event is starting")
	assert.Equal(t, false, got["00000000-0000-0000-0000-0000000000a2"], "one launched an hour ago is not")
	assert.Equal(t, false, got["00000000-0000-0000-0000-0000000000a3"], "a sleeping one is not")
}
