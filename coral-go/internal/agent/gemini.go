package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// GeminiAgent implements the Agent interface for Gemini CLI.
type GeminiAgent struct{}

func (a *GeminiAgent) AgentType() string { return "gemini" }

// SupportsResume returns false because Gemini's --resume flag only accepts
// index numbers or "latest", not session UUIDs. With multiple Gemini agents
// per project, both are unsafe — "latest" or an index could resume the wrong
// agent's session, leaking conversation context across roles.
// TODO: Re-enable when Gemini CLI adds --resume <sessionId> support. At that
// point, store the Gemini sessionId at launch and map to our Coral session ID.
func (a *GeminiAgent) SupportsResume() bool { return false }

func (a *GeminiAgent) HistoryBasePath() string {
	if v := os.Getenv("GEMINI_TMP_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "tmp")
}

func (a *GeminiAgent) HistoryGlobPattern() string { return "session-*.json" }

// ExtractSessions scans Gemini history files under basePath and returns indexed sessions.
// Files whose mtime matches knownMtimes are skipped.
func (a *GeminiAgent) ExtractSessions(basePath string, knownMtimes map[string]float64) ([]IndexedSession, error) {
	if basePath == "" {
		return nil, nil
	}
	// Gemini stores sessions in ~/.gemini/tmp/<uuid>/chats/session-*.json
	entries, err := os.ReadDir(basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sessions []IndexedSession
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		chatsDir := filepath.Join(basePath, entry.Name(), "chats")
		files, err := filepath.Glob(filepath.Join(chatsDir, a.HistoryGlobPattern()))
		if err != nil {
			continue
		}
		for _, fpath := range files {
			info, err := os.Stat(fpath)
			if err != nil {
				continue
			}
			mtime := float64(info.ModTime().Unix())
			if prev, ok := knownMtimes[fpath]; ok && prev == mtime {
				continue
			}
			sess, err := parseGeminiSession(fpath, mtime)
			if err != nil {
				slog.Debug("gemini: failed to parse session file", "path", fpath, "error", err)
				continue
			}
			if sess != nil {
				sessions = append(sessions, *sess)
			}
		}
	}
	return sessions, nil
}

// parseGeminiSession parses a Gemini JSON session file.
func parseGeminiSession(fpath string, mtime float64) (*IndexedSession, error) {
	data, err := os.ReadFile(fpath)
	if err != nil {
		return nil, err
	}
	// Gemini sessions are JSON arrays of message objects
	var messages []map[string]any
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, nil
	}

	// Extract session ID from filename: session-<id>.json
	base := filepath.Base(fpath)
	sessionID := strings.TrimSuffix(strings.TrimPrefix(base, "session-"), ".json")

	var firstTS, lastTS *string
	var summary string
	for _, msg := range messages {
		ts, _ := msg["timestamp"].(string)
		if ts != "" {
			if firstTS == nil {
				firstTS = &ts
			}
			tsCopy := ts
			lastTS = &tsCopy
		}
		// Grab first model response as summary
		if summary == "" {
			if role, _ := msg["role"].(string); role == "model" {
				if parts, _ := msg["parts"].([]any); len(parts) > 0 {
					if p, ok := parts[0].(map[string]any); ok {
						if text, _ := p["text"].(string); text != "" {
							if len(text) > 200 {
								text = text[:200]
							}
							summary = text
						}
					}
				}
			}
		}
	}

	return &IndexedSession{
		SessionID:      sessionID,
		SourceType:     "gemini",
		SourceFile:     fpath,
		FileMtime:      mtime,
		FirstTimestamp: firstTS,
		LastTimestamp:  lastTS,
		MessageCount:   len(messages),
		DisplaySummary: summary,
	}, nil
}

func (a *GeminiAgent) BuildLaunchCommand(params LaunchParams) string {
	bin := resolveBinary(params.CLIPath, "gemini")
	var parts []string

	// Inject system prompt via GEMINI_SYSTEM_MD env var
	var sysParts []string
	if proto := readProtocolFile(params.ProtocolPath); proto != "" {
		sysParts = append(sysParts, proto)
	}
	boardSysPrompt := BuildBoardSystemPrompt(params.BoardName, params.Role, "", params.PromptOverrides, params.BoardType)
	if boardSysPrompt != "" {
		sysParts = append(sysParts, boardSysPrompt)
	}
	sysParts = appendCoralSessionMarker(sysParts, params.SessionID)

	// Export env vars so child processes (coral-board, hooks) inherit them.
	// Single quotes prevent shell expansion; SanitizeShellValue strips metacharacters.
	if len(sysParts) > 0 {
		sysFile := writeTempFile("gemini_sys", params.SessionID, "md", []byte(strings.Join(sysParts, "\n\n")))
		parts = append(parts, fmt.Sprintf(`export GEMINI_SYSTEM_MD="%s" &&`, sysFile))
	}

	if params.SessionName != "" {
		parts = append(parts, fmt.Sprintf(`export CORAL_SESSION_NAME='%s' &&`, SanitizeShellValue(params.SessionName)))
	}
	if params.Role != "" {
		parts = append(parts, fmt.Sprintf(`export CORAL_SUBSCRIBER_ID='%s' &&`, SanitizeShellValue(params.Role)))
	}
	if params.ProxyBaseURL != "" {
		parts = append(parts, fmt.Sprintf(`export GEMINI_API_BASE='%s' &&`, sanitizeURL(params.ProxyBaseURL)))
	}

	// NOTE: PATH injection is handled by callers via WrapWithBundlePath()

	parts = append(parts, bin)

	// Resume is disabled — see SupportsResume() comment.
	// The --resume flag code is kept commented for future reference:
	// if params.ResumeSessionID != "" {
	//     parts = append(parts, "--resume", "<gemini-session-id>")
	// }

	// Permission flags from capabilities
	if perms := TranslateToGeminiPermissions(params.Capabilities); perms != nil {
		if perms.ApprovalMode != "" {
			parts = append(parts, "--approval-mode", perms.ApprovalMode)
		}
		if perms.Sandbox {
			parts = append(parts, "--sandbox")
		}
	}

	// User-provided flags — drop Claude-specific flags that Gemini doesn't understand
	claudeOnlyFlags := map[string]bool{
		"--settings": true, "--session-id": true, "--dangerously-skip-permissions": true,
	}
	for _, flag := range params.Flags {
		if claudeOnlyFlags[flag] {
			slog.Warn("dropping Claude-specific flag for Gemini agent", "flag", flag)
			continue
		}
		parts = append(parts, flag)
	}

	// Action prompt via temp file for robustness
	cliPrompt := BuildBoardActionPrompt(params.BoardName, params.Role, params.Prompt, params.PromptOverrides, params.BoardType)
	if cliPrompt == "" {
		cliPrompt = params.Prompt
	}

	if cliPrompt != "" {
		promptFile := writeTempFile("gemini_prompt", params.SessionID, "txt", []byte(cliPrompt))
		parts = append(parts, FormatPromptFileArg(promptFile))
	}

	return strings.Join(ShellQuoteParts(parts), " ")
}
