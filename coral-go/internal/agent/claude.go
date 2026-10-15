package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeAgent implements the Agent interface for Claude Code.
type ClaudeAgent struct{}

func (a *ClaudeAgent) AgentType() string    { return "claude" }
func (a *ClaudeAgent) SupportsResume() bool { return true }

func (a *ClaudeAgent) HistoryBasePath() string {
	if v := os.Getenv("CLAUDE_PROJECTS_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

func (a *ClaudeAgent) HistoryGlobPattern() string { return "*.jsonl" }

// ExtractSessions scans Claude history files under basePath and returns indexed sessions.
// Files whose mtime matches knownMtimes are skipped.
func (a *ClaudeAgent) ExtractSessions(basePath string, knownMtimes map[string]float64) ([]IndexedSession, error) {
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
		projectDir := filepath.Join(basePath, entry.Name())
		files, err := filepath.Glob(filepath.Join(projectDir, a.HistoryGlobPattern()))
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
				continue // file unchanged since last index
			}
			sess, err := parseClaudeSessions(fpath, mtime)
			if err != nil {
				slog.Debug("claude: failed to parse session file", "path", fpath, "error", err)
				continue
			}
			sessions = append(sessions, sess...)
		}
	}
	return sessions, nil
}

// parseClaudeSessions parses a Claude JSONL file and extracts session metadata.
func parseClaudeSessions(fpath string, mtime float64) ([]IndexedSession, error) {
	f, err := os.Open(fpath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sessionID := strings.TrimSuffix(filepath.Base(fpath), ".jsonl")
	var firstTS, lastTS *string
	var msgCount int
	var summaryParts []string
	var fts FTSBodyBuilder

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
		ts, _ := entry["timestamp"].(string)
		if ts != "" {
			if firstTS == nil {
				firstTS = &ts
			}
			lastCopy := ts
			lastTS = &lastCopy
		}
		etype, _ := entry["type"].(string)
		if etype == "user" || etype == "assistant" {
			msgCount++
			if msg, _ := entry["message"].(map[string]any); msg != nil {
				fts.Add(extractFirstText(msg["content"]))
			}
		}
		// Grab first assistant text for display summary
		if etype == "assistant" && len(summaryParts) < 1 {
			if msg, _ := entry["message"].(map[string]any); msg != nil {
				if text := extractFirstText(msg["content"]); text != "" {
					if len(text) > 200 {
						text = text[:200]
					}
					summaryParts = append(summaryParts, text)
				}
			}
		}
	}
	if msgCount == 0 {
		return nil, nil
	}
	summary := strings.Join(summaryParts, " ")
	return []IndexedSession{{
		SessionID:      sessionID,
		SourceType:     "claude",
		SourceFile:     fpath,
		FileMtime:      mtime,
		FirstTimestamp: firstTS,
		LastTimestamp:  lastTS,
		MessageCount:   msgCount,
		DisplaySummary: summary,
		FTSBody:        buildFTSBody(summary, &fts),
	}}, nil
}

// extractFirstText gets the first text string from message content.
func extractFirstText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		for _, block := range c {
			if b, ok := block.(map[string]any); ok {
				if bt, _ := b["type"].(string); bt == "text" {
					if t, _ := b["text"].(string); t != "" {
						return t
					}
				}
			}
		}
	}
	return ""
}

func (a *ClaudeAgent) BuildLaunchCommand(params LaunchParams) string {
	bin := resolveBinary(params.CLIPath, "claude")
	parts := []string{bin}
	// raw: the shell syntax this builder makes itself; every other part is quoted (#3362).
	var raw []string

	effectiveID := params.SessionID
	if params.ResumeSessionID != "" {
		effectiveID = params.ResumeSessionID
		parts = append(parts, "--resume", params.ResumeSessionID)
	} else {
		parts = append(parts, "--session-id", params.SessionID)
	}

	settingsFile := claudeSettingsFile(params, effectiveID)
	parts = append(parts, "--settings", settingsFile)

	if len(params.Flags) > 0 {
		parts = append(parts, params.Flags...)
	}

	parts = appendPermissionMode(parts, params)

	// Pass the prompt as a CLI positional argument so the agent starts immediately
	// without relying on fragile tmux send-keys delivery.
	cliPrompt := BuildBoardActionPrompt(params.boardPromptName(), params.Role, params.Prompt, params.PromptOverrides, params.BoardType)
	if cliPrompt != "" {
		promptFile := writeTempFile("prompt", effectiveID, "txt", []byte(cliPrompt))
		arg := FormatPromptFileArg(promptFile)
		raw = append(raw, arg)
		parts = append(parts, arg)
	}

	parts, raw = withSessionToken(parts, raw, params, "claude")
	return ShellJoin(parts, raw...)
}

