package agent

import (
	"strings"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// Capability represents a Coral-level permission capability.
// These are agent-agnostic; each agent adapter translates them
// to the native format at launch time.
type Capability = string

// Standard capabilities.
const (
	CapFileRead   Capability = "file_read"
	CapFileWrite  Capability = "file_write"
	CapShell      Capability = "shell"
	CapWebAccess  Capability = "web_access"
	CapGitWrite   Capability = "git_write"
	CapAgentSpawn Capability = "agent_spawn"
	CapNotebook   Capability = "notebook"
)

// Capabilities defines an agent's permission scope.
type Capabilities struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// IsEmpty returns true if no capabilities are defined.
func (c *Capabilities) IsEmpty() bool {
	return c == nil || (len(c.Allow) == 0 && len(c.Deny) == 0)
}

// ClaudePermissions is the native Claude Code settings.json permissions format.
type ClaudePermissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// TranslateToClaudePermissions converts Coral capabilities to Claude Code's
// native permissions format for injection into settings.json.
func TranslateToClaudePermissions(caps *Capabilities) *ClaudePermissions {
	if caps.IsEmpty() {
		return nil
	}

	var allow, deny []string

	// Check if allow has any shell:<pattern> entries
	hasShellPatterns := false
	for _, cap := range caps.Allow {
		if strings.HasPrefix(cap, "shell:") {
			hasShellPatterns = true
			break
		}
	}

	for _, cap := range caps.Allow {
		allow = append(allow, mapCapToClaudeTools(cap)...)
	}
	for _, cap := range caps.Deny {
		// Skip blanket Bash deny when specific shell patterns are allowed —
		// the allow whitelist is sufficient, and a blanket deny would override
		// the specific Bash(pattern) allows in Claude Code.
		if cap == CapShell && hasShellPatterns {
			continue
		}
		deny = append(deny, mapCapToClaudeTools(cap)...)
	}

	if len(allow) == 0 && len(deny) == 0 {
		return nil
	}
	return &ClaudePermissions{Allow: allow, Deny: deny}
}

// mapCapToClaudeTools maps a single Coral capability to Claude Code tool patterns.
func mapCapToClaudeTools(cap string) []string {
	// Handle shell:<pattern> syntax
	if strings.HasPrefix(cap, "shell:") {
		pattern := strings.TrimPrefix(cap, "shell:")
		return []string{"Bash(" + pattern + ")"}
	}

	switch cap {
	case CapFileRead:
		return []string{"Read", "Glob", "Grep"}
	case CapFileWrite:
		return []string{"Write", "Edit"}
	case CapShell:
		return []string{"Bash"}
	case CapWebAccess:
		return []string{"WebFetch", "WebSearch"}
	case CapGitWrite:
		return []string{"Bash(git push *)", "Bash(git commit *)", "Bash(git branch *)", "Bash(git merge *)", "Bash(git rebase *)"}
	case CapAgentSpawn:
		return []string{"Agent"}
	case CapNotebook:
		return []string{"NotebookEdit"}
	default:
		// Pass through unknown capabilities as-is (future-proof)
		return []string{cap}
	}
}

// CodexPermissions represents codex-cli sandbox/permission settings.
type CodexPermissions struct {
	FullAuto       bool   `json:"full_auto,omitempty"`
	BypassSandbox  bool   `json:"bypass_sandbox,omitempty"`
	SandboxMode    string `json:"sandbox_mode,omitempty"`    // "read-only", "workspace-write", "danger-full-access"
	ApprovalPolicy string `json:"approval_policy,omitempty"` // "on-request" or "never" (the only two Codex 0.155 takes)
	Search         bool   `json:"search,omitempty"`
}

// TranslateToCodexPermissions converts Coral capabilities to Codex CLI flags.
func TranslateToCodexPermissions(caps *Capabilities) *CodexPermissions {
	if caps.IsEmpty() {
		return nil
	}

	allowSet := map[string]bool{}
	for _, cap := range caps.Allow {
		allowSet[cap] = true
	}
	hasDeny := len(caps.Deny) > 0

	perms := &CodexPermissions{}

	// Check for web_access → --search
	if allowSet[CapWebAccess] {
		perms.Search = true
	}

	// Full access (all major caps, no deny) → bypass sandbox entirely.
	// Design decision: this matches the "full_access" preset from the spec. The combination
	// of CapShell + CapFileRead + CapFileWrite + CapGitWrite with no deny list represents
	// an explicitly unrestricted agent. Individual caps (e.g. CapGitWrite alone) do NOT
	// trigger bypass — only the full combination does.
	if allowSet[CapShell] && allowSet[CapFileRead] && allowSet[CapFileWrite] && allowSet[CapGitWrite] && !hasDeny {
		perms.BypassSandbox = true
		return perms
	}

	// Shell with no deny → automatic workspace writes without approval prompts.
	if allowSet[CapShell] && !hasDeny {
		perms.FullAuto = true
		return perms
	}

	// Shell with deny list → workspace-write, on-request (Codex 0.155 has only on-request and never; on-request
	// lets sandboxed commands run without asking, #4545 E)
	if allowSet[CapShell] && hasDeny {
		perms.SandboxMode = "workspace-write"
		perms.ApprovalPolicy = "on-request"
		return perms
	}

	// file_read + file_write → workspace-write, on-request
	if allowSet[CapFileRead] && allowSet[CapFileWrite] {
		perms.SandboxMode = "workspace-write"
		perms.ApprovalPolicy = "on-request"
		return perms
	}

	// file_read only → read-only, on-request
	if allowSet[CapFileRead] {
		perms.SandboxMode = "read-only"
		perms.ApprovalPolicy = "on-request"
		return perms
	}

	// Search-only or other non-write capabilities should stay read-only.
	if allowSet[CapWebAccess] {
		perms.SandboxMode = "read-only"
		perms.ApprovalPolicy = "on-request"
		return perms
	}

	// Default: workspace-write, on-request
	perms.SandboxMode = "workspace-write"
	perms.ApprovalPolicy = "on-request"
	return perms
}

// AgyPermissions represents Antigravity CLI (agy) permission settings.
type AgyPermissions struct {
	DangerouslySkipPermissions bool   `json:"dangerously_skip_permissions,omitempty"`
	Mode                       string `json:"mode,omitempty"`          // "accept-edits", "plan"
	ApprovalMode               string `json:"approval_mode,omitempty"` // backward-compatibility alias ("default", "auto_edit", "yolo", "plan")
	Sandbox                    bool   `json:"sandbox,omitempty"`
}

// TranslateToAgyPermissions converts Coral capabilities to Antigravity CLI (agy) flags.
func TranslateToAgyPermissions(caps *Capabilities) *AgyPermissions {
	if caps.IsEmpty() {
		return nil
	}

	allowSet := map[string]bool{}
	for _, cap := range caps.Allow {
		allowSet[cap] = true
	}

	// Full access (shell + file_write + no deny) → dangerously-skip-permissions
	if allowSet[CapShell] && allowSet[CapFileWrite] && len(caps.Deny) == 0 {
		return &AgyPermissions{DangerouslySkipPermissions: true, ApprovalMode: "yolo"}
	}

	// Shell + file_write with restrictions → accept-edits
	if allowSet[CapShell] && allowSet[CapFileWrite] {
		return &AgyPermissions{Mode: "accept-edits", ApprovalMode: "auto_edit"}
	}

	// Shell without file_write → accept-edits (shell implies some write)
	if allowSet[CapShell] {
		return &AgyPermissions{Mode: "accept-edits", ApprovalMode: "auto_edit"}
	}

	// file_read only → plan mode (read-only)
	if allowSet[CapFileRead] && !allowSet[CapFileWrite] && !allowSet[CapShell] {
		return &AgyPermissions{Mode: "plan", ApprovalMode: "plan"}
	}

	// file_read + file_write → accept-edits
	if allowSet[CapFileRead] && allowSet[CapFileWrite] {
		return &AgyPermissions{Mode: "accept-edits", ApprovalMode: "auto_edit"}
	}

	// Default
	return &AgyPermissions{ApprovalMode: "default"}
}

// PermissionModeFromFlags extracts generic permission mode ("bypassPermissions", "plan", "acceptEdits", "auto", "dontAsk")
// from CLI flags if present.
func PermissionModeFromFlags(flags []string) string {
	for i := 0; i < len(flags); i++ {
		f := flags[i]
		switch {
		case f == "--dangerously-skip-permissions" || f == "--yolo" || f == "--dangerously-bypass-approvals-and-sandbox":
			return "bypassPermissions"
		case f == "--full-auto":
			return "auto"
		case f == "--permission-mode":
			if i+1 < len(flags) {
				return flags[i+1]
			}
		case strings.HasPrefix(f, "--permission-mode="):
			return strings.TrimPrefix(f, "--permission-mode=")
		case f == "--mode":
			if i+1 < len(flags) {
				switch flags[i+1] {
				case "plan":
					return "plan"
				case "accept-edits":
					return "acceptEdits"
				}
			}
		case strings.HasPrefix(f, "--mode="):
			switch strings.TrimPrefix(f, "--mode=") {
			case "plan":
				return "plan"
			case "accept-edits":
				return "acceptEdits"
			}
		}
	}
	return ""
}

// PermissionFlagsForAgent returns the CLI flags representing the permission mode for a given agent type.
func PermissionFlagsForAgent(agentType, mode string) []string {
	if mode == "" || mode == "default" || agentType == at.Pi || agentType == at.Terminal {
		return nil
	}
	switch agentType {
	case at.Claude:
		return []string{"--permission-mode", mode}
	case at.Agy, "antigravity", "gemini":
		switch mode {
		case "plan":
			return []string{"--mode", "plan"}
		case "acceptEdits":
			return []string{"--mode", "accept-edits"}
		case "bypassPermissions":
			return []string{"--dangerously-skip-permissions"}
		default:
			return nil
		}
	case at.Codex:
		switch mode {
		case "bypassPermissions":
			return []string{"--dangerously-bypass-approvals-and-sandbox"}
		case "plan":
			return []string{"--sandbox", "read-only", "-a", "on-request"}
		case "acceptEdits":
			return []string{"--sandbox", "workspace-write", "-a", "on-request"}
		case "auto", "dontAsk":
			return []string{"--sandbox", "workspace-write", "-a", "never"}
		default:
			return []string{"--sandbox", "workspace-write", "-a", "on-request"}
		}
	default:
		if mode == "bypassPermissions" {
			return []string{"--dangerously-skip-permissions"}
		}
		return nil
	}
}

// DefaultLaunchPermissionMode is the permission mode a launch gets when nothing chose one (no mode, no permission flag, no saved default) (#5836): Claude starts in Auto, so a
// new user is not met with an agent that bypasses every prompt and the agent is not stopped by prompts nobody is there to answer; every other CLI starts as its own default does
// (Codex's Auto is a sandbox that cannot reach the board on localhost, Antigravity and Pi have no mode between asking and skipping).
func DefaultLaunchPermissionMode(agentType string) string {
	if agentType == "" || agentType == "claude" {
		return "auto"
	}
	return "default"
}

// ValidPermissionMode reports whether mode is a recognized permission mode string.
func ValidPermissionMode(mode string) bool {
	switch mode {
	case "default", "plan", "acceptEdits", "auto", "bypassPermissions", "dontAsk":
		return true
	default:
		return false
	}
}

// SkipsPermissionPrompts reports whether flags could let an agentType agent act without asking:
// a permission mode other than default (or, for the other CLIs than Claude, plan), a bypass, pre-allowed tools, extra folders, another
// settings or config file, or any flag it does not know (#126 F4). It is an allowlist, so a spelling
// nobody listed counts as skipping: false only when every word is a flag below with a value that
// keeps the prompts on. Each flag is "--flag value" or "--flag=value"; a value that is itself a flag
// counts as skipping.
//
//   - every type: --model / -m, and --permission-mode default|plan (Coral translates it per type; for Claude only default|manual: its Plan mode runs classifier-approved commands without asking, #5894)
//   - Claude, agy, Antigravity: --effort (the thinking level, agentmodels.ThinkingArgs)
//   - agy, Antigravity, Gemini: --mode plan, --approval-mode default|plan
//   - Pi: nothing. Pi has no mode that asks, so every Pi launch skips (#5901)
//   - Codex: -a / --ask-for-approval / --approval-mode untrusted|on-request, -s / --sandbox
//     read-only|workspace-write (Coral's default for Codex), -c / --config model_reasoning_effort=<level> (a lowercase word)
func SkipsPermissionPrompts(agentType string, flags []string) bool {
	if agentType == at.Pi {
		// Pi has no mode that asks: its tools run with the launching user's permissions, with no approval before a call (Pi's security documentation, #5901), so any Pi launch counts
		// as skipping, whatever its flags: a Coral Hub request for one needs the owner's switch, Launch mode and the dashboard's provenance, like Auto.
		return true
	}
	for i := 0; i < len(flags); i++ {
		name, value, inline := strings.Cut(flags[i], "=")
		keeps := promptKeepingFlag(agentType, name)
		if keeps == nil {
			return true
		}
		if !inline {
			if i+1 >= len(flags) || strings.HasPrefix(flags[i+1], "-") {
				return true
			}
			i++
			value = flags[i]
		}
		if value == "" || !keeps(value) {
			return true
		}
	}
	return false
}

// promptKeepingFlag is the value check for a flag SkipsPermissionPrompts allows for agentType, nil
// for any other flag.
func promptKeepingFlag(agentType, name string) func(string) bool {
	anyValue := func(string) bool { return true }
	oneOf := func(values ...string) func(string) bool {
		return func(v string) bool {
			for _, ok := range values {
				if v == ok {
					return true
				}
			}
			return false
		}
	}
	switch name {
	case "--model", "-m":
		return anyValue
	case "--permission-mode":
		if agentType == at.Claude || agentType == "" { // no type is Claude, the default agent
			// Claude's Plan mode is not prompt-keeping (#5894, Anthropic's permission-modes page): with auto mode available it runs the commands its classifier approves
			// instead of asking, and only blocks source edits. Default (Manual) is the mode that asks. The other CLIs' plan modes are their own and are left as they were.
			return oneOf("default", "manual")
		}
		return oneOf("default", "plan")
	}
	switch agentType {
	case at.Claude:
		if name == "--effort" {
			return anyValue
		}
	case at.Agy, at.Antigravity, at.Gemini:
		switch {
		case name == "--effort" && agentType != at.Gemini:
			return anyValue
		case name == "--mode":
			return oneOf("plan")
		case name == "--approval-mode":
			return oneOf("default", "plan")
		}
	case at.Codex:
		switch name {
		case "-a", "--ask-for-approval", "--approval-mode":
			return oneOf("untrusted", "on-request")
		case "-s", "--sandbox":
			return oneOf("read-only", "workspace-write")
		case "-c", "--config":
			return func(v string) bool {
				key, level, ok := strings.Cut(v, "=")
				return ok && key == "model_reasoning_effort" && level != "" && strings.Trim(level, "abcdefghijklmnopqrstuvwxyz") == ""
			}
		}
	}
	return nil
}
