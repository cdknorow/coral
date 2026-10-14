package routes

import (
	"context"
	"log/slog"

	"github.com/cdknorow/coral/internal/logparse"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

// heldInputSummary is the notification recorded when automated input has been
// held a while because the agent waits on a prompt: it shows the agent as
// needing the user on the dashboard (sessionstate classifies "needs your
// input"), instead of anything being typed into that prompt (#3431).
const heldInputSummary = "Coral is holding automated messages: this agent needs your input in its terminal (a prompt is open)"

// InstallAutomatedInputHooks gives the automated-input gate the agents' state
// and a way to surface a send held too long. The server calls it once.
func (h *SessionsHandler) InstallAutomatedInputHooks() {
	ptymanager.SetNeedsInputFunc(func(ctx context.Context, name, agentType, sessionID string) bool {
		if sessionID == "" {
			_, sessionID = logparse.ParseSessionName(name)
		}
		if sessionID == "" || h.ts == nil {
			return false
		}
		return h.deriveSingleSessionState(ctx, sessionID, 0).NeedsInput
	})
	ptymanager.SetHeldTooLongFunc(func(name, agentType, sessionID, what string) {
		if sessionID == "" {
			_, sessionID = logparse.ParseSessionName(name)
		}
		if h.ts == nil || sessionID == "" {
			return
		}
		agentName := name
		if ls, err := h.ss.GetLiveSession(context.Background(), sessionID); err == nil && ls != nil {
			agentName = ls.AgentName
		}
		sid := sessionID
		if _, err := h.ts.InsertAgentEvent(context.Background(), &store.AgentEvent{
			AgentName: agentName, SessionID: &sid, EventType: "notification", Summary: heldInputSummary,
		}); err != nil {
			slog.Warn("could not record held automated input", "session", name, "error", err)
		}
	})
}
