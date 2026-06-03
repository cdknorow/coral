package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findTempFile finds a Coral temp file matching the given prefix and session ID.
// Since writeTempFile now uses os.CreateTemp with random suffixes, we glob for matches.
func findTempFile(t *testing.T, prefix, sessionID, ext string) string {
	t.Helper()
	pattern := filepath.Join(os.TempDir(), "coral_"+prefix+"_"+sessionID+"_*."+ext)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob error: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("no temp file found matching %s", pattern)
	}
	return matches[len(matches)-1]
}

// ── Factory Tests ───────────────────────────────────────────

func TestGetAgent(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"claude", "claude"},
		{"gemini", "gemini"},
		{"codex", "codex"},
		{"pi", "pi"},
		{"unknown-agent", "claude"},
		{"", "claude"},
	}
	for _, tt := range tests {
		t.Run(tt.input+"->"+tt.expected, func(t *testing.T) {
			a := GetAgent(tt.input)
			if a.AgentType() != tt.expected {
				t.Errorf("GetAgent(%q): expected %s, got %s", tt.input, tt.expected, a.AgentType())
			}
		})
	}
}

// ── SupportsResume Tests ────────────────────────────────────

func TestSupportsResume(t *testing.T) {
	tests := []struct {
		agentType string
		expected  bool
	}{
		{"claude", true},
		{"codex", true},
		{"gemini", false},
		{"pi", true},
	}
	for _, tt := range tests {
		a := GetAgent(tt.agentType)
		if a.SupportsResume() != tt.expected {
			t.Errorf("%s: SupportsResume() = %v, want %v", tt.agentType, a.SupportsResume(), tt.expected)
		}
	}
}

// ── HistoryGlobPattern Tests ────────────────────────────────

func TestHistoryGlobPattern(t *testing.T) {
	tests := []struct {
		agentType string
		expected  string
	}{
		{"claude", "*.jsonl"},
		{"codex", "rollout-*.jsonl"},
		{"gemini", "session-*.json"},
		{"pi", "*/*.jsonl"},
	}
	for _, tt := range tests {
		a := GetAgent(tt.agentType)
		if a.HistoryGlobPattern() != tt.expected {
			t.Errorf("%s: HistoryGlobPattern() = %q, want %q", tt.agentType, a.HistoryGlobPattern(), tt.expected)
		}
	}
}

// ── CLIInfo Tests ───────────────────────────────────────────

func TestGetCLIInfo(t *testing.T) {
	for _, agentType := range []string{"claude", "gemini", "codex", "pi"} {
		info := GetCLIInfo(agentType)
		if info == nil {
			t.Errorf("GetCLIInfo(%q) returned nil", agentType)
			continue
		}
		if info.Binary == "" {
			t.Errorf("GetCLIInfo(%q).Binary is empty", agentType)
		}
		if info.InstallCommand == "" {
			t.Errorf("GetCLIInfo(%q).InstallCommand is empty", agentType)
		}
	}
}

func TestGetCLIInfo_Unknown(t *testing.T) {
	info := GetCLIInfo("unknown")
	if info != nil {
		t.Errorf("GetCLIInfo(unknown) should return nil, got %+v", info)
	}
}

// ── GetCLIName Tests ────────────────────────────────────────

func TestGetCLIName(t *testing.T) {
	if name := GetCLIName(""); name != "coral-board" {
		t.Errorf("GetCLIName(\"\") = %q, want coral-board", name)
	}
	if name := GetCLIName("coral"); name != "coral-board" {
		t.Errorf("GetCLIName(coral) = %q, want coral-board", name)
	}
	if name := GetCLIName("unknown"); name != "coral-board" {
		t.Errorf("GetCLIName(unknown) = %q, want coral-board (default)", name)
	}
}

// ── Claude BuildLaunchCommand Tests ─────────────────────────

func TestClaude_BasicLaunch(t *testing.T) {
	a := &ClaudeAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID: "test-session-123",
	})
	if !strings.HasPrefix(cmd, "claude ") {
		t.Errorf("expected command to start with 'claude ', got %q", cmd)
	}
	if !strings.Contains(cmd, "--session-id test-session-123") {
		t.Errorf("expected --session-id, got %q", cmd)
	}
}

func TestClaude_Resume(t *testing.T) {
	a := &ClaudeAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID:       "test-session-123",
		ResumeSessionID: "resume-456",
	})
	if !strings.Contains(cmd, "--resume resume-456") {
		t.Errorf("expected --resume flag, got %q", cmd)
	}
}

func TestClaude_PermissionModeAllValues(t *testing.T) {
	tests := []struct {
		mode       string
		expectFlag bool
	}{
		{"plan", true},
		{"acceptEdits", true},
		{"auto", true},
		{"bypassPermissions", true},
		{"dontAsk", true},
		{"default", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run("mode="+tt.mode, func(t *testing.T) {
			a := &ClaudeAgent{}
			cmd := a.BuildLaunchCommand(LaunchParams{
				SessionID:      "perm-test",
				PermissionMode: tt.mode,
			})
			hasFlag := strings.Contains(cmd, "--permission-mode")
			if tt.expectFlag && !hasFlag {
				t.Errorf("expected --permission-mode flag for mode %q, got %q", tt.mode, cmd)
			}
			if !tt.expectFlag && hasFlag {
				t.Errorf("did not expect --permission-mode flag for mode %q, got %q", tt.mode, cmd)
			}
			if tt.expectFlag && !strings.Contains(cmd, "--permission-mode "+tt.mode) {
				t.Errorf("expected '--permission-mode %s', got %q", tt.mode, cmd)
			}
		})
	}
}

func TestClaude_PermissionModeWithOtherFlags(t *testing.T) {
	a := &ClaudeAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID:      "s1",
		PermissionMode: "auto",
		Flags:          []string{"--verbose"},
	})
	if !strings.Contains(cmd, "--permission-mode auto") {
		t.Errorf("expected --permission-mode auto, got %q", cmd)
	}
	if !strings.Contains(cmd, "--verbose") {
		t.Errorf("expected --verbose flag, got %q", cmd)
	}
}

func TestClaude_WithFlags(t *testing.T) {
	a := &ClaudeAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID: "s1",
		Flags:     []string{"--verbose", "--model", "opus"},
	})
	if !strings.Contains(cmd, "--verbose") || !strings.Contains(cmd, "--model opus") {
		t.Errorf("expected flags, got %q", cmd)
	}
}

func TestClaude_WithBoardWorker(t *testing.T) {
	a := &ClaudeAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID: "claude-board-w1",
		BoardName: "test-board",
		Role:      "developer",
	})
	if !strings.Contains(cmd, "--settings") {
		t.Errorf("expected --settings flag, got %q", cmd)
	}
	promptFile := findTempFile(t, "prompt", "claude-board-w1", "txt")
	data, _ := os.ReadFile(promptFile)
	if !strings.Contains(string(data), "test-board") {
		t.Errorf("expected board name in prompt file, got %q", string(data))
	}
}

func TestClaude_WithBoardOrchestrator(t *testing.T) {
	a := &ClaudeAgent{}
	a.BuildLaunchCommand(LaunchParams{
		SessionID: "claude-board-o1",
		BoardName: "test-board",
		Role:      "Orchestrator",
	})
	promptFile := findTempFile(t, "prompt", "claude-board-o1", "txt")
	data, _ := os.ReadFile(promptFile)
	if !strings.Contains(string(data), "discuss your proposed plan") {
		t.Errorf("expected orchestrator action in prompt file, got %q", string(data))
	}
}

func TestClaude_WithPrompt(t *testing.T) {
	a := &ClaudeAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID: "s1",
		Prompt:    "Fix the login bug",
	})
	if !strings.Contains(cmd, "coral_prompt_") {
		t.Errorf("expected prompt file reference, got %q", cmd)
	}
}

