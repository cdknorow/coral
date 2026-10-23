package routes

import (
	"context"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"

	"github.com/subgentic/coral/internal/sessionstate"
)

// The derivation lives in internal/sessionstate so the background idle
// detector can share it. These aliases keep the payload builders readable.
type (
	StateEvent        = sessionstate.Event
	SessionStateInput = sessionstate.Input
	SessionState      = sessionstate.State
)

// DeriveSessionState applies the shared state machine to a session's events.
func DeriveSessionState(in SessionStateInput) SessionState {
	return sessionstate.Derive(in)
}

// deriveSingleSessionState derives the state of one session from the store.
// The single-session endpoints must agree with the list and WebSocket
// payloads: the popout merges the resolver record over the live row, so a
// resolver that still treated any trailing notification as a request showed
// "Needs input" for an idle agent.
func (h *SessionsHandler) deriveSingleSessionState(ctx context.Context, sessionID string, stalenessSeconds float64) SessionState {
	in := SessionStateInput{StalenessSeconds: stalenessSeconds}
	if events, err := h.ts.GetSessionStateEvents(ctx, []string{sessionID}); err == nil {
		for _, ev := range events[sessionID] {
			in.Events = append(in.Events, ev.StateEvent())
		}
		if session, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && session != nil {
			in.Archived = session.IsArchived == 1
			h.applyTranscriptState(&in, events[sessionID], session.AgentType, naming.SessionName(session.AgentType, sessionID), sessionID, session.WorkingDir)
		}
	}
	return DeriveSessionState(in)
}

// All session payloads use the same transcript fallback when hooks are missing.
// name is the agent's terminal, read for a login screen while a Claude agent has no transcript.
func (h *SessionsHandler) applyTranscriptState(input *SessionStateInput, events []store.AgentEvent, agentType, name, sessionID, workingDir string) {
	switch agentType {
	case at.Codex:
		kind, atTime, activity := h.jsonl.ReadCodexTurn(sessionID, workingDir)
		if kind == "prompt_submit" && activity {
			kind = "turn_activity" // the turn going on, not a new turn: it never answers an open request (#6672)
		}
		mergeTranscriptTurnEvent(input, events, kind, atTime)
	case at.Agy, at.Antigravity, at.Gemini:
		kind, atTime, summary := h.jsonl.ReadAgyTurnEvent(sessionID, workingDir)
		mergeTranscriptTurnEvent(input, events, kind, atTime, summary)
	case at.Claude:
		// Claude fires no hook when the user denies a prompt or interrupts a turn; its
		// transcript records the turn's end (#3481). Read only while the hooks leave a
		// turn or request open, so an idle agent costs nothing.
		if claudeTurnOpen(events) {
			kind, atTime := h.jsonl.ReadClaudeTurnEvent(sessionID, workingDir)
			mergeTranscriptTurnEvent(input, events, kind, atTime)
		}
		// A failed sign-in ends a turn like any API error; the transcript says which (#3830).
		input.NeedsLogin, input.LoginReason = h.claudeLoginState(name, sessionID, workingDir, func() []store.AgentEvent { return events })
	}
	// No event at all, from hooks or the transcript: the CLI may still be on a prompt it draws at launch (#4543).
	if len(input.Events) == 0 && !input.NeedsLogin && !input.Archived && !input.Sleeping {
		input.StartupPrompt, input.StartupSummary = h.startupPromptState(name, agentType, sessionID)
	}
}

// claudeTurnOpen reports whether the latest of a session's state events leaves a turn
// or a request open (a prompt, a tool, a request to the user).
func claudeTurnOpen(events []store.AgentEvent) bool {
	if len(events) == 0 {
		return false
	}
	switch events[len(events)-1].EventType {
	case "prompt_submit", "tool_use", "permission_request", "notification":
		return true
	}
	return false
}

// Hook signals win ties (their timestamps can have second precision). A newer
// transcript turn boundary repairs missing hooks without masking newer prompts.
func mergeTranscriptTurnEvent(input *SessionStateInput, events []store.AgentEvent, kind string, at time.Time, summary ...string) {
	if kind == "" || at.IsZero() {
		return
	}
	for _, e := range events {
		timestamp, err := time.Parse(time.RFC3339Nano, e.CreatedAt)
		if err != nil || !at.Truncate(time.Second).After(timestamp.Truncate(time.Second)) {
			return
		}
	}
	var summ string
	if len(summary) > 0 {
		summ = summary[0]
	}
	input.Events = append(input.Events, StateEvent{Type: kind, Summary: summ, At: at})
	// Explicit turn state is stronger evidence than terminal log age; a long
	// model/tool operation can be silent while the turn remains in progress.
	input.StalenessSeconds = 0
	input.NotStarted = false
}
