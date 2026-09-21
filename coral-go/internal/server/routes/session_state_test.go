package routes

import "testing"

func TestDeriveSessionStateDurableNeedsInput(t *testing.T) {
	cases := []struct {
		name     string
		events   []StateEvent
		needs    bool
		awaiting bool
		working  bool
	}{
		{"notification", []StateEvent{{Type: "notification", Summary: "Claude needs your permission"}}, true, false, false},
		{"waiting notification is your turn", []StateEvent{{Type: "notification", Summary: "Claude is waiting for your input"}}, false, true, false},
		{"permission notification needs input", []StateEvent{{Type: "notification", Summary: "Claude needs your permission"}}, true, false, false},
		{"informational notification is ignored", []StateEvent{{Type: "notification", Summary: "Claude Code login successful"}}, false, false, false},
		{"informational notification preserves need", []StateEvent{{Type: "notification", Summary: "Claude needs your permission"}, {Type: "notification", Summary: "Claude Code login successful"}}, true, false, false},
		{"notification then stop", []StateEvent{{Type: "notification", Summary: "Claude needs your permission"}, {Type: "stop"}}, true, false, false},
		{"notification then noise", []StateEvent{{Type: "notification", Summary: "Claude needs your permission"}, {Type: "stop"}, {Type: "notification", Summary: "Claude is waiting for your input"}}, true, false, false},
		{"prompt clears", []StateEvent{{Type: "notification"}, {Type: "stop"}, {Type: "prompt_submit"}}, false, false, true},
		{"tool clears", []StateEvent{{Type: "notification"}, {Type: "stop"}, {Type: "tool_use", Summary: "Ran: ls"}}, false, false, true},
		{"plain stop is your turn", []StateEvent{{Type: "stop"}}, false, true, false},
		{"sleep loop is not working", []StateEvent{{Type: "tool_use", Summary: "Ran: sleep 1"}}, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveSessionState(SessionStateInput{Events: tc.events, StalenessSeconds: 1})
			if got.NeedsInput != tc.needs || got.AwaitingUser != tc.awaiting || got.Working != tc.working {
				t.Fatalf("state = %+v, want needs=%v awaiting=%v working=%v", got, tc.needs, tc.awaiting, tc.working)
			}
			if got.Done || got.Stuck {
				t.Fatalf("deprecated done/stuck must remain false: %+v", got)
			}
		})
	}
}

func TestDeriveSessionStateFlags(t *testing.T) {
	got := DeriveSessionState(SessionStateInput{NotStarted: true, Sleeping: true, StalenessSeconds: 999})
	if !got.NotStarted || !got.Sleeping || got.Working {
		t.Fatalf("unexpected flags: %+v", got)
	}
}
