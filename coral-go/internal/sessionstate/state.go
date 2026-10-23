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
	// Wants is what a permission_request wants to do ("Wants to run: <command>"), as the server stored it: already cleaned, redacted and cut (#6622). It is display text only: no
	// classifier reads it, so an agent's command cannot say "needs your permission" to them.
	Wants string
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
		Wants            string `json:"wants"`
	}
	if json.Unmarshal([]byte(*detailJSON), &d) == nil {
		if eventType == "permission_request" && len(d.Wants) <= maxWantsBytes {
			e.Wants = d.Wants
		}
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

// maxWantsBytes bounds the stored Wants of an event read back (the server stores at most 200 characters; this is the read side's own bound).
const maxWantsBytes = 2048

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
	// NeedsLogin: the agent's CLI waits for the user to sign in (#3830): its transcript's
	// latest message is an authentication error, or, before it has a transcript, its login
	// screen is open. LoginReason is the CLI's own message, when it gave one.
	NeedsLogin  bool
	LoginReason string
	// StartupPrompt: the agent has sent no event yet and its CLI waits on a prompt it draws at launch,
	// before any hook runs (#4543): the reason (ptymanager.StartupTrust, StartupUpdate, StartupPrompt), and
	// StartupSummary the prompt's question when it could be read.
	StartupPrompt  string
	StartupSummary string
}

// KindStartup is the WaitingKind of an agent held by a prompt its CLI draws at launch (#4543).
const KindStartup = "startup"

// StartupSummary is the WaitingSummary of such an agent when its prompt's question couldn't be read.
const StartupSummary = "The agent waits on a prompt in its terminal before it starts"

// KindLogin is the WaitingKind of an agent that needs the user to sign in (#3830).
const KindLogin = "login"

// LoginSummary is the WaitingSummary of an agent that needs the user to sign in, when its
// CLI gave no message of its own.
const LoginSummary = "The agent needs you to sign in (in its terminal)"

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
	// WaitingWants is what the open request wants to do (Event.Wants), or "" (#6622); only the row's display text reads it.
	WaitingWants string
	Working      bool
	NotStarted   bool
	Sleeping     bool
	Stuck        bool
	Done         bool
	// NeedsLogin: the agent waits for the user to sign in (Input.NeedsLogin). It is also
	// NeedsInput, kind KindLogin, so every surface that shows a request shows this one.
	NeedsLogin bool

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
	d := &deriver{state: State{NotStarted: in.NotStarted, Sleeping: in.Sleeping}, pending: map[string]int{}}
	for _, event := range in.Events {
		before := d.phase()
		if !event.At.IsZero() {
			d.state.LastEvent = event.At
		}
		if !d.apply(event) {
			continue
		}
		if event.Type != "session_end" {
			d.state.Gone = false
		}
		if d.phase() != before && !event.At.IsZero() {
			d.state.Since = event.At
		}
	}
	d.summarise(in)
	d.override(in)
	return d.state
}

// deriver folds a session's events into its State.
type deriver struct {
	state      State
	latestType string
	// What a request replaced, restored when Coral's own held notice is cleared.
	beforeType     string
	beforeAwaiting bool
	// pending counts the agent's open requests per tool (PermissionRequest). Claude
	// runs tool calls in parallel, so another tool's event must not clear a request
	// that is still open (#3481, Backend's MUST 3).
	pending map[string]int
	open    int
}

func (d *deriver) clearRequest() {
	d.state.NeedsInput = false
	d.state.WaitingReason = ""
	d.state.WaitingSummary = ""
	d.state.WaitingKind = ""
	d.state.WaitingWants = ""
	d.pending, d.open = map[string]int{}, 0
}

