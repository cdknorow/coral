package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
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
	}, nil
}

func codexIndexMessage(entry map[string]any) (role, text string) {
	if role, _ := entry["role"].(string); role == "user" || role == "assistant" {
		return role, extractFirstText(entry["content"])
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
	// Single quotes prevent shell expansion; SanitizeShellValue strips metacharacters.
	if params.SessionName != "" {
		parts = append(parts, fmt.Sprintf(`export CORAL_SESSION_NAME='%s' &&`, SanitizeShellValue(params.SessionName)))
	}
	if params.Role != "" {
		parts = append(parts, fmt.Sprintf(`export CORAL_SUBSCRIBER_ID='%s' &&`, SanitizeShellValue(params.Role)))
	}
	// Route LLM traffic through the Coral MITM proxy for transparent cost tracking.
	// HTTPS_PROXY must be exported BEFORE the binary (it's an env var, not a flag).
	if params.ProxyBaseURL != "" {
		proxyHost := extractProxyHost(params.ProxyBaseURL)
		if proxyHost != "" {
			parts = append(parts, fmt.Sprintf(`export HTTPS_PROXY='%s' &&`, proxyHost))
		}
		// Point SSL_CERT_FILE to a combined bundle (system CAs + Coral MITM CA)
		// so the Rust HTTP client trusts our dynamically generated certs.
		coralDir := params.CoralDir
		if coralDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				coralDir = filepath.Join(home, ".coral")
			}
		}
		if coralDir != "" {
			bundlePath := filepath.Join(coralDir, "proxy-ca-bundle.pem")
			parts = append(parts, fmt.Sprintf(`export SSL_CERT_FILE='%s' &&`, bundlePath))
		}
	}

	// NOTE: PATH injection is handled by callers via WrapWithBundlePath()

	// Binary and resume
	if params.ResumeSessionID != "" {
		parts = append(parts, bin, "resume", params.ResumeSessionID)
	} else {
		parts = append(parts, bin)
	}

	// Codex -c flags for MITM proxy (must come AFTER the binary)
	if params.ProxyBaseURL != "" {
		// Also set base URL for non-CONNECT fallback (direct HTTP proxy mode)
		if isCodexOAuthMode() {
			parts = append(parts, fmt.Sprintf(`-c chatgpt_base_url="%s"`, sanitizeURL(params.ProxyBaseURL)))
		} else {
			parts = append(parts, fmt.Sprintf(`-c openai_base_url="%s"`, sanitizeURL(params.ProxyBaseURL)))
		}
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

	parts = appendCodexCoralHooks(parts)

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
				parts = append(parts, "-a", perms.ApprovalPolicy)
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
		if flag == "--sandbox" || strings.HasPrefix(flag, "--sandbox=") || flag == "-a" || flag == "--approval-mode" || strings.HasPrefix(flag, "--approval-mode=") {
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

func appendCodexCoralHooks(parts []string) []string {
	hookConfig := map[string]string{
		"hooks.SessionStart":     `[{hooks=[{type="command",command="coral-hook-agentic-state"}]}]`,
		"hooks.UserPromptSubmit": `[{hooks=[{type="command",command="coral-hook-agentic-state"}]}]`,
		"hooks.PostToolUse":      `[{hooks=[{type="command",command="coral-hook-agentic-state"}]}]`,
		"hooks.Stop":             `[{hooks=[{type="command",command="coral-hook-agentic-state"}]}]`,
	}
	for _, key := range []string{"hooks.SessionStart", "hooks.UserPromptSubmit", "hooks.PostToolUse", "hooks.Stop"} {
		parts = append(parts, "-c", key+"="+hookConfig[key])
	}
	return parts
}

func appendCodexFullAuto(parts []string) []string {
	return append(parts, "--sandbox", "workspace-write", "-a", "on-request")
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
		parts = append(parts, "--sandbox", "read-only", "-a", "untrusted")
		return parts, bypassSandbox, true
	default:
		slog.Warn("dropping unsupported permission mode for Codex agent", "mode", mode)
		return parts, bypassSandbox, false
	}
}

// isCodexOAuthMode checks if the Codex CLI is configured to use ChatGPT OAuth
// auth (as opposed to an API key). Reads ~/.codex/auth.json and checks auth_mode.
func isCodexOAuthMode() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		return false
	}
	var auth struct {
		AuthMode string `json:"auth_mode"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return false
	}
	return auth.AuthMode == "chatgpt"
}

// extractProxyHost extracts the scheme://host:port from a proxy URL.
// e.g. "http://127.0.0.1:8420/proxy/abc" → "http://127.0.0.1:8420"
func extractProxyHost(proxyURL string) string {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
}
