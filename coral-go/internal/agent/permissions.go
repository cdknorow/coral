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
