package tracking

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Telemetry can only reach the real PostHog from a KEYED build: tracking.Enabled() is
// posthogKeyPresent(), and the key is baked only by -X .../internal/config.PostHogKey (release.yml, and
// installers/build-macos.sh by default). A from-source go build/go test binary carries no key and sends
// nothing. So every script or workflow that STARTS a Coral must have CORAL_TELEMETRY_DISABLED in scope,
// so CI (and a local run through it) can never send real events (#4937). This census DISCOVERS the
// Coral-starting runners under tests/ and tools/ rather than listing them, so a new one that forgets the
// opt-out fails here.

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, ".github", "workflows")); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal(".github/workflows not found above the working directory")
		}
		dir = parent
	}
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// startsCoral matches a command that runs a Coral executable or a Coral-starting runner. It is applied
// per non-comment line, so a reference in a comment or a string does not count.
var startsCoral = regexp.MustCompile(`coral-tray|coral-board|coral-agent|launch-coral|(\./|/|\$[A-Za-z_{][A-Za-z_}]*/)coral([ "']|$)|--encryption-self-test|run_cdp_suites|operator_cli_smoke|smoke_standard|smoke_windows|run_agent_tasks|go run \./cmd/coral`)

// notAStarter are files that match startsCoral but do not start a Coral that could send: each is here
// with the reason. The census checks every OTHER matching file; a new starter is not allowlisted, so it
// must carry the opt-out. If one of these starts sending, remove it here and it will be required to set
// the flag.
var notAStarter = map[string]string{
	// Build / generate / download tools: they build or fetch, they do not run Coral.
	"tools/build_windows.ps1":          "builds the Windows binaries",
	"tools/gen_route_queries.py":       "code generator",
	"tools/gen_windows_resources.sh":   "resource generator",
	"tools/make_windows_icon.py":       "icon generator",
	"tools/devops/promote_manifest.py": "writes promote.json from verified inputs (#326); the repository name is only the manifest's repo field, no Coral starts",
	// Inspect the bundle's files; do not execute Coral (file/otool/readelf, or a self-test on a
	// from-source, keyless build so it cannot send).
	"tests/release/verify_standard_bundle.sh":    "inspects the package (file, lists expected binaries); does not run Coral",
	"tests/release/verify_encrypted_bundle.sh":   "runs the from-source (keyless) bundle's --encryption-self-test, which carries no PostHog key and sends nothing",
	"tests/release/verify_encrypted_bundle.ps1":  "Windows counterpart: runs the from-source (keyless) bundle's --encryption-self-test, which carries no PostHog key and sends nothing",
	"tests/release/resolve_release_test.sh":      "unit-tests resolve_release.sh; no Coral",
	"tests/release/gate_check_test.py":           "unit-tests the required-check logic with fake run lists; the repository name subgentic/coral is only a string, no Coral starts",
	"tests/release/release_provenance_test.py":   "unit-tests the release provenance record with fake run lists; the repository name subgentic/coral is only a string, no Coral starts",
	"tests/release/verify_buildinfo_test.py":     "unit-tests verify_buildinfo.sh with fake build-info texts; subgentic/coral and coral-board are only strings in them, no Coral starts",
	"tests/release/windows_build_census_test.py": "reads windows-tests.yml and release.yml as text (their build lines name coral-tray); no Coral starts",
	// Sourced by run_agent_tasks.sh / run.sh (the parent exports the opt-out); not a top-level runner.
	"tests/stress/agent_task_assignment.sh":   "sourced by run_agent_tasks.sh",
	"tests/stress/agent_task_workflows.sh":    "sourced by run_agent_tasks.sh",
	"tests/stress/personal_task_workflows.sh": "sourced by run_agent_tasks.sh",
	// Load generators and node helpers that reference run_cdp_suites/coral in strings but do not start one.
	"tests/perf/load.py":                               "drives an already-running server; does not start Coral",
	"tests/frontend/remote_browse.test.mjs":            "a node unit test; does not start a server",
	"tests/frontend/shared_images.test.mjs":            "a node unit test of the page's pasted-image text; names coral-agent in a string, starts nothing",
	"tests/frontend/team_artifacts_copy_link.test.mjs": "a node unit test; does not start a server",
	"tests/frontend/run_runner_folder_loss.mjs":        "a node helper; names run_cdp_suites in text",
}

// runnerExts are the file types the runner census reads under tests/ and tools/.
var runnerExts = map[string]bool{".sh": true, ".mjs": true, ".py": true, ".ps1": true, ".bat": true, ".cmd": true}

// disableAssign is an assignment of CORAL_TELEMETRY_DISABLED to a true value in any of the forms the
// runners use: export X=1, X=1 cmd, $env:X = "1", process.env.X = '1', os.environ["X"] = "1", X: "1".
var disableAssign = regexp.MustCompile(`(?i)CORAL_TELEMETRY_DISABLED["'\]]*\s*[:=]\s*["']?(1|true|yes|on)\b`)

// codeLines is the file without its comment-only and blank lines, so a flag named in a comment (or
// assigned =0) does not satisfy the census.
func codeLines(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "REM ") || strings.HasPrefix(strings.ToLower(t), "rem ") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// setsDisable reports whether a non-comment line assigns CORAL_TELEMETRY_DISABLED a true value.
func setsDisable(content string) bool {
	for _, line := range codeLines(content) {
		if disableAssign.MatchString(line) {
			return true
		}
	}
	return false
}

