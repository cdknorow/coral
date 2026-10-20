package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
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
