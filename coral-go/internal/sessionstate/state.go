// Package sessionstate derives the authoritative live state of an agent
// session from its event log. It has no dependencies so that the HTTP and
// WebSocket payload builders, the single-session endpoints and the background
// idle detector all apply exactly the same rules.
package sessionstate

import "strings"

// Event is the event subset used to derive a live session state.
// Events must be ordered oldest first.
type Event struct {
	Type    string
	Summary string
}

// Input contains the latest event context needed to derive a state.
type Input struct {
	Events           []Event
	StalenessSeconds float64
	NotStarted       bool
	Sleeping         bool
}

// State is the authoritative live-state contract. Done is retained as a
// deprecated compatibility field and is always false: ended/history rows are
// represented by the client killed-session state, not a live stop event.
type State struct {
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

// Derive applies one restart-safe state machine.
//
// NeedsInput means the agent asked the user something directly (a permission
// or approval prompt, or an input dialog) and is blocked on the answer. It is
// raised only by such a notification and is cleared by anything that proves
// the request is no longer pending: a prompt, a tool event, a session reset,
// or a stop. A prompt blocks the turn, so a stop can only be recorded after
// the request was answered. That matters when the answer was a denial or the
// approved tool failed, because neither produces a tool event, and the agent
// would otherwise keep showing NeedsInput while sitting idle.
//
// AwaitingUser is the calm "turn ended, your move" state. The idle reminder
// Claude Code sends about a minute after a turn ends maps here, and it never
// downgrades a request that is still pending.
func Derive(in Input) State {
	state := State{NotStarted: in.NotStarted, Sleeping: in.Sleeping}
	var latestType, latestSummary string
	clearRequest := func() {
		state.NeedsInput = false
		state.WaitingReason = ""
		state.WaitingSummary = ""
	}
	for _, event := range in.Events {
		latestType, latestSummary = event.Type, event.Summary
		switch event.Type {
		case "session_reset", "prompt_submit", "tool_use":
			clearRequest()
			state.AwaitingUser = false
		case "stop":
			clearRequest()
			state.AwaitingUser = true
		case "notification":
			switch Classify(event.Summary) {
			case NotificationNeedsInput:
				state.NeedsInput = true
				state.AwaitingUser = false
				state.WaitingReason = "notification"
				state.WaitingSummary = event.Summary
			case NotificationAwaitingUser:
				if !state.NeedsInput {
					state.AwaitingUser = true
				}
			}
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

// NotificationClass is what a notification means for the session state.
type NotificationClass uint8

const (
	// NotificationInformational changes nothing (login success, updates).
	NotificationInformational NotificationClass = iota
	// NotificationNeedsInput is a request made directly to the user.
	NotificationNeedsInput
	// NotificationAwaitingUser is the idle reminder after a finished turn.
	NotificationAwaitingUser
)

// needsInputPhrases are the requests an agent makes directly to the user.
// Only Claude Code registers a Notification hook today. Its messages are
// "Claude needs your permission to use <tool>", "... needs your approval ..."
// and, for MCP input dialogs, "... needs your input". Matching whole phrases
// rather than a bare "permission" or "question" keeps an informational
// message that merely mentions those words from raising NeedsInput.
// Everything that matches neither list is informational.
var needsInputPhrases = []string{
	"needs your permission",
	"needs your approval",
	"needs your input",
}

var awaitingUserPhrases = []string{
	"waiting for your input",
	"waiting for input",
}

// Classify maps a notification summary to its effect on the session state.
func Classify(summary string) NotificationClass {
	text := strings.ToLower(summary)
	for _, phrase := range needsInputPhrases {
		if strings.Contains(text, phrase) {
			return NotificationNeedsInput
		}
	}
	for _, phrase := range awaitingUserPhrases {
		if strings.Contains(text, phrase) {
			return NotificationAwaitingUser
		}
	}
	return NotificationInformational
}