func TestCIRunnersThatStartCoralDisableTelemetry(t *testing.T) {
	root := repoRoot(t)
	var offenders, starters []string
	matchedNotAStarter := map[string]bool{}
	for _, base := range []string{"tests", "tools"} {
		_ = filepath.WalkDir(filepath.Join(root, base), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !runnerExts[filepath.Ext(p)] {
				return nil
			}
			rel := filepath.ToSlash(mustRel(root, p))
			b, err := os.ReadFile(p)
			if err != nil {
				t.Errorf("%s: %v", rel, err)
				return nil
			}
			if !startsCoralLine(string(b)) {
				return nil
			}
			if _, ok := notAStarter[rel]; ok {
				matchedNotAStarter[rel] = true
				return nil
			}
			starters = append(starters, rel)
			if !setsDisable(string(b)) {
				offenders = append(offenders, rel)
			}
			return nil
		})
	}
	for _, o := range offenders {
		t.Errorf("%s starts a Coral but does not assign CORAL_TELEMETRY_DISABLED a true value on a non-comment line (add it, or if it does not start a sender, add it to notAStarter with a reason)", o)
	}

	// A floor, so a changed regex or moved directory cannot make discovery find nothing and pass.
	under := func(prefix string) (n int) {
		for _, s := range starters {
			if strings.HasPrefix(s, prefix) {
				n++
			}
		}
		return n
	}
	if len(starters) < minStarters || under("tests/stress/") < minStressStarters || under("tests/release/") < minReleaseStarters {
		t.Errorf("discovery found %d Coral-starting runners (%d in tests/stress, %d in tests/release); expected at least %d, %d, %d: the census may have gone blind (a changed regex or a moved directory)",
			len(starters), under("tests/stress/"), under("tests/release/"), minStarters, minStressStarters, minReleaseStarters)
	}

	// notAStarter must not rot into a blanket allowlist: every entry still exists and still matches.
	for rel := range notAStarter {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("notAStarter lists %s, which no longer exists: remove the entry", rel)
		} else if !matchedNotAStarter[rel] {
			t.Errorf("notAStarter lists %s, which no longer matches startsCoral: remove the entry", rel)
		}
	}
}

// Floors for the discovery census, set below today's counts so ordinary churn does not trip them.
const (
	minStarters        = 15
	minStressStarters  = 5
	minReleaseStarters = 3
)

// startsCoralLine reports whether any non-comment line runs a Coral.
func startsCoralLine(content string) bool {
	for _, line := range codeLines(content) {
		if startsCoral.MatchString(line) {
			return true
		}
	}
	return false
}

func mustRel(root, p string) string { r, _ := filepath.Rel(root, p); return r }

// noCoralWorkflows are the workflows that run no Coral binary and bake no key (docs, formatting, vulnerability
// and audit scans, the SQLCipher amalgamation, the hub impact check, and the keyless from-source encryption
// verify). Every OTHER workflow must set the opt-out, so a new workflow that runs a published or keyed Coral
// is caught without being listed. Add a workflow here only when it runs no Coral.
var noCoralWorkflows = map[string]string{
	"docs.yml":                   "docs only",
	"gofmt.yml":                  "formatting check",
	"govulncheck.yml":            "vulnerability scan",
	"frontend-audit.yml":         "npm audit",
	"sqlcipher-amalgamation.yml": "builds the SQLCipher amalgamation",
	"hub-impact.yml":             "reports hub impact; runs no Coral",
	"encryption-verify.yml":      "verifies a from-source (keyless) bundle",
}

var keyRef = regexp.MustCompile(`POSTHOG_PROJECT_KEY|config\.PostHogKey`)

type ciWorkflow struct {
	Env  map[string]string `yaml:"env"`
	Jobs map[string]struct {
		Env map[string]string `yaml:"env"`
	} `yaml:"jobs"`
}

func TestCIWorkflowsDisableTelemetry(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
			continue
		}
		seen[name] = true
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if _, ok := noCoralWorkflows[name]; ok {
			if keyRef.Match(raw) {
				t.Errorf("%s is in noCoralWorkflows but bakes the PostHog key: remove it from the list and set CORAL_TELEMETRY_DISABLED", name)
			}
			continue
		}
		checked++
		var wf ciWorkflow
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Errorf("%s: parse: %v", name, err)
			continue
		}
		ok := truthy(wf.Env["CORAL_TELEMETRY_DISABLED"])
		if !ok && len(wf.Jobs) > 0 {
			ok = true
			for _, j := range wf.Jobs {
				if !truthy(j.Env["CORAL_TELEMETRY_DISABLED"]) {
					ok = false
					break
				}
			}
		}
		if !ok {
			t.Errorf("%s is not in noCoralWorkflows and does not set CORAL_TELEMETRY_DISABLED=1 at its top-level env (nor in every job); CI could send real events", name)
		}
	}
	for name := range noCoralWorkflows {
		if !seen[name] {
			t.Errorf("noCoralWorkflows lists %s, which no longer exists: remove the entry", name)
		}
	}
	if checked < 8 {
		t.Errorf("only %d workflows were checked; expected at least 8: the census may have gone blind", checked)
	}
}
