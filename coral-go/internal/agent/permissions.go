package agent

import (
	"strings"

	at "github.com/cdknorow/coral/internal/agenttypes"
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

// TranslatePermissions dispatches capability translation to the appropriate
// agent-specific translator. Returns nil if no translation is needed.
func TranslatePermissions(agentType string, caps *Capabilities) any {
	// Ensure every agent can use coral-board for message board communication
	if caps != nil {
		hasBoardCap := false
		for _, c := range caps.Allow {
			if c == "shell:coral-board *" {
				hasBoardCap = true
				break
			}
		}
		if !hasBoardCap {
			caps.Allow = append(caps.Allow, "shell:coral-board *")
		}
	}

	switch agentType {
	case at.Claude:
		return TranslateToClaudePermissions(caps)
	case at.Codex:
		return TranslateToCodexPermissions(caps)
	case at.Agy, at.Antigravity, at.Gemini:
		return TranslateToAgyPermissions(caps)
	case at.Pi:
		return nil
	default:
		return TranslateToClaudePermissions(caps)
	}
}

// CodexPermissions represents codex-cli sandbox/permission settings.
type CodexPermissions struct {
	FullAuto       bool   `json:"full_auto,omitempty"`
	BypassSandbox  bool   `json:"bypass_sandbox,omitempty"`
	SandboxMode    string `json:"sandbox_mode,omitempty"`    // "read-only", "workspace-write", "danger-full-access"
	ApprovalPolicy string `json:"approval_policy,omitempty"` // "untrusted", "on-request", "never"
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

	// Shell with deny list → workspace-write but untrusted approval
	if allowSet[CapShell] && hasDeny {
		perms.SandboxMode = "workspace-write"
		perms.ApprovalPolicy = "untrusted"
		return perms
	}

	// file_read + file_write → workspace-write, untrusted
	if allowSet[CapFileRead] && allowSet[CapFileWrite] {
		perms.SandboxMode = "workspace-write"
		perms.ApprovalPolicy = "untrusted"
		return perms
	}

	// file_read only → read-only, untrusted
	if allowSet[CapFileRead] {
		perms.SandboxMode = "read-only"
		perms.ApprovalPolicy = "untrusted"
		return perms
	}

	// Search-only or other non-write capabilities should stay read-only.
	if allowSet[CapWebAccess] {
		perms.SandboxMode = "read-only"
		perms.ApprovalPolicy = "untrusted"
		return perms
	}

	// Default: workspace-write with untrusted
	perms.SandboxMode = "workspace-write"
	perms.ApprovalPolicy = "untrusted"
	return perms
}

// AgyPermissions represents Antigravity CLI (agy) permission settings.
type AgyPermissions struct {
	DangerouslySkipPermissions bool   `json:"dangerously_skip_permissions,omitempty"`
	Mode                       string `json:"mode,omitempty"`          // "accept-edits", "plan"
	ApprovalMode               string `json:"approval_mode,omitempty"` // backward-compatibility alias ("default", "auto_edit", "yolo", "plan")
	Sandbox                    bool   `json:"sandbox,omitempty"`
}

// Deprecated: use AgyPermissions.
type GeminiPermissions = AgyPermissions

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

// Deprecated: use TranslateToAgyPermissions.
func TranslateToGeminiPermissions(caps *Capabilities) *GeminiPermissions {
	return TranslateToAgyPermissions(caps)
}

// Preset permission profiles for built-in agent roles.
var Presets = map[string]*Capabilities{
	"lead_dev": {
		Allow: []string{CapFileRead, CapFileWrite, CapShell, CapGitWrite, CapAgentSpawn},
	},
	"qa": {
		Allow: []string{CapFileRead, CapShell},
	},
	"frontend_dev": {
		Allow: []string{CapFileRead, CapFileWrite, "shell:npm *", "shell:npx *", CapWebAccess},
	},
	"orchestrator": {
		Allow: []string{CapFileRead, CapFileWrite, CapShell, CapGitWrite, CapAgentSpawn, CapWebAccess, CapNotebook},
	},
	"devops": {
		Allow: []string{CapFileRead, CapFileWrite, CapShell, CapGitWrite},
	},
	"read_only": {
		Allow: []string{CapFileRead, CapWebAccess},
	},
	"full_access": {
		Allow: []string{CapFileRead, CapFileWrite, CapShell, CapGitWrite, CapAgentSpawn, CapWebAccess, CapNotebook},
	},
}