func (d *deriver) raise(event Event, kind string) {
	// A request already open keeps its kind: the agent's PermissionRequest says
	// "plan" or "question", the notification that follows only "permission".
	if !d.state.NeedsInput || d.state.WaitingKind == "" {
		d.state.WaitingKind = kind
	}
	if !d.state.NeedsInput {
		d.beforeType, d.beforeAwaiting = d.latestType, d.state.AwaitingUser
	}
	d.latestType = "notification"
	d.state.NeedsInput = true
	d.state.AwaitingUser = false
	d.state.WaitingReason = "notification"
	d.state.WaitingSummary = event.Summary
	if event.Wants != "" { // kept when the agent's notification for the same request follows: it carries no words of the request's own
		d.state.WaitingWants = event.Wants
	}
}

// phase is what the session is in: a request, a turn, idle, or nothing yet.
func (d *deriver) phase() string {
	switch {
	case d.state.NeedsInput:
		return "request"
	case d.latestType == "tool_use" || d.latestType == "prompt_submit":
		return "turn"
	case d.latestType == "":
		return "none"
	}
	return "idle"
}

// apply takes one event. It reports false for a notification that changes nothing of the session's phase
// (Coral's held notice ended, or one held over the agent's own request): its time sets nothing either.
func (d *deriver) apply(event Event) bool {
	switch event.Type {
	case "tool_use":
		d.toolUsed(event)
	case "session_reset", "prompt_submit":
		d.latestType = event.Type
		d.clearRequest()
		d.state.AwaitingUser = false
	case "auto_accept":
		// Coral answered the prompt (auto-accept): the agent is now
		// running the approved tool.
		d.latestType = "tool_use"
		d.clearRequest()
		d.state.AwaitingUser = false
	case "stop":
		d.latestType = event.Type
		d.clearRequest()
		d.state.AwaitingUser = true
		d.state.LastStop = event.At
	case "permission_request":
		// The agent's own hook (Claude's PermissionRequest or Elicitation) says a
		// request to the user is open, at once rather than ~6 s later (#3481).
		d.raise(event, event.Kind)
		if event.Kind != "elicitation" {
			d.pending[event.ToolName]++
			d.open++
		}
	case "request_resolved":
		// An elicitation was answered (accept, decline or cancel): the agent goes on.
		if d.state.NeedsInput && d.state.WaitingKind == "elicitation" {
			d.clearRequest()
			d.latestType = "tool_use"
			d.state.AwaitingUser = false
		}
	case "session_end":
		d.latestType = ""
		d.clearRequest()
		d.state.AwaitingUser = false
		d.state.Gone = true
	case "notification":
		return d.notified(event)
	}
	return true
}

// toolUsed: a tool finished. It answers its own tool's request (an approval); a
// request for another tool stays open.
func (d *deriver) toolUsed(event Event) {
	if d.open > 0 {
		if d.pending[event.ToolName] > 0 {
			d.pending[event.ToolName]--
			d.open--
		}
		if d.open > 0 {
			return
		}
	}
	d.latestType = event.Type
	d.clearRequest()
	d.state.AwaitingUser = false
}

// notified takes a notification: Coral's held notice ending, Coral's held notice (never over the agent's own
// request), a request to the user, or the idle reminder. It reports false for the two that change nothing.
func (d *deriver) notified(event Event) bool {
	if event.Summary == HeldInputEndedSummary {
		// Coral's held sends were delivered or dropped: its own notice no
		// longer stands. A request the agent made itself is kept (#3471).
		if d.state.NeedsInput && IsHeldInputSummary(d.state.WaitingSummary) {
			d.clearRequest()
			d.latestType, d.state.AwaitingUser = d.beforeType, d.beforeAwaiting
		}
		return false
	}
	if IsHeldInputSummary(event.Summary) && d.state.NeedsInput && !IsHeldInputSummary(d.state.WaitingSummary) {
		// Coral's held notice while the agent's own request is open: the request is
		// what the agent waits on, so it stays as it is (#3655). Replacing its summary
		// made OwnRequest false, and the gate read the agent as idle with its menu open.
		return false
	}
	class := Classify(event.Summary)
	if event.Kind != "" {
		class = ClassifyType(event.Kind)
	}
	switch class {
	case NotificationNeedsInput:
		d.raise(event, notificationKinds[event.Kind])
	case NotificationAwaitingUser:
		if !d.state.NeedsInput {
			d.latestType = "notification"
			d.state.AwaitingUser = true
		}
	}
	return true
}