func TestClaude_WithCapabilities(t *testing.T) {
	a := &ClaudeAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID:    "s1",
		Capabilities: &Capabilities{Allow: []string{CapFileRead, CapShell}},
	})
	if !strings.Contains(cmd, "--settings") {
		t.Errorf("expected --settings with capabilities, got %q", cmd)
	}
}

// ── BuildBoardSystemPrompt Tests (shared helper) ────────────

func TestBuildBoardSystemPrompt_Worker(t *testing.T) {
	prompt := BuildBoardSystemPrompt("my-board", "developer", "Build the UI", nil, "")
	if !strings.Contains(prompt, "Build the UI") || !strings.Contains(prompt, "my-board") ||
		!strings.Contains(prompt, "coral-board") || !strings.Contains(prompt, DefaultWorkerSystemPrompt) {
		t.Error("missing expected content in worker system prompt")
	}
}

func TestBuildBoardSystemPrompt_Orchestrator(t *testing.T) {
	prompt := BuildBoardSystemPrompt("my-board", "Orchestrator", "", nil, "")
	if !strings.Contains(prompt, DefaultOrchestratorSystemPrompt) {
		t.Error("expected orchestrator system prompt")
	}
}

func TestBuildBoardSystemPrompt_WithOverrides(t *testing.T) {
	overrides := map[string]string{"default_prompt_worker": "Custom worker instructions"}
	prompt := BuildBoardSystemPrompt("board1", "dev", "", overrides, "")
	if !strings.Contains(prompt, "Custom worker instructions") {
		t.Error("expected custom worker prompt override")
	}
}

func TestBuildBoardSystemPrompt_NoBoard(t *testing.T) {
	if prompt := BuildBoardSystemPrompt("", "", "", nil, ""); prompt != "" {
		t.Errorf("expected empty prompt without board, got %q", prompt)
	}
}

func TestBuildBoardSystemPrompt_PromptOnly(t *testing.T) {
	if prompt := BuildBoardSystemPrompt("", "", "Do something", nil, ""); prompt != "Do something" {
		t.Errorf("expected just the prompt, got %q", prompt)
	}
}

// ── Codex BuildLaunchCommand Tests ──────────────────────────

func TestCodex_BasicLaunch(t *testing.T) {
	a := &CodexAgent{}
	if cmd := a.BuildLaunchCommand(LaunchParams{}); cmd != "codex" {
		t.Errorf("expected bare 'codex', got %q", cmd)
	}
}

func TestCodex_Resume(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{ResumeSessionID: "resume-789"})
	if !strings.Contains(cmd, "codex resume resume-789") {
		t.Errorf("expected 'codex resume resume-789', got %q", cmd)
	}
}

func TestCodex_WithFlags(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{Flags: []string{"--json", "--model", "gpt-4"}})
	if !strings.Contains(cmd, "--json") || !strings.Contains(cmd, "--model gpt-4") {
		t.Errorf("expected flags, got %q", cmd)
	}
}

func TestCodex_WithPrompt(t *testing.T) {
	a := &CodexAgent{}
	sid := "codex-prompt-test1"
	a.BuildLaunchCommand(LaunchParams{SessionID: sid, Prompt: "Fix the bug"})
	promptFile := findTempFile(t, "codex_prompt", sid, "txt")
	content, _ := os.ReadFile(promptFile)
	if !strings.Contains(string(content), "Fix the bug") {
		t.Errorf("expected prompt in file, got %q", string(content))
	}
}

func TestCodex_WithBoardWorker(t *testing.T) {
	a := &CodexAgent{}
	sid := "codex-board-w1"
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID: sid, Prompt: "Build frontend", BoardName: "dev-board", Role: "developer",
	})
	if !strings.Contains(cmd, "-c developer_instructions=") {
		t.Errorf("expected -c developer_instructions flag, got %q", cmd)
	}
	sysFile := findTempFile(t, "codex_instructions", sid, "md")
	sysContent, _ := os.ReadFile(sysFile)
	if !strings.Contains(string(sysContent), "dev-board") {
		t.Errorf("expected board name in system instructions, got %q", string(sysContent))
	}
	promptFile := findTempFile(t, "codex_prompt", sid, "txt")
	content, _ := os.ReadFile(promptFile)
	if !strings.Contains(string(content), "Build frontend") {
		t.Errorf("expected prompt in action file, got %q", string(content))
	}
}

func TestCodex_WithBoardOrchestrator(t *testing.T) {
	a := &CodexAgent{}
	sid := "codex-board-o1"
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID: sid, Prompt: "Coordinate team", BoardName: "dev-board", Role: "orchestrator",
	})
	if !strings.Contains(cmd, "-c developer_instructions=") {
		t.Errorf("expected -c developer_instructions flag, got %q", cmd)
	}
	promptFile := findTempFile(t, "codex_prompt", sid, "txt")
	content, _ := os.ReadFile(promptFile)
	if !strings.Contains(string(content), "discuss your proposed plan") {
		t.Errorf("expected orchestrator action in prompt, got %q", string(content))
	}
}

func TestCodex_SystemPromptSeparation(t *testing.T) {
	a := &CodexAgent{}
	sid := "codex-sep-test1"
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID: sid, Prompt: "Do work", BoardName: "board1", Role: "dev",
	})
	findTempFile(t, "codex_instructions", sid, "md") // verifies existence
	findTempFile(t, "codex_prompt", sid, "txt")
	if !strings.Contains(cmd, "developer_instructions") || !strings.Contains(cmd, "codex_prompt") {
		t.Errorf("expected both file references in command, got %q", cmd)
	}
}

// ── Codex Permission Tests ─────────────────────────────────

func TestCodex_WithCapabilities_FullAuto(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{Capabilities: &Capabilities{Allow: []string{CapShell}}})
	if !strings.Contains(cmd, "--sandbox workspace-write") || !strings.Contains(cmd, "-a on-request") {
		t.Errorf("expected full-auto equivalent flags, got %q", cmd)
	}
	if strings.Contains(cmd, "--full-auto") {
		t.Errorf("should not use removed --full-auto alias, got %q", cmd)
	}
}

func TestCodex_WithCapabilities_BypassSandbox(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		Capabilities: &Capabilities{Allow: []string{CapShell, CapFileRead, CapFileWrite, CapGitWrite}},
	})
	if !strings.Contains(cmd, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("expected bypass sandbox, got %q", cmd)
	}
}

func TestCodex_WithCapabilities_ReadOnly(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{Capabilities: &Capabilities{Allow: []string{CapFileRead}}})
	if !strings.Contains(cmd, "--sandbox read-only") || !strings.Contains(cmd, "-a untrusted") {
		t.Errorf("expected read-only sandbox, got %q", cmd)
	}
}

func TestCodex_WithCapabilities_ReadWrite(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		Capabilities: &Capabilities{Allow: []string{CapFileRead, CapFileWrite}},
	})
	if !strings.Contains(cmd, "--sandbox workspace-write") || !strings.Contains(cmd, "-a untrusted") {
		t.Errorf("expected workspace-write sandbox, got %q", cmd)
	}
}

func TestCodex_WithCapabilities_ShellWithDeny(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		Capabilities: &Capabilities{Allow: []string{CapShell}, Deny: []string{CapGitWrite}},
	})
	if strings.Contains(cmd, "--full-auto") {
		t.Errorf("should not have --full-auto with deny list, got %q", cmd)
	}
	if !strings.Contains(cmd, "--sandbox workspace-write") {
		t.Errorf("expected workspace-write sandbox, got %q", cmd)
	}
}

