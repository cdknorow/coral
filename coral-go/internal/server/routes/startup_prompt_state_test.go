package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// paneScreenTerminal is the mock with tmux's ScreenReader: its outputs are the pane's text.
type paneScreenTerminal struct{ *mockSessionTerminal }

func (s paneScreenTerminal) AgentScreen(ctx context.Context, name, agentType, sessionID string) (string, error) {
	return s.CaptureOutput(ctx, name, 40, agentType, sessionID)
}

// A just-launched agent held by a prompt its CLI draws before any hook runs is waiting for the user (#4543):
// waiting_for_input with the reason and the prompt's question, in the session state every payload uses and
// in the list Coral Hub's "Needs you" page and alerts read; cleared once the prompt goes.
func TestStartupPromptsHoldTheSession(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	cases := []struct {
		name, agent, backend, screen, cleared, reason, question string
	}{
		{"codex hook review", "codex", "tmux", codexHookTrust(t, "tmux_open.txt"), codexHookTrust(t, "tmux_trusted.txt"), "startup_trust",
			"Hooks need review 2 hooks are new or changed. Hooks can run outside the sandbox after you trust them."},
		{"codex hook review (pty)", "codex", "pty", codexHookTrust(t, "pty_open.raw"), codexHookTrust(t, "pty_trusted.raw"), "startup_trust",
			"Hooks need review 2 hooks are new or changed. Hooks can run outside the sandbox after you trust them."},
		{"codex update offer", "codex", "tmux", screenCodexUpdate, codexHookTrust(t, "tmux_trusted.txt"), "cli_update", "✨ Update available! 0.155.1 -> 0.156.1"},
		{"claude folder trust", "claude", "tmux", claudeUnnumbered(t, "trust.txt"), claudeUnnumbered(t, "ready.txt"), "startup_trust",
			"Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review what's in this folder first."},
		{"claude folder trust (pty)", "claude", "pty", claudeUnnumbered(t, "pty_trust.raw"), claudeUnnumbered(t, "pty_ready.raw"), "startup_trust",
			"Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review what's in this folder first."},
		{"claude API key", "claude", "tmux", claudeUnnumbered(t, "apikey.txt"), claudeUnnumbered(t, "ready.txt"), "startup_prompt", "Do you want to use this API key?"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server, h, mock, ss := setupSessionsTestServer(t)
			if c.backend == "pty" {
				h.terminal = ptyTerminal{mock}
			} else {
				h.terminal = paneScreenTerminal{mock}
			}
			ctx := context.Background()
			sid := fmt.Sprintf("eeeeeeee-4543-4000-8000-%012d", i)
			name := naming.SessionName(c.agent, sid)
			mock.addSession(name, "/tmp/project")
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: c.agent, AgentName: "starting", WorkingDir: "/tmp/project"}))
			set := func(s string) {
				mock.mu.Lock()
				mock.outputs[name] = s
				mock.mu.Unlock()
				h.startupScreens = loginScreens{stampDecides: true} // a fresh screen read
			}

			set(c.screen)
			st := h.deriveSingleSessionState(ctx, sid, 0)
			assert.True(t, st.NeedsInput, "waiting for the user")
			assert.False(t, st.AwaitingUser)
			assert.False(t, st.NotStarted)
			assert.Equal(t, c.reason, st.WaitingReason)
			assert.Equal(t, "startup", st.WaitingKind)
			assert.Equal(t, c.question, st.WaitingSummary)

			// The list, as Coral Hub reads it
			h.startupScreens = loginScreens{stampDecides: true}
			resp, err := http.Get(server.URL + "/api/sessions/live")
			require.NoError(t, err)
			defer resp.Body.Close()
			var rows []map[string]any
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
			var row map[string]any
			for _, r := range rows {
				if r["session_id"] == sid {
					row = r
				}
			}
			require.NotNil(t, row, "the session is listed")
			assert.Equal(t, true, row["waiting_for_input"])
			assert.Equal(t, c.reason, row["waiting_reason"])
			assert.Equal(t, "startup", row["waiting_kind"])

			set(c.cleared)
			st = h.deriveSingleSessionState(ctx, sid, 0)
			assert.False(t, st.NeedsInput, "the prompt went: cleared")
			assert.Empty(t, st.WaitingReason)
		})
	}

	t.Run("an agent that has sent events is not read", func(t *testing.T) {
		_, h, mock, ss := setupSessionsTestServer(t)
		h.terminal = paneScreenTerminal{mock}
		ctx := context.Background()
		sid := "eeeeeeee-4543-4000-8000-000000000099"
		name := naming.SessionName("codex", sid)
		mock.addSession(name, "/tmp/project")
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "codex", AgentName: "running", WorkingDir: "/tmp/project"}))
		_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: name, SessionID: &sid, EventType: "stop", Summary: "stop"})
		require.NoError(t, err)
		mock.outputs[name] = codexHookTrust(t, "tmux_open.txt")
		before := len(mock.captures)
		st := h.deriveSingleSessionState(ctx, sid, 0)
		assert.NotEqual(t, "startup", st.WaitingKind)
		assert.Equal(t, before, len(mock.captures), "no screen read once the agent's events say what it does")
	})
}

