// Package sessionstate derives the authoritative live state of an agent
// session from its event log. It has no dependencies so that the HTTP and
// WebSocket payload builders, the single-session endpoints and the background
// idle detector all apply exactly the same rules.
package sessionstate

import (
	"encoding/json"
	"strings"
	"time"
)

// Event is the event subset used to derive a live session state.
// Events must be ordered oldest first.
type Event struct {
	Type    string
	Summary string
	// Kind is what a permission_request asks (permission, question, plan,
	// elicitation), or a notification's notification_type (Claude Code), from the
	// event's detail (#3481). Empty for older events and other CLIs.
	Kind string
	// ToolName is the tool of a tool_use or permission_request, when known.
	ToolName string
	// At is when the event was recorded (zero when unknown).
	At time.Time
}

// NewEvent builds an Event from a stored event, reading its kind from detailJSON.
func NewEvent(eventType, summary string, detailJSON *string) Event {
	e := Event{Type: eventType, Summary: summary}
	if detailJSON == nil || *detailJSON == "" || (eventType != "permission_request" && eventType != "request_resolved" && eventType != "notification") {
		return e
	}
	var d struct {
		Kind             string `json:"kind"`
		NotificationType string `json:"notification_type"`
	}
	if json.Unmarshal([]byte(*detailJSON), &d) == nil {
		e.Kind = d.Kind
		if !RequestKinds[e.Kind] {
			e.Kind = "" // agent-controlled: only the known kinds
		}
		if eventType == "notification" {
			e.Kind = d.NotificationType
		}
	}
	return e
}

// RequestKinds are what an agent's request to the user can be (#3481). The kind comes
// from the agent's own event, so anything else is ignored.
var RequestKinds = map[string]bool{"permission": true, "question": true, "plan": true, "elicitation": true}

// Input contains the latest event context needed to derive a state.
type Input struct {
	Events           []Event
	StalenessSeconds float64
	NotStarted       bool
	Sleeping         bool
	// Archived agents never report a pending request: nobody is meant to answer
	// it until the agent is unarchived, even if its terminal outlived the archive.
	Archived bool
}

// State is the authoritative live-state contract. Done is retained as a
// deprecated compatibility field and is always false: ended/history rows are
// represented by the client killed-session state, not a live stop event.
type State struct {
	NeedsInput     bool
	AwaitingUser   bool
	WaitingReason  string
	WaitingSummary string
	// WaitingKind is what the agent waits on while NeedsInput: permission, question,
	// plan or elicitation, when the agent said so (#3481); empty otherwise.
	WaitingKind string
	Working     bool
	NotStarted  bool
	Sleeping    bool
	Stuck       bool
	Done        bool

	// For the automated-input gate (#3481, #3475 phase 1), from the agent's events:
	// InTurn: its turn is running (a prompt or a tool event is the latest), however
	// long ago (unlike Working). OwnRequest: NeedsInput comes from the agent's own
	// request, not Coral's held notice. Gone: its session ended. Since: when the
	// current state began; LastEvent and LastStop: the latest event, and end of a turn.
	InTurn     bool
	OwnRequest bool
	Gone       bool
	Since      time.Time
	LastEvent  time.Time
	LastStop   time.Time
}