func TestCodex_WithCapabilities_WebSearch(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		Capabilities: &Capabilities{Allow: []string{CapFileRead, CapWebAccess}},
	})
	if !strings.Contains(cmd, "--search") {
		t.Errorf("expected --search, got %q", cmd)
	}
}

func TestCodex_WithCapabilities_Nil(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{})
	if strings.Contains(cmd, "--full-auto") {
		t.Errorf("should not have --full-auto with nil capabilities, got %q", cmd)
	}
}

func TestCodex_ResumeWithPromptAndInstructions(t *testing.T) {
	a := &CodexAgent{}
	sid := "codex-resume-test1"
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID: sid, ResumeSessionID: "resume-abc", Prompt: "Continue work",
		BoardName: "board1", Role: "dev",
	})
	if !strings.Contains(cmd, "codex resume resume-abc") {
		t.Errorf("expected resume, got %q", cmd)
	}
	if !strings.Contains(cmd, "-c developer_instructions=") {
		t.Errorf("expected developer_instructions on resume, got %q", cmd)
	}
}

func TestCodex_FlagTranslation(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{Flags: []string{"--dangerously-skip-permissions"}})
	if strings.Contains(cmd, "--dangerously-skip-permissions") ||
		!strings.Contains(cmd, "--sandbox workspace-write") ||
		!strings.Contains(cmd, "-a on-request") {
		t.Errorf("expected flag translation, got %q", cmd)
	}

	cmd = a.BuildLaunchCommand(LaunchParams{Flags: []string{"--full-auto"}})
	if strings.Contains(cmd, "--full-auto") ||
		!strings.Contains(cmd, "--sandbox workspace-write") ||
		!strings.Contains(cmd, "-a on-request") {
		t.Errorf("expected --full-auto alias translation, got %q", cmd)
	}
}

func TestCodex_PermissionModeTranslation(t *testing.T) {
	tests := []struct {
		name    string
		params  LaunchParams
		want    []string
		notWant []string
	}{
		{
			name:    "setting bypass",
			params:  LaunchParams{PermissionMode: "bypassPermissions"},
			want:    []string{"--dangerously-bypass-approvals-and-sandbox"},
			notWant: []string{"--permission-mode", "bypassPermissions"},
		},
		{
			name:    "team flag bypass",
			params:  LaunchParams{Flags: []string{"--permission-mode", "bypassPermissions"}},
			want:    []string{"--dangerously-bypass-approvals-and-sandbox"},
			notWant: []string{"--permission-mode", "bypassPermissions"},
		},
		{
			name:    "team flag equals auto",
			params:  LaunchParams{Flags: []string{"--permission-mode=auto"}},
			want:    []string{"--sandbox workspace-write", "-a on-request"},
			notWant: []string{"--permission-mode"},
		},
		{
			name:    "plan",
			params:  LaunchParams{PermissionMode: "plan"},
			want:    []string{"--sandbox read-only", "-a untrusted"},
			notWant: []string{"--permission-mode", "plan"},
		},
		{
			name: "capabilities win over permission mode",
			params: LaunchParams{
				PermissionMode: "bypassPermissions",
				Flags:          []string{"--permission-mode", "auto"},
				Capabilities:   &Capabilities{Allow: []string{CapFileRead}},
			},
			want:    []string{"--sandbox read-only", "-a untrusted"},
			notWant: []string{"--dangerously-bypass-approvals-and-sandbox", "--full-auto", "--permission-mode"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &CodexAgent{}
			cmd := a.BuildLaunchCommand(tt.params)
			for _, want := range tt.want {
				if !strings.Contains(cmd, want) {
					t.Errorf("expected %q in %q", want, cmd)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(cmd, notWant) {
					t.Errorf("did not expect %q in %q", notWant, cmd)
				}
			}
		})
	}
}

// ── Gemini BuildLaunchCommand Tests ─────────────────────────

func TestGemini_BasicLaunch(t *testing.T) {
	a := &GeminiAgent{}
	if cmd := a.BuildLaunchCommand(LaunchParams{}); cmd != "gemini" {
		t.Errorf("expected bare 'gemini', got %q", cmd)
	}
}

func TestGemini_WithFlags(t *testing.T) {
	a := &GeminiAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{Flags: []string{"--verbose"}})
	if !strings.Contains(cmd, "--verbose") {
		t.Errorf("expected --verbose, got %q", cmd)
	}
}

func TestGemini_ResumeDisabled(t *testing.T) {
	a := &GeminiAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{ResumeSessionID: "some-id"})
	// Gemini resume is disabled — --resume should NOT appear
	if strings.Contains(cmd, "--resume") {
		t.Errorf("gemini should not have --resume flag (disabled), got %q", cmd)
	}
}

func TestGemini_WithPromptTempFile(t *testing.T) {
	a := &GeminiAgent{}
	sid := "gemini-prompt-test1"
	cmd := a.BuildLaunchCommand(LaunchParams{SessionID: sid, Prompt: "Analyze the codebase"})
	promptFile := findTempFile(t, "gemini_prompt", sid, "txt")
	content, _ := os.ReadFile(promptFile)
	if !strings.Contains(string(content), "Analyze the codebase") {
		t.Errorf("expected prompt in temp file, got %q", string(content))
	}
	if !strings.Contains(cmd, "gemini_prompt") {
		t.Errorf("expected gemini_prompt reference, got %q", cmd)
	}
}

func TestGemini_WithBoardWorker(t *testing.T) {
	a := &GeminiAgent{}
	sid := "gemini-board-w1"
	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID: sid, Prompt: "Build API", BoardName: "team-board", Role: "developer",
	})
	if !strings.Contains(cmd, "GEMINI_SYSTEM_MD=") {
		t.Errorf("expected GEMINI_SYSTEM_MD, got %q", cmd)
	}
	promptFile := findTempFile(t, "gemini_prompt", sid, "txt")
	content, _ := os.ReadFile(promptFile)
	if !strings.Contains(string(content), "team-board") {
		t.Errorf("expected board name in prompt, got %q", string(content))
	}
}

// ── Gemini Permission Tests ─────────────────────────────────

func TestGemini_WithCapabilities_Yolo(t *testing.T) {
	a := &GeminiAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		Capabilities: &Capabilities{Allow: []string{CapShell, CapFileWrite}},
	})
	if !strings.Contains(cmd, "--approval-mode yolo") {
		t.Errorf("expected yolo, got %q", cmd)
	}
}

func TestGemini_WithCapabilities_AutoEdit(t *testing.T) {
	a := &GeminiAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		Capabilities: &Capabilities{Allow: []string{CapShell, CapFileWrite}, Deny: []string{CapGitWrite}},
	})
	if !strings.Contains(cmd, "--approval-mode auto_edit") {
		t.Errorf("expected auto_edit, got %q", cmd)
	}
}

func TestGemini_WithCapabilities_Plan(t *testing.T) {
	a := &GeminiAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		Capabilities: &Capabilities{Allow: []string{CapFileRead}},
	})
	if !strings.Contains(cmd, "--approval-mode plan") {
		t.Errorf("expected plan, got %q", cmd)
	}
}

func TestGemini_WithCapabilities_ReadWrite(t *testing.T) {
	a := &GeminiAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		Capabilities: &Capabilities{Allow: []string{CapFileRead, CapFileWrite}},
	})
	if !strings.Contains(cmd, "--approval-mode auto_edit") {
		t.Errorf("expected auto_edit, got %q", cmd)
	}
}

func TestGemini_WithCapabilities_Nil(t *testing.T) {
	a := &GeminiAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{})
	if strings.Contains(cmd, "--approval-mode") {
		t.Errorf("should not have --approval-mode, got %q", cmd)
	}
}