// claudeSettingsFile writes the agent's merged settings (hooks, system prompt, permissions,
// tools, MCP servers, Coral's environment) to a temp file and returns its path.
func claudeSettingsFile(params LaunchParams, effectiveID string) string {
	// Build merged settings with hooks and system prompt
	merged := buildMergedSettings(params.Hooks)

	// Combine protocol + board system prompt into systemPrompt
	var sysParts []string
	if proto := readProtocolFile(params.ProtocolPath); proto != "" {
		sysParts = append(sysParts, proto)
	}
	boardSysPrompt := BuildBoardSystemPrompt(params.BoardName, params.Role, params.Prompt, params.PromptOverrides, params.BoardType)
	if boardSysPrompt != "" {
		sysParts = append(sysParts, boardSysPrompt)
	}
	sysParts = appendCoralSessionMarker(sysParts, params.SessionID)
	if len(sysParts) > 0 {
		merged["systemPrompt"] = strings.Join(sysParts, "\n\n")
	}

	// Inject permissions from capabilities
	if perms := TranslateToClaudePermissions(params.Capabilities); perms != nil {
		permMap := map[string]any{}
		if len(perms.Allow) > 0 {
			permMap["allow"] = perms.Allow
		}
		if len(perms.Deny) > 0 {
			permMap["deny"] = perms.Deny
		}
		merged["permissions"] = permMap
	}

	// Inject allowed tools
	if len(params.Tools) > 0 {
		merged["allowedTools"] = params.Tools
	}

	// Inject MCP server configs
	if len(params.MCPServers) > 0 {
		merged["mcpServers"] = params.MCPServers
	}

	// Coral environment so coral-board and the hooks can find, and identify
	// themselves to, the server that launched this agent. Built by CoralEnv so
	// every launch path agrees; see that function for why.
	{
		envMap, _ := merged["env"].(map[string]interface{})
		if envMap == nil {
			envMap = make(map[string]interface{})
		}
		for _, kv := range CoralEnv(params) {
			envMap[kv[0]] = kv[1]
		}
		merged["env"] = envMap
	}
	// Add Coral tools dir to PATH in env settings so that coral-board,
	// coral hooks, etc. can be found by Claude CLI and its subprocesses.
	if macosDir := CoralToolsDir(); macosDir != "" {
		envMap, _ := merged["env"].(map[string]interface{})
		if envMap == nil {
			envMap = make(map[string]interface{})
		}
		if existingPath, ok := envMap["PATH"].(string); ok {
			// Prepend if not already present
			if !strings.Contains(existingPath, macosDir) {
				envMap["PATH"] = macosDir + ":" + existingPath
			}
		} else {
			// Resolve $PATH from the actual environment — JSON settings files
			// are not shell-expanded, so literal "$PATH" would be passed as-is.
			currentPath := os.Getenv("PATH")
			envMap["PATH"] = macosDir + ":" + currentPath
		}
		merged["env"] = envMap
	}

	// Write settings to temp file using writeTempFile for safe creation
	data, _ := json.MarshalIndent(merged, "", "  ")
	return writeTempFile("settings", effectiveID, "json", append(data, '\n'))
}

// appendPermissionMode adds --permission-mode from the settings unless Flags already has it.
func appendPermissionMode(parts []string, params LaunchParams) []string {
	if params.PermissionMode != "" && params.PermissionMode != "default" {
		hasPermMode := false
		for _, f := range params.Flags {
			if f == "--permission-mode" {
				hasPermMode = true
				break
			}
		}
		if !hasPermMode {
			parts = append(parts, "--permission-mode", params.PermissionMode)
		}
	}

	return parts
}

