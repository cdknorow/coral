package testguard

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A test binary started from an agent shell must not see that agent's CORAL_* identity
// and production links. The scrub runs at package init, before any test, so the check
// runs in a child test binary that gets them in its starting environment.
func TestAgentShellEnvIsScrubbedAtInit(t *testing.T) {
	if os.Getenv("TESTGUARD_SCRUB_CHILD") == "1" {
		var seen []string
		for _, k := range []string{"CORAL_HUB", "CORAL_SESSION_ID", "CORAL_CLOUD_URL", "CORAL_PREV", "CORAL_STEP_2_OUTPUT", "CORAL_WORKFLOW_RUN_ID"} {
			if v, ok := os.LookupEnv(k); ok {
				seen = append(seen, k+"="+v)
			}
		}
		if len(seen) > 0 {
			t.Fatalf("agent-shell variables reached the test binary: %v", seen)
		}
		if os.Getenv("CORAL_BACKEND") != "tmux" {
			t.Fatalf("a test knob was scrubbed too: CORAL_BACKEND=%q", os.Getenv("CORAL_BACKEND"))
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAgentShellEnvIsScrubbedAtInit$", "-test.count=1")
	// A fresh test binary as 'go test' starts it from an agent shell: without the marker this
	// binary set, so the child scrubs its own environment.
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, scrubbedMarker+"=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "TESTGUARD_SCRUB_CHILD=1",
		"CORAL_HUB=1", "CORAL_SESSION_ID=agent-session", "CORAL_CLOUD_URL=https://example.invalid", "CORAL_PREV=x",
		"CORAL_STEP_2_OUTPUT=y", "CORAL_WORKFLOW_RUN_ID=7", "CORAL_BACKEND=tmux")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child test binary failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("child test binary did not run the check:\n%s", out)
	}
}

// A test binary never resolves Coral's data folder to the one its shell names
// (an agent shell's CORAL_DATA_DIR / CORAL_DIR is production's ~/.coral): the
// scrub drops CORAL_DIR and points CORAL_DATA_DIR at a per-process folder under
// the temp dir.
func TestTestBinaryDataDirIsNeverTheShells(t *testing.T) {
	if os.Getenv("TESTGUARD_DATADIR_CHILD") == "1" {
		if v, ok := os.LookupEnv("CORAL_DIR"); ok {
			t.Fatalf("CORAL_DIR reached the test binary: %q", v)
		}
		got := os.Getenv("CORAL_DATA_DIR")
		if got == "/prod/.coral" || !strings.HasPrefix(got, os.TempDir()) || !strings.Contains(got, "coral-testguard-data-") {
			t.Fatalf("CORAL_DATA_DIR = %q, want a testguard folder under %s", got, os.TempDir())
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTestBinaryDataDirIsNeverTheShells$", "-test.count=1")
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, scrubbedMarker+"=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "TESTGUARD_DATADIR_CHILD=1", "CORAL_DATA_DIR=/prod/.coral", "CORAL_DIR=/prod/.coral")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child test binary failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("child test binary did not run the check:\n%s", out)
	}
}