func TestGemini_ResumeWithPermissions(t *testing.T) {
	a := &GeminiAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{
		ResumeSessionID: "resume-xyz",
		Capabilities:    &Capabilities{Allow: []string{CapShell, CapFileWrite}},
	})
	// Resume is disabled, but permissions should still be emitted
	if strings.Contains(cmd, "--resume") {
		t.Errorf("gemini should not have --resume flag (disabled), got %q", cmd)
	}
	if !strings.Contains(cmd, "--approval-mode yolo") {
		t.Errorf("expected yolo even without resume, got %q", cmd)
	}
}

// ── Permission Translation Tests ────────────────────────────

func TestTranslateToClaudePermissions_Nil(t *testing.T) {
	if TranslateToClaudePermissions(nil) != nil {
		t.Error("expected nil")
	}
}

func TestTranslateToClaudePermissions_Empty(t *testing.T) {
	if TranslateToClaudePermissions(&Capabilities{}) != nil {
		t.Error("expected nil")
	}
}

func TestTranslateToClaudePermissions_FileRead(t *testing.T) {
	result := TranslateToClaudePermissions(&Capabilities{Allow: []string{CapFileRead}})
	if result == nil {
		t.Fatal("expected non-nil")
	}
	allowStr := strings.Join(result.Allow, ",")
	for _, tool := range []string{"Read", "Glob", "Grep"} {
		if !strings.Contains(allowStr, tool) {
			t.Errorf("missing %q in allow list", tool)
		}
	}
}

func TestTranslateToClaudePermissions_Shell(t *testing.T) {
	result := TranslateToClaudePermissions(&Capabilities{Allow: []string{CapShell}})
	if result == nil {
		t.Fatal("expected non-nil")
	}
	found := false
	for _, a := range result.Allow {
		if a == "Bash" {
			found = true
		}
	}
	if !found {
		t.Error("expected Bash in allow list")
	}
}

func TestTranslateToClaudePermissions_DenyList(t *testing.T) {
	result := TranslateToClaudePermissions(&Capabilities{Allow: []string{CapFileRead}, Deny: []string{CapShell}})
	if result == nil {
		t.Fatal("expected non-nil")
	}
	found := false
	for _, d := range result.Deny {
		if d == "Bash" {
			found = true
		}
	}
	if !found {
		t.Error("expected Bash in deny list")
	}
}

func TestTranslateToClaudePermissions_DenyShellWithShellPatternAllow(t *testing.T) {
	// When deny has 'shell' but allow has shell:<pattern>, blanket Bash deny
	// must be skipped so the pattern-based allow isn't overridden.
	result := TranslateToClaudePermissions(&Capabilities{
		Allow: []string{CapFileRead, "shell:coral-board *"},
		Deny:  []string{CapShell},
	})
	if result == nil {
		t.Fatal("expected non-nil")
	}

	// Bash should NOT be in deny — blanket deny would override the pattern allow
	for _, d := range result.Deny {
		if d == "Bash" {
			t.Error("Bash should not be in deny when shell:<pattern> is in allow")
		}
	}

	// Bash(coral-board *) should be in allow
	allowStr := strings.Join(result.Allow, ",")
	if !strings.Contains(allowStr, "Bash(coral-board *)") {
		t.Errorf("expected Bash(coral-board *) in allow, got %q", allowStr)
	}
}

func TestTranslateToClaudePermissions_AllCapabilities(t *testing.T) {
	result := TranslateToClaudePermissions(&Capabilities{
		Allow: []string{CapFileRead, CapFileWrite, CapShell, CapWebAccess, CapGitWrite, CapAgentSpawn, CapNotebook},
	})
	if result == nil {
		t.Fatal("expected non-nil")
	}
	allowStr := strings.Join(result.Allow, ",")
	for _, tool := range []string{"Read", "Write", "Bash", "WebFetch", "Agent", "NotebookEdit", "Bash(git push *)"} {
		if !strings.Contains(allowStr, tool) {
			t.Errorf("missing %q", tool)
		}
	}
}

// ── Codex Permission Translation Tests ──────────────────────

func TestTranslateToCodexPermissions_Nil(t *testing.T) {
	if TranslateToCodexPermissions(nil) != nil {
		t.Error("expected nil")
	}
}

func TestTranslateToCodexPermissions_Empty(t *testing.T) {
	if TranslateToCodexPermissions(&Capabilities{}) != nil {
		t.Error("expected nil")
	}
}

func TestTranslateToCodexPermissions_ShellFullAuto(t *testing.T) {
	result := TranslateToCodexPermissions(&Capabilities{Allow: []string{CapShell}})
	if result == nil || !result.FullAuto {
		t.Error("expected FullAuto=true")
	}
}

func TestTranslateToCodexPermissions_ShellWithDenyNotFullAuto(t *testing.T) {
	result := TranslateToCodexPermissions(&Capabilities{Allow: []string{CapShell}, Deny: []string{CapGitWrite}})
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if result.FullAuto || result.SandboxMode != "workspace-write" || result.ApprovalPolicy != "untrusted" {
		t.Errorf("unexpected: %+v", result)
	}
}

func TestTranslateToCodexPermissions_ReadOnly(t *testing.T) {
	result := TranslateToCodexPermissions(&Capabilities{Allow: []string{CapFileRead}})
	if result == nil || result.SandboxMode != "read-only" || result.ApprovalPolicy != "untrusted" {
		t.Errorf("unexpected: %+v", result)
	}
}

func TestTranslateToCodexPermissions_FullAccess(t *testing.T) {
	result := TranslateToCodexPermissions(&Capabilities{
		Allow: []string{CapShell, CapFileRead, CapFileWrite, CapGitWrite},
	})
	if result == nil || !result.BypassSandbox {
		t.Error("expected BypassSandbox=true")
	}
}

func TestTranslateToCodexPermissions_WebSearch(t *testing.T) {
	result := TranslateToCodexPermissions(&Capabilities{Allow: []string{CapFileRead, CapWebAccess}})
	if result == nil || !result.Search {
		t.Error("expected Search=true")
	}
}

func TestTranslateToCodexPermissions_WebOnlyIsReadOnly(t *testing.T) {
	result := TranslateToCodexPermissions(&Capabilities{Allow: []string{CapWebAccess}})
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if !result.Search {
		t.Error("expected Search=true")
	}
	if result.SandboxMode != "read-only" || result.ApprovalPolicy != "untrusted" {
		t.Errorf("unexpected: %+v", result)
	}
}

// ── Gemini Permission Translation Tests ─────────────────────

func TestTranslateToGeminiPermissions_Nil(t *testing.T) {
	if TranslateToGeminiPermissions(nil) != nil {
		t.Error("expected nil")
	}
}

func TestTranslateToGeminiPermissions_Empty(t *testing.T) {
	if TranslateToGeminiPermissions(&Capabilities{}) != nil {
		t.Error("expected nil")
	}
}

func TestTranslateToGeminiPermissions_Yolo(t *testing.T) {
	result := TranslateToGeminiPermissions(&Capabilities{Allow: []string{CapShell, CapFileWrite}})
	if result == nil || result.ApprovalMode != "yolo" {
		t.Error("expected yolo")
	}
}

func TestTranslateToGeminiPermissions_AutoEdit(t *testing.T) {
	result := TranslateToGeminiPermissions(&Capabilities{
		Allow: []string{CapShell, CapFileWrite}, Deny: []string{CapGitWrite},
	})
	if result == nil || result.ApprovalMode != "auto_edit" {
		t.Error("expected auto_edit")
	}
}

func TestTranslateToGeminiPermissions_Plan(t *testing.T) {
	result := TranslateToGeminiPermissions(&Capabilities{Allow: []string{CapFileRead}})
	if result == nil || result.ApprovalMode != "plan" {
		t.Error("expected plan")
	}
}

