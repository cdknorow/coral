package testguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A shared test harness that starts a test Coral with mock agents must use the mock-agent sandbox
// (#4354): an isolated HOME and a stub claude/codex/gemini first on PATH, so a run can never reach
// the operator's real CLI or write to the real ~/.claude. Two leaks happened before the sandbox
// existed (a perf harness started 13 real Claude CLIs; a mock run wrote transcripts into the real
// ~/.claude/projects). This lint fails if a harness script launches the coral server binary and uses
// mock agents but does not reference the sandbox. It keeps bash harnesses and the node CDP runner in
// step with tests/lib/mock_sandbox.sh.
//
// "Starts a test Coral with mock agents" = both of:
//   - it runs the coral SERVER binary (a coral token with --host/--port/--home, not coral-board/-agent/-hook);
//   - it uses the mock agent (the mock-agent binary, or points cli_path_claude at one). A harness that
//     launches REAL agents to exercise a real CLI (tests/integration/agent_permissions) is not a mock
//     run and is out of scope here — it must not get a stub 'claude' that exits 1.
// "Uses the sandbox" = it calls coral_test_sandbox (bash) or carries REAL_CLI_WAS_CALLED (the node stub
// marker in run_cdp_suites.mjs).
//
// Caching note: this reads scripts under tests/ and tools/, which are outside this Go module, so `go
// test` cannot see them change and may serve a cached pass after a harness is edited without touching
// any .go file. Run it fresh (go test -count=1 ./internal/testguard, or on a clean CI checkout) to be
// sure; a failure is never cached. A touch of this file also busts the cache.

var (
	// A coral server launch: the word "coral" (not a coral-* subtool) near a --host/--port/--home flag.
	reCoralServer = regexp.MustCompile(`coral(?:["'\s/]|\b)(?:[^\n]{0,200})(?:--host|--port|--home)`)
	reCoralSubIsh = regexp.MustCompile(`coral-(?:board|agent|hook|tray|coordination|shadow)`)
	reMockAgent   = regexp.MustCompile(`mock-agent|cli_path_claude`)
	reSandbox     = regexp.MustCompile(`coral_test_sandbox|REAL_CLI_WAS_CALLED`)
)

// launchesCoralServer reports whether the content runs the coral server binary (ignoring coral-* subtools).
func launchesCoralServer(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if reCoralSubIsh.MatchString(line) {
			// A coral-* subtool line alone is not a server launch; drop the subtool tokens and re-check.
			line = reCoralSubIsh.ReplaceAllString(line, "")
		}
		if reCoralServer.MatchString(line) {
			return true
		}
	}
	return false
}

func TestHarnessScriptsUseTheMockAgentSandbox(t *testing.T) {
	root := repoRootForLint(t)
	var offenders []string
	for _, sub := range []string{"tests", "tools"} {
		base := filepath.Join(root, sub)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			switch filepath.Ext(p) {
			case ".sh", ".mjs":
			default:
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			content := string(b)
			if !launchesCoralServer(content) || !reMockAgent.MatchString(content) {
				return nil
			}
			if !reSandbox.MatchString(content) {
				rel, _ := filepath.Rel(root, p)
				offenders = append(offenders, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", base, err)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("these harness scripts start a test Coral with mock agents but do not use the #4354 sandbox "+
			"(source tests/lib/agent_env.sh or mock_sandbox.sh and call coral_test_sandbox / coral_test_sandbox_check, "+
			"or the node stub as in run_cdp_suites.mjs):\n  %s", strings.Join(offenders, "\n  "))
	}
}

// repoRootForLint walks up from the test's working directory (coral-go/internal/testguard) to the
// repo root (the dir holding both coral-go/ and tests/).
func repoRootForLint(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for dir := wd; ; {
		if fi, err := os.Stat(filepath.Join(dir, "tests")); err == nil && fi.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, "coral-go")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("repo root (with tests/ and coral-go/) not found from " + wd + "; skipping harness lint")
		}
		dir = parent
	}
}
