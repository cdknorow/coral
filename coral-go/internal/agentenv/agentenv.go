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
//
// Names are compared without case, on every OS: Windows reads a variable whatever case it was
// set in (Coral_Secret_Key is CORAL_SECRET_KEY there), so a filter that matched the case let a
// server setting spelled another way through to every agent (#19, Security's #20 F1).
//
// Claude Code's own session markers (ClaudeSessionMarkers) are removed too: they belong to the Claude Code session that started the server, not to the agent.
//
// The variables that are not CORAL_ ones pass on purpose, secrets among them (GITHUB_TOKEN,
// AWS_*, ANTHROPIC_API_KEY): the agent CLIs and the tools they run need the user's own keys.
// This filter keeps Coral's server settings away from agents, not the user's environment.
package agentenv

import (
	"os"
	"strconv"
	"strings"
)

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
	SessionAuthFileEnv:         true, // a Codex agent's session token file (#13)
	"CORAL_SHELL":              true, // the user's shell preference
	"CORAL_TELEMETRY_DISABLED": true, // a user opt-out: tools an agent runs honour it too
	"CORAL_DEBUG":              true,
}

// AllowedPrefixes are workflow step context Coral sets for the step it runs.
var AllowedPrefixes = []string{"CORAL_WORKFLOW_", "CORAL_STEP_", "CORAL_PREV"}

// IsCoral reports whether a variable named name is a CORAL_ one, in any case.
func IsCoral(name string) bool {
	return len(name) >= len("CORAL_") && strings.EqualFold(name[:len("CORAL_")], "CORAL_")
}

// ClaudeSessionMarkers are the variables a running Claude Code session sets for the programs it starts: they say "this process was started by that session" (and where to
// reach it), not anything an agent needs. A Coral server started from a Claude Code session (an agent starting a test server, say) used to hand them to every agent it launched,
// and a Claude Code agent that finds them acts as that session's child: QA saw "Transcript saving is off: inherited CLAUDE_CODE_CHILD_SESSION". They are removed.
//
// Kept on purpose: the credential and provider variables (agentsignin's credentialEnv: CLAUDE_CODE_OAUTH_TOKEN, CLAUDE_CODE_USE_BEDROCK/VERTEX/FOUNDRY, ANTHROPIC_*), and every
// setting a person chooses (CLAUDE_CONFIG_DIR, CLAUDE_CODE_MAX_OUTPUT_TOKENS, ...): none is a marker of a parent session.
var ClaudeSessionMarkers = map[string]bool{
	"CLAUDECODE":                   true, // =1 in every process a Claude Code session starts
	"CLAUDE_CODE_CHILD_SESSION":    true,
	"CLAUDE_CODE_ENTRYPOINT":       true, // cli, sdk, ...: how the parent was started
	"CLAUDE_CODE_EXECPATH":         true, // the parent's own executable
	"CLAUDE_CODE_MESSAGING_SOCKET": true, // how to reach the parent
	"CLAUDE_CODE_MESSAGING_TOKEN":  true, // and the token for it: never for another agent
	"CLAUDE_CODE_SESSION_ATTENDED": true,
	"CLAUDE_CODE_SESSION_ID":       true,
	"CLAUDE_CODE_SSE_PORT":         true, // the parent's IDE link
	"CLAUDE_PID":                   true,
}

// Keep reports whether a variable named name may reach an agent.
func Keep(name string) bool {
	if ClaudeSessionMarkers[strings.ToUpper(name)] {
		return false
	}
	if !IsCoral(name) {
		return true
	}
	upper := strings.ToUpper(name)
	if Allowed[upper] {
		return true
	}
	for _, p := range AllowedPrefixes {
		if strings.HasPrefix(upper, p) {
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

// IdentityVars name an agent session's identity. A Coral server is never an agent session,
// so it never keeps them from whatever started it (#3421: a test server started from an
// agent's shell handed that agent's name, role and token to its own agents).
var IdentityVars = []string{"CORAL_SESSION_NAME", "CORAL_SESSION_ID", "CORAL_SUBSCRIBER_ID", "CORAL_SESSION_TOKEN", SessionAuthFileEnv}

// SessionAuthFileEnv names the file a Codex agent's tools read its session token from when
// CORAL_SESSION_TOKEN is missing (#13). Codex hides from the commands it runs every variable
// whose name holds KEY, SECRET or TOKEN (its default excludes, before 0.161), so the token's
// own variable never reached coral-board or the hooks there. This name holds none of those,
// nor PASS or CRED (TestSessionAuthFileEnvPassesCodexExcludes), and the variable is a path,
// not the secret, so a command that prints its environment doesn't print the token.
const SessionAuthFileEnv = "CORAL_SESSION_AUTH_FILE"

// LocationVars tell an agent's tools which Coral to talk to and where its data is.
var LocationVars = []string{"CORAL_PORT", "CORAL_HOST", "CORAL_URL", "CORAL_DIR", "CORAL_DATA_DIR"}

// UseServerEnv makes this process's environment, which every agent, tmux server and
// workflow step inherits (filtered by Filter), say THIS server: it removes inherited
// agent identity and sets the location variables to this server's own host, port and
// data folder (#3421). Call it once at startup, after the port is bound.
func UseServerEnv(host string, port int, dataDir string) {
	for _, k := range IdentityVars {
		os.Unsetenv(k)
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1" // an agent dials a real address, not "every interface"
	}
	set := map[string]string{"CORAL_HOST": host}
	if port > 0 {
		set["CORAL_PORT"] = strconv.Itoa(port)
		h := host
		if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
			h = "[" + h + "]"
		}
		set["CORAL_URL"] = "http://" + h + ":" + strconv.Itoa(port)
	}
	if dataDir != "" {
		set["CORAL_DIR"], set["CORAL_DATA_DIR"] = dataDir, dataDir
	}
	for _, k := range LocationVars {
		if v, ok := set[k]; ok {
			os.Setenv(k, v)
		} else {
			os.Unsetenv(k)
		}
	}
}
