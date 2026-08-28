package agent

import (
	"strings"
	"testing"

	at "github.com/cdknorow/coral/internal/agenttypes"
)

// The defect: GetAgent's default arm returned ClaudeAgent for any unrecognised
// type, so a launch naming an unsupported agent returned 200 ok:true and
// started a real Claude session — spending the user's tokens on an agent they
// never asked for.
func TestValidateAgentType(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		wantErr   bool
	}{
		{"claude", at.Claude, false},
		{"codex", at.Codex, false},
		{"gemini", at.Gemini, false},
		{"pi", at.Pi, false},
		// A plain shell. It has no Agent implementation and never reaches
		// GetAgent, but it is a valid agent_type at the API and rejecting it
		// would break terminal sessions.
		{"terminal", at.Terminal, false},
		// Not specified is not the same as specified-and-unknown. Only the
		// second is an error.
		{"empty means use the default", "", false},

		{"the type that started this", "shell", true},
		{"an agent we do not support", "cursor", true},
		{"a typo", "claude-code", true},
		{"wrong case is not a match", "Claude", true},
		{"whitespace is not trimmed into a match", " claude", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAgentType(tc.agentType)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateAgentType(%q) = nil, want an error", tc.agentType)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateAgentType(%q) = %v, want nil", tc.agentType, err)
			}
		})
	}
}

// The error is what the user sees, so it has to name both what they asked for
// and what they could have asked for instead.
func TestValidateAgentTypeErrorIsActionable(t *testing.T) {
	err := ValidateAgentType("shell")
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "shell") {
		t.Errorf("error does not name the rejected type: %s", msg)
	}
	for _, known := range LaunchableAgentTypes() {
		if !strings.Contains(msg, known) {
			t.Errorf("error does not offer %q as a supported type: %s", known, msg)
		}
	}
}

// Every launchable type except terminal must resolve to an implementation that
// actually reports that type — a fallback here is how the original bug looked.
func TestGetAgentReturnsTheTypeThatWasAskedFor(t *testing.T) {
	for _, agentType := range LaunchableAgentTypes() {
		if agentType == at.Terminal {
			continue // a shell, deliberately not an Agent
		}
		t.Run(agentType, func(t *testing.T) {
			impl := GetAgent(agentType)
			if impl == nil {
				t.Fatalf("GetAgent(%q) returned nil", agentType)
			}
			if got := impl.AgentType(); got != agentType {
				t.Fatalf("GetAgent(%q) returned an implementation for %q", agentType, got)
			}
		})
	}
}

// An empty type still means Claude — that is the intended default and is a
// separate case from an unknown type.
func TestGetAgentDefaultsToClaudeOnlyForAnUnspecifiedType(t *testing.T) {
	if got := GetAgent("").AgentType(); got != at.Claude {
		t.Errorf("GetAgent(\"\") = %q, want claude", got)
	}
}

// LaunchableAgentTypes drives both the validation and the error message, so it
// has to stay in step with the constants it is derived from.
func TestLaunchableAgentTypesCoversEveryDeclaredType(t *testing.T) {
	declared := []string{at.Claude, at.Codex, at.Gemini, at.Pi, at.Terminal}
	launchable := LaunchableAgentTypes()
	if len(launchable) != len(declared) {
		t.Fatalf("LaunchableAgentTypes has %d entries, agenttypes declares %d", len(launchable), len(declared))
	}
	for _, d := range declared {
		if ValidateAgentType(d) != nil {
			t.Errorf("declared agent type %q is rejected by ValidateAgentType", d)
		}
	}
}
