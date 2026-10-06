package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	at "github.com/cdknorow/coral/internal/agenttypes"
)

// AgyAgent implements the Agent interface for Antigravity CLI (agy).
type AgyAgent struct {
	customType string
}

// Deprecated: use AgyAgent.
type GeminiAgent = AgyAgent

func (a *AgyAgent) AgentType() string {
	if a.customType != "" {
		return a.customType
	}
	return at.Agy
}

// SupportsResume returns true because agy CLI supports resuming previous
// conversations by ID via the --conversation flag.
func (a *AgyAgent) SupportsResume() bool { return true }

func (a *AgyAgent) HistoryBasePath() string {
	if v := os.Getenv("ANTIGRAVITY_DATA_DIR"); v != "" {
		return filepath.Join(v, "brain")
	}
	if v := os.Getenv("GEMINI_TMP_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	agyBrain := filepath.Join(home, ".gemini", "antigravity-cli", "brain")
	if _, err := os.Stat(agyBrain); err == nil {
		return agyBrain
	}
	return filepath.Join(home, ".gemini", "tmp")
}

func (a *AgyAgent) HistoryGlobPattern() string { return "transcript.jsonl" }

// ExtractSessions scans Antigravity brain history files (and legacy Gemini files)
// under basePath and returns indexed sessions.
// Files whose mtime matches knownMtimes are skipped.
func (a *AgyAgent) ExtractSessions(basePath string, knownMtimes map[string]float64) ([]IndexedSession, error) {
	if basePath == "" {
		return nil, nil
	}
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

		// 1. Antigravity format: basePath/<uuid>/.system_generated/logs/transcript.jsonl
		transcriptPath := filepath.Join(basePath, entry.Name(), ".system_generated", "logs", "transcript.jsonl")
		if info, err := os.Stat(transcriptPath); err == nil {
			mtime := float64(info.ModTime().Unix())
			if prev, ok := knownMtimes[transcriptPath]; !ok || prev != mtime {
				sess, err := parseAgyTranscriptSession(entry.Name(), transcriptPath, mtime)
				if err != nil {
					slog.Debug("agy: failed to parse transcript file", "path", transcriptPath, "error", err)
				} else if sess != nil {
					sessions = append(sessions, *sess)
				}
			}
		}

		// 2. Legacy Gemini format: basePath/<uuid>/chats/session-*.json
		chatsDir := filepath.Join(basePath, entry.Name(), "chats")
		if files, err := filepath.Glob(filepath.Join(chatsDir, "session-*.json")); err == nil {
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
					slog.Debug("gemini: failed to parse legacy session file", "path", fpath, "error", err)
					continue
				}
				if sess != nil {
					sessions = append(sessions, *sess)
				}
			}
		}
	}
	return sessions, nil
}

// parseAgyTranscriptSession parses an Antigravity JSONL transcript file.
func parseAgyTranscriptSession(convID, fpath string, mtime float64) (*IndexedSession, error) {
	f, err := os.Open(fpath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

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
		ts, _ := entry["created_at"].(string)
		if ts == "" {
			ts, _ = entry["timestamp"].(string)
		}
		if ts != "" {
			if firstTS == nil {
				firstTS = &ts
			}
			tsCopy := ts
			lastTS = &tsCopy
		}
		etype, _ := entry["type"].(string)
		if etype == "USER_INPUT" || etype == "PLANNER_RESPONSE" {
			msgCount++
		}
		if content, _ := entry["content"].(string); content != "" {
			fts.Add(content)
			if summary == "" {
				text := content
				if len(text) > 200 {
					text = text[:200]
				}
				summary = text
			}
		}
		if toolCalls, ok := entry["tool_calls"].([]any); ok {
			for _, tc := range toolCalls {
				if tcm, ok := tc.(map[string]any); ok {
					if name, _ := tcm["name"].(string); name != "" {
						fts.Add(name)
					}
				}
			}
		}
	}
	if msgCount == 0 {
		return nil, nil
	}

	sessionID := convID
	if coralSessionID != "" {
		sessionID = coralSessionID
	}

	return &IndexedSession{
		SessionID:      sessionID,
		SourceType:     at.Agy,
		SourceFile:     fpath,
		FileMtime:      mtime,
		FirstTimestamp: firstTS,
		LastTimestamp:  lastTS,
		MessageCount:   msgCount,
		DisplaySummary: summary,
		FTSBody:        buildFTSBody(summary, &fts),
	}, nil
}

