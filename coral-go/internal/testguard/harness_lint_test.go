package testguard

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A shared test harness that starts a test Coral server must use the mock-agent sandbox (#4354): an
// isolated HOME and a stub for every CLI-backed agent type first on PATH, so a run can never reach the
// operator's real CLI or write to the real ~/.claude. Two leaks happened before the sandbox existed (a
// perf harness started real Claude CLIs; a mock run wrote transcripts into the real ~/.claude/projects).
//
// This lint is INVERTED from the first version (#4354, Security's SHOULD): it flags ANY script under
// tests/ or tools/ that STARTS THE CORAL SERVER but does not reference the sandbox -- regardless of
// whether it names a mock agent -- unless the script is on the allowlist below, each with a one-line
// reason. The old form only checked a script that both launched coral AND named mock-agent/cli_path,
// so a harness that started coral and agents without naming a mock slipped through; and its "coral"
// match was case-sensitive, so a launch through $CORAL_BIN was invisible. A new harness now fails this
// lint until its author either wires the sandbox or adds an allowlist entry with a reason.
//
// "Uses the sandbox" = it references coral_test_sandbox (bash) or REAL_CLI_WAS_CALLED (the node stub
// marker in run_cdp_suites.mjs).
//
// Caching note: this reads scripts under tests/ and tools/, outside this Go module, so `go test` may
// serve a cached pass after a harness is edited without touching any .go file. Run it fresh (go test
// -count=1 ./internal/testguard) to be sure; a failure is never cached. A touch of this file busts it.

var (
	// A reference to the coral SERVER executable, anywhere in the file (case-insensitive): building or
	// running ./cmd/coral, $CORAL_BIN / $BIN/coral, a path that ends in /coral used as a binary, or a
	// bare "coral" argv entry. A server run through a variable (BINARY="$DIR/coral"; "$BINARY" --port)
	// is caught because the assignment matches the /coral path here.
	reCoralExe = regexp.MustCompile(`(?i)(?:\$\{?coral_bin\}?|\$\{?bin\}?/coral\b|go\s+(?:build|run)\b[^\n]*\./cmd/coral\b|[="'\x60]\S*/coral["'\x60\s]|[/"' \x60]coral["' \x60])`)
	// A coral server-run flag on a line (these flags belong to the coral server, not to other commands).
	reServerFlags = regexp.MustCompile(`--host\b|--port\b|--home\b|--backend\b|--cloud-url\b|--no-browser\b`)
	// coral-<subtool> tokens are not the server; they are stripped from a line before the flag match.
	reCoralSubIsh = regexp.MustCompile(`(?i)coral-(?:board|agent|hook[a-z-]*|tray|coordination|shadow|app|command)`)
	reSandbox     = regexp.MustCompile(`coral_test_sandbox|REAL_CLI_WAS_CALLED`)
)

// allowlistReasons: a script that starts the coral server but legitimately must NOT use the sandbox.
// The key is the path relative to the repo root; the value is the reason (shown if the entry is stale).
var allowlistReasons = map[string]string{
	"tests/release/operator_cli_smoke.sh":            "an env -i smoke of the CLI with no agents; the stub PATH and isolated HOME would defeat its clean-environment purpose",
	"tests/release/smoke_linux.sh":                   "runs the release tarball's server in an env -i with only system dirs on PATH and cli_path_claude set to the stand-in CLI (smoke_macos/standin_cli.py) before any launch; no real agent CLI can start, and the sandbox's stub PATH would defeat the point of testing the shipped binaries",
	"tests/release/smoke_macos.sh":                   "runs the released app's server in an env -i with only system dirs on PATH and cli_path_claude set to its own stand-in CLI (smoke_macos/standin_cli.py) before any launch; no real agent CLI can start, and the sandbox's stub PATH would defeat the point of testing the released binaries",
	"tools/test-macos-app.sh":                        "launches the built macOS app (coral-tray) on purpose, in a data folder and HOME of its own (#4960); no agents, so nothing for a stub CLI to guard",
	"tests/integration/agent_permissions/harness.sh": "launches REAL agent CLIs on purpose to exercise a real CLI; a stub that exits 1 would break it",
	"tools/dev-screenshot.sh":                        "developer screenshot helper, not a test harness: a throwaway /tmp data dir, no agents, CORAL_NO_DEMO_SEED set so nothing is launched",
}

// launchesCoralServer reports whether the content starts the coral server binary: it must both
// reference the coral server executable and run something with a coral server flag (after stripping
// any coral-* subtool tokens from that line, which take flags of their own).
func launchesCoralServer(content string) bool {
	if !reCoralExe.MatchString(content) {
		return false
	}
	for _, line := range strings.Split(content, "\n") {
		stripped := reCoralSubIsh.ReplaceAllString(line, "")
		if reServerFlags.MatchString(stripped) {
			return true
		}
	}
	return false
}

func TestHarnessScriptsUseTheMockAgentSandbox(t *testing.T) {
	root := repoRootForLint(t)
	var offenders []string
	seenAllowed := map[string]bool{}
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
			if !launchesCoralServer(string(b)) {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if _, ok := allowlistReasons[rel]; ok {
				seenAllowed[rel] = true
				return nil // allowlisted: a reason is recorded above
			}
			if !reSandbox.MatchString(string(b)) {
				offenders = append(offenders, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", base, err)
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("these scripts start a test Coral server but do not use the #4354 sandbox and are not "+
			"allowlisted. Either wire the sandbox (source tests/lib/agent_env.sh or mock_sandbox.sh and call "+
			"coral_test_sandbox / coral_test_sandbox_check, or the node stub as in run_cdp_suites.mjs), or add "+
			"the script to allowlistReasons in this test with a one-line reason:\n  %s", strings.Join(offenders, "\n  "))
	}
	// A stale allowlist entry (the file is gone, or no longer launches a server) should be removed so the
	// list keeps meaning. Only flag entries whose file exists but was not seen as a launcher; a missing
	// file (e.g. the agent_permissions scripts may be named differently) is not failed here.
	for rel := range allowlistReasons {
		if seenAllowed[rel] {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			t.Errorf("allowlist entry %q exists but no longer looks like a coral-server launch; remove it from allowlistReasons", rel)
		}
	}
}

// TestMockSandboxStubsEveryAgentCLI keeps CORAL_TEST_STUB_CLIS in tests/lib/mock_sandbox.sh in step
// with the agent types Coral can launch (#4354 INFO 2): every agenttypes constant except "terminal"
// (a shell, not a CLI) and every binary agentCLIs resolves them to must be stubbed, so a new agent
// type or a changed launch binary fails this test until the stub list is updated.
func TestMockSandboxStubsEveryAgentCLI(t *testing.T) {
	root := repoRootForLint(t)
	stub := readStubCLIs(t, root)   // CORAL_TEST_STUB_CLIS in tests/lib/mock_sandbox.sh
	cdp := readCDPCLITypes(t, root) // CLI_TYPES in tests/frontend/run_cdp_suites.mjs

	want := map[string]string{} // name -> where it came from, for the failure message
	for _, v := range constValues(t, filepath.Join(root, "coral-go/internal/agenttypes/types.go")) {
		if v == "terminal" {
			continue // a plain shell, not a CLI binary looked up on PATH
		}
		want[v] = "agenttypes type"
	}
	for _, v := range binaryValues(t, filepath.Join(root, "coral-go/internal/agent/agent.go")) {
		want[v] = "agentCLIs binary"
	}

	// Both harness lists must cover every agent CLI, so a seventh agent type can't be missed in one of
	// the three lists (mock_sandbox.sh, run_cdp_suites.mjs, agenttypes/agentCLIs). #4354.
	for _, l := range []struct {
		name string
		set  map[string]bool
	}{
		{"CORAL_TEST_STUB_CLIS in tests/lib/mock_sandbox.sh", stub},
		{"CLI_TYPES in tests/frontend/run_cdp_suites.mjs", cdp},
	} {
		var missing []string
		for name, from := range want {
			if !l.set[name] {
				missing = append(missing, name+" ("+from+")")
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s does not include every agent CLI; add:\n  %s\n(current list: %s)",
				l.name, strings.Join(missing, "\n  "), stubKeys(l.set))
		}
	}
	// And the two harness lists must match each other exactly, so one never quietly drifts from the other.
	if a, b := stubKeys(stub), stubKeys(cdp); a != b {
		t.Errorf("the two harness CLI lists differ (keep them identical):\n"+
			"  mock_sandbox.sh CORAL_TEST_STUB_CLIS: %s\n  run_cdp_suites.mjs CLI_TYPES:         %s", a, b)
	}
}

var (
	// The defaults are the literal names before any ${...} expansion in the assignment.
	reStubList    = regexp.MustCompile(`CORAL_TEST_STUB_CLIS="([^"$]*)`)
	reConstString = regexp.MustCompile(`^\s*[A-Za-z0-9_]+\s*=\s*"([a-z0-9]+)"`)
	reBinaryField = regexp.MustCompile(`Binary:\s*"([a-z0-9]+)"`)
	reCDPCLIList  = regexp.MustCompile(`CLI_TYPES\s*=\s*\[([^\]]*)\]`)
	reCDPName     = regexp.MustCompile(`'([a-z0-9]+)'`)
)

func readStubCLIs(t *testing.T, root string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "tests/lib/mock_sandbox.sh"))
	if err != nil {
		t.Fatalf("reading mock_sandbox.sh: %v", err)
	}
	m := reStubList.FindSubmatch(b)
	if m == nil {
		t.Fatalf("could not find the CORAL_TEST_STUB_CLIS default list in mock_sandbox.sh")
	}
	out := map[string]bool{}
	for _, f := range strings.Fields(string(m[1])) {
		out[f] = true
	}
	return out
}

// readCDPCLITypes reads the CLI_TYPES array in tests/frontend/run_cdp_suites.mjs (the agent types each
// CDP test server stubs and points cli_path at).
func readCDPCLITypes(t *testing.T, root string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "tests/frontend/run_cdp_suites.mjs"))
	if err != nil {
		t.Fatalf("reading run_cdp_suites.mjs: %v", err)
	}
	m := reCDPCLIList.FindSubmatch(b)
	if m == nil {
		t.Fatalf("could not find the CLI_TYPES array in run_cdp_suites.mjs")
	}
	out := map[string]bool{}
	for _, q := range reCDPName.FindAllSubmatch(m[1], -1) {
		out[string(q[1])] = true
	}
	if len(out) == 0 {
		t.Fatalf("CLI_TYPES in run_cdp_suites.mjs is empty")
	}
	return out
}

func constValues(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if m := reConstString.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		t.Fatalf("found no agent-type constants in %s", path)
	}
	return out
}

func binaryValues(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out []string
	for _, m := range reBinaryField.FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("found no agentCLIs Binary values in %s", path)
	}
	return out
}

func stubKeys(m map[string]bool) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, " ")
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
