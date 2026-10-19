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
	"tools/build_macos.sh":             "builds the macOS app (does not run it)",
	"tools/build_windows.ps1":          "builds the Windows binaries",
	"tools/gen_route_queries.py":       "code generator",
	"tools/gen_windows_resources.sh":   "resource generator",
	"tools/make_windows_icon.py":       "icon generator",
	"tools/devops/download-release.sh": "downloads release assets, does not run them",
	// Inspect the bundle's files; do not execute Coral (file/otool/readelf, or a self-test on a
	// from-source, keyless build so it cannot send).
	"tests/release/verify_standard_bundle.sh":   "inspects the package (file, lists expected binaries); does not run Coral",
	"tests/release/verify_encrypted_bundle.sh":  "runs the from-source (keyless) bundle's --encryption-self-test, which carries no PostHog key and sends nothing",
	"tests/release/verify_encrypted_bundle.ps1": "Windows counterpart: runs the from-source (keyless) bundle's --encryption-self-test, which carries no PostHog key and sends nothing",
	"tests/release/resolve_release_test.sh":     "unit-tests resolve_release.sh; no Coral",
	// Sourced by run_agent_tasks.sh / run.sh (the parent exports the opt-out); not a top-level runner.
	"tests/stress/agent_task_assignment.sh":   "sourced by run_agent_tasks.sh",
	"tests/stress/agent_task_workflows.sh":    "sourced by run_agent_tasks.sh",
	"tests/stress/personal_task_workflows.sh": "sourced by run_agent_tasks.sh",
	"tests/lib/agent_env.sh":                  "a sourced helper, not a runner",
	// Load generators and node helpers that reference run_cdp_suites/coral in strings but do not start one.
	"tests/perf/load.py":                               "drives an already-running server; does not start Coral",
	"tests/perf/scale_load.py":                         "drives an already-running server; does not start Coral",
	"tests/frontend/failing_output.mjs":                "formats runner output; names run_cdp_suites in text",
	"tests/frontend/suites.mjs":                        "classifies suites; names run_cdp_suites in text",
	"tests/frontend/update_suite_durations.mjs":        "writes suite_durations.json; names run_cdp_suites in text",
	"tests/frontend/remote_browse.test.mjs":            "a node unit test; does not start a server",
	"tests/frontend/team_artifacts_copy_link.test.mjs": "a node unit test; does not start a server",
	"tests/frontend/run_runner_folder_loss.mjs":        "a node helper; names run_cdp_suites in text",
}

func TestCIRunnersThatStartCoralDisableTelemetry(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	for _, base := range []string{"tests", "tools"} {
		_ = filepath.WalkDir(filepath.Join(root, base), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			switch filepath.Ext(p) {
			case ".sh", ".mjs", ".py", ".ps1":
			default:
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
				return nil
			}
			if !strings.Contains(string(b), "CORAL_TELEMETRY_DISABLED") {
				offenders = append(offenders, rel)
			}
			return nil
		})
	}
	for _, o := range offenders {
		t.Errorf("%s starts a Coral but does not set/export CORAL_TELEMETRY_DISABLED (add it, or if it does not start a sender, add it to notAStarter with a reason)", o)
	}
}

// startsCoralLine reports whether any non-comment line runs a Coral.
func startsCoralLine(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") {
			continue
		}
		if startsCoral.MatchString(line) {
			return true
		}
	}
	return false
}

func mustRel(root, p string) string { r, _ := filepath.Rel(root, p); return r }

// artifactRunnerWorkflows run a published or keyed Coral binary (the release build, or the published
// runner warmed/verified). Each must set the opt-out at its top-level env. Add a workflow here if it
// starts running a release/published Coral.
var artifactRunnerWorkflows = map[string]bool{
	"release.yml":                 true,
	"standard-runtime-verify.yml": true,
	"windows-runner-dryrun.yml":   true,
	"linux-runner-dryrun.yml":     true,
	"release-cache-warm.yml":      true,
}

var keyRef = regexp.MustCompile(`POSTHOG_PROJECT_KEY|config\.PostHogKey`)

type ciWorkflow struct {
	Env  map[string]string `yaml:"env"`
	Jobs map[string]struct {
		Env map[string]string `yaml:"env"`
	} `yaml:"jobs"`
}

func TestCIWorkflowsWithAKeyedCoralDisableTelemetry(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !keyRef.Match(raw) && !artifactRunnerWorkflows[name] {
			continue // a from-source build carries no key and sends nothing
		}
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
			t.Errorf("%s builds or runs a keyed Coral but does not set CORAL_TELEMETRY_DISABLED=1 at its top-level env (nor in every job); CI could send real events", name)
		}
	}
}
