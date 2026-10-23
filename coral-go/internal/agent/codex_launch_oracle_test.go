package agent

import (
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/transcriptlink"
)

// codexTempFile is a temporary file a Codex start command names (its instructions and prompt files).
var codexTempFile = regexp.MustCompile(`[^\s'"$()]*codex_(instructions|prompt)_[^\s'"$()]*`)

// The outcomes the Codex launch oracle must reach, each counted from the command built, with a floor.
var codexLaunchOutcomes = map[string]int{
	"a resume":                        20,
	"developer instructions":          100,
	"an action prompt":                100,
	"bypass from an explicit mode":    3,
	"full auto from capabilities":     10,
	"sandbox and approval from caps":  10,
	"web search":                      10,
	"permission mode from a flag":     20,
	"permission mode from the launch": 20,
	"untrusted normalized":            10,
	"a Claude-only flag dropped":      20,
	"a user flag passed through":      40,
	"a skip flag after a permission":  10,
	"a value-less -a passed as is":    5,
}

// #4845: CodexAgent.LaunchCommand split into codexStart, codexInstructions, codexHookArgs, codexCapabilityFlags,
// codexUserFlags and codexActionPrompt. On random launches (resumes, CLI paths, protocol files, boards, roles and
// prompts, capabilities, user flags in every spelling Coral translates, Claude-only flags and permission modes) the
// old builder and the new one build the same command, write the same files, and fail alike.
func TestCodexLaunchCommandAgreesWithBefore4845(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // the launch's temporary files are this test's own
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(new(strings.Builder), nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	proto := filepath.Join(t.TempDir(), "protocol.md")
	require.NoError(t, os.WriteFile(proto, []byte("# Protocol\nfollow it\n"), 0o600))
	empty := filepath.Join(t.TempDir(), "empty.md")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	a := &CodexAgent{}
	flagWords := []string{"--permission-mode", "acceptEdits", "--permission-mode=plan", "--permission-mode=bypassPermissions",
		"--dangerously-skip-permissions", "--dangerously-bypass-approvals-and-sandbox", "--full-auto", "-a", "untrusted", "never",
		"-a=untrusted", "--approval-mode", "on-request", "--approval-mode=untrusted", "--ask-for-approval", "--ask-for-approval=untrusted", "on-failure", "--sandbox", "read-only", "--sandbox=workspace-write",
		"--settings", "--session-id", "--resume", "--model", "o3", "--search", "-c", "model=\"gpt-5\""}
	capSets := [][]string{nil, {CapWebAccess}, {CapShell}, {CapShell, CapFileRead, CapFileWrite, CapGitWrite}, {CapFileRead}, {CapShell, CapWebAccess}}
	counts := map[string]int{}
	for seed := int64(1); seed <= 2000; seed++ {
		rng := rand.New(rand.NewSource(seed))
		p := LaunchParams{
			SessionID:      fmt.Sprintf("4845%04d-0000-4000-8000-000000000001", seed%10000),
			SessionName:    "codex-4845",
			ProtocolPath:   []string{"", proto, empty, "/nonexistent/protocol.md"}[rng.Intn(4)],
			CLIPath:        []string{"", "/usr/bin/true", "codex"}[rng.Intn(3)],
			BoardName:      []string{"", "team"}[rng.Intn(2)],
			Role:           []string{"", "Developer", "Orchestrator"}[rng.Intn(3)],
			Prompt:         []string{"", "do the work", "--dangerously-skip-permissions"}[rng.Intn(3)],
			BoardType:      []string{"", "coding"}[rng.Intn(2)],
			PermissionMode: []string{"", "default", "auto", "dontAsk", "acceptEdits", "plan", "bypassPermissions", "weird"}[rng.Intn(8)],
			CoralDir:       "/tmp/coral-4845",
			CoralHost:      "127.0.0.1",
			CoralPort:      8450,
		}
		if rng.Intn(4) == 0 {
			p.ResumeSessionID = "019a0000-4845-7000-8000-00000000000" + fmt.Sprint(rng.Intn(10))
		}
		if allow := capSets[rng.Intn(len(capSets))]; allow != nil {
			p.Capabilities = &Capabilities{Allow: allow}
			if rng.Intn(4) == 0 {
				p.Capabilities.Deny = []string{"Bash(rm:*)"}
			}
		}
		for k := rng.Intn(5); k > 0; k-- {
			p.Flags = append(p.Flags, flagWords[rng.Intn(len(flagWords))])
		}
		want, werr := a.launchCommandBefore4845(p)
		got, gerr := a.LaunchCommand(p)
		// #5925 adds Coral networking to autonomous workspace launches; its policy
		// and precedence have separate tests. Compare the pre-existing behavior here.
		got = strings.ReplaceAll(got, " -c sandbox_workspace_write.network_access=true", "")
		label := fmt.Sprintf("seed %d %+v", seed, p)
		require.Equal(t, werr == nil, gerr == nil, label)
		files := func(cmd string) []string {
			var out []string
			for _, path := range codexTempFile.FindAllString(cmd, -1) {
				b, err := os.ReadFile(path)
				out = append(out, fmt.Sprintf("%s: %q %v", codexTempFile.ReplaceAllString(filepath.Base(path), "<f>"), b, err == nil))
			}
			return out
		}
		require.Equal(t, files(want), files(got), label)
		require.Equal(t, codexTempFile.ReplaceAllString(want, "<tmp>"), codexTempFile.ReplaceAllString(got, "<tmp>"), label)

		c := got
		has := func(s string) bool { return strings.Contains(c, s) }
		if p.ResumeSessionID != "" && has(" resume ") {
			counts["a resume"]++
		}
		if has("developer_instructions") {
			counts["developer instructions"]++
		}
		if has("codex_prompt") {
			counts["an action prompt"]++
		}
		if perms := TranslateToCodexPermissions(p.Capabilities); perms != nil {
			switch {
			case p.PermissionMode == "bypassPermissions" && has("--dangerously-bypass-approvals-and-sandbox"):
				counts["bypass from an explicit mode"]++
			case perms.FullAuto:
				counts["full auto from capabilities"]++
			case perms.SandboxMode != "" || perms.ApprovalPolicy != "":
				counts["sandbox and approval from caps"]++
			}
			if perms.Search && has("--search") {
				counts["web search"]++
			}
		}
		flags := strings.Join(p.Flags, " ")
		if p.Capabilities.IsEmpty() && strings.Contains(flags, "--permission-mode") {
			counts["permission mode from a flag"]++
		}
		if p.Capabilities.IsEmpty() && !strings.Contains(flags, "--permission-mode") && !strings.Contains(flags, "-a") && !strings.Contains(flags, "--sandbox") &&
			!strings.Contains(flags, "--dangerously") && !strings.Contains(flags, "--full-auto") && !strings.Contains(flags, "--approval-mode") {
			if p.PermissionMode != "" && p.PermissionMode != "default" && p.PermissionMode != "weird" {
				counts["permission mode from the launch"]++
			}
		}
		if strings.Contains(flags, "untrusted") && has("on-request") {
			counts["untrusted normalized"]++
		}
		for _, f := range []string{"--settings", "--session-id", "--resume"} {
			for _, g := range p.Flags {
				if g == f {
					counts["a Claude-only flag dropped"]++
				}
			}
		}
		for _, f := range p.Flags {
			if (f == "--model" || f == "--search" || f == "-c") && has(f) {
				counts["a user flag passed through"]++
				break
			}
		}
		if !p.Capabilities.IsEmpty() && strings.Contains(flags, "--dangerously-skip-permissions") {
			counts["a skip flag after a permission"]++
		}
		if len(p.Flags) > 0 && p.Flags[len(p.Flags)-1] == "-a" {
			counts["a value-less -a passed as is"]++
		}
	}
	t.Logf("compared, per outcome: %v", counts)
	for name, floor := range codexLaunchOutcomes {
		require.GreaterOrEqual(t, counts[name], floor, "%q went (almost) unexercised", name)
	}
	_ = transcriptlink.Codex
}

// launchCommandBefore4845 is CodexAgent.LaunchCommand as it was before #4845.
func (a *CodexAgent) launchCommandBefore4845(params LaunchParams) (string, error) {
	files := &launchFiles{}
	bin := resolveBinary(params.CLIPath, "codex")
	var parts []string
	// raw: the shell syntax this builder makes itself; every other part is quoted (#3362).
	var raw []string

	// Export env vars so child processes (coral-board, hooks) inherit them.
	// formatEnvExport generates platform-appropriate syntax (export on Unix,
	// $env: on PowerShell) and quotes values so the shell never expands them.
	for _, kv := range CoralEnv(params) {
		export := formatEnvExport(kv[0], kv[1])
		raw = append(raw, export)
		parts = append(parts, export)
	}
	// NOTE: PATH injection is handled by callers via WrapWithBundlePath()

	// Binary and resume
	if params.ResumeSessionID != "" {
		resumeID := params.ResumeSessionID
		if link := transcriptlink.Codex(resumeID); link.ThreadID != "" {
			resumeID = link.ThreadID
		}
		parts = append(parts, bin, "resume", resumeID)
	} else {
		parts = append(parts, bin)
	}

	// System prompt injection via -c developer_instructions
	// Combines protocol file + board system prompt (CLI usage, role instructions)
	// The temp file path is from os.TempDir() (safe). Content sources (protocol files,
	// board prompts) are trusted internal strings.
	var sysParts []string
	if proto := readProtocolFile(params.ProtocolPath); proto != "" {
		sysParts = append(sysParts, proto)
	}
	boardSysPrompt := BuildBoardSystemPrompt(params.BoardName, params.Role, "", params.PromptOverrides, params.BoardType)
	if boardSysPrompt != "" {
		sysParts = append(sysParts, boardSysPrompt)
	}
	sysParts = appendCoralSessionMarker(sysParts, params.SessionID)

	if len(sysParts) > 0 {
		// The file's writing and reading back as #29 made it, in both builders.
		instructions := files.promptArg("codex_instructions", params.SessionID, "md", "developer_instructions=", strings.Join(sysParts, "\n\n"))
		raw = append(raw, instructions)
		parts = append(parts, "-c", instructions)
	}

	// Codex does not read Claude's settings.json, so install Coral's activity
	// hook directly in its config. Without these hooks Codex sessions can still
	// appear active, but no tool/thinking/stop events reach the activity view.
	// (The hooks' form is #49's in both builders: not what #4845 changed.)
	parts = append(parts, codexHookArgs(params.SessionID)...)

	// Note: Codex's sandbox may strip env vars from child processes.
	// coral-board handles this via board_state file fallback (reads job_title
	// from ~/.coral/board_state_{session}.json when CORAL_SUBSCRIBER_ID is unavailable).

	// Permission flags from capabilities
	bypassSandbox := false
	permissionApplied := false
	if perms := TranslateToCodexPermissions(params.Capabilities); perms != nil {
		permissionApplied = true
		// The two deliberate changes since #4845 (#6422, #6425): no bypass is read from capabilities, and an explicit mode is applied with the capabilities only narrowing it.
		pair := codexPairOf(perms)
		if m, ok := codexPairForMode(params.PermissionMode); ok {
			if perms.FullAuto {
				pair = codexPair{codexSandboxNone, "never"}
			}
			pair = narrowCodexPair(m, pair)
		}
		if pair.sandbox == codexSandboxNone && pair.approval == "never" {
			bypassSandbox = true
			parts = append(parts, "--dangerously-bypass-approvals-and-sandbox")
		} else if pair.sandbox == "workspace-write" && pair.approval == "never" {
			parts = appendCodexFullAuto(parts)
		} else {
			if pair.sandbox != "" {
				parts = append(parts, "--sandbox", pair.sandbox)
			}
			if pair.approval != "" {
				parts = append(parts, "-a", normalizeCodexApprovalPolicy(pair.approval))
			}
		}
		if perms.Search {
			parts = append(parts, "--search")
		}
	}

	// User-provided flags — translate or drop Claude-specific flags
	claudeOnlyFlags := map[string]bool{
		"--settings": true, "--session-id": true, "--resume": true,
	}
	for i := 0; i < len(params.Flags); i++ {
		flag := params.Flags[i]
		if flag == "--permission-mode" {
			if i+1 < len(params.Flags) {
				i++
				if !permissionApplied {
					parts, bypassSandbox, permissionApplied = appendCodexPermissionMode(parts, bypassSandbox, params.Flags[i])
				}
			}
			continue
		}
		if strings.HasPrefix(flag, "--permission-mode=") {
			if !permissionApplied {
				mode := strings.TrimPrefix(flag, "--permission-mode=")
				parts, bypassSandbox, permissionApplied = appendCodexPermissionMode(parts, bypassSandbox, mode)
			}
			continue
		}
		if flag == "--dangerously-skip-permissions" {
			// Translate to Codex equivalent, but skip if bypass was already added
			if !bypassSandbox && !permissionApplied {
				parts = appendCodexFullAuto(parts)
				permissionApplied = true
			}
			continue
		}
		if flag == "--dangerously-bypass-approvals-and-sandbox" {
			if !bypassSandbox && !permissionApplied {
				parts = append(parts, flag)
				bypassSandbox = true
				permissionApplied = true
			}
			continue
		}
		if flag == "--full-auto" {
			// Newer Codex versions removed the alias; emit its equivalent directly.
			if !bypassSandbox && !permissionApplied {
				parts = appendCodexFullAuto(parts)
				permissionApplied = true
			}
			continue
		}
		if (flag == "-a" || flag == "--ask-for-approval" || flag == "--approval-mode") && i+1 < len(params.Flags) {
			// Older Coral permission profiles used Codex's former "untrusted" policy. Current Codex accepts only on-request or never, and its flag is
			// -a / --ask-for-approval, not --approval-mode (#5890: the one deliberate change from the builder this oracle was taken from).
			name := flag
			if name == "--approval-mode" {
				name = "-a"
			}
			parts = append(parts, name, normalizeCodexApprovalPolicy(params.Flags[i+1]))
			i++
			permissionApplied = true
			continue
		}
		if p := strings.Index(flag, "="); p > 0 && (flag[:p] == "-a" || flag[:p] == "--ask-for-approval" || flag[:p] == "--approval-mode") {
			name := flag[:p]
			if name == "--approval-mode" {
				name = "-a"
			}
			parts = append(parts, name+"="+normalizeCodexApprovalPolicy(flag[p+1:]))
			permissionApplied = true
			continue
		}
		if flag == "--sandbox" || strings.HasPrefix(flag, "--sandbox=") {
			permissionApplied = true
		}
		if claudeOnlyFlags[flag] {
			slog.Warn("dropping Claude-specific flag for Codex agent", "flag", flag)
			continue
		}
		parts = append(parts, flag)
	}

	if !permissionApplied {
		parts, bypassSandbox, permissionApplied = appendCodexPermissionMode(parts, bypassSandbox, params.PermissionMode)
	}
	parts = collapseCodexSandboxFlags(parts, !params.Capabilities.IsEmpty()) // #5222: the one change since #4845 on purpose: Codex refuses a repeated --sandbox or -a, so the last one of each is kept

	// Action prompt as separate positional argument
	actionPrompt := BuildBoardActionPrompt(params.boardPromptName(), params.Role, params.Prompt, params.PromptOverrides, params.BoardType)
	if actionPrompt == "" {
		actionPrompt = params.Prompt
	}

	if actionPrompt != "" {
		arg := files.promptArg("codex_prompt", params.SessionID, "txt", "", actionPrompt)
		raw = append(raw, arg)
		// "--": a prompt that is only a flag ("--dangerously-skip-permissions") is the
		// prompt, not that option (#3690). The prompt is the last argument.
		parts = append(parts, "--", arg)
	}

	parts, raw = withSessionToken(parts, raw, params, "codex")
	return files.done(ShellJoin(parts, raw...))
}
