package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// CodexAgent implements the Agent interface for OpenAI Codex CLI.
type CodexAgent struct{}

func (a *CodexAgent) AgentType() string    { return "codex" }
func (a *CodexAgent) SupportsResume() bool { return true }

func (a *CodexAgent) HistoryBasePath() string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return filepath.Join(v, "sessions")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "sessions")
}

func (a *CodexAgent) HistoryGlobPattern() string { return "rollout-*.jsonl" }

// ExtractSessions scans Codex history files under basePath and returns indexed sessions.
// Files whose mtime matches knownMtimes are skipped.
func (a *CodexAgent) ExtractSessions(basePath string, knownMtimes map[string]float64) ([]IndexedSession, error) {
	if basePath == "" {
		return nil, nil
	}
	if _, err := os.Stat(basePath); os.IsNotExist(err) {
		return nil, nil
	}
	// Codex stores sessions in YYYY/MM/DD/rollout-*.jsonl
	var sessions []IndexedSession
	err := filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasPrefix(filepath.Base(path), "rollout-") || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		mtime := float64(info.ModTime().Unix())
		if prev, ok := knownMtimes[path]; ok && prev == mtime {
			return nil // file unchanged since last index
		}
		sess, err := parseCodexSession(path, mtime)
		if err != nil {
			slog.Debug("codex: failed to parse session file", "path", path, "error", err)
			return nil
		}
		if sess != nil {
			sessions = append(sessions, *sess)
		}
		return nil
	})
	if err != nil {
		return sessions, err
	}
	return sessions, nil
}

// parseCodexSession parses a Codex JSONL session file.
func parseCodexSession(fpath string, mtime float64) (*IndexedSession, error) {
	f, err := os.Open(fpath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Derive session ID from filename: rollout-<timestamp>-<id>.jsonl
	sessionID := strings.TrimSuffix(filepath.Base(fpath), ".jsonl")

	var firstTS, lastTS *string
	var msgCount int
	var summary string
	var fts FTSBodyBuilder
	var coralSessionID string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if id := ExtractCoralSessionID(entry); id != "" {
			coralSessionID = id
		}
		ts, _ := entry["timestamp"].(string)
		if ts != "" {
			if firstTS == nil {
				firstTS = &ts
			}
			tsCopy := ts
			lastTS = &tsCopy
		}

		role, text := codexIndexMessage(entry)
		if role == "user" || role == "assistant" {
			msgCount++
			fts.Add(text)
		}
		if summary == "" && role == "assistant" {
			if len(text) > 200 {
				text = text[:200]
			}
			summary = text
		}
	}
	if msgCount == 0 {
		return nil, nil
	}
	if coralSessionID != "" {
		sessionID = coralSessionID
	}
	return &IndexedSession{
		SessionID:      sessionID,
		SourceType:     "codex",
		SourceFile:     fpath,
		FileMtime:      mtime,
		FirstTimestamp: firstTS,
		LastTimestamp:  lastTS,
		MessageCount:   msgCount,
		DisplaySummary: summary,
		FTSBody:        buildFTSBody(summary, &fts),
	}, nil
}

func extractCodexFirstText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		for _, block := range c {
			if b, ok := block.(map[string]any); ok {
				bt, _ := b["type"].(string)
				if bt == "text" || bt == "input_text" {
					if t, _ := b["text"].(string); t != "" {
						return t
					}
				}
				if bt == "input_image" {
					if imgURL, ok := b["image_url"].(map[string]any); ok {
						if u, ok := imgURL["url"].(string); ok && u != "" {
							return u
						}
					}
				}
				if bt == "image" {
					if p, ok := b["path"].(string); ok && p != "" {
						return p
					}
				}
			}
		}
	}
	return ""
}

func codexIndexMessage(entry map[string]any) (role, text string) {
	if role, _ := entry["role"].(string); role == "user" || role == "assistant" {
		return role, extractCodexFirstText(entry["content"])
	}

	if entryType, _ := entry["type"].(string); entryType != "event_msg" {
		return "", ""
	}
	payload, _ := entry["payload"].(map[string]any)
	if payload == nil {
		return "", ""
	}
	message, _ := payload["message"].(string)
	if strings.TrimSpace(message) == "" {
		if imgs, ok := payload["images"].([]any); ok && len(imgs) > 0 {
			for _, img := range imgs {
				if p, ok := img.(string); ok && p != "" {
					message = p
					break
				}
			}
		}
	}
	if strings.TrimSpace(message) == "" {
		return "", ""
	}
	switch payloadType, _ := payload["type"].(string); payloadType {
	case "user_message":
		return "user", message
	case "agent_message":
		return "assistant", message
	default:
		return "", ""
	}
}