// PrintArgs is the argument list for a non-interactive "claude --print" run with the same
// settings a launch gets (hooks, permissions, tools, MCP), as real argv, not a shell line:
// no session id, and no prompt, which the caller sends on stdin (#3521). A workflow step
// used to split BuildLaunchCommand's shell line on spaces, so Claude got env-var names and
// a "$(cat ...)" fragment instead of the prompt and settings. settingsFile is the temp file
// the args name: the caller removes it once the command has exited.
func (a *ClaudeAgent) PrintArgs(params LaunchParams) (args []string, settingsFile string) {
	settingsFile = claudeSettingsFile(params, params.SessionID)
	args = []string{"--print", "--no-session-persistence", "--settings", settingsFile}
	args = append(args, params.Flags...)
	return appendPermissionMode(args, params), settingsFile
}

func (a *ClaudeAgent) PrepareResume(sessionID, workingDir string) {
	basePath := a.HistoryBasePath()
	encoded := strings.ReplaceAll(strings.ReplaceAll(workingDir, "/", "-"), "_", "-")
	targetProject := filepath.Join(basePath, encoded)
	targetJSONL := filepath.Join(targetProject, sessionID+".jsonl")

	if _, err := os.Stat(targetJSONL); err == nil {
		return // Already exists
	}

	// Search for the session file in other project dirs
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		source := filepath.Join(basePath, entry.Name(), sessionID+".jsonl")
		if _, err := os.Stat(source); err == nil {
			os.MkdirAll(targetProject, 0755)
			data, err := os.ReadFile(source)
			if err == nil {
				os.WriteFile(targetJSONL, data, 0644)
			}
			// Copy session directory if it exists
			sourceDir := filepath.Join(basePath, entry.Name(), sessionID)
			targetDir := filepath.Join(targetProject, sessionID)
			if info, err := os.Stat(sourceDir); err == nil && info.IsDir() {
				copyDir(sourceDir, targetDir)
			}
			return
		}
	}
}

// Coral hooks to inject into every Claude session.
var coralHooks = map[string][]map[string]interface{}{
	// Before a tool runs: lets the chat show what an open permission prompt,
	// AskUserQuestion or plan approval is asking (it reaches the transcript
	// only after it completes).
	"PreToolUse": {
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": "coral-hook-agentic-state"},
		}},
	},
	"PostToolUse": {
		{"matcher": "TaskCreate|TaskUpdate", "hooks": []map[string]interface{}{
			{"type": "command", "command": "coral-hook-task-sync"},
		}},
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": "coral-hook-agentic-state"},
		}},
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": "coral-hook-message-check"},
		}},
	},
	// A tool that errors or is interrupted fires this instead of PostToolUse.
	"PostToolUseFailure": {
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": "coral-hook-agentic-state"},
		}},
	},
	"Stop": {
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": "coral-hook-agentic-state"},
		}},
	},
	"Notification": {
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": "coral-hook-agentic-state"},
		}},
	},
	"SessionStart": {
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": "coral-hook-agentic-state"},
		}},
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": "coral-hook-session-start"},
		}},
	},
	// The agent's own turn and request signals (#3481, #3475 phase 0), verified on
	// Claude Code 2.1.300: a prompt starts a turn; PermissionRequest fires as a tool
	// permission, an AskUserQuestion or a plan approval opens (Notification follows
	// only ~6 s later); Elicitation and ElicitationResult open and close an MCP input
	// request; StopFailure ends a turn on an API error; SessionEnd ends the session.
	"UserPromptSubmit":  {agenticStateHook},
	"PermissionRequest": {agenticStateHook},
	"Elicitation":       {agenticStateHook},
	"ElicitationResult": {agenticStateHook},
	"StopFailure":       {agenticStateHook},
	"SessionEnd":        {agenticStateHook},
}

// agenticStateHook is the hook group that sends an event to Coral's state.
var agenticStateHook = map[string]interface{}{"hooks": []map[string]interface{}{
	{"type": "command", "command": "coral-hook-agentic-state"},
}}