func TestTranslateToGeminiPermissions_Default(t *testing.T) {
	result := TranslateToGeminiPermissions(&Capabilities{Allow: []string{CapWebAccess}})
	if result == nil || result.ApprovalMode != "default" {
		t.Error("expected default")
	}
}

func TestTranslateToClaudePermissions_ShellPattern(t *testing.T) {
	result := TranslateToClaudePermissions(&Capabilities{Allow: []string{"shell:npm test"}})
	if result == nil {
		t.Fatal("expected non-nil")
	}
	allowStr := strings.Join(result.Allow, ",")
	if !strings.Contains(allowStr, "Bash(npm test)") {
		t.Errorf("expected shell pattern translation, got %q", allowStr)
	}
}

// ── TranslatePermissions Dispatcher Tests ───────────────────

func TestTranslatePermissions_Claude(t *testing.T) {
	if _, ok := TranslatePermissions("claude", &Capabilities{Allow: []string{CapFileRead}}).(*ClaudePermissions); !ok {
		t.Error("expected *ClaudePermissions")
	}
}

func TestTranslatePermissions_Codex(t *testing.T) {
	cp, ok := TranslatePermissions("codex", &Capabilities{Allow: []string{CapShell}}).(*CodexPermissions)
	if !ok || !cp.FullAuto {
		t.Error("expected CodexPermissions with FullAuto")
	}
}

func TestTranslatePermissions_Gemini(t *testing.T) {
	if _, ok := TranslatePermissions("gemini", &Capabilities{Allow: []string{CapFileRead}}).(*GeminiPermissions); !ok {
		t.Error("expected *GeminiPermissions")
	}
}

func TestTranslatePermissions_NilCaps(t *testing.T) {
	if cp, ok := TranslatePermissions("claude", nil).(*ClaudePermissions); ok && cp != nil {
		t.Error("expected nil inner value")
	}
}

// ── Capabilities.IsEmpty Tests ──────────────────────────────

func TestCapabilities_IsEmpty(t *testing.T) {
	if !((*Capabilities)(nil)).IsEmpty() {
		t.Error("nil should be empty")
	}
	if !(&Capabilities{}).IsEmpty() {
		t.Error("zero-value should be empty")
	}
	if (&Capabilities{Allow: []string{"x"}}).IsEmpty() {
		t.Error("with Allow should not be empty")
	}
}

// ── Presets Tests ───────────────────────────────────────────

func TestPresets_Exist(t *testing.T) {
	for _, name := range []string{"lead_dev", "qa", "frontend_dev", "orchestrator", "devops", "read_only", "full_access"} {
		if _, ok := Presets[name]; !ok {
			t.Errorf("missing preset %q", name)
		}
	}
}

func TestPresets_FullAccessHasAll(t *testing.T) {
	fa := Presets["full_access"]
	allowSet := map[string]bool{}
	for _, a := range fa.Allow {
		allowSet[a] = true
	}
	for _, cap := range []string{CapFileRead, CapFileWrite, CapShell, CapGitWrite, CapAgentSpawn, CapWebAccess, CapNotebook} {
		if !allowSet[cap] {
			t.Errorf("full_access missing %s", cap)
		}
	}
}

func TestPresetTranslations(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		preset    string
		check     func(t *testing.T, got any)
	}{
		{
			name:      "qa claude",
			agentType: "claude",
			preset:    "qa",
			check: func(t *testing.T, got any) {
				t.Helper()
				perms, ok := got.(*ClaudePermissions)
				if !ok || perms == nil {
					t.Fatalf("expected *ClaudePermissions, got %T", got)
				}
				allowStr := strings.Join(perms.Allow, ",")
				if !strings.Contains(allowStr, "Read") {
					t.Fatalf("expected read tools, got %+v", perms.Allow)
				}
				if !strings.Contains(allowStr, "Bash") {
					t.Fatalf("expected Bash in allow, got %+v", perms.Allow)
				}
				if len(perms.Deny) > 0 {
					t.Fatalf("expected no deny list, got %+v", perms.Deny)
				}
			},
		},
		{
			name:      "orchestrator codex",
			agentType: "codex",
			preset:    "orchestrator",
			check: func(t *testing.T, got any) {
				t.Helper()
				perms, ok := got.(*CodexPermissions)
				if !ok || perms == nil {
					t.Fatalf("expected *CodexPermissions, got %T", got)
				}
				if !perms.BypassSandbox || !perms.Search {
					t.Fatalf("unexpected codex perms: %+v", perms)
				}
			},
		},
		{
			name:      "frontend gemini",
			agentType: "gemini",
			preset:    "frontend_dev",
			check: func(t *testing.T, got any) {
				t.Helper()
				perms, ok := got.(*GeminiPermissions)
				if !ok || perms == nil {
					t.Fatalf("expected *GeminiPermissions, got %T", got)
				}
				if perms.ApprovalMode != "auto_edit" {
					t.Fatalf("unexpected gemini perms: %+v", perms)
				}
			},
		},
		{
			name:      "full access codex",
			agentType: "codex",
			preset:    "full_access",
			check: func(t *testing.T, got any) {
				t.Helper()
				perms, ok := got.(*CodexPermissions)
				if !ok || perms == nil {
					t.Fatalf("expected *CodexPermissions, got %T", got)
				}
				if !perms.BypassSandbox || !perms.Search {
					t.Fatalf("unexpected codex perms: %+v", perms)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, TranslatePermissions(tt.agentType, Presets[tt.preset]))
		})
	}
}

// ── SanitizeShellValue Tests ────────────────────────────────

func TestSanitizeShellValue_Clean(t *testing.T) {
	if got := SanitizeShellValue("my-session_123"); got != "my-session_123" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeShellValue_StripsDangerousChars(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{`$(whoami)`, "whoami"},
		{"`id`", "id"},
		{`role"; rm -rf /; echo "`, "role rm -rf  echo "},
		{"a'b", "ab"},
		{"a;b", "ab"},
		{"a|b", "ab"},
		{"a&b", "ab"},
	}
	for _, tt := range tests {
		if got := SanitizeShellValue(tt.input); got != tt.expected {
			t.Errorf("SanitizeShellValue(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestCodex_EnvVarsExported(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{SessionName: "codex-abc123", Role: "developer"})
	if !strings.Contains(cmd, "export CORAL_SESSION_NAME='codex-abc123' &&") {
		t.Errorf("expected exported single-quoted session name, got %q", cmd)
	}
	if !strings.Contains(cmd, "export CORAL_SUBSCRIBER_ID='developer' &&") {
		t.Errorf("expected exported single-quoted role, got %q", cmd)
	}
}

func TestGemini_EnvVarsExported(t *testing.T) {
	a := &GeminiAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{SessionName: "gemini-xyz789", Role: "qa"})
	if !strings.Contains(cmd, "export CORAL_SESSION_NAME='gemini-xyz789' &&") {
		t.Errorf("expected exported single-quoted session name, got %q", cmd)
	}
	if !strings.Contains(cmd, "export CORAL_SUBSCRIBER_ID='qa' &&") {
		t.Errorf("expected exported single-quoted role, got %q", cmd)
	}
}

func TestCodex_EnvVarsSanitized(t *testing.T) {
	a := &CodexAgent{}
	cmd := a.BuildLaunchCommand(LaunchParams{SessionName: `$(evil)`, Role: "`whoami`"})
	if strings.Contains(cmd, "$") || strings.Contains(cmd, "`") {
		t.Errorf("expected sanitized, got %q", cmd)
	}
}

// ── Temp File Tests ─────────────────────────────────────────

func TestWriteTempFile_CreatesFile(t *testing.T) {
	path := writeTempFile("test", "session123", "txt", []byte("hello"))
	defer os.Remove(path)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("got %q", string(content))
	}
	if !strings.Contains(path, "session123") {
		t.Errorf("expected session ID in path, got %q", path)
	}
}

func TestCleanupTempFiles(t *testing.T) {
	sid := "cleanup-test-sid"
	p1 := writeTempFile("prompt", sid, "txt", []byte("prompt"))
	p2 := writeTempFile("instructions", sid, "md", []byte("instructions"))
	CleanupTempFiles(sid)
	if _, err := os.Stat(p1); !os.IsNotExist(err) {
		t.Error("prompt file should be removed")
	}
	if _, err := os.Stat(p2); !os.IsNotExist(err) {
		t.Error("instructions file should be removed")
	}
}

func TestCleanupTempFiles_EmptySessionID(t *testing.T) {
	CleanupTempFiles("") // should not panic
}

// ── Env Deep-Merge Tests ──────────────────────────────────────

// writeSettingsFile creates a settings JSON file in the given directory.
func writeSettingsFile(t *testing.T, dir, filename string, content map[string]interface{}) {
	t.Helper()
	data, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal settings: %v", err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create dir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0644); err != nil {
		t.Fatalf("failed to write %s: %v", filename, err)
	}
}