// parseGeminiSession parses a legacy Gemini JSON session file.
func parseGeminiSession(fpath string, mtime float64) (*IndexedSession, error) {
	data, err := os.ReadFile(fpath)
	if err != nil {
		return nil, err
	}
	var messages []map[string]any
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, nil
	}

	base := filepath.Base(fpath)
	sessionID := strings.TrimSuffix(strings.TrimPrefix(base, "session-"), ".json")

	var firstTS, lastTS *string
	var summary string
	var fts FTSBodyBuilder
	for _, msg := range messages {
		ts, _ := msg["timestamp"].(string)
		if ts != "" {
			if firstTS == nil {
				firstTS = &ts
			}
			tsCopy := ts
			lastTS = &tsCopy
		}
		role, _ := msg["role"].(string)
		if parts, _ := msg["parts"].([]any); len(parts) > 0 {
			for _, part := range parts {
				if pm, ok := part.(map[string]any); ok {
					if text, _ := pm["text"].(string); text != "" {
						fts.Add(text)
					}
				}
			}
			if summary == "" && role == "model" {
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

	return &IndexedSession{
		SessionID:      sessionID,
		SourceType:     "gemini",
		SourceFile:     fpath,
		FileMtime:      mtime,
		FirstTimestamp: firstTS,
		LastTimestamp:  lastTS,
		MessageCount:   len(messages),
		DisplaySummary: summary,
		FTSBody:        buildFTSBody(summary, &fts),
	}, nil
}

func (a *AgyAgent) BuildLaunchCommand(params LaunchParams) string {
	bin := resolveBinary(params.CLIPath, "agy")
	var parts []string

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
	for _, kv := range CoralEnv(params) {
		parts = append(parts, fmt.Sprintf(`export %s=%s &&`, kv[0], singleQuote(kv[1])))
	}
	// NOTE: PATH injection is handled by callers via WrapWithBundlePath()

	parts = append(parts, bin)

	// Resume previous conversation
	if params.ResumeSessionID != "" {
		parts = append(parts, "--conversation", params.ResumeSessionID)
	}

	// Permission flags from capabilities
	if perms := TranslateToAgyPermissions(params.Capabilities); perms != nil {
		if perms.DangerouslySkipPermissions {
			parts = append(parts, "--dangerously-skip-permissions")
		}
		if perms.Mode != "" {
			parts = append(parts, "--mode", perms.Mode)
		}
		if perms.Sandbox {
			parts = append(parts, "--sandbox")
		}
	}

	// User-provided flags — translate or drop flags that agy doesn't understand
	claudeOnlyFlags := map[string]bool{
		"--settings": true, "--session-id": true, "--approval-mode": true,
	}
	permissionApplied := params.Capabilities != nil && !params.Capabilities.IsEmpty()
	for i := 0; i < len(params.Flags); i++ {
		flag := params.Flags[i]
		if claudeOnlyFlags[flag] {
			slog.Warn("dropping unsupported flag for agy agent", "flag", flag)
			continue
		}
		if flag == "--yolo" {
			parts = append(parts, "--dangerously-skip-permissions")
			continue
		}
		if flag == "--permission-mode" {
			if i+1 < len(params.Flags) {
				i++
				if !permissionApplied {
					parts = appendAgyPermissionMode(parts, params.Flags[i])
					permissionApplied = true
				}
			}
			continue
		}
		if mode, ok := strings.CutPrefix(flag, "--permission-mode="); ok {
			if !permissionApplied {
				parts = appendAgyPermissionMode(parts, mode)
				permissionApplied = true
			}
			continue
		}
		parts = append(parts, flag)
	}

	if !permissionApplied && params.PermissionMode != "" && params.PermissionMode != "default" {
		parts = appendAgyPermissionMode(parts, params.PermissionMode)
	}

	// Action prompt via temp file for robustness
	cliPrompt := BuildBoardActionPrompt(params.BoardName, params.Role, params.Prompt, params.PromptOverrides, params.BoardType)
	if cliPrompt == "" {
		cliPrompt = params.Prompt
	}

	var promptSections []string
	if len(sysParts) > 0 {
		promptSections = append(promptSections, strings.Join(sysParts, "\n\n"))
	}
	if cliPrompt != "" {
		promptSections = append(promptSections, cliPrompt)
	}

	if len(promptSections) > 0 {
		fullPrompt := strings.Join(promptSections, "\n\n")
		promptFile := writeTempFile("agy_prompt", params.SessionID, "txt", []byte(fullPrompt))
		parts = append(parts, "-i", FormatPromptFileArg(promptFile))
	}

	return strings.Join(ShellQuoteParts(parts), " ")
}

func appendAgyPermissionMode(parts []string, mode string) []string {
	switch mode {
	case "", "default":
		return parts
	case "bypassPermissions":
		return append(parts, "--dangerously-skip-permissions")
	case "auto":
		return append(parts, "--dangerously-skip-permissions")
	case "acceptEdits":
		return append(parts, "--mode", "accept-edits")
	case "plan":
		return append(parts, "--mode", "plan")
	case "dontAsk":
		return append(parts, "--mode", "plan")
	default:
		slog.Warn("dropping unsupported permission mode for agy agent", "mode", mode)
		return parts
	}
}
