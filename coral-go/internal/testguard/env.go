package testguard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// agentShellVars and agentShellPrefixes name what Coral puts in an agent's shell: its
// identity, its links to production (Cloud Hub URL, hub mode, tmux socket) and its
// workflow/step context. 'go test' run from an agent shell would otherwise hand them to
// every test that reads the environment, e.g. a config built from CORAL_HUB. Keep in step
// with tests/lib/agent_env.sh and AGENT_SHELL_VARS in tests/frontend/run_cdp_suites.mjs.
var agentShellVars = []string{
	"CORAL_SESSION_NAME", "CORAL_SESSION_ID", "CORAL_SESSION_TOKEN", "CORAL_SESSION_AUTH_FILE", "CORAL_SUBSCRIBER_ID", "CORAL_CLOUD_URL", "CORAL_HUB", "CORAL_SHELL",
	"CORAL_TMUX_SOCKET", "CORAL_TMUX_FALLBACK", "CORAL_AGENT_HELPER", "CORAL_AGENT_ARGS", "CORAL_BOARD_HELPER", "CORAL_BOARD_ARGS",
}

var agentShellPrefixes = []string{"CORAL_WORKFLOW_", "CORAL_PREV", "CORAL_STEP_"}

// scrubbedMarker is set once the scrub has run, and test binaries started from a test
// (helper re-execs such as the CLI help tests) inherit it, so they keep the CORAL_* values
// their parent test gave them on purpose.
// Not a CORAL_ name: some helper re-execs drop every inherited CORAL_* variable.
const scrubbedMarker = "TESTGUARD_AGENT_ENV_SCRUBBED"

// init clears them from the environment of the test binary that 'go test' starts, before
// any test runs. Shipped binaries are untouched. Tests that need one set it themselves
// (t.Setenv, or a child process's Env).
func init() {
	if !testing.Testing() || os.Getenv(scrubbedMarker) == "1" {
		return
	}
	scrubAgentShellEnv()
	os.Setenv(scrubbedMarker, "1")
}

func scrubAgentShellEnv() {
	for _, k := range agentShellVars {
		os.Unsetenv(k)
	}
	isolateDataDir()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		for _, p := range agentShellPrefixes {
			if strings.HasPrefix(k, p) {
				os.Unsetenv(k)
				break
			}
		}
	}
}

// isolateDataDir keeps a test binary out of the production data folder. Agent
// shells export CORAL_DATA_DIR / CORAL_DIR pointing at ~/.coral, and without
// them Coral falls back to ~/.coral itself, so a test that resolved the data
// folder the way the server or a CLI does read production's files: with the
// command-line token there, `coral auto-accept off` in a test signed with it.
// CORAL_DIR is dropped and CORAL_DATA_DIR points at a per-process folder under
// the temp dir, not created here (a test that needs one sets its own with
// t.Setenv or t.TempDir). HOME is left alone: Go's build cache lives under it,
// and tests that build binaries would start from an empty cache.
func isolateDataDir() {
	os.Unsetenv("CORAL_DIR")
	os.Setenv("CORAL_DATA_DIR", filepath.Join(os.TempDir(), "coral-testguard-data-"+strconv.Itoa(os.Getpid())))
}