// buildMergedSettings is the settings file Coral passes with --settings: the user's global
// ~/.claude/settings.json, Coral's hooks and the agent's own hooks. Never the repository's
// .claude/settings.json or settings.local.json (#3591): a file passed with --settings is the
// CLI's flag channel, so a repository's hooks, env, statusLine or apiKeyHelper put there
// would apply before the folder-trust prompt, and its permissions would replace the user's.
// Claude Code reads the project and local files itself, with its own trust gating and
// scope merge.
func buildMergedSettings(agentHooks map[string]interface{}) map[string]interface{} {
	home, _ := os.UserHomeDir()
	global := readSettingsFile(filepath.Join(home, ".claude", "settings.json"))

	merged := make(map[string]interface{})
	for k, v := range global {
		merged[k] = v
	}

	mergedEnv := make(map[string]interface{})
	for _, source := range []map[string]interface{}{global} {
		if env, ok := source["env"].(map[string]interface{}); ok {
			for k, v := range env {
				mergedEnv[k] = v
			}
		}
	}
	if len(mergedEnv) > 0 {
		merged["env"] = mergedEnv
	}

	// The user's hooks, then Coral's
	mergedHooks := make(map[string][]interface{})
	for _, source := range []map[string]interface{}{global} {
		if hooks, ok := source["hooks"].(map[string]interface{}); ok {
			for event, groups := range hooks {
				if groupList, ok := groups.([]interface{}); ok {
					mergedHooks[event] = append(mergedHooks[event], groupList...)
				}
			}
		}
	}

	// Append Coral hooks
	for event, groups := range coralHooks {
		existing := mergedHooks[event]
		for _, group := range groups {
			command := ""
			if hooks, ok := group["hooks"].([]map[string]interface{}); ok && len(hooks) > 0 {
				if cmd, ok := hooks[0]["command"].(string); ok {
					command = cmd
				}
			}
			if !hookEntryExists(existing, command) {
				mergedHooks[event] = append(mergedHooks[event], group)
			}
		}
	}

	// Append per-agent hooks (level 5: workflow step / team agent config)
	for event, groups := range agentHooks {
		if groupList, ok := groups.([]interface{}); ok {
			mergedHooks[event] = append(mergedHooks[event], groupList...)
		}
	}

	merged["hooks"] = mergedHooks
	return merged
}

// maxSettingsFile caps how much of a settings file Coral reads.
const maxSettingsFile = 1 << 20

// readSettingsFile reads a settings file: a regular file, not a symlink, a FIFO or a device
// (opened non-blocking, then checked on the handle), at most maxSettingsFile bytes. Anything
// else, or invalid JSON, is an empty map.
func readSettingsFile(path string) map[string]interface{} {
	f, err := os.OpenFile(path, os.O_RDONLY|settingsOpenFlags, 0)
	if err != nil {
		return map[string]interface{}{}
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() > maxSettingsFile {
		return map[string]interface{}{}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSettingsFile+1))
	if err != nil || len(data) > maxSettingsFile {
		return map[string]interface{}{}
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]interface{}{}
	}
	return result
}

func hookEntryExists(groups []interface{}, command string) bool {
	if command == "" {
		return false
	}
	for _, g := range groups {
		if gMap, ok := g.(map[string]interface{}); ok {
			if hooks, ok := gMap["hooks"].([]interface{}); ok {
				for _, h := range hooks {
					if hMap, ok := h.(map[string]interface{}); ok {
						if hMap["command"] == command {
							return true
						}
					}
				}
			}
			// Also handle typed version
			if hooks, ok := gMap["hooks"].([]map[string]interface{}); ok {
				for _, h := range hooks {
					if h["command"] == command {
						return true
					}
				}
			}
		}
	}
	return false
}

func copyDir(src, dst string) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		slog.Warn("copyDir: failed to create directory", "dst", dst, "error", err)
		return
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			copyDir(srcPath, dstPath)
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				slog.Warn("copyDir: failed to read file", "src", srcPath, "error", err)
				continue
			}
			if err := os.WriteFile(dstPath, data, 0644); err != nil {
				slog.Warn("copyDir: failed to write file", "dst", dstPath, "error", err)
			}
		}
	}
}
