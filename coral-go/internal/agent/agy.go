package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/secfile"
)

// AgyAgent implements the Agent interface for Antigravity CLI (agy).
type AgyAgent struct {
	customType string
}

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
					slog.Debug("agy: failed to parse transcript file", "path", logsafe.Dir(transcriptPath), "error", err)
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
					slog.Debug("gemini: failed to parse legacy session file", "path", logsafe.Dir(fpath), "error", err)
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
	f, err := openHistoryFile(fpath) // regular file, never a link or FIFO (#3601, W21)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var firstTS, lastTS *string
	var msgCount int
	var summary string
	var firstMsg string
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
		if etype == "USER_INPUT" && firstMsg == "" {
			if content, _ := entry["content"].(string); content != "" {
				if t, ok := typedText(content); ok {
					firstMsg = clipFirstMessage(t)
				}
			}
		}
		if content, _ := entry["content"].(string); content != "" {
			fts.Add(content)
			if summary == "" {
				summary = redactedCut(content, 200)
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
		SessionID:        sessionID,
		SourceType:       at.Agy,
		SourceFile:       fpath,
		FileMtime:        mtime,
		FirstTimestamp:   firstTS,
		LastTimestamp:    lastTS,
		MessageCount:     msgCount,
		DisplaySummary:   summary,
		FirstUserMessage: firstMsg,
		IndexVersion:     HistoryIndexVersion,
		FTSBody:          buildFTSBody(summary, &fts),
	}, nil
}

// parseGeminiSession parses a legacy Gemini JSON session file.
func parseGeminiSession(fpath string, mtime float64) (*IndexedSession, error) {
	data, err := readHistoryFile(fpath)
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
	var summary, firstMsg string
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
			if firstMsg == "" && role == "user" {
				var typed []string
				for _, part := range parts {
					if pm, ok := part.(map[string]any); ok {
						if text, _ := pm["text"].(string); text != "" {
							typed = append(typed, text)
						}
					}
				}
				if t, ok := typedText(strings.Join(typed, " ")); ok {
					firstMsg = clipFirstMessage(t)
				}
			}
			if summary == "" && role == "model" {
				if p, ok := parts[0].(map[string]any); ok {
					if text, _ := p["text"].(string); text != "" {
						summary = redactedCut(text, 200)
					}
				}
			}
		}
	}

	return &IndexedSession{
		SessionID:        sessionID,
		SourceType:       "gemini",
		SourceFile:       fpath,
		FileMtime:        mtime,
		FirstTimestamp:   firstTS,
		LastTimestamp:    lastTS,
		MessageCount:     len(messages),
		DisplaySummary:   summary,
		FirstUserMessage: firstMsg,
		IndexVersion:     HistoryIndexVersion,
		FTSBody:          buildFTSBody(summary, &fts),
	}, nil
}

func (a *AgyAgent) LaunchCommand(params LaunchParams) (string, error) {
	files := &launchFiles{}
	bin := resolveBinary(params.CLIPath, "agy")
	var parts []string
	// raw: the shell syntax this builder makes itself; every other part is quoted (#3362).
	var raw []string

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
		export := formatEnvExport(kv[0], kv[1])
		raw = append(raw, export)
		parts = append(parts, export)
	}
	if runtime.GOOS == "windows" {
		// cmd.exe, which agy runs its hooks and commands through, then finds a bare program name
		// only on PATH, never in the current folder, the agent's repository (#21, Security's #20
		// F1b). Set in the launch line, so it holds whatever environment the terminal started with.
		export := formatEnvExport("NoDefaultCurrentDirectoryInExePath", "1")
		raw = append(raw, export)
		parts = append(parts, export)
	}
	// NOTE: PATH injection is handled by callers via WrapWithBundlePath()

	parts = append(parts, bin)

	// Coral's hooks, in a folder of this session's own added to agy's workspace
	// (#3481): agy reads <dir>/.agents/hooks.json from an --add-dir folder (verified
	// on agy 1.3.5), so nothing is written into the user's repo or ~/.gemini.
	if params.SessionID == "" {
		// no session (a preview or test line): no hooks
	} else if dir := files.dir(agyHooksDir(params.SessionID)); dir != "" {
		parts = append(parts, "--add-dir", dir)
	}

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
		"--settings": true, "--session-id": true,
	}
	permissionApplied := params.Capabilities != nil && !params.Capabilities.IsEmpty()
	for i := 0; i < len(params.Flags); i++ {
		flag := params.Flags[i]
		// The former Gemini spelling of the mode (--approval-mode X, --approval-mode=X): agy has --mode, so a recognised value is translated, both tokens consumed (dropping only the
		// name left its value as a stray argument, and the = form went through untouched), and any other value is refused, not passed (#5901).
		if value, isApproval := agyApprovalModeValue(params.Flags, &i); isApproval {
			mode, known := agyLegacyApprovalModes[value]
			if !known {
				return "", fmt.Errorf("agy does not take --approval-mode %q: use --mode plan, --mode accept-edits or --dangerously-skip-permissions (the old values are default, plan, auto_edit and yolo)", value)
			}
			if permissionApplied {
				slog.Info("dropping --approval-mode for agy agent: its capabilities or an earlier mode set it", "value", value)
				continue
			}
			parts = appendAgyPermissionMode(parts, mode)
			permissionApplied = true
			continue
		}
		if claudeOnlyFlags[flag] {
			slog.Warn("dropping unsupported flag for agy agent", "flag", flag)
			continue
		}
		// A capability set decides the mode, and a flag that would set another is dropped, as for Codex
		// (#4545, Security's B): the launch dialog's default --dangerously-skip-permissions came after the
		// capabilities' flags, so a read_only preset launched as "--dangerously-skip-permissions --mode plan".
		if flag == "--yolo" || flag == "--dangerously-skip-permissions" {
			if !permissionApplied {
				parts = append(parts, "--dangerously-skip-permissions")
				permissionApplied = true
			} else {
				slog.Info("dropping a permission flag for agy agent: its capabilities set the mode", "flag", flag)
			}
			continue
		}
		if flag == "--mode" {
			if i+1 < len(params.Flags) {
				i++
				if !permissionApplied {
					parts = append(parts, flag, params.Flags[i])
					permissionApplied = true
				} else {
					slog.Info("dropping a permission flag for agy agent: its capabilities set the mode", "flag", flag)
				}
			}
			continue
		}
		if strings.HasPrefix(flag, "--mode=") {
			if !permissionApplied {
				parts = append(parts, flag)
				permissionApplied = true
			} else {
				slog.Info("dropping a permission flag for agy agent: its capabilities set the mode", "flag", flag)
			}
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
	cliPrompt := BuildBoardActionPrompt(params.boardPromptName(), params.Role, params.Prompt, params.PromptOverrides, params.BoardType)
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
		arg := files.promptArg("agy_prompt", params.SessionID, "txt", "", fullPrompt)
		raw = append(raw, arg)
		parts = append(parts, "-i", arg)
	}

	parts, raw = withSessionToken(parts, raw, params, a.AgentType())
	return files.done(ShellJoin(parts, raw...))
}

