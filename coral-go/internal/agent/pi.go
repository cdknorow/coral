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

// PiAgent implements the Agent interface for the Pi coding agent CLI.
type PiAgent struct{}

func (a *PiAgent) AgentType() string    { return at.Pi }
func (a *PiAgent) SupportsResume() bool { return true }

func (a *PiAgent) HistoryBasePath() string {
	if v := os.Getenv("PI_CODING_AGENT_DIR"); v != "" {
		return filepath.Join(v, "sessions")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent", "sessions")
}

func (a *PiAgent) HistoryGlobPattern() string { return "*/*.jsonl" }

func (a *PiAgent) ExtractSessions(basePath string, knownMtimes map[string]float64) ([]IndexedSession, error) {
	if basePath == "" {
		return nil, nil
	}
	if _, err := os.Stat(basePath); os.IsNotExist(err) {
		return nil, nil
	}

	var sessions []IndexedSession
	err := filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		mtime := float64(info.ModTime().Unix())
		if prev, ok := knownMtimes[path]; ok && prev == mtime {
			return nil
		}
		sess, parseErr := parsePiSession(path, mtime)
		if parseErr != nil {
			slog.Debug("pi: failed to parse session file", "path", path, "error", parseErr)
			return nil
		}
		if sess != nil {
			sessions = append(sessions, *sess)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

func parsePiSession(fpath string, mtime float64) (*IndexedSession, error) {
	f, err := os.Open(fpath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var sessionID string
	var firstTS, lastTS *string
	var summary string
	msgCount := 0

	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}

		entryType, _ := entry["type"].(string)

		switch entryType {
		case "session":
			if id, ok := entry["id"].(string); ok {
				sessionID = id
			}
		case "message":
			msgCount++
			ts, _ := entry["timestamp"].(string)
			if ts != "" {
				if firstTS == nil {
					firstTS = &ts
				}
				tsCopy := ts
				lastTS = &tsCopy
			}
			if summary == "" {
				if role, _ := entry["role"].(string); role == "user" {
					if content, _ := entry["content"].(string); content != "" {
						if len(content) > 200 {
							content = content[:200]
						}
						summary = content
					}
				}
			}
		}
	}

	if sessionID == "" {
		base := filepath.Base(fpath)
		sessionID = strings.TrimSuffix(base, ".jsonl")
	}

	return &IndexedSession{
		SessionID:      sessionID,
		SourceType:     "pi",
		SourceFile:     fpath,
		FileMtime:      mtime,
		FirstTimestamp: firstTS,
		LastTimestamp:  lastTS,
		MessageCount:   msgCount,
		DisplaySummary: summary,
	}, nil
}

func (a *PiAgent) BuildLaunchCommand(params LaunchParams) string {
	bin := resolveBinary(params.CLIPath, "pi")
	var parts []string

	// Build system prompt for --append-system-prompt
	var sysParts []string
	if proto := readProtocolFile(params.ProtocolPath); proto != "" {
		sysParts = append(sysParts, proto)
	}
	boardSysPrompt := BuildBoardSystemPrompt(params.BoardName, params.Role, "", params.PromptOverrides, params.BoardType)
	if boardSysPrompt != "" {
		sysParts = append(sysParts, boardSysPrompt)
	}
	sysParts = appendCoralSessionMarker(sysParts, params.SessionID)

	// Export env vars
	// Coral environment, built by CoralEnv so every launch path agrees.
	for _, kv := range CoralEnv(params) {
		parts = append(parts, fmt.Sprintf(`export %s='%s' &&`, kv[0], SanitizeShellValue(kv[1])))
	}
	if params.ProxyBaseURL != "" {
		parts = append(parts, fmt.Sprintf(`export HTTPS_PROXY='%s' &&`, sanitizeURL(params.ProxyBaseURL)))
	}

	parts = append(parts, bin)

	// Use a per-session directory so Pi's internal sessions map to Coral sessions.
	// On resume, point to the old session dir and use --continue.
	coralDir := params.CoralDir
	if coralDir == "" {
		if v := os.Getenv("CORAL_DATA_DIR"); v != "" {
			coralDir = v
		} else if home, err := os.UserHomeDir(); err == nil {
			coralDir = filepath.Join(home, ".coral")
		}
	}
	if coralDir != "" {
		effectiveID := params.SessionID
		if params.ResumeSessionID != "" {
			effectiveID = params.ResumeSessionID
		}
		sessionDir := filepath.Join(coralDir, "pi-sessions", effectiveID)
		os.MkdirAll(sessionDir, 0755)
		parts = append(parts, "--session-dir", sessionDir)
		if params.ResumeSessionID != "" {
			parts = append(parts, "--continue")
		}
	}

	// Pass through user flags, dropping ones Pi doesn't understand
	unsupportedFlags := map[string]bool{
		"--permission-mode": true, "--settings": true, "--session-id": true,
		"--dangerously-skip-permissions": true, "--full-auto": true,
		"--approval-mode": true, "--sandbox": true, "--yolo": true,
		"--session": true, "--session-dir": true,
	}
	skipNext := false
	for _, flag := range params.Flags {
		if skipNext {
			skipNext = false
			continue
		}
		if unsupportedFlags[flag] {
			if flag == "--permission-mode" || flag == "--approval-mode" || flag == "--sandbox" || flag == "--session" || flag == "--session-id" || flag == "--session-dir" {
				skipNext = true
			}
			continue
		}
		parts = append(parts, flag)
	}

	// System prompt via --append-system-prompt with temp file
	if len(sysParts) > 0 {
		sysFile := writeTempFile("pi_sys", params.SessionID, "md", []byte(strings.Join(sysParts, "\n\n")))
		parts = append(parts, "--append-system-prompt", FormatPromptFileArg(sysFile))
	}

	// Action prompt as positional argument
	cliPrompt := BuildBoardActionPrompt(params.BoardName, params.Role, params.Prompt, params.PromptOverrides, params.BoardType)
	if cliPrompt == "" {
		cliPrompt = params.Prompt
	}
	if cliPrompt != "" {
		promptFile := writeTempFile("pi_prompt", params.SessionID, "txt", []byte(cliPrompt))
		parts = append(parts, FormatPromptFileArg(promptFile))
	}

	return strings.Join(ShellQuoteParts(parts), " ")
}