func TestBuildMergedSettings_EnvDeepMerge_GlobalAndProject(t *testing.T) {
	tmpDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSettingsFile(t, filepath.Join(home, ".claude"), "settings.json", map[string]interface{}{
		"env": map[string]interface{}{
			"AWS_REGION":              "us-east-1",
			"CLAUDE_CODE_USE_BEDROCK": "1",
		},
	})
	writeSettingsFile(t, filepath.Join(tmpDir, ".claude"), "settings.json", map[string]interface{}{
		"env": map[string]interface{}{
			"ANTHROPIC_API_KEY": "sk-test-key",
		},
	})

	merged := buildMergedSettings(tmpDir, nil)
	env, ok := merged["env"].(map[string]interface{})
	if !ok {
		t.Fatal("expected env map in merged settings")
	}
	if env["AWS_REGION"] != "us-east-1" {
		t.Errorf("AWS_REGION lost during merge, got %v", env["AWS_REGION"])
	}
	if env["CLAUDE_CODE_USE_BEDROCK"] != "1" {
		t.Errorf("CLAUDE_CODE_USE_BEDROCK lost during merge, got %v", env["CLAUDE_CODE_USE_BEDROCK"])
	}
	if env["ANTHROPIC_API_KEY"] != "sk-test-key" {
		t.Errorf("ANTHROPIC_API_KEY missing, got %v", env["ANTHROPIC_API_KEY"])
	}
}

func TestBuildMergedSettings_EnvDeepMerge_ProjectOverridesGlobal(t *testing.T) {
	tmpDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSettingsFile(t, filepath.Join(home, ".claude"), "settings.json", map[string]interface{}{
		"env": map[string]interface{}{
			"AWS_REGION":      "us-east-1",
			"ANTHROPIC_MODEL": "claude-3-opus",
		},
	})
	writeSettingsFile(t, filepath.Join(tmpDir, ".claude"), "settings.json", map[string]interface{}{
		"env": map[string]interface{}{
			"AWS_REGION": "eu-west-1",
		},
	})

	merged := buildMergedSettings(tmpDir, nil)
	env := merged["env"].(map[string]interface{})
	if env["AWS_REGION"] != "eu-west-1" {
		t.Errorf("project should override global AWS_REGION, got %v", env["AWS_REGION"])
	}
	if env["ANTHROPIC_MODEL"] != "claude-3-opus" {
		t.Errorf("global ANTHROPIC_MODEL should be preserved, got %v", env["ANTHROPIC_MODEL"])
	}
}

func TestBuildMergedSettings_EnvDeepMerge_LocalOverridesProject(t *testing.T) {
	tmpDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSettingsFile(t, filepath.Join(tmpDir, ".claude"), "settings.json", map[string]interface{}{
		"env": map[string]interface{}{
			"AWS_REGION":      "us-east-1",
			"ANTHROPIC_MODEL": "claude-3-opus",
		},
	})
	writeSettingsFile(t, filepath.Join(tmpDir, ".claude"), "settings.local.json", map[string]interface{}{
		"env": map[string]interface{}{
			"AWS_REGION": "ap-southeast-1",
		},
	})

	merged := buildMergedSettings(tmpDir, nil)
	env := merged["env"].(map[string]interface{})
	if env["AWS_REGION"] != "ap-southeast-1" {
		t.Errorf("local should override project AWS_REGION, got %v", env["AWS_REGION"])
	}
	if env["ANTHROPIC_MODEL"] != "claude-3-opus" {
		t.Errorf("project ANTHROPIC_MODEL should be preserved, got %v", env["ANTHROPIC_MODEL"])
	}
}

func TestBuildMergedSettings_EnvDeepMerge_EmptyEnvDoesNotWipe(t *testing.T) {
	tmpDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSettingsFile(t, filepath.Join(home, ".claude"), "settings.json", map[string]interface{}{
		"env": map[string]interface{}{
			"AWS_REGION": "us-east-1",
		},
	})
	writeSettingsFile(t, filepath.Join(tmpDir, ".claude"), "settings.json", map[string]interface{}{
		"env": map[string]interface{}{},
	})

	merged := buildMergedSettings(tmpDir, nil)
	env, ok := merged["env"].(map[string]interface{})
	if !ok {
		t.Fatal("expected env map in merged settings")
	}
	if env["AWS_REGION"] != "us-east-1" {
		t.Errorf("empty project env should not wipe global, got %v", env["AWS_REGION"])
	}
}

func TestBuildMergedSettings_EnvDeepMerge_ThreeLevelPrecedence(t *testing.T) {
	tmpDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSettingsFile(t, filepath.Join(home, ".claude"), "settings.json", map[string]interface{}{
		"env": map[string]interface{}{
			"VAR_A": "global-a",
			"VAR_B": "global-b",
			"VAR_C": "global-c",
		},
	})
	writeSettingsFile(t, filepath.Join(tmpDir, ".claude"), "settings.json", map[string]interface{}{
		"env": map[string]interface{}{
			"VAR_B": "project-b",
			"VAR_D": "project-d",
		},
	})
	writeSettingsFile(t, filepath.Join(tmpDir, ".claude"), "settings.local.json", map[string]interface{}{
		"env": map[string]interface{}{
			"VAR_C": "local-c",
			"VAR_E": "local-e",
		},
	})

	merged := buildMergedSettings(tmpDir, nil)
	env := merged["env"].(map[string]interface{})

	expected := map[string]string{
		"VAR_A": "global-a",
		"VAR_B": "project-b",
		"VAR_C": "local-c",
		"VAR_D": "project-d",
		"VAR_E": "local-e",
	}
	for k, want := range expected {
		if got := env[k]; got != want {
			t.Errorf("env[%q] = %v, want %q", k, got, want)
		}
	}
}

func TestBuildMergedSettings_EnvDeepMerge_NoEnvBlocks(t *testing.T) {
	tmpDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSettingsFile(t, filepath.Join(home, ".claude"), "settings.json", map[string]interface{}{
		"permissions": map[string]interface{}{"allow": []string{"Read"}},
	})

	merged := buildMergedSettings(tmpDir, nil)
	if _, ok := merged["env"]; ok {
		t.Error("should not have env key when no sources define env")
	}
}

// ── Provider Detection: Env Precedence Tests ─────────────────

func TestDetectUpstreamURL_MergedEnvTakesPrecedenceOverOS(t *testing.T) {
	// Merged env should take priority over os.Getenv()
	t.Setenv("ANTHROPIC_BASE_URL", "https://os-level-gateway.example.com")
	env := map[string]interface{}{
		"ANTHROPIC_BASE_URL": "https://settings-gateway.example.com",
	}
	info := DetectUpstreamURL(env)
	if info.UpstreamURL != "https://settings-gateway.example.com" {
		t.Errorf("merged env should take precedence over os env, got %q", info.UpstreamURL)
	}
}

