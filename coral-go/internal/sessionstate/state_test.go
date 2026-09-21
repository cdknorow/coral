package sessionstate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
		{"sleep loop is not working", []Event{{Type: "tool_use", Summary: "Ran: sleep 1"}}, false, false, false},
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