// A startup reading follows the screen: one taken with another output stamp is never reused, however
// young (the chat poll's first read, before Codex drew its menu, hid the menu for loginScreenTTL); the
// login cache keeps its TTL. An answer drops the reading.
func TestStartupScreensFollowTheOutputStamp(t *testing.T) {
	now := time.Now()
	reads := 0
	read := func(on bool) func() (bool, string) {
		return func() (bool, string) { reads++; return on, "" }
	}
	startup := loginScreens{stampDecides: true}
	on, _ := startup.read("k", "s1", now, read(false))
	assert.False(t, on)
	on, _ = startup.read("k", "s2", now.Add(time.Second), read(true))
	assert.True(t, on, "the terminal wrote since: read again")
	on, _ = startup.read("k", "s2", now.Add(2*time.Second), read(false))
	assert.True(t, on, "nothing written since: the reading stands")
	assert.Equal(t, 2, reads)
	startup.forget("k")
	startup.read("k", "s2", now.Add(2*time.Second), read(false))
	assert.Equal(t, 3, reads, "forgotten: read again")

	login := loginScreens{}
	login.read("k", "s1", now, read(false))
	on, _ = login.read("k", "s2", now.Add(time.Second), read(true))
	assert.False(t, on, "the login cache keeps a young reading")
}

// An answer drops the session's startup reading, so the list stops saying it waits at once, not a TTL later.
func TestAnswerForgetsTheStartupReading(t *testing.T) {
	server, h, mock, ss := setupSessionsTestServer(t)
	h.terminal = paneScreenTerminal{mock}
	fastPromptChecks(t)
	ctx := context.Background()
	sid := "eeeeeeee-4543-4000-8000-000000000098"
	name := naming.SessionName("codex", sid)
	mock.addSession(name, "/tmp/project")
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "codex", AgentName: "starting", WorkingDir: "/tmp/project"}))
	mock.outputs[name] = codexHookTrust(t, "tmux_open.txt")
	require.Equal(t, "startup", h.deriveSingleSessionState(ctx, sid, 0).WaitingKind)
	mock.onRaw = func(string) {
		mock.mu.Lock()
		mock.outputs[name] = codexHookTrust(t, "tmux_selected_2.txt")
		mock.mu.Unlock()
	}
	mock.onEnter = func(string) {
		mock.mu.Lock()
		mock.outputs[name] = codexHookTrust(t, "tmux_trusted.txt")
		mock.mu.Unlock()
	}
	b, _ := json.Marshal(map[string]any{"n": 2, "label": "Trust all and continue", "session_id": sid, "agent_type": "codex"})
	resp, err := http.Post(server.URL+"/api/sessions/live/"+name+"/answer-prompt", "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.False(t, h.deriveSingleSessionState(ctx, sid, 0).NeedsInput, "no longer waiting, within the cache's TTL")
}

// The CLI's own signal comes first (#4543, the operator's rule): an agent whose events say it waits on a
// plan is waiting on that, from the event, even with a startup menu's text on its screen, and the screen is
// not read for a startup prompt.
func TestStartupPromptSignalsComeFirst(t *testing.T) {
	_, h, mock, ss := setupSessionsTestServer(t)
	h.terminal = paneScreenTerminal{mock}
	ctx := context.Background()
	sid := "eeeeeeee-4543-4000-8000-000000000097"
	name := naming.SessionName("claude", sid)
	mock.addSession(name, "/tmp/project")
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "planning", WorkingDir: "/tmp/project"}))
	detail, tool := `{"kind":"plan","tool_name":"ExitPlanMode"}`, "ExitPlanMode"
	_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: name, SessionID: &sid, EventType: "permission_request",
		ToolName: &tool, Summary: "Waiting for the user: plan (ExitPlanMode)", DetailJSON: &detail})
	require.NoError(t, err)
	mock.outputs[name] = claudeUnnumbered(t, "trust.txt")
	st := h.deriveSingleSessionState(ctx, sid, 0)
	assert.True(t, st.NeedsInput)
	assert.Equal(t, "plan", st.WaitingKind, "the event says what the agent waits on")
	h.startupScreens.mu.Lock()
	_, read := h.startupScreens.m[promptReadKey(sid, name, "claude")]
	h.startupScreens.mu.Unlock()
	assert.False(t, read, "no startup screen read while the events tell (Claude's login check, #3830, is its own)")
}
