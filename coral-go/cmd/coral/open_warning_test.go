package main

import (
	"bytes"
	"strings"
	"testing"
)

// #3970 (Security's #3967 finding 4): a printed sign-in link warns when this looks like an
// agent's terminal or a tmux pane, and only then.
func TestOpenWarnsInAnAgentTerminal(t *testing.T) {
	for _, k := range []string{"TMUX", "CORAL_SESSION_NAME", "CORAL_SESSION_ID", "CORAL_SESSION_TOKEN", "CORAL_SUBSCRIBER_ID"} {
		t.Setenv(k, "")
	}
	var out bytes.Buffer
	openDashboard(&out, "http://localhost:8420/?login_token=x", false)
	if strings.Contains(out.String(), "Warning") {
		t.Fatalf("warned in a plain terminal:\n%s", out.String())
	}
	for _, k := range []string{"TMUX", "CORAL_SESSION_NAME"} {
		t.Setenv(k, "set")
		out.Reset()
		openDashboard(&out, "http://localhost:8420/?login_token=x", false)
		if !strings.Contains(out.String(), "agent's terminal") {
			t.Errorf("%s set: no warning:\n%s", k, out.String())
		}
		t.Setenv(k, "")
	}
}
