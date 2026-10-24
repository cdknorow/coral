package sessionstate

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Derive, split into a deriver with a method per kind of event, the summary and the overrides (#4756), derives
// what deriveBefore4756 (the function at v1.4.94, below) derived, for random runs of events of every type (held
// notices, parallel tool requests, elicitations, the end of a session) and every input flag. Each rule has a floor,
// counted where the rule actually changed something.

var deriveEventTypes = []string{"tool_use", "tool_use", "prompt_submit", "session_reset", "auto_accept", "stop", "stop", "permission_request",
	"permission_request", "request_resolved", "session_end", "notification", "notification", "notification", "mystery"}

var deriveSummaries = []string{"", "Claude needs your permission to use Bash", "Claude needs your approval", "needs your input", "Claude is waiting for your input",
	"waiting for input", "Login successful", HeldInputSummary, HeldDialogSummary, HeldInputEndedSummary, "an update is available"}

func randomDeriveInput(rng *rand.Rand) Input {
	var in Input
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	for k := rng.Intn(12); k > 0; k-- {
		e := Event{Type: deriveEventTypes[rng.Intn(len(deriveEventTypes))]}
		switch e.Type {
		case "tool_use", "permission_request":
			e.ToolName = []string{"Bash", "Edit", "Read", ""}[rng.Intn(4)]
		}
		switch e.Type {
		case "permission_request":
			e.Kind = []string{"permission", "question", "plan", "elicitation", ""}[rng.Intn(5)]
			e.Summary = deriveSummaries[rng.Intn(4)]
		case "notification":
			e.Summary = deriveSummaries[rng.Intn(len(deriveSummaries))]
			if rng.Intn(3) == 0 {
				e.Kind = []string{"permission_prompt", "worker_permission_prompt", "elicitation_dialog", "elicitation_url_dialog", "agent_needs_input", "idle_prompt", "auth_success", "unknown_type"}[rng.Intn(8)]
			}
		}
		if rng.Intn(5) > 0 {
			at = at.Add(time.Duration(1+rng.Intn(90)) * time.Second)
			e.At = at
		}
		in.Events = append(in.Events, e)
	}
	if rng.Intn(10) == 0 { // a turn's end, Coral's held notice twice, then the notice ended: what it replaced comes back
		at = at.Add(time.Minute)
		for _, e := range []Event{{Type: "stop"}, {Type: "notification", Summary: HeldInputSummary}, {Type: "notification", Summary: HeldDialogSummary},
			{Type: "notification", Summary: HeldInputEndedSummary}} {
			at = at.Add(time.Second)
			e.At = at
			in.Events = append(in.Events, e)
		}
	}
	in.StalenessSeconds = []float64{0, 30, 119, 120, 600}[rng.Intn(5)]
	in.NotStarted = rng.Intn(5) == 0
	in.Sleeping = rng.Intn(6) == 0
	in.Archived = rng.Intn(8) == 0
	if rng.Intn(8) == 0 {
		in.NeedsLogin = true
		in.LoginReason = []string{"", "Your session expired"}[rng.Intn(2)]
	}
	if rng.Intn(8) == 0 {
		in.StartupPrompt = []string{"trust", "theme"}[rng.Intn(2)]
		in.StartupSummary = []string{"", "Trust this folder?"}[rng.Intn(2)]
	}
	return in
}

// deriveKinds names what a run exercised, from its input and the state derived.
func deriveKinds(in Input, st State) []string {
	var out []string
	add := func(on bool, k string) {
		if on {
			out = append(out, k)
		}
	}
	add(st.NeedsInput && st.WaitingReason == "notification", "a request open")
	for _, k := range []string{"permission", "question", "plan", "elicitation"} {
		add(st.NeedsInput && st.WaitingKind == k, "a request: "+k)
	}
	add(st.NeedsInput && st.WaitingReason == "notification" && !st.OwnRequest, "Coral's held notice")
	add(st.InTurn, "in a turn")
	add(st.Working, "working")
	add(st.InTurn && !st.Working, "a turn gone quiet")
	add(st.AwaitingUser, "awaiting the user")
	add(st.Gone, "gone")
	add(st.NeedsLogin, "a sign-in")
	add(st.WaitingKind == KindStartup, "a startup prompt")
	add(in.Archived && !st.NeedsInput, "archived")
	add(!st.Since.IsZero(), "a phase with a time")
	add(!st.LastStop.IsZero(), "a turn's end")
	// The rules that need an order of events, seen in the input.
	open := map[string]int{}
	heldOverOwn, heldEnded, otherTool, heldRaised := false, false, false, false
	for i, e := range in.Events {
		switch {
		case e.Type == "permission_request" && e.Kind != "elicitation":
			open[e.ToolName]++
		case e.Type == "tool_use" && len(open) > 0 && open[e.ToolName] == 0:
			otherTool = true
		case e.Type == "notification" && IsHeldInputSummary(e.Summary) && i > 0 && len(open) > 0:
			heldOverOwn = true
		case e.Type == "notification" && IsHeldInputSummary(e.Summary) && len(open) == 0:
			heldRaised = true // Coral's own notice, no request of the agent's open
		case e.Type == "notification" && e.Summary == HeldInputEndedSummary && heldRaised:
			heldEnded = true // the notice it ends was raised before it
		}
	}
	add(otherTool && st.NeedsInput, "another tool's event, the request still open")
	add(heldOverOwn && st.OwnRequest, "a held notice over the agent's own request")
	add(heldEnded, "a held notice ended")
	add(heldEnded && st.AwaitingUser, "a held notice ended, the turn's end restored")
	return out
}

func TestDeriveAgreesWithBefore4756(t *testing.T) {
	kinds := map[string]int{}
	for seed := int64(1); seed <= 60000; seed++ {
		rng := rand.New(rand.NewSource(seed))
		in := randomDeriveInput(rng)
		old, got := deriveBefore4756(in), Derive(in)
		got.WaitingSince = time.Time{} // the one field added since (#6981): the copy has none; routes' TestWaitingSinceThroughDerive checks it
		if !reflect.DeepEqual(old, got) {
			require.Equal(t, fmt.Sprintf("%+v", old), fmt.Sprintf("%+v", got), "seed %d: %+v", seed, in)
		}
		for _, k := range deriveKinds(in, got) {
			kinds[k]++
		}
	}
	t.Logf("compared, per kind: %v", kinds)
	for _, k := range []string{"a request open", "a request: permission", "a request: question", "a request: plan", "a request: elicitation",
		"Coral's held notice", "in a turn", "working", "a turn gone quiet", "awaiting the user", "gone", "a sign-in", "a startup prompt",
		"archived", "a phase with a time", "a turn's end", "another tool's event, the request still open",
		"a held notice over the agent's own request", "a held notice ended", "a held notice ended, the turn's end restored"} {
		assert.GreaterOrEqual(t, kinds[k], 50, "%q went (almost) unexercised", k)
	}
}

// deriveBefore4756 is Derive before #4756 split it (state.go at v1.4.94), kept to compare with.
func deriveBefore4756(in Input) State {
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
				if state.NeedsInput && IsHeldInputSummary(state.WaitingSummary) {
					clearRequest()
					latestType, state.AwaitingUser = beforeType, beforeAwaiting
				}
				continue
			}
			if IsHeldInputSummary(event.Summary) && state.NeedsInput && !IsHeldInputSummary(state.WaitingSummary) {
				// Coral's held notice while the agent's own request is open: the request is
				// what the agent waits on, so it stays as it is (#3655). Replacing its summary
				// made OwnRequest false, and the gate read the agent as idle with its menu open.
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
	state.OwnRequest = state.NeedsInput && !IsHeldInputSummary(state.WaitingSummary)
	state.Working = (latestType == "tool_use" || latestType == "prompt_submit") &&
		in.StalenessSeconds < 120
	// No authoritative stuck signal exists in the event schema yet. Keep the
	// field explicit and false rather than inventing a timeout heuristic.
	state.Stuck = false
	state.Done = false
	if in.NeedsLogin {
		// Nothing runs until the user signs in: whatever the events say, the agent waits
		// on this, and automated input is held (an Enter would answer the login screen).
		state.NeedsLogin = true
		state.NeedsInput, state.AwaitingUser, state.Working, state.InTurn, state.NotStarted = true, false, false, false, false
		state.WaitingReason, state.WaitingKind = "login", KindLogin
		state.WaitingSummary = in.LoginReason
		if state.WaitingSummary == "" {
			state.WaitingSummary = LoginSummary
		}
		state.OwnRequest = true
	}
	if in.StartupPrompt != "" && !in.NeedsLogin {
		// Nothing runs until the prompt is answered: a request to the user, as a login is.
		state.NeedsInput, state.AwaitingUser, state.Working, state.InTurn, state.NotStarted = true, false, false, false, false
		state.WaitingReason, state.WaitingKind = in.StartupPrompt, KindStartup
		state.WaitingSummary = in.StartupSummary
		if state.WaitingSummary == "" {
			state.WaitingSummary = StartupSummary
		}
		state.OwnRequest = true
	}
	if in.Archived {
		state.NeedsLogin = false
		clearRequest()
		state.AwaitingUser = false
		state.Working = false
		state.InTurn = false
		state.OwnRequest = false
	}
	return state
}