// Derive applies one restart-safe state machine.
//
// NeedsInput means the agent asked the user something directly (a permission
// or approval prompt, or an input dialog) and is blocked on the answer. It is
// raised only by such a notification and is cleared by anything that proves
// the request is no longer pending: a prompt, a tool event, a session reset,
// or a stop, or Coral auto-accepting it. A prompt blocks the turn, so a stop can only be recorded after
// the request was answered. That matters when the answer was a denial or the
// approved tool failed, because neither produces a tool event, and the agent
// would otherwise keep showing NeedsInput while sitting idle.
//
// AwaitingUser is the calm "turn ended, your move" state. The idle reminder
// Claude Code sends about a minute after a turn ends maps here, and it never
// downgrades a request that is still pending.
func Derive(in Input) State {
	state := State{NotStarted: in.NotStarted, Sleeping: in.Sleeping}
	var latestType string
	// What a request replaced, restored when Coral's own held notice is cleared.
	var beforeType string
	var beforeAwaiting bool
	// pending counts the agent's open requests per tool (PermissionRequest). Claude
	// runs tool calls in parallel, so another tool's event must not clear a request
	// that is still open (#3481, Backend's MUST 3).
	pending := map[string]int{}
	open := 0
	clearRequest := func() {
		state.NeedsInput = false
		state.WaitingReason = ""
		state.WaitingSummary = ""
		state.WaitingKind = ""
		pending, open = map[string]int{}, 0
	}
	raise := func(event Event, kind string) {
		// A request already open keeps its kind: the agent's PermissionRequest says
		// "plan" or "question", the notification that follows only "permission".
		if !state.NeedsInput || state.WaitingKind == "" {
			state.WaitingKind = kind
		}
		if !state.NeedsInput {
			beforeType, beforeAwaiting = latestType, state.AwaitingUser
		}
		latestType = "notification"
		state.NeedsInput = true
		state.AwaitingUser = false
		state.WaitingReason = "notification"
		state.WaitingSummary = event.Summary
	}
	phase := func() string {
		switch {
		case state.NeedsInput:
			return "request"
		case latestType == "tool_use" || latestType == "prompt_submit":
			return "turn"
		case latestType == "":
			return "none"
		}
		return "idle"
	}
	for _, event := range in.Events {
		before := phase()
		if !event.At.IsZero() {
			state.LastEvent = event.At
		}
		switch event.Type {
		case "tool_use":
			// A tool finished. It answers its own tool's request (an approval); a
			// request for another tool stays open.
			if open > 0 {
				if pending[event.ToolName] > 0 {
					pending[event.ToolName]--
					open--
				}
				if open > 0 {
					break
				}
			}
			latestType = event.Type
			clearRequest()
			state.AwaitingUser = false
		case "session_reset", "prompt_submit":
			latestType = event.Type
			clearRequest()
			state.AwaitingUser = false
		case "auto_accept":
			// Coral answered the prompt (auto-accept): the agent is now
			// running the approved tool.
			latestType = "tool_use"
			clearRequest()
			state.AwaitingUser = false
		case "stop":
			latestType = event.Type
			clearRequest()
			state.AwaitingUser = true
			state.LastStop = event.At
		case "permission_request":
			// The agent's own hook (Claude's PermissionRequest or Elicitation) says a
			// request to the user is open, at once rather than ~6 s later (#3481).
			raise(event, event.Kind)
			if event.Kind != "elicitation" {
				pending[event.ToolName]++
				open++
			}
		case "request_resolved":
			// An elicitation was answered (accept, decline or cancel): the agent goes on.
			if state.NeedsInput && state.WaitingKind == "elicitation" {
				clearRequest()
				latestType = "tool_use"
				state.AwaitingUser = false
			}
		case "session_end":
			latestType = ""
			clearRequest()
			state.AwaitingUser = false
			state.Gone = true
		case "notification":
			if event.Summary == HeldInputEndedSummary {
				// Coral's held sends were delivered or dropped: its own notice no
				// longer stands. A request the agent made itself is kept (#3471).
				if state.NeedsInput && state.WaitingSummary == HeldInputSummary {
					clearRequest()
					latestType, state.AwaitingUser = beforeType, beforeAwaiting
				}
				continue
			}
			class := Classify(event.Summary)
			if event.Kind != "" {
				class = ClassifyType(event.Kind)
			}
			switch class {
			case NotificationNeedsInput:
				raise(event, notificationKinds[event.Kind])
			case NotificationAwaitingUser:
				if !state.NeedsInput {
					latestType = "notification"
					state.AwaitingUser = true
				}
			}
		}
		if event.Type != "session_end" {
			state.Gone = false
		}
		if phase() != before && !event.At.IsZero() {
			state.Since = event.At
		}
	}
	state.InTurn = !state.NeedsInput && (latestType == "tool_use" || latestType == "prompt_submit")
	state.OwnRequest = state.NeedsInput && state.WaitingSummary != HeldInputSummary
	state.Working = (latestType == "tool_use" || latestType == "prompt_submit") &&
		in.StalenessSeconds < 120
	// No authoritative stuck signal exists in the event schema yet. Keep the
	// field explicit and false rather than inventing a timeout heuristic.
	state.Stuck = false
	state.Done = false
	if in.Archived {
		clearRequest()
		state.AwaitingUser = false
		state.Working = false
		state.InTurn = false
		state.OwnRequest = false
	}
	return state
}

// HeldInputSummary is the notification Coral records when automated input has
// been held a while because the agent waits on a prompt (#3431): it shows the
// agent as needing the user (it matches "needs your input").
const HeldInputSummary = "Coral is holding automated messages: this agent needs your input in its terminal (a prompt is open)"

// HeldInputEndedSummary is recorded when those held sends were delivered or
// dropped. It clears HeldInputSummary's request and nothing else, so an agent
// with no hook events of its own (agy, Codex) doesn't stay at Needs input (#3471).
const HeldInputEndedSummary = "Coral delivered or dropped the automated messages it was holding"

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

// notificationTypes maps Claude Code's notification_type (hooks reference; the
// 2.1.300 binary) to its effect. Unknown types are informational.
var notificationTypes = map[string]NotificationClass{
	"permission_prompt":        NotificationNeedsInput,
	"worker_permission_prompt": NotificationNeedsInput,
	"elicitation_dialog":       NotificationNeedsInput,
	"elicitation_url_dialog":   NotificationNeedsInput,
	"agent_needs_input":        NotificationNeedsInput,
	"idle_prompt":              NotificationAwaitingUser,
}

// notificationKinds is the WaitingKind a needs-input notification_type implies.
var notificationKinds = map[string]string{
	"permission_prompt": "permission", "worker_permission_prompt": "permission",
	"elicitation_dialog": "elicitation", "elicitation_url_dialog": "elicitation",
}

// ClassifyType maps a notification_type to its effect on the session state: the
// CLI's own word for it, rather than its English message (#3481).
func ClassifyType(notificationType string) NotificationClass {
	return notificationTypes[notificationType]
}

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

// IsApprovalRequest reports whether a request is a tool permission or approval
// prompt ("needs your permission" / "needs your approval"), as opposed to an
// input dialog ("needs your input", MCP elicitation) that only the user answers.
// Auto-accept answers only the former (#3182).
func IsApprovalRequest(summary string) bool {
	text := strings.ToLower(summary)
	return strings.Contains(text, "needs your permission") || strings.Contains(text, "needs your approval")
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