func (a *CodexAgent) BuildLaunchCommand(params LaunchParams) string {
	bin := resolveBinary(params.CLIPath, "codex")
	var parts []string

	// Export env vars so child processes (coral-board, hooks) inherit them.
	// singleQuote single-quotes each value (escaping embedded quotes) so the
	// shell never expands it. Values must NOT be run through SanitizeShellValue:
	// CORAL_URL and CORAL_DIR contain ':' and '/', and stripping those broke the
	// URL every hook posted to and pointed CORAL_DATA_DIR at a relative path.
	// Coral environment, built by CoralEnv so every launch path agrees.
	for _, kv := range CoralEnv(params) {
		parts = append(parts, fmt.Sprintf(`export %s=%s &&`, kv[0], singleQuote(kv[1])))
	}
	// NOTE: PATH injection is handled by callers via WrapWithBundlePath()

	// Binary and resume
	if params.ResumeSessionID != "" {
		parts = append(parts, bin, "resume", params.ResumeSessionID)
	} else {
		parts = append(parts, bin)
	}

	// System prompt injection via -c developer_instructions
	// Combines protocol file + board system prompt (CLI usage, role instructions)
	// Note: The $(cat '...') pattern shell-expands the file path but not its content.
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
		sysFile := writeTempFile("codex_instructions", params.SessionID, "md", []byte(strings.Join(sysParts, "\n\n")))
		parts = append(parts, fmt.Sprintf(`-c developer_instructions="$(cat '%s')"`, sysFile))
	}

	// Codex does not read Claude's settings.json, so install Coral's activity
	// hook directly in its config. Without these hooks Codex sessions can still
	// appear active, but no tool/thinking/stop events reach the activity view.
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		parts = append(parts, "-c", fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command="coral-hook-agentic-state"}]}]`, event))
	}

	// Note: Codex's sandbox may strip env vars from child processes.
	// coral-board handles this via board_state file fallback (reads job_title
	// from ~/.coral/board_state_{session}.json when CORAL_SUBSCRIBER_ID is unavailable).

	// Permission flags from capabilities
	bypassSandbox := false
	permissionApplied := false
	if perms := TranslateToCodexPermissions(params.Capabilities); perms != nil {
		permissionApplied = true
		if perms.BypassSandbox {
			bypassSandbox = true
			parts = append(parts, "--dangerously-bypass-approvals-and-sandbox")
		} else if perms.FullAuto {
			parts = appendCodexFullAuto(parts)
		} else {
			if perms.SandboxMode != "" {
				parts = append(parts, "--sandbox", perms.SandboxMode)
			}
			if perms.ApprovalPolicy != "" {
				parts = append(parts, "-a", normalizeCodexApprovalPolicy(perms.ApprovalPolicy))
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
		if flag == "-a" && i+1 < len(params.Flags) {
			// Older Coral permission profiles used Codex's former "untrusted"
			// policy. Current Codex accepts only on-request or never.
			parts = append(parts, flag, normalizeCodexApprovalPolicy(params.Flags[i+1]))
			i++
			permissionApplied = true
			continue
		}
		if strings.HasPrefix(flag, "-a=") {
			parts = append(parts, "-a="+normalizeCodexApprovalPolicy(strings.TrimPrefix(flag, "-a=")))
			permissionApplied = true
			continue
		}
		if flag == "--approval-mode" && i+1 < len(params.Flags) {
			parts = append(parts, flag, normalizeCodexApprovalPolicy(params.Flags[i+1]))
			i++
			permissionApplied = true
			continue
		}
		if strings.HasPrefix(flag, "--approval-mode=") {
			parts = append(parts, "--approval-mode="+normalizeCodexApprovalPolicy(strings.TrimPrefix(flag, "--approval-mode=")))
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

	// Action prompt as separate positional argument
	actionPrompt := BuildBoardActionPrompt(params.BoardName, params.Role, params.Prompt, params.PromptOverrides, params.BoardType)
	if actionPrompt == "" {
		actionPrompt = params.Prompt
	}

	if actionPrompt != "" {
		promptFile := writeTempFile("codex_prompt", params.SessionID, "txt", []byte(actionPrompt))
		parts = append(parts, FormatPromptFileArg(promptFile))
	}

	return strings.Join(ShellQuoteParts(parts), " ")
}

func normalizeCodexApprovalPolicy(policy string) string {
	if policy == "untrusted" {
		return "on-request"
	}
	return policy
}

func appendCodexFullAuto(parts []string) []string {
	return append(parts, "--sandbox", "workspace-write", "-a", "never")
}

func appendCodexPermissionMode(parts []string, bypassSandbox bool, mode string) ([]string, bool, bool) {
	switch mode {
	case "", "default":
		return parts, bypassSandbox, false
	case "bypassPermissions":
		if !bypassSandbox {
			parts = append(parts, "--dangerously-bypass-approvals-and-sandbox")
			bypassSandbox = true
		}
		return parts, bypassSandbox, true
	case "auto", "dontAsk":
		if !bypassSandbox {
			parts = appendCodexFullAuto(parts)
		}
		return parts, bypassSandbox, true
	case "acceptEdits":
		parts = append(parts, "--sandbox", "workspace-write", "-a", "on-request")
		return parts, bypassSandbox, true
	case "plan":
		parts = append(parts, "--sandbox", "read-only", "-a", "on-request")
		return parts, bypassSandbox, true
	default:
		slog.Warn("dropping unsupported permission mode for Codex agent", "mode", mode)
		return parts, bypassSandbox, false
	}
}
