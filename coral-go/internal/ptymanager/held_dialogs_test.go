package ptymanager

import (
	"testing"
	"time"
)

// HeldAtDialog lists a session only while a send to it is held by a request to the user, from
// its oldest such send; a hold for the input box or a busy agent is not a dialog (#4175 step 3).
func TestHeldAtDialogListsOnlyDialogHolds(t *testing.T) {
	gate.mu.Lock()
	saved := gate.held
	t0 := time.Date(2026, 10, 17, 4, 0, 0, 0, time.UTC)
	gate.held = map[string][]*heldInput{
		"claude-dialog": {
			{in: AutomatedInput{Key: "a", Text: "Password for deploy@host: hunter2"}, heldAt: t0.Add(time.Minute), kind: kindPrompt},
			{in: AutomatedInput{Key: "b"}, heldAt: t0, kind: kindTrust},
		},
		"claude-draft": {{in: AutomatedInput{Key: "c"}, heldAt: t0, kind: ""}},
	}
	gate.mu.Unlock()
	t.Cleanup(func() {
		gate.mu.Lock()
		gate.held = saved
		gate.mu.Unlock()
	})
	got := HeldAtDialog()
	if len(got) != 1 || !got["claude-dialog"].Equal(t0) {
		t.Fatalf("HeldAtDialog() = %v; want only claude-dialog since %v", got, t0)
	}
}
