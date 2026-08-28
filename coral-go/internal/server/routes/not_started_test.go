package routes

import (
	"testing"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/tmux"
)

// The defect this guards: an agent blocked on its CLI's trust-folder prompt
// looks identical to a working agent in the dashboard, on a new user's very
// first launch. Nothing here matches the prompt's wording — any interactive
// prompt, auth flow, or first-run question produces the same shape.
func TestAgentNeverStarted(t *testing.T) {
	const inWindow = 60.0

	tests := []struct {
		name      string
		agentType string
		latestEv  string
		staleness float64
		age       float64
		want      bool
	}{
		{"blocked before it ever started", "claude", "", notStartedThresholdSeconds + 5, inWindow, true},
		{"still starting up, not yet past the threshold", "claude", "", notStartedThresholdSeconds - 1, inWindow, false},
		{"exactly at the threshold counts", "claude", "", notStartedThresholdSeconds, inWindow, true},
		{"it started and is now working", "claude", "tool_use", 600, inWindow, false},
		{"it started and is asking a question", "claude", "notification", 600, inWindow, false},
		{"it started and finished", "claude", "stop", 9000, inWindow, false},
		// A terminal is a shell. Sitting at a prompt forever is the point, and
		// it never emits agent events, so it would otherwise be flagged always.
		{"a terminal session is never flagged", at.Terminal, "", 100000, inWindow, false},
		// No log file yields staleness 0, which must not read as a stall.
		{"no log yet is not a stall", "claude", "", 0, inWindow, false},
		{"applies to every agent CLI, not just claude", "codex", "", 300, inWindow, true},
		// Measured against a real agent: one launched without a task never emits
		// an event, so once it is idle at its own prompt every other condition
		// holds. That is reported honestly as "has not done anything yet", but
		// it must not persist for the life of the session.
		{"an agent idle long after launch is not flagged", "claude", "", 300, notStartedWindowSeconds + 1, false},
		{"still inside the window at the boundary", "claude", "", 300, notStartedWindowSeconds, true},
		{"an unknown launch time is never flagged", "claude", "", 300, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := agentNeverStarted(tc.agentType, tc.latestEv, tc.staleness, tc.age)
			if got != tc.want {
				t.Fatalf("agentNeverStarted(%q, %q, staleness=%v, age=%v) = %v, want %v",
					tc.agentType, tc.latestEv, tc.staleness, tc.age, got, tc.want)
			}
		})
	}
}

// The threshold has to be long enough that a slow start is not called a stall,
// and short enough that a new user has not already given up.
func TestNotStartedThresholdIsWithinAReasonableRange(t *testing.T) {
	if notStartedThresholdSeconds < 15 {
		t.Errorf("threshold %ds will flag agents that are merely slow to start", notStartedThresholdSeconds)
	}
	if notStartedThresholdSeconds > 60 {
		t.Errorf("threshold %ds is longer than a new user will wait at a blank screen", notStartedThresholdSeconds)
	}
}

func TestSessionAgeSeconds(t *testing.T) {
	if got := sessionAgeSeconds(""); got != 0 {
		t.Errorf("missing timestamp = %v, want 0", got)
	}
	if got := sessionAgeSeconds("not a time"); got != 0 {
		t.Errorf("unparseable timestamp = %v, want 0", got)
	}
	// Clock skew putting creation in the future must not read as a huge age.
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if got := sessionAgeSeconds(future); got != 0 {
		t.Errorf("future timestamp = %v, want 0", got)
	}
	past := time.Now().UTC().Add(-90 * time.Second).Format(time.RFC3339)
	if got := sessionAgeSeconds(past); got < 85 || got > 95 {
		t.Errorf("90s-old timestamp = %v, want ~90", got)
	}
}

// The launch response used to report "pty" for tmux-backed sessions, which
// misled someone debugging a backend problem into a wrong conclusion about
// which terminal their agent ran on.
func TestTerminalKindNamesWhatActuallyRuns(t *testing.T) {
	if got := terminalKind(ptymanager.NewPTYBackend()); got != "pty" {
		t.Errorf("PTY backend reported as %q, want \"pty\"", got)
	}
	tmuxBackend := ptymanager.NewTmuxBackend(tmux.NewClient(t.TempDir()), t.TempDir())
	if got := terminalKind(tmuxBackend); got != "tmux" {
		t.Errorf("tmux backend reported as %q, want \"tmux\"", got)
	}
	// No backend configured means tmux is driven directly.
	if got := terminalKind(nil); got != "tmux" {
		t.Errorf("nil backend reported as %q, want \"tmux\"", got)
	}
}