// summarise sets what follows from the events as a whole: in a turn, the agent's own request, working (a turn
// with output in the last two minutes).
func (d *deriver) summarise(in Input) {
	d.state.InTurn = !d.state.NeedsInput && (d.latestType == "tool_use" || d.latestType == "prompt_submit")
	d.state.OwnRequest = d.state.NeedsInput && !IsHeldInputSummary(d.state.WaitingSummary)
	d.state.Working = (d.latestType == "tool_use" || d.latestType == "prompt_submit") &&
		in.StalenessSeconds < 120
	// No authoritative stuck signal exists in the event schema yet. Keep the
	// field explicit and false rather than inventing a timeout heuristic.
	d.state.Stuck = false
	d.state.Done = false
}

// override applies what outranks the events: a sign-in the agent waits on, a startup prompt, an archived session.
func (d *deriver) override(in Input) {
	if in.NeedsLogin {
		// Nothing runs until the user signs in: whatever the events say, the agent waits
		// on this, and automated input is held (an Enter would answer the login screen).
		d.state.NeedsLogin = true
		d.state.NeedsInput, d.state.AwaitingUser, d.state.Working, d.state.InTurn, d.state.NotStarted = true, false, false, false, false
		d.state.WaitingReason, d.state.WaitingKind = "login", KindLogin
		d.state.WaitingSummary = in.LoginReason
		if d.state.WaitingSummary == "" {
			d.state.WaitingSummary = LoginSummary
		}
		d.state.OwnRequest = true
	}
	if in.StartupPrompt != "" && !in.NeedsLogin {
		// Nothing runs until the prompt is answered: a request to the user, as a login is.
		d.state.NeedsInput, d.state.AwaitingUser, d.state.Working, d.state.InTurn, d.state.NotStarted = true, false, false, false, false
		d.state.WaitingReason, d.state.WaitingKind = in.StartupPrompt, KindStartup
		d.state.WaitingSummary = in.StartupSummary
		if d.state.WaitingSummary == "" {
			d.state.WaitingSummary = StartupSummary
		}
		d.state.OwnRequest = true
	}
	if in.Archived {
		d.state.NeedsLogin = false
		d.clearRequest()
		d.state.AwaitingUser = false
		d.state.Working = false
		d.state.InTurn = false
		d.state.OwnRequest = false
	}
}

// SignedInSince reports whether an event after t shows an agent is past a sign-in its CLI
// asked for at t (#3830): Claude Code's auth_success notification, or a prompt or tool run
// since. Its transcript's next message clears the error too; these cover the time before it
// is written.
func SignedInSince(events []Event, t time.Time) bool {
	for _, ev := range events {
		if ev.At.IsZero() || !ev.At.Truncate(time.Second).After(t.Truncate(time.Second)) {
			continue
		}
		switch {
		case ev.Type == "notification" && ev.Kind == "auth_success",
			ev.Type == "prompt_submit", ev.Type == "tool_use":
			return true
		}
	}
	return false
}

// HeldInputSummary is the notification Coral records when automated input has
// been held a while because the agent waits on a prompt (#3431): it shows the
// agent as needing the user (it matches "needs your input").
const HeldInputSummary = "Coral is holding automated messages: this agent needs your input in its terminal (a prompt is open)"

// HeldDialogSummary is HeldInputSummary for a dialog Claude Code draws over its input box (its
// bug-report draft, #4104): the same request, saying what to do. It matches "needs your input".
const HeldDialogSummary = "Delivery held: a dialog is open in this agent's terminal (Claude Code's bug-report draft) and needs your input: review, send or dismiss it, and Coral's messages follow"

// IsHeldInputSummary reports whether a notification is one of Coral's own held notices
// (HeldInputSummary, HeldDialogSummary): Coral's, not a request the agent made.
func IsHeldInputSummary(s string) bool { return s == HeldInputSummary || s == HeldDialogSummary }

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
