package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #126 F4: SkipsPermissionPrompts is an allowlist. Every spelling the audit named counts as
// skipping, for its own type, in the "--flag value" and "--flag=value" forms; so does a flag
// nobody listed, a value that is itself a flag and a flag with no value. Only the model, the
// thinking level and the modes that keep the prompts on pass.
func TestSkipsPermissionPrompts(t *testing.T) {
	keeps := map[string][]string{
		"claude": {
			"", "--model opus", "--model=opus", "-m opus", "--effort high", "--effort=high",
			"--permission-mode default", "--permission-mode plan", "--permission-mode=plan", "--model opus --effort low --permission-mode plan",
		},
		"agy":         {"--model x", "--effort high", "--mode plan", "--mode=plan", "--approval-mode default", "--approval-mode=plan", "--permission-mode plan"},
		"antigravity": {"--effort medium", "--mode plan"},
		"gemini":      {"-m gemini-2.5-pro", "--approval-mode default", "--approval-mode plan", "--mode plan"},
		"codex": {
			"-m gpt-5", "-a on-request", "-a untrusted", "--ask-for-approval on-request", "--ask-for-approval=untrusted", "--approval-mode on-request",
			"-s read-only", "--sandbox workspace-write", "--sandbox=read-only", "--sandbox workspace-write -a on-request",
			"-c model_reasoning_effort=high", "--config model_reasoning_effort=low", "-c=model_reasoning_effort=medium", "--permission-mode default",
		},
		"pi":       {"--model x"},
		"terminal": {""},
	}
	skips := map[string][]string{
		"claude": {
			"--dangerously-skip-permissions", "--permission-mode acceptEdits", "--permission-mode dontAsk", "--permission-mode auto",
			"--permission-mode bypassPermissions", "--permission-mode=bypassPermissions", "--allowedTools Bash(*)", "--allowed-tools Bash",
			"--settings s.json", "--settings={}", "--add-dir /", "--mcp-config m.json", "--permission-prompt-tool t", "--verbose",
			"--yolo", "--mode accept-edits", "-a never", "--effort", "--effort --dangerously-skip-permissions", "--model --yolo",
			"--model=", "opus", "--", "--model opus --dangerously-skip-permissions", "--Permission-Mode plan",
		},
		"agy":         {"--dangerously-skip-permissions", "--yolo", "--mode accept-edits", "--mode=accept-edits", "--approval-mode yolo", "--allowed-tools x", "--settings s"},
		"antigravity": {"--yolo", "--mode accept-edits"},
		"gemini":      {"-y", "--yolo", "--approval-mode yolo", "--approval-mode auto_edit", "--approval-mode=yolo", "--mode accept-edits", "--allowed-tools ShellTool", "--effort high"},
		"codex": {
			"--dangerously-bypass-approvals-and-sandbox", "--yolo", "--full-auto", "--dangerously-skip-permissions",
			"-a never", "-a=never", "--ask-for-approval never", "--ask-for-approval=never", "--approval-mode never", "-a on-failure",
			"--sandbox danger-full-access", "-s danger-full-access", "--sandbox=danger-full-access",
			"-c approval_policy=never", "-c approval_policy=\"never\"", "--config sandbox_mode=danger-full-access", "-c=approval_policy=never",
			"-c model_reasoning_effort=high\napproval_policy=never", "-c model_reasoning_effort=", "-p yolo", "--profile yolo", "--add-dir /",
			"--permission-mode bypassPermissions", "--permission-mode auto", "--effort high",
		},
		"pi":       {"--yolo", "--permission-mode plan --approval-mode yolo"},
		"terminal": {"--anything"},
	}
	words := func(s string) []string {
		if strings.Contains(s, "\n") { // one argument with a newline in it
			name, value, _ := strings.Cut(s, " ")
			return []string{name, value}
		}
		return strings.Fields(s)
	}
	for agentType, list := range keeps {
		for _, flags := range list {
			assert.False(t, SkipsPermissionPrompts(agentType, words(flags)), "%s %q keeps the prompts", agentType, flags)
		}
	}
	for agentType, list := range skips {
		for _, flags := range list {
			assert.True(t, SkipsPermissionPrompts(agentType, words(flags)), "%s %q skips them", agentType, flags)
		}
	}
	// The mode flags Coral itself makes for a mode that keeps the prompts pass for that type; those
	// for a mode that doesn't (Claude's acceptEdits, every type's bypass) count as skipping.
	for _, agentType := range []string{"claude", "agy", "antigravity", "gemini", "codex"} {
		assert.False(t, SkipsPermissionPrompts(agentType, PermissionFlagsForAgent(agentType, "plan")), "%s plan", agentType)
		assert.True(t, SkipsPermissionPrompts(agentType, PermissionFlagsForAgent(agentType, "bypassPermissions")), "%s bypass", agentType)
	}
	assert.True(t, SkipsPermissionPrompts("claude", PermissionFlagsForAgent("claude", "acceptEdits")))
	assert.True(t, SkipsPermissionPrompts("codex", PermissionFlagsForAgent("codex", "auto")))
}
