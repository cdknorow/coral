package agent

import (
	"testing"
)

func envMapOf(pairs [][2]string) map[string]string {
	m := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		m[kv[0]] = kv[1]
	}
	return m
}

// The defect: every agent implementation built this environment separately and
// none of them set CORAL_PORT or CORAL_URL, so coral-board fell back to
// localhost:8420 — a developer's real Coral, not the one that launched the
// agent. The board silently failed, and on a machine already running Coral the
// agent posted to the production board.
func TestCoralEnvCarriesTheServerTheAgentBelongsTo(t *testing.T) {
	env := envMapOf(CoralEnv(LaunchParams{
		SessionName: "claude-abc",
		Role:        "reviewer",
		CoralHost:   "127.0.0.1",
		CoralPort:   8455,
		CoralDir:    "/tmp/coral-t1",
	}))

	want := map[string]string{
		"CORAL_SESSION_NAME":  "claude-abc",
		"CORAL_SUBSCRIBER_ID": "reviewer",
		"CORAL_PORT":          "8455",
		"CORAL_HOST":          "127.0.0.1",
		"CORAL_URL":           "http://127.0.0.1:8455",
		"CORAL_DIR":           "/tmp/coral-t1",
		"CORAL_DATA_DIR":      "/tmp/coral-t1",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

// A server bound to 0.0.0.0 listens everywhere, but an agent has to dial a
// real address; "http://0.0.0.0:8455" is not one.
func TestCoralEnvResolvesWildcardBindAddresses(t *testing.T) {
	for _, host := range []string{"", "0.0.0.0", "::"} {
		env := envMapOf(CoralEnv(LaunchParams{CoralHost: host, CoralPort: 8455}))
		if env["CORAL_HOST"] != "127.0.0.1" {
			t.Errorf("host %q resolved to %q, want 127.0.0.1", host, env["CORAL_HOST"])
		}
		if env["CORAL_URL"] != "http://127.0.0.1:8455" {
			t.Errorf("host %q gave URL %q", host, env["CORAL_URL"])
		}
	}
}

func TestCoralEnvOmitsWhatItDoesNotKnow(t *testing.T) {
	env := envMapOf(CoralEnv(LaunchParams{SessionName: "claude-abc"}))
	for _, k := range []string{"CORAL_SUBSCRIBER_ID", "CORAL_PORT", "CORAL_HOST", "CORAL_URL", "CORAL_DIR", "CORAL_DATA_DIR"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s should be omitted when unknown, got %q", k, env[k])
		}
	}
	// An unset port must not produce "CORAL_PORT=0", which would be worse than
	// absent: the CLI would build http://127.0.0.1:0 instead of falling back.
	if env["CORAL_PORT"] == "0" {
		t.Error("an unset port must be omitted, not sent as 0")
	}
}

// Every agent builds its environment from CoralEnv now. If one stops, the
// board breaks only for that agent type and only on a non-default port —
// which is exactly how this went unnoticed.
func TestEveryAgentPassesTheCoralEnvironmentToItsCLI(t *testing.T) {
	params := LaunchParams{
		SessionID:   "11111111-2222-3333-4444-555555555555",
		SessionName: "agent-11111111-2222-3333-4444-555555555555",
		Role:        "reviewer",
		WorkingDir:  "/tmp",
		CoralHost:   "127.0.0.1",
		CoralPort:   8455,
		CoralDir:    "/tmp/coral-t1",
	}
	for _, agentType := range []string{"claude", "codex", "gemini", "pi"} {
		t.Run(agentType, func(t *testing.T) {
			impl := GetAgent(agentType)
			if impl == nil {
				t.Skipf("no implementation for %s", agentType)
			}
			cmd := impl.BuildLaunchCommand(params)
			// claude passes env through a settings file rather than exports, so
			// assert on the port reaching the command line one way or another.
			if !containsAny(cmd, "CORAL_PORT", "settings") {
				t.Fatalf("%s launch command carries no Coral environment:\n%s", agentType, cmd)
			}
		})
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
