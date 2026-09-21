package routes

import (
	"context"

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
	}
	return DeriveSessionState(in)
}
