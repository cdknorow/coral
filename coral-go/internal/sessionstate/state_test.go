package sessionstate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	permission  = "Notification: Claude needs your permission to use Bash"
	approval    = "Notification: Claude needs your approval for the plan"
	elicitation = "Notification: Claude Code needs your input"
	idle        = "Notification: Claude is waiting for your input"
	login       = "Notification: Claude Code login successful"
)

func note(summary string) Event { return Event{Type: "notification", Summary: summary} }

var (
	stop   = Event{Type: "stop", Summary: "Agent stopped: unknown"}
	prompt = Event{Type: "prompt_submit", Summary: "User submitted prompt"}
	tool   = Event{Type: "tool_use", Summary: "Ran: ls"}
	reset  = Event{Type: "session_reset", Summary: "Session reset: /clear"}
)

func TestDerive(t *testing.T) {
	cases := []struct {
		name     string
		events   []Event
		needs    bool
		awaiting bool
		working  bool
	}{
		{"no events is idle", nil, false, false, false},
		{"permission request needs input", []Event{note(permission)}, true, false, false},
		{"approval request needs input", []Event{note(approval)}, true, false, false},
		{"MCP elicitation needs input", []Event{note(elicitation)}, true, false, false},
		{"plain stop is your turn", []Event{stop}, false, true, false},
		{"idle reminder alone is your turn", []Event{note(idle)}, false, true, false},
		{"idle reminder after stop stays your turn", []Event{prompt, tool, stop, note(idle)}, false, true, false},
		{"login notification is ignored", []Event{note(login)}, false, false, false},
		{"informational notification does not hide active tool work", []Event{tool, note(login)}, false, false, true},
		{"awaiting notification ends active tool work", []Event{tool, note(idle)}, false, true, false},
		{"needs-input notification overrides active tool work", []Event{tool, note(permission)}, true, false, false},
		{"login notification after stop stays your turn", []Event{stop, note(login)}, false, true, false},
		{"unknown notification text is ignored", []Event{note("Notification: a question about permission settings")}, false, false, false},
		{"informational notification preserves a pending request", []Event{note(permission), note(login)}, true, false, false},
		{"idle reminder never downgrades a pending request", []Event{note(permission), note(idle)}, true, false, false},

		// A prompt blocks the turn, so a stop proves the request was answered.
		// Neither a denial nor a failed tool produces a tool event.
		{"permission denied then turn ends is your turn", []Event{prompt, note(permission), stop}, false, true, false},
		{"approved tool fails then turn ends is your turn", []Event{prompt, tool, note(permission), stop}, false, true, false},
		{"permission then stop then idle reminder is your turn", []Event{note(permission), stop, note(idle)}, false, true, false},

		{"approved tool clears the request", []Event{note(permission), tool}, false, false, true},
		{"prompt clears the request", []Event{note(permission), prompt}, false, false, true},
		{"session reset clears the request", []Event{note(permission), reset}, false, false, false},
		{"session reset clears your turn", []Event{stop, reset}, false, false, false},
		{"a new request after a new prompt is pending again", []Event{note(permission), stop, prompt, note(permission)}, true, false, false},
		{"work then stop again is your turn", []Event{stop, prompt, tool, stop}, false, true, false},
		{"sleep loop is working", []Event{{Type: "tool_use", Summary: "Ran: sleep 1"}}, false, false, true},
		// Coral auto-accepted the prompt (#3182): the request is answered and the tool runs.
		{"auto-accept clears the request", []Event{note(permission), {Type: "auto_accept", Summary: "Auto-approved Bash: ls"}}, false, false, true},
		{"a new request after an auto-accept is pending again", []Event{note(permission), {Type: "auto_accept", Summary: "Auto-approved Bash: ls"}, note(permission)}, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Derive(Input{Events: tc.events, StalenessSeconds: 1})
			if got.NeedsInput != tc.needs || got.AwaitingUser != tc.awaiting || got.Working != tc.working {
				t.Fatalf("state = %+v, want needs=%v awaiting=%v working=%v", got, tc.needs, tc.awaiting, tc.working)
			}
			if got.NeedsInput && got.AwaitingUser {
				t.Fatalf("needs input and your turn are mutually exclusive: %+v", got)
			}
			if got.NeedsInput != (got.WaitingReason == "notification") || got.NeedsInput != (got.WaitingSummary != "") {
				t.Fatalf("waiting reason and summary must exist only while a request is pending: %+v", got)
			}
			if got.Done || got.Stuck {
				t.Fatalf("deprecated done/stuck must remain false: %+v", got)
			}
		})
	}
}