// agyHookEvents are the agy hooks Coral installs; coral-hook-agentic-state turns each
// into Coral's event (or drops it): see its fromAgy.
var agyHookEvents = []string{"SessionStart", "PreInvocation", "PreToolUse", "PostToolUse", "Stop"}

// agyHooksDir writes this session's hooks.json into a new private temp folder,
// <tmp>/coral_agyhooks_<session>_*/.agents/hooks.json, and returns the folder. On an
// error the launch fails (#3601 W12: an agent without Coral's hooks is silently degraded).
// CleanupTempFiles removes it with the session's other temp files.
//
// On Windows agy runs each hook through cmd.exe, which looks in the agent's folder first, so
// a hook command must name the tool by an absolute path cmd.exe reads (ResolveCmdHookCommand).
// When there is none, no hooks are installed ("" and no error): agy runs without Coral's
// state tracking, which is logged, rather than with a command a repository could hijack
// (#21, Security's #20 F1). The folder, .agents and hooks.json (the commands agy runs) are
// readable and writable only by this user and SYSTEM (#21, #25 H2).
func agyHooksDir(sessionID string) (string, error) {
	spec := map[string]any{}
	for _, event := range agyHookEvents {
		command, ok := ResolveCmdHookCommand("coral-hook-agentic-state --agy " + event)
		if !ok {
			slog.Warn("agy runs without Coral's hooks, so its state isn't tracked: the hook tool has no absolute path cmd.exe can run (no tools folder, no tool in it, or a path with characters cmd.exe reads and no 8.3 short name)",
				"session", sessionID, "tools", CoralToolsDir())
			return "", nil
		}
		handler := map[string]any{"type": "command", "command": command, "timeout": 10}
		switch event {
		case "PreToolUse", "PostToolUse":
			spec[event] = []any{map[string]any{"matcher": ".*", "hooks": []any{handler}}}
		default:
			spec[event] = []any{handler}
		}
	}
	b, _ := json.MarshalIndent(map[string]any{"coral": spec}, "", "  ")
	dir, err := os.MkdirTemp(LaunchTempDir(), "coral_agyhooks_"+safeFileID(sessionID)+"_*")
	if err != nil {
		return "", fmt.Errorf("can't create agy's hooks folder in the temp folder %s: %w", LaunchTempDir(), err)
	}
	fail := func(format string, err error) (string, error) {
		os.RemoveAll(dir)
		return "", fmt.Errorf(format, dir, err)
	}
	if err := secfile.Protect(dir); err != nil {
		return fail("can't make agy's hooks folder %s private: %w", err)
	}
	agents := filepath.Join(dir, ".agents")
	if err := os.Mkdir(agents, 0o700); err != nil {
		return fail("can't create agy's hooks folder in %s: %w", err)
	}
	if err := secfile.Protect(agents); err != nil {
		return fail("can't make agy's hooks folder in %s private: %w", err)
	}
	if err := secfile.WriteFileNoFollow(filepath.Join(agents, "hooks.json"), b, 0o600); err != nil {
		return fail("can't write agy's hooks.json in %s: %w", err)
	}
	rememberLaunchTemp(safeFileID(sessionID), dir)
	return dir, nil
}

// agyLegacyApprovalModes maps the values of Gemini's former --approval-mode to Coral's generic permission modes (appendAgyPermissionMode makes agy's own flags of them).
var agyLegacyApprovalModes = map[string]string{"default": "default", "plan": "plan", "auto_edit": "acceptEdits", "yolo": "bypassPermissions"}

// agyApprovalModeValue reads --approval-mode at flags[*i] ("--approval-mode X" or "--approval-mode=X"), moving *i past its value, and reports whether it is that flag. A name with no value
// after it (the end, or another flag) has the empty value, which is not a known one.
func agyApprovalModeValue(flags []string, i *int) (value string, ok bool) {
	flag := flags[*i]
	if v, eq := strings.CutPrefix(flag, "--approval-mode="); eq {
		return v, true
	}
	if flag != "--approval-mode" {
		return "", false
	}
	if *i+1 < len(flags) && !strings.HasPrefix(flags[*i+1], "-") {
		*i++
		return flags[*i], true
	}
	return "", true
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