func TestDetectUpstreamURL_OSEnvFallback(t *testing.T) {
	// When merged env is empty, falls back to os.Getenv()
	t.Setenv("ANTHROPIC_BASE_URL", "https://os-level-gateway.example.com")
	info := DetectUpstreamURL(map[string]interface{}{})
	if info.Provider != "anthropic" {
		t.Errorf("expected provider 'anthropic', got %q", info.Provider)
	}
	if info.UpstreamURL != "https://os-level-gateway.example.com" {
		t.Errorf("should fall back to os env, got %q", info.UpstreamURL)
	}
}

func TestDetectUpstreamURL_BedrockOSEnvFallback(t *testing.T) {
	// Bedrock via OS env only (not in settings)
	t.Setenv("ANTHROPIC_BEDROCK_BASE_URL", "https://bedrock-runtime.us-west-2.amazonaws.com")
	info := DetectUpstreamURL(map[string]interface{}{})
	if info.Provider != "bedrock" {
		t.Errorf("expected bedrock from OS env fallback, got %q", info.Provider)
	}
	if info.UpstreamURL != "https://bedrock-runtime.us-west-2.amazonaws.com" {
		t.Errorf("expected bedrock URL from OS env, got %q", info.UpstreamURL)
	}
}

func TestDetectUpstreamURL_BedrockModeViaOSEnv(t *testing.T) {
	// CLAUDE_CODE_USE_BEDROCK=1 via OS env with AWS_REGION in merged env
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	env := map[string]interface{}{
		"AWS_REGION": "ap-northeast-1",
	}
	info := DetectUpstreamURL(env)
	if info.Provider != "bedrock" {
		t.Errorf("expected bedrock, got %q", info.Provider)
	}
	if info.UpstreamURL != "https://bedrock-runtime.ap-northeast-1.amazonaws.com" {
		t.Errorf("expected bedrock URL with ap-northeast-1, got %q", info.UpstreamURL)
	}
}

func TestDetectUpstreamURL_VertexPrecedenceOverAnthropic(t *testing.T) {
	// Vertex should take precedence over direct Anthropic
	env := map[string]interface{}{
		"ANTHROPIC_VERTEX_BASE_URL": "https://us-central1-aiplatform.googleapis.com",
		"ANTHROPIC_BASE_URL":        "https://api.anthropic.com",
	}
	info := DetectUpstreamURL(env)
	if info.Provider != "vertex" {
		t.Errorf("vertex should take precedence over anthropic, got %q", info.Provider)
	}
}

func TestDetectUpstreamURL_AnthropicPrecedenceOverOpenAI(t *testing.T) {
	// Direct Anthropic should take precedence over OpenAI
	env := map[string]interface{}{
		"ANTHROPIC_BASE_URL": "https://custom-anthropic.example.com",
		"OPENAI_BASE_URL":    "https://api.openai.com",
	}
	info := DetectUpstreamURL(env)
	if info.Provider != "anthropic" {
		t.Errorf("anthropic should take precedence over openai, got %q", info.Provider)
	}
}

// ── Shell Detection Tests ───────────────────────────────────

