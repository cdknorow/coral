package routes

import (
	"context"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/store"

	"github.com/cdknorow/coral/internal/sessionstate"
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
			in.Events = append(in.Events, StateEvent{Type: ev.EventType, Summary: ev.Summary})
		}
		if session, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && session != nil {
			h.applyTranscriptState(&in, events[sessionID], session.AgentType, sessionID, session.WorkingDir)
		}
	}
	return DeriveSessionState(in)
}

// All session payloads use the same transcript fallback when hooks are missing.
func (h *SessionsHandler) applyTranscriptState(input *SessionStateInput, events []store.AgentEvent, agentType, sessionID, workingDir string) {
	switch agentType {
	case at.Codex:
		kind, atTime := h.jsonl.ReadCodexTurnEvent(sessionID, workingDir)
		mergeTranscriptTurnEvent(input, events, kind, atTime)
	case at.Agy, at.Antigravity, at.Gemini:
		kind, atTime, summary := h.jsonl.ReadAgyTurnEvent(sessionID, workingDir)
		mergeTranscriptTurnEvent(input, events, kind, atTime, summary)
	}
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
	input.Events = append(input.Events, StateEvent{Type: kind, Summary: summ})
	// Explicit turn state is stronger evidence than terminal log age; a long
	// model/tool operation can be silent while the turn remains in progress.
	input.StalenessSeconds = 0
	input.NotStarted = false
}
