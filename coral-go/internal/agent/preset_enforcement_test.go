package agent

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var updatePresetGolden = flag.Bool("update", false, "rewrite testdata/preset_enforcement.golden")

// The presets the dashboard sends are agent_presets.js's (#4545, Security's C: the Go Presets map no launch
// path read disagreed with them and is gone). This is what each one makes each agent CLI enforce, through
// the translation its launch uses: Claude a pre-approval list and deny rules, Codex a sandbox and an approval
// mode, Agy (and "gemini", which launches agy) a mode. The dashboard's per-agent wording is checked against
// this table (tests/frontend preset_enforcement.test.mjs), so it says what is enforced and no more.
const presetGolden = "testdata/preset_enforcement.golden"

type uiPreset struct {
	Name  string
	Caps  *Capabilities
	Fixed bool // the preset names its own agent type (Terminal)
}

var (
	presetBlockRe = regexp.MustCompile(`(?s)\{\s*name:\s*"([^"]+)",.*?capabilities:\s*\{([^}]*)\}`)
	capListRe     = regexp.MustCompile(`(allow|deny):\s*\[([^\]]*)\]`)
	quotedRe      = regexp.MustCompile(`['"]([^'"]+)['"]`)
)

func readUIPresets(t *testing.T) []uiPreset {
	t.Helper()
	src, err := os.ReadFile("../../pkg/frontend/static/agent_presets.js")
	if err != nil {
		t.Fatal(err)
	}
	var out []uiPreset
	blocks := presetBlockRe.FindAllStringSubmatchIndex(string(src), -1)
	for _, m := range blocks {
		name, body := string(src[m[2]:m[3]]), string(src[m[4]:m[5]])
		caps := &Capabilities{}
		for _, l := range capListRe.FindAllStringSubmatch(body, -1) {
			var items []string
			for _, q := range quotedRe.FindAllStringSubmatch(l[2], -1) {
				items = append(items, q[1])
			}
			if l[1] == "allow" {
				caps.Allow = items
			} else {
				caps.Deny = items
			}
		}
		out = append(out, uiPreset{Name: name, Caps: caps, Fixed: strings.Contains(string(src[m[0]:m[1]]), "agent_type:")})
	}
	if n := strings.Count(string(src), "capabilities:"); len(out) != n {
		t.Fatalf("read %d presets from agent_presets.js, it has %d", len(out), n)
	}
	return out
}

// codexEnforced is the sandbox and approval mode a Codex launch gets, as codex.go emits them.
func codexEnforced(caps *Capabilities) string {
	p := TranslateToCodexPermissions(caps)
	if p == nil {
		return "sandbox=default approval=default"
	}
	var s string
	switch {
	case p.FullAuto:
		s = "sandbox=workspace-write approval=never"
	default:
		s = fmt.Sprintf("sandbox=%s approval=%s", p.SandboxMode, normalizeCodexApprovalPolicy(p.ApprovalPolicy))
	}
	if p.Search {
		s += " search"
	}
	return s
}

// agyEnforced is the mode an Antigravity launch gets, as agy.go emits it.
func agyEnforced(caps *Capabilities) string {
	p := TranslateToAgyPermissions(caps)
	switch {
	case p == nil:
		return "mode=default"
	case p.DangerouslySkipPermissions:
		return "mode=skip-permissions"
	case p.Mode != "":
		return "mode=" + p.Mode
	}
	return "mode=default"
}

// claudeEnforced is the settings Claude gets: pre-approved tools and deny rules.
func claudeEnforced(caps *Capabilities) string {
	p := TranslateToClaudePermissions(caps)
	if p == nil {
		return "allow=- deny=-"
	}
	list := func(l []string) string {
		if len(l) == 0 {
			return "-"
		}
		l = append([]string(nil), l...)
		sort.Strings(l)
		return strings.Join(l, ",")
	}
	return "allow=" + list(p.Allow) + " deny=" + list(p.Deny)
}