func TestDeriveKeepsTheUnresolvedRequestAsSummary(t *testing.T) {
	got := Derive(Input{Events: []Event{note(permission), note(idle), note(login)}})
	if got.WaitingSummary != permission {
		t.Fatalf("summary = %q, want the pending request %q", got.WaitingSummary, permission)
	}
}

func TestDeriveFlags(t *testing.T) {
	got := Derive(Input{NotStarted: true, Sleeping: true, StalenessSeconds: 999})
	if !got.NotStarted || !got.Sleeping || got.Working {
		t.Fatalf("unexpected flags: %+v", got)
	}
	stale := Derive(Input{Events: []Event{tool}, StalenessSeconds: 120})
	if stale.Working {
		t.Fatalf("a stale tool event is not working: %+v", stale)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		summary string
		want    NotificationClass
	}{
		{permission, NotificationNeedsInput},
		{approval, NotificationNeedsInput},
		{elicitation, NotificationNeedsInput},
		{"NOTIFICATION: CLAUDE NEEDS YOUR PERMISSION TO USE BASH", NotificationNeedsInput},
		{idle, NotificationAwaitingUser},
		{"Notification: Agent is waiting for input", NotificationAwaitingUser},
		{login, NotificationInformational},
		{"", NotificationInformational},
		// Bare keywords used to match anywhere in the text.
		{"Notification: permission mode changed to plan", NotificationInformational},
		{"Notification: answered your question about the build", NotificationInformational},
	}
	for _, tc := range cases {
		if got := Classify(tc.summary); got != tc.want {
			t.Errorf("Classify(%q) = %v, want %v", tc.summary, got, tc.want)
		}
	}
}

