package sessionstate

import (
	"testing"
	"time"
)

// #3655: the observed sequence (the Orchestrator's AskUserQuestion, 16 Oct 00:39 to 00:44 UTC).
// The agent's own request is open from its notification until the question is answered; Coral's
// held notice two minutes in must not turn it into Coral's own notice (OwnRequest false, which
// the gate read as idle with the menu on screen).
func TestHeldNoticeKeepsTheAgentsOwnRequest(t *testing.T) {
	at := func(s string) time.Time {
		v, _ := time.Parse("15:04:05", s)
		return v
	}
	permission := Event{Type: "notification", Summary: "Notification: Claude needs your permission", Kind: "permission_prompt", At: at("00:39:53")}
	held := Event{Type: "notification", Summary: HeldInputSummary, At: at("00:41:49")}
	heldEnded := Event{Type: "notification", Summary: HeldInputEndedSummary, At: at("00:44:35")}
	run := Event{Type: "tool_use", ToolName: "Bash", At: at("00:39:43")}
	answered := Event{Type: "tool_use", ToolName: "AskUserQuestion", At: at("00:44:32")}

	check := func(name string, events []Event, own, needs bool) State {
		t.Helper()
		st := Derive(Input{Events: events})
		if st.OwnRequest != own || st.NeedsInput != needs {
			t.Fatalf("%s: OwnRequest=%v NeedsInput=%v, want %v %v (summary %q)", name, st.OwnRequest, st.NeedsInput, own, needs, st.WaitingSummary)
		}
		return st
	}

	st := check("the agent's request, then Coral's held notice", []Event{run, permission, held}, true, true)
	if st.WaitingKind != "permission" || st.WaitingSummary != permission.Summary {
		t.Fatalf("the agent's request is kept: kind %q summary %q", st.WaitingKind, st.WaitingSummary)
	}
	if !st.Since.Equal(permission.At) {
		t.Fatalf("still waiting since the agent asked: %v", st.Since)
	}
	check("the held sends then end: the agent's request is still open", []Event{run, permission, held, heldEnded}, true, true)
	check("the question is answered", []Event{run, permission, held, answered}, false, false)

	// Coral's held notice on its own (an agent with no request signal) stays Coral's notice.
	check("the held notice alone", []Event{run, held}, false, true)
	check("then ended", []Event{run, held, heldEnded}, false, false)
	// An agent's request after Coral's notice becomes the agent's own.
	check("held notice, then the agent's request", []Event{run, held, permission}, true, true)
	// The agent's own PermissionRequest is kept the same way.
	pr := Event{Type: "permission_request", Kind: "question", ToolName: "AskUserQuestion", Summary: "Question", At: at("00:39:45")}
	st = check("PermissionRequest, then the held notice", []Event{run, pr, held}, true, true)
	if st.WaitingKind != "question" {
		t.Fatalf("kind %q, want question", st.WaitingKind)
	}
}
