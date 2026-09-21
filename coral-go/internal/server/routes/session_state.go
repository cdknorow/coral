package routes

import "strings"

// StateEvent is the event subset used to derive a live session state.
// Events must be ordered oldest first.
type StateEvent struct {
	Type    string
	Summary string
}

// SessionStateInput contains the latest event context needed by the shared
// HTTP and WebSocket session builders.
type SessionStateInput struct {
	Events           []StateEvent
	StalenessSeconds float64
	NotStarted       bool
	Sleeping         bool
}

// SessionState is the authoritative live-state contract. Done is retained as
// a deprecated compatibility field and is always false: ended/history rows
// are represented by the client killed-session state, not a live stop event.
type SessionState struct {
	NeedsInput     bool
	AwaitingUser   bool
	WaitingReason  string
	WaitingSummary string
	Working        bool
	NotStarted     bool
	Sleeping       bool
	Stuck          bool
	Done           bool
}

// DeriveSessionState applies the same restart-safe state machine to HTTP and
// WebSocket payloads. A waiting notification remains actionable through stop
// and unrelated events until a prompt or resumed tool event arrives.
func DeriveSessionState(in SessionStateInput) SessionState {
	state := SessionState{NotStarted: in.NotStarted, Sleeping: in.Sleeping}
	var latestType, latestSummary string
	for _, event := range in.Events {
		latestType, latestSummary = event.Type, event.Summary
		switch event.Type {
		case "session_reset":
			state.NeedsInput = false
			state.AwaitingUser = false
			state.WaitingReason = ""
			state.WaitingSummary = ""
		case "notification":
			switch classifyNotification(event.Summary) {
			case notificationNeedsInput:
				state.NeedsInput = true
				state.AwaitingUser = false
				state.WaitingReason = "notification"
				state.WaitingSummary = event.Summary
			case notificationAwaitingUser:
				if !state.NeedsInput {
					state.AwaitingUser = true
				}
			}
		case "stop":
			if !state.NeedsInput {
				state.AwaitingUser = true
			}
		case "prompt_submit", "tool_use":
			state.NeedsInput = false
			state.AwaitingUser = false
			state.WaitingReason = ""
			state.WaitingSummary = ""
		}
	}
	state.Working = (latestType == "tool_use" || latestType == "prompt_submit") &&
		in.StalenessSeconds < 120 && !strings.HasPrefix(latestSummary, "Ran: sleep")
	// No authoritative stuck signal exists in the event schema yet. Keep the
	// field explicit and false rather than inventing a timeout heuristic.
	state.Stuck = false
	state.Done = false
	return state
}

type notificationClass uint8

const (
	notificationInformational notificationClass = iota
	notificationNeedsInput
	notificationAwaitingUser
)

func classifyNotification(summary string) notificationClass {
	text := strings.ToLower(summary)
	if strings.Contains(text, "permission") || strings.Contains(text, "needs your approval") || strings.Contains(text, "needs your input") || strings.Contains(text, "question") {
		return notificationNeedsInput
	}
	if strings.Contains(text, "waiting for your input") || strings.Contains(text, "waiting for input") {
		return notificationAwaitingUser
	}
	return notificationInformational
}
