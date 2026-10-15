package routes

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/sessionstate"
	"github.com/cdknorow/coral/internal/store"
)

// #3471: an agent waiting on its teammates (coral-board wait) whose nudge is held
// behind its input box must not show as needing the operator. Only a hold for an
// open prompt does, and Coral's own notice never makes the gate treat a later hold
// as a prompt hold.
func TestHeldTooLongShowsNeedsInputOnlyForAPrompt(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	defer server.Close()
	ctx := context.Background()
	sid := "dddddddd-3471-0000-0000-000000000001"
	name := "claude-" + sid
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: name, WorkingDir: "/tmp/p"}))
	require.Equal(t, sessionstate.NotificationInformational, sessionstate.Classify(heldForInputBoxSummary), "the input-box note raises nothing")

	// A send held behind the input box (a draft, or a suggestion): a calm note only.
	h.recordHeldTooLong(name, "claude", sid, "board nudge", "")
	st := h.deriveSingleSessionState(ctx, sid, 0)
	assert.False(t, st.NeedsInput, "an input-box hold is not Needs input")
	assert.False(t, h.gateNeedsInput(ctx, name, "claude", sid))

	// A send held for an open prompt: Needs input, as before (#3431).
	h.recordHeldTooLong(name, "claude", sid, "board nudge", "prompt")
	st = h.deriveSingleSessionState(ctx, sid, 0)
	assert.True(t, st.NeedsInput, "a prompt hold shows Needs input")
	assert.Equal(t, heldInputSummary, st.WaitingSummary)
	// The loop guard: Coral's own notice doesn't tell the gate the agent waits on a
	// prompt, so a later draft hold is still reported as a draft hold.
	assert.False(t, h.gateNeedsInput(ctx, name, "claude", sid), "Coral's own notice never feeds the gate")
	h.recordHeldTooLong(name, "claude", sid, "board nudge", "")
	st = h.deriveSingleSessionState(ctx, sid, 0)
	assert.Equal(t, heldInputSummary, st.WaitingSummary, "the informational note neither raises nor rewrites Needs input")

	// The held sends were delivered or dropped: Coral's notice is cleared, so a
	// hookless agent (agy, Codex) doesn't stay at Needs input (#3471).
	h.recordHeldEnded(name, "claude", sid)
	st = h.deriveSingleSessionState(ctx, sid, 0)
	assert.False(t, st.NeedsInput, "cleared when the hold ended")

	// The agent's own permission request does feed the gate, and the end of a
	// hold never clears it.
	_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: name, SessionID: &sid, EventType: "notification", Summary: "Claude needs your permission to use Bash"})
	require.NoError(t, err)
	assert.True(t, h.gateNeedsInput(ctx, name, "claude", sid))
	h.recordHeldEnded(name, "claude", sid)
	assert.True(t, h.deriveSingleSessionState(ctx, sid, 0).NeedsInput, "the agent's own request stays")
}