// Every state consumer must go through Derive. Before this package existed,
// three of five consumers kept their own "latest event is a notification"
// rule, which turned the idle reminder into a false "Needs input" in the
// popout, the status endpoint and the needs_input webhook. Fail if that rule
// reappears anywhere in production code.
func TestNoConsumerReimplementsTheNotificationRule(t *testing.T) {
	oldRule := regexp.MustCompile(`[=!]=\s*"notification"|"notification"\s*[=!]=`)
	root := filepath.Join("..", "..")
	var scanned int
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if filepath.Base(filepath.Dir(path)) == "sessionstate" {
				return nil
			}
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			scanned++
			for i, line := range strings.Split(string(src), "\n") {
				if oldRule.MatchString(line) {
					t.Errorf("%s:%d compares an event type to \"notification\"; use sessionstate.Derive instead:\n\t%s", path, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 50 {
		t.Fatalf("guard scanned only %d files; the source root moved", scanned)
	}
}

// An archived agent never reports a pending request, an idle "your move" or
// work in progress, whatever its last events say; the same events on an agent
// that is only sleeping are unchanged.
func TestDeriveArchivedNeverWaits(t *testing.T) {
	for _, events := range [][]Event{{note(permission)}, {note(approval)}, {note(elicitation)}, {stop, note(idle)}, {prompt}, {tool}} {
		got := Derive(Input{Events: events, Sleeping: true, Archived: true})
		if got.NeedsInput || got.AwaitingUser || got.Working || got.WaitingReason != "" || got.WaitingSummary != "" {
			t.Fatalf("archived agent reports activity for %v: %+v", events, got)
		}
		if !got.Sleeping {
			t.Fatalf("archived agent is still sleeping: %+v", got)
		}
	}
	if sleeping := Derive(Input{Events: []Event{note(permission)}, Sleeping: true}); !sleeping.NeedsInput {
		t.Fatalf("a sleeping (not archived) agent keeps its pending request: %+v", sleeping)
	}
}

func TestIsApprovalRequest(t *testing.T) {
	for summary, want := range map[string]bool{
		permission:  true,
		approval:    true,
		elicitation: false,
		idle:        false,
		login:       false,
		"Notification: Claude NEEDS YOUR PERMISSION to use Bash": true,
	} {
		if got := IsApprovalRequest(summary); got != want {
			t.Errorf("IsApprovalRequest(%q) = %v, want %v", summary, got, want)
		}
	}
}

// #3471: once Coral's held sends are delivered or dropped, its own held notice
// stops showing Needs input, so an agent with no hook events (agy, Codex) doesn't
// keep it. A request the agent made itself is never cleared by it.
func TestHeldInputEndedClearsOnlyCoralsOwnNotice(t *testing.T) {
	ended := note(HeldInputEndedSummary)
	if Classify(HeldInputEndedSummary) != NotificationInformational {
		t.Fatal("the ended note must raise nothing")
	}
	if Classify(HeldInputSummary) != NotificationNeedsInput {
		t.Fatal("the held notice shows Needs input")
	}
	for _, c := range []struct {
		name   string
		events []Event
		want   bool
	}{
		{"held notice", []Event{stop, note(HeldInputSummary)}, true},
		{"held notice, then ended", []Event{stop, note(HeldInputSummary), ended}, false},
		{"agent's own permission request, then ended", []Event{tool, note(permission), ended}, true},
		{"held notice, the agent's own request, then ended", []Event{stop, note(HeldInputSummary), note(permission), ended}, true},
		{"ended alone", []Event{stop, ended}, false},
	} {
		got := Derive(Input{Events: c.events})
		if got.NeedsInput != c.want {
			t.Errorf("%s: NeedsInput = %v, want %v", c.name, got.NeedsInput, c.want)
		}
		if c.name == "held notice, then ended" && !got.AwaitingUser {
			t.Errorf("%s: the stop's calm 'your move' state should remain", c.name)
		}
	}
}

// #3481: the agent's own request events raise Needs input at once, with what it waits
// on; an answered elicitation and the end of a session clear it; Claude's
// notification_type decides a notification's effect when present, whatever its words.
func TestRequestEventsAndTypedNotifications(t *testing.T) {
	req := func(kind string) Event {
		return Event{Type: "permission_request", Summary: "Waiting for the user: " + kind, Kind: kind}
	}
	typed := func(typ, msg string) Event {
		return Event{Type: "notification", Summary: "Notification: " + msg, Kind: typ}
	}
	cases := []struct {
		name     string
		events   []Event
		needs    bool
		kind     string
		awaiting bool
	}{
		{"a tool permission", []Event{prompt, req("permission")}, true, "permission", false},
		{"a question", []Event{prompt, req("question")}, true, "question", false},
		{"a plan, then its notification ~6 s later keeps the kind", []Event{prompt, req("plan"), typed("permission_prompt", "Claude Code needs your approval for the plan")}, true, "plan", false},
		{"approved: the tool ran", []Event{prompt, req("permission"), tool}, false, "", false},
		{"an elicitation, answered", []Event{prompt, req("elicitation"), {Type: "request_resolved", Kind: "elicitation"}}, false, "", false},
		{"request_resolved doesn't clear a permission", []Event{prompt, req("permission"), {Type: "request_resolved", Kind: "elicitation"}}, true, "permission", false},
		{"session ended", []Event{prompt, req("permission"), {Type: "session_end"}}, false, "", false},
		{"typed permission_prompt, any wording", []Event{tool, typed("permission_prompt", "Approve this?")}, true, "permission", false},
		{"typed idle_prompt is the calm your-move", []Event{stop, typed("idle_prompt", "needs your input (wording changed)")}, false, "", true},
		{"typed auth_success raises nothing", []Event{stop, typed("auth_success", "needs your permission")}, false, "", true},
		{"no type: the words still decide (older CLIs)", []Event{tool, note(permission)}, true, "", false},
	}
	for _, c := range cases {
		got := Derive(Input{Events: c.events})
		if got.NeedsInput != c.needs || got.WaitingKind != c.kind || got.AwaitingUser != c.awaiting {
			t.Errorf("%s: needs=%v kind=%q awaiting=%v, want %v %q %v", c.name, got.NeedsInput, got.WaitingKind, got.AwaitingUser, c.needs, c.kind, c.awaiting)
		}
	}
	detail := `{"kind":"question","tool_name":"AskUserQuestion"}`
	if e := NewEvent("permission_request", "s", &detail); e.Kind != "question" {
		t.Errorf("NewEvent kind = %q", e.Kind)
	}
	nt := `{"notification_type":"idle_prompt"}`
	if e := NewEvent("notification", "s", &nt); e.Kind != "idle_prompt" {
		t.Errorf("NewEvent notification kind = %q", e.Kind)
	}
	if e := NewEvent("tool_use", "s", &detail); e.Kind != "" {
		t.Errorf("tool_use has no kind, got %q", e.Kind)
	}
}

// #3481 phase 1 (Backend's MUST 3 and note): a request stays open until its own tool
// finishes or the turn ends. Claude runs tools in parallel, and each hook is its own
// process, so another tool's event (in either order) must not clear it.
func TestRequestsClearPerTool(t *testing.T) {
	at := func(s int) time.Time { return time.Date(2026, 10, 15, 1, 0, s, 0, time.UTC) }
	req := func(tool string, s int) Event {
		return Event{Type: "permission_request", Summary: "Waiting for the user: permission (" + tool + ")", Kind: "permission", ToolName: tool, At: at(s)}
	}
	toolEv := func(tool string, s int) Event {
		return Event{Type: "tool_use", Summary: "Ran", ToolName: tool, At: at(s)}
	}
	p := Event{Type: "prompt_submit", Summary: "User submitted prompt", At: at(0)}
	s := Event{Type: "stop", Summary: "Agent stopped", At: at(30)}

	cases := []struct {
		name          string
		events        []Event
		needs, inTurn bool
	}{
		{"another tool finishing doesn't clear Bash's request", []Event{p, req("Bash", 1), toolEv("Read", 2)}, true, false},
		{"its own tool finishing (approved) clears it", []Event{p, req("Bash", 1), toolEv("Read", 2), toolEv("Bash", 9)}, false, true},
		{"swapped arrival: an earlier tool's event after the request", []Event{p, req("Bash", 2), toolEv("Read", 1)}, true, false},
		{"two requests: one approved, the other still open", []Event{p, req("Bash", 1), req("Edit", 1), toolEv("Bash", 5)}, true, false},
		{"the turn ends (a denial shows as the transcript's stop)", []Event{p, req("Bash", 1), s}, false, false},
		{"a notification-only request clears on any tool (older CLIs)", []Event{p, note(permission), toolEv("Read", 3)}, false, true},
	}
	for _, c := range cases {
		got := Derive(Input{Events: c.events})
		if got.NeedsInput != c.needs || got.InTurn != c.inTurn || got.OwnRequest != c.needs {
			t.Errorf("%s: needs=%v inTurn=%v own=%v, want %v %v", c.name, got.NeedsInput, got.InTurn, got.OwnRequest, c.needs, c.inTurn)
		}
	}

	// The fields the gate reads.
	got := Derive(Input{Events: []Event{p, toolEv("Read", 4), s, {Type: "prompt_submit", At: at(40)}, req("Bash", 41)}})
	if !got.Since.Equal(at(41)) || !got.LastEvent.Equal(at(41)) || !got.LastStop.Equal(at(30)) {
		t.Errorf("since=%v last=%v stop=%v", got.Since, got.LastEvent, got.LastStop)
	}
	got = Derive(Input{Events: []Event{p, {Type: "session_end", At: at(50)}}})
	if !got.Gone || got.InTurn {
		t.Errorf("session_end: gone=%v inTurn=%v", got.Gone, got.InTurn)
	}
	// Coral's own held notice shows Needs input but is not the agent's request.
	got = Derive(Input{Events: []Event{s, note(HeldInputSummary)}})
	if !got.NeedsInput || got.OwnRequest {
		t.Errorf("held notice: needs=%v own=%v", got.NeedsInput, got.OwnRequest)
	}
	// A long turn stays InTurn (Working drops after 2 min of staleness).
	got = Derive(Input{Events: []Event{p, toolEv("Read", 4)}, StalenessSeconds: 600})
	if !got.InTurn || got.Working {
		t.Errorf("stale turn: inTurn=%v working=%v", got.InTurn, got.Working)
	}
}
