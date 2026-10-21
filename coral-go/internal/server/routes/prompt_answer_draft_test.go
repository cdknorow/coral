package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #5254: a person's draft in the agent's input box that is itself a numbered list ("❯ 1. buy milk") is not a menu. Answering it typed a digit into
// the draft (Claude Code, Codex), or an Enter that sent it. No key is sent, and no card is offered for it.
const draftRule = "────────────────────────────────────────"

var draftScreens = map[string]string{
	"claude": "● Done.\n\n" + draftRule + "\n❯ 1. buy milk\n  2. buy eggs\n" + draftRule + "\n  ? for shortcuts",
	"codex":  "• Done.\n\n› 1. buy milk\n  2. buy eggs\n\n  gpt-5-codex medium · 100% context left · ~/x",
}

func TestParsePromptScreenIgnoresADraftThatLooksLikeAMenu(t *testing.T) {
	for agent, screen := range draftScreens {
		_, ok := parsePromptScreen(screen)
		assert.False(t, ok, agent)
	}
	// The same lines with a question above and no input box under them are a menu.
	_, ok := parsePromptScreen("Do you want to proceed?\n❯ 1. buy milk\n  2. buy eggs\n\n Press enter to confirm or esc to go back")
	assert.True(t, ok)
}

func TestAnswerPromptSendsNoKeyOverADraft(t *testing.T) {
	const sid = "00000000-0000-0000-0000-000000005254"
	for agentType, screen := range draftScreens {
		t.Run(agentType, func(t *testing.T) {
			server, _, term, ss := setupSessionsTestServer(t)
			require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: agentType, AgentName: "a-draft", WorkingDir: "/tmp/test"}))
			term.addSession("a-draft", "/tmp/test")
			fastPromptChecks(t)
			term.mu.Lock()
			term.outputs["a-draft"] = screen
			term.mu.Unlock()
			b, _ := json.Marshal(map[string]any{"n": 1, "label": "buy milk", "session_id": sid, "agent_type": agentType})
			resp, err := http.Post(server.URL+"/api/sessions/live/a-draft/answer-prompt", "application/json", bytes.NewReader(b))
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusConflict, resp.StatusCode)
			term.mu.Lock()
			defer term.mu.Unlock()
			assert.Empty(t, term.raw["a-draft"])
			assert.Empty(t, term.arrows["a-draft"])
			assert.Zero(t, term.enters["a-draft"])
		})
	}
}

// A Claude prompt is answered only while its hooks say a request may be open (or a tool is pending): after the agent has stopped, a screen that looks like a menu is not one.
func TestAnswerPromptClaudeNeedsAnOpenRequest(t *testing.T) {
	const sid = "00000000-0000-0000-0000-000000005255"
	menu := "Do you want to proceed?\n❯ 1. Yes\n  2. No, and tell Claude what to do differently (esc)\n"
	answer := func(t *testing.T, event string) (int, int) {
		server, handler, term, ss := setupSessionsTestServer(t)
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "c-hooks", WorkingDir: "/tmp/test"}))
		term.addSession("c-hooks", "/tmp/test")
		fastPromptChecks(t)
		term.mu.Lock()
		term.outputs["c-hooks"] = menu
		term.mu.Unlock()
		if event != "" {
			handler.hints.note(sid, event)
		}
		b, _ := json.Marshal(map[string]any{"n": 1, "label": "Yes", "session_id": sid, "agent_type": "claude"})
		resp, err := http.Post(server.URL+"/api/sessions/live/c-hooks/answer-prompt", "application/json", bytes.NewReader(b))
		require.NoError(t, err)
		defer resp.Body.Close()
		term.mu.Lock()
		defer term.mu.Unlock()
		return resp.StatusCode, len(term.raw["c-hooks"])
	}
	status, keys := answer(t, "stop")
	assert.Equal(t, http.StatusConflict, status)
	assert.Zero(t, keys, "the agent stopped: nothing is waiting, no digit")
	_, keys = answer(t, "permission_request")
	assert.Equal(t, 1, keys, "a request is open: answered")
	_, keys = answer(t, "")
	assert.Equal(t, 1, keys, "no event seen yet (a restarted server): answered as before")
}

// Auto-accept on the agents that report no hook for their approvals reads the screen only: a stale approval header above a person's menu-shaped draft is not an approval.
func TestClassifyApprovalIgnoresADraftUnderAStaleHeader(t *testing.T) {
	codex := "Would you like to run the following command?\n  $ touch x\n\n• Ran touch x\n\n› 1. Yes, proceed (y)\n  2. No, and tell Codex what to do differently (esc)\n\n  gpt-5-codex medium · 100% context left · ~/x"
	assert.False(t, classifyApproval("codex", codex).Prompt)
	agy := "Requesting permission for: run_command\n\nDone.\n\n" + draftRule + "\n❯ 1. Yes, run command\n  2. No\n" + draftRule + "\n  ? for shortcuts"
	assert.False(t, classifyApproval("agy", agy).Prompt)
}
