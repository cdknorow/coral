// Package agentenv builds the environment Coral gives agents and the commands it runs for
// them (#3389).
//
// Coral used to hand every agent its own whole process environment, so whatever configured
// the server reached every agent: CORAL_SECRET_KEY (the key that seals the Coral Hub token),
// CORAL_CLOUD_URL (an agent that starts a test server then talked to the real Coral Hub),
// TLS key paths, the input debug log switch and so on. The policy is explicit:
//
//   - every variable that is not a CORAL_ one passes through (PATH, HOME, the agent CLI's own
//     API keys and settings);
//   - a CORAL_ variable passes only if it is on Allowed, the ones agent tools read: where the
//     server and the board are (CORAL_URL/HOST/PORT, CORAL_DATA_DIR/DIR), the agent's own
//     identity (CORAL_SESSION_NAME/ID, CORAL_SUBSCRIBER_ID) and a few preferences;
//   - every other CORAL_ variable is the server's own and is removed, including any added
//     later: a new server setting is private until it is added to Allowed on purpose.
//
// Workflow step variables (CORAL_WORKFLOW_*, CORAL_STEP_*, CORAL_PREV*) are allowed: Coral
// sets them for the step it runs.
package agentenv

import "strings"

// Allowed are the CORAL_ variables an agent may inherit.
var Allowed = map[string]bool{
	"CORAL_URL":                true, // coral-board / coral-agent: the server to talk to
	"CORAL_HOST":               true,
	"CORAL_PORT":               true,
	"CORAL_DATA_DIR":           true, // the board database (coral-board, the message-check hook)
	"CORAL_DIR":                true,
	"CORAL_SESSION_NAME":       true, // the agent's identity, set by Coral for each agent
	"CORAL_SESSION_ID":         true,
	"CORAL_SUBSCRIBER_ID":      true,
	"CORAL_SHELL":              true, // the user's shell preference
	"CORAL_TELEMETRY_DISABLED": true, // a user opt-out: tools an agent runs honour it too
	"CORAL_DEBUG":              true,
}

// AllowedPrefixes are workflow step context Coral sets for the step it runs.
var AllowedPrefixes = []string{"CORAL_WORKFLOW_", "CORAL_STEP_", "CORAL_PREV"}

// Keep reports whether a variable named name may reach an agent.
func Keep(name string) bool {
	if !strings.HasPrefix(name, "CORAL_") {
		return true
	}
	if Allowed[name] {
		return true
	}
	for _, p := range AllowedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Filter returns env ("NAME=value" entries) without the variables an agent may not have.
// The result is a new slice; env is not changed.
func Filter(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if Keep(name) {
			out = append(out, kv)
		}
	}
	return out
}