func TestClassifyShell(t *testing.T) {
	tests := []struct {
		input    string
		expected ShellType
	}{
		{"/bin/bash", ShellBash}, {"/bin/zsh", ShellZsh}, {"zsh", ShellZsh},
		{"pwsh", ShellPowerShell}, {"powershell.exe", ShellPowerShell},
		{"cmd", ShellCmd}, {"cmd.exe", ShellCmd}, {"sh", ShellBash},
	}
	for _, tt := range tests {
		if got := classifyShell(tt.input); got != tt.expected {
			t.Errorf("classifyShell(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// ── DetectUpstreamURL Tests ───────────────────────────────────

func TestDetectUpstreamURL_Default(t *testing.T) {
	// Clear env vars that would interfere with default detection
	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_BEDROCK_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "ANTHROPIC_VERTEX_BASE_URL", "CLAUDE_CODE_USE_VERTEX", "OPENAI_BASE_URL"} {
		t.Setenv(key, "")
	}
	info := DetectUpstreamURL(map[string]interface{}{})
	if info.Provider != "anthropic" || info.UpstreamURL != "https://api.anthropic.com" {
		t.Errorf("expected default anthropic, got %+v", info)
	}
}

func TestDetectUpstreamURL_BedrockBaseURL(t *testing.T) {
	env := map[string]interface{}{
		"ANTHROPIC_BEDROCK_BASE_URL": "https://bedrock-runtime.us-west-2.amazonaws.com",
	}
	info := DetectUpstreamURL(env)
	if info.Provider != "bedrock" || info.UpstreamURL != "https://bedrock-runtime.us-west-2.amazonaws.com" {
		t.Errorf("expected bedrock, got %+v", info)
	}
}

func TestDetectUpstreamURL_BedrockFlag(t *testing.T) {
	env := map[string]interface{}{
		"CLAUDE_CODE_USE_BEDROCK": "1",
		"AWS_REGION":              "eu-west-1",
	}
	info := DetectUpstreamURL(env)
	if info.Provider != "bedrock" || info.UpstreamURL != "https://bedrock-runtime.eu-west-1.amazonaws.com" {
		t.Errorf("expected bedrock eu-west-1, got %+v", info)
	}
}

func TestDetectUpstreamURL_BedrockFlagDefaultRegion(t *testing.T) {
	env := map[string]interface{}{
		"CLAUDE_CODE_USE_BEDROCK": "1",
	}
	info := DetectUpstreamURL(env)
	if info.Provider != "bedrock" || !strings.Contains(info.UpstreamURL, "us-east-1") {
		t.Errorf("expected bedrock us-east-1 default, got %+v", info)
	}
}

func TestDetectUpstreamURL_VertexBaseURL(t *testing.T) {
	env := map[string]interface{}{
		"ANTHROPIC_VERTEX_BASE_URL": "https://us-east5-aiplatform.googleapis.com",
	}
	info := DetectUpstreamURL(env)
	if info.Provider != "vertex" || info.UpstreamURL != "https://us-east5-aiplatform.googleapis.com" {
		t.Errorf("expected vertex, got %+v", info)
	}
}

func TestDetectUpstreamURL_VertexFlag(t *testing.T) {
	env := map[string]interface{}{
		"CLAUDE_CODE_USE_VERTEX": "1",
	}
	info := DetectUpstreamURL(env)
	if info.Provider != "vertex" {
		t.Errorf("expected vertex provider, got %+v", info)
	}
}

func TestDetectUpstreamURL_CustomAnthropicBase(t *testing.T) {
	env := map[string]interface{}{
		"ANTHROPIC_BASE_URL": "https://my-gateway.corp.com",
	}
	info := DetectUpstreamURL(env)
	if info.Provider != "anthropic" || info.UpstreamURL != "https://my-gateway.corp.com" {
		t.Errorf("expected custom anthropic, got %+v", info)
	}
}

func TestDetectUpstreamURL_OpenAI(t *testing.T) {
	// Clear ANTHROPIC_BASE_URL so it doesn't take priority over OpenAI
	t.Setenv("ANTHROPIC_BASE_URL", "")
	env := map[string]interface{}{
		"OPENAI_BASE_URL": "https://api.openai.com",
	}
	info := DetectUpstreamURL(env)
	if info.Provider != "openai" || info.UpstreamURL != "https://api.openai.com" {
		t.Errorf("expected openai, got %+v", info)
	}
}

func TestDetectUpstreamURL_BedrockTakesPriority(t *testing.T) {
	env := map[string]interface{}{
		"ANTHROPIC_BEDROCK_BASE_URL": "https://bedrock.us-east-1.amazonaws.com",
		"ANTHROPIC_BASE_URL":         "https://api.anthropic.com",
		"OPENAI_BASE_URL":            "https://api.openai.com",
	}
	info := DetectUpstreamURL(env)
	if info.Provider != "bedrock" {
		t.Errorf("expected bedrock to take priority, got %+v", info)
	}
}

// ── Env Deep-Merge Tests ──────────────────────────────────────

func TestBuildMergedSettings_EnvDeepMerge(t *testing.T) {
	tmpDir := t.TempDir()

	// Create global settings with AWS env vars
	globalDir := filepath.Join(tmpDir, "global")
	os.MkdirAll(globalDir, 0755)
	globalSettings := map[string]interface{}{
		"env": map[string]interface{}{
			"AWS_REGION":              "us-east-1",
			"CLAUDE_CODE_USE_BEDROCK": "1",
		},
	}
	globalData, _ := json.Marshal(globalSettings)
	os.WriteFile(filepath.Join(globalDir, "settings.json"), globalData, 0644)

	// Create project settings with a different env var
	projectClaudeDir := filepath.Join(tmpDir, "project", ".claude")
	os.MkdirAll(projectClaudeDir, 0755)
	projectSettings := map[string]interface{}{
		"env": map[string]interface{}{
			"ANTHROPIC_API_KEY": "sk-test-key",
		},
	}
	projectData, _ := json.Marshal(projectSettings)
	os.WriteFile(filepath.Join(projectClaudeDir, "settings.json"), projectData, 0644)

	// We can't easily test buildMergedSettings with custom home dir,
	// so test the merge logic directly by calling readSettingsFile and merging.
	global := readSettingsFile(filepath.Join(globalDir, "settings.json"))
	project := readSettingsFile(filepath.Join(projectClaudeDir, "settings.json"))

	// Simulate the deep-merge logic
	mergedEnv := make(map[string]interface{})
	for _, source := range []map[string]interface{}{global, project} {
		if env, ok := source["env"].(map[string]interface{}); ok {
			for k, v := range env {
				mergedEnv[k] = v
			}
		}
	}

	// Verify all env vars are present (deep merge)
	if mergedEnv["AWS_REGION"] != "us-east-1" {
		t.Errorf("expected AWS_REGION from global, got %v", mergedEnv["AWS_REGION"])
	}
	if mergedEnv["CLAUDE_CODE_USE_BEDROCK"] != "1" {
		t.Errorf("expected CLAUDE_CODE_USE_BEDROCK from global, got %v", mergedEnv["CLAUDE_CODE_USE_BEDROCK"])
	}
	if mergedEnv["ANTHROPIC_API_KEY"] != "sk-test-key" {
		t.Errorf("expected ANTHROPIC_API_KEY from project, got %v", mergedEnv["ANTHROPIC_API_KEY"])
	}
}

func TestBuildMergedSettings_EnvProjectOverridesGlobal(t *testing.T) {
	// Verify that project-level env var overrides global
	global := map[string]interface{}{
		"env": map[string]interface{}{
			"AWS_REGION": "us-east-1",
			"FOO":        "global-val",
		},
	}
	project := map[string]interface{}{
		"env": map[string]interface{}{
			"FOO": "project-val",
		},
	}

	mergedEnv := make(map[string]interface{})
	for _, source := range []map[string]interface{}{global, project} {
		if env, ok := source["env"].(map[string]interface{}); ok {
			for k, v := range env {
				mergedEnv[k] = v
			}
		}
	}

	if mergedEnv["AWS_REGION"] != "us-east-1" {
		t.Error("global AWS_REGION should be preserved")
	}
	if mergedEnv["FOO"] != "project-val" {
		t.Errorf("project FOO should override global, got %v", mergedEnv["FOO"])
	}
}

// ── Hooks Merge Tests ─────────────────────────────────────────

func TestBuildMergedSettings_AgentHooksAppended(t *testing.T) {
	tmpDir := t.TempDir()

	agentHooks := map[string]interface{}{
		"PostToolUse": []interface{}{
			map[string]interface{}{
				"matcher": "Write",
				"hooks": []interface{}{
					map[string]interface{}{"type": "command", "command": "echo wrote"},
				},
			},
		},
		"Stop": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{"type": "command", "command": "curl webhook"},
				},
			},
		},
	}

	merged := buildMergedSettings(tmpDir, agentHooks)
	hooks, ok := merged["hooks"].(map[string][]interface{})
	if !ok {
		t.Fatal("merged settings should contain hooks map")
	}

	// PostToolUse should have Coral hooks + our agent hook
	ptGroups := hooks["PostToolUse"]
	found := false
	for _, g := range ptGroups {
		if gMap, ok := g.(map[string]interface{}); ok {
			if gMap["matcher"] == "Write" {
				found = true
			}
		}
	}
	if !found {
		t.Error("agent PostToolUse hook with matcher 'Write' not found in merged hooks")
	}

	// Coral system hooks should still be present
	coralFound := false
	for _, g := range ptGroups {
		if gMap, ok := g.(map[string]interface{}); ok {
			if hooks, ok := gMap["hooks"].([]map[string]interface{}); ok {
				for _, h := range hooks {
					if h["command"] == "coral-hook-agentic-state" {
						coralFound = true
					}
				}
			}
		}
	}
	if !coralFound {
		t.Error("Coral system hook 'coral-hook-agentic-state' missing from merged PostToolUse hooks")
	}

	// Stop should have both Coral hook and agent hook
	stopGroups := hooks["Stop"]
	if len(stopGroups) < 2 {
		t.Errorf("Stop should have at least 2 groups (coral + agent), got %d", len(stopGroups))
	}
}

func TestBuildMergedSettings_NilAgentHooks(t *testing.T) {
	tmpDir := t.TempDir()

	merged := buildMergedSettings(tmpDir, nil)
	hooks, ok := merged["hooks"].(map[string][]interface{})
	if !ok {
		t.Fatal("merged settings should contain hooks map")
	}
	if len(hooks["PostToolUse"]) == 0 {
		t.Error("Coral PostToolUse hooks should still be present with nil agent hooks")
	}
}

func TestBuildLaunchCommand_HooksInSettings(t *testing.T) {
	tmpDir := t.TempDir()
	a := &ClaudeAgent{}

	hooks := map[string]interface{}{
		"Stop": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{"type": "command", "command": "echo done"},
				},
			},
		},
	}

	cmd := a.BuildLaunchCommand(LaunchParams{
		SessionID:  "test-hooks-session",
		WorkingDir: tmpDir,
		Hooks:      hooks,
	})

	if !strings.Contains(cmd, "--settings") {
		t.Error("launch command should contain --settings flag")
	}

	// Read the settings file and verify hooks are present
	settingsFile := findTempFile(t, "settings", "test-hooks-session", "json")
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		t.Fatalf("failed to read settings file: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("failed to parse settings JSON: %v", err)
	}

	hooksMap, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("settings should contain hooks")
	}

	stopGroups, ok := hooksMap["Stop"].([]interface{})
	if !ok || len(stopGroups) == 0 {
		t.Error("settings hooks should contain Stop groups")
	}

	foundAgentHook := false
	for _, g := range stopGroups {
		gMap, ok := g.(map[string]interface{})
		if !ok {
			continue
		}
		hooksList, ok := gMap["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range hooksList {
			hMap, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if hMap["command"] == "echo done" {
				foundAgentHook = true
			}
		}
	}
	if !foundAgentHook {
		t.Error("agent Stop hook 'echo done' not found in settings file")
	}

	CleanupTempFiles("test-hooks-session")
}