func TestPresetEnforcementTable(t *testing.T) {
	var b strings.Builder
	b.WriteString("# preset<TAB>agent<TAB>what the CLI enforces (go test ./internal/agent -run TestPresetEnforcementTable -update)\n")
	for _, p := range readUIPresets(t) {
		if p.Fixed {
			continue
		}
		fmt.Fprintf(&b, "%s\tclaude\t%s\n", p.Name, claudeEnforced(p.Caps))
		fmt.Fprintf(&b, "%s\tcodex\t%s\n", p.Name, codexEnforced(p.Caps))
		fmt.Fprintf(&b, "%s\tagy\t%s\n", p.Name, agyEnforced(p.Caps))
	}
	if *updatePresetGolden {
		if err := os.WriteFile(presetGolden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(presetGolden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if string(want) != b.String() {
		t.Errorf("a preset or a translation changed what a CLI enforces: review, then rerun with -update\n--- golden\n%s\n--- now\n%s", want, b.String())
	}
}

// The rows Security's #4550 audit read from the real launch commands, pinned here too.
func TestPresetEnforcementMatchesTheAudit(t *testing.T) {
	byName := map[string]*Capabilities{}
	for _, p := range readUIPresets(t) {
		byName[p.Name] = p.Caps
	}
	for _, c := range []struct{ preset, codex, agy string }{
		{"Security Reviewer", "sandbox=read-only approval=on-request search", "mode=plan"},
		{"Frontend Dev", "sandbox=workspace-write approval=on-request search", "mode=accept-edits"},
		{"QA Engineer", "sandbox=workspace-write approval=never", "mode=skip-permissions"},
		{"Lead Developer", "sandbox=workspace-write approval=never", "mode=skip-permissions"},
		{"Orchestrator", "sandbox=workspace-write approval=never search", "mode=skip-permissions"},
	} {
		caps := byName[c.preset]
		if caps == nil {
			t.Fatalf("no preset %q", c.preset)
		}
		if got := codexEnforced(caps); got != c.codex {
			t.Errorf("%s codex: %s, the audit read %s", c.preset, got, c.codex)
		}
		if got := agyEnforced(caps); got != c.agy {
			t.Errorf("%s agy: %s, the audit read %s", c.preset, got, c.agy)
		}
	}
}

// team-config.md's preset table names, for each built-in preset, the flags the real Codex and Antigravity
// launch commands carry (#4545): each row's flags are in the command that preset launches, and every preset
// has a row.
func TestTeamConfigDocPresetTableMatchesTheLaunch(t *testing.T) {
	doc, err := os.ReadFile("../../agent_docs/team-config.md")
	if err != nil {
		t.Fatal(err)
	}
	rowRe := regexp.MustCompile("(?m)^\\| ([^|`]+?) \\| `([^`]+)` \\| `([^`]+)` \\|$")
	rows := map[string][2]string{}
	// A Windows checkout gives the doc CRLF line endings, and (?m)$ matches only before \n.
	for _, m := range rowRe.FindAllStringSubmatch(strings.ReplaceAll(string(doc), "\r\n", "\n"), -1) {
		rows[m[1]] = [2]string{m[2], m[3]}
	}
	for _, p := range readUIPresets(t) {
		if p.Fixed {
			continue
		}
		row, ok := rows[p.Name]
		if !ok {
			t.Errorf("team-config.md has no row for the %q preset", p.Name)
			continue
		}
		codex := buildLaunch(&CodexAgent{}, LaunchParams{Capabilities: p.Caps})
		agy := buildLaunch(&AgyAgent{}, LaunchParams{Capabilities: p.Caps})
		if !strings.Contains(codex, " "+row[0]) {
			t.Errorf("%s: the doc says Codex gets %q; its launch is %q", p.Name, row[0], codex)
		}
		if !strings.Contains(agy, " "+row[1]) {
			t.Errorf("%s: the doc says Antigravity gets %q; its launch is %q", p.Name, row[1], agy)
		}
		for _, other := range []string{"--dangerously-bypass-approvals-and-sandbox", "--search"} {
			if strings.Contains(codex, other) != strings.Contains(row[0], other) {
				t.Errorf("%s: %s differs between the doc and Codex's launch %q", p.Name, other, codex)
			}
		}
	}
}
