package agent

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/subgentic/coral/internal/transcriptlink"
)

// ClaudeAgent implements the Agent interface for Claude Code.
type ClaudeAgent struct {
	hist *claudeHistory // the indexer's passes over the history (#3728); made on first use
}

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
	// A transcript that grew is read from where the last pass stopped (#3728).
	hist := a.history()
	seen := map[string]bool{}
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
			seen[fpath] = true
			mtime := float64(info.ModTime().Unix())
			prev, indexed := knownMtimes[fpath]
			if indexed && prev == mtime {
				continue // file unchanged since last index
			}
			sess, err := hist.session(fpath, info, mtime, indexed)
			if err != nil {
				slog.Debug("claude: failed to parse session file", "path", fpath, "error", err)
				continue
			}
			if sess != nil {
				sessions = append(sessions, *sess)
			}
		}
	}
	hist.forget(seen)
	return sessions, nil
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

func (a *ClaudeAgent) LaunchCommand(params LaunchParams) (string, error) {
	files := &launchFiles{}
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

	settingsFile := claudeSettingsFile(files, params, effectiveID)
	parts = append(parts, "--settings", settingsFile)

	if len(params.Flags) > 0 {
		parts = append(parts, params.Flags...)
	}

	parts = appendPermissionMode(parts, params)

	// Pass the prompt as a CLI positional argument so the agent starts immediately
	// without relying on fragile tmux send-keys delivery.
	cliPrompt := BuildBoardActionPrompt(params.boardPromptName(), params.Role, params.Prompt, params.PromptOverrides, params.BoardType)
	if cliPrompt != "" {
		cliPrompt = guardSlashPrompt(cliPrompt)
		arg := files.promptArg("prompt", effectiveID, "txt", "", cliPrompt)
		raw = append(raw, arg)
		// "--": a prompt that is only a flag ("--dangerously-skip-permissions") is the
		// prompt, not that option (#3690). The prompt is the last argument.
		parts = append(parts, "--", arg)
	}

	parts, raw = withSessionToken(parts, raw, params, "claude")
	return files.done(ShellJoin(parts, raw...))
}

// claudeSettingsFile writes the agent's merged settings (hooks, system prompt, permissions,
// tools, Coral's environment) to a temp file and returns its path. MCP servers are never among
// them: Coral doesn't configure MCP servers (#3644); the CLI's own configuration does.
func claudeSettingsFile(files *launchFiles, params LaunchParams, effectiveID string) string {
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

	delete(merged, "mcpServers") // never from anything Coral was given (#3644)

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
	return files.write("settings", effectiveID, "json", append(data, '\n'))
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
// used to split the launch command's shell line on spaces, so Claude got env-var names and
// a "$(cat ...)" fragment instead of the prompt and settings. settingsFile is the temp file
// the args name: the caller removes it once the command has exited.
func (a *ClaudeAgent) PrintArgs(params LaunchParams) (args []string, settingsFile string, err error) {
	files := &launchFiles{}
	settingsFile = claudeSettingsFile(files, params, params.SessionID)
	if _, err := files.done(""); err != nil {
		return nil, "", err
	}
	args = []string{"--print", "--no-session-persistence", "--settings", settingsFile}
	args = append(args, params.Flags...)
	return appendPermissionMode(args, params), settingsFile, nil
}

func (a *ClaudeAgent) PrepareResume(sessionID, workingDir string) {
	if !IsResumeID(a.AgentType(), sessionID) {
		return // not a canonical session id: never touch the filesystem with it (#3582)
	}
	basePath := a.HistoryBasePath()
	encoded := transcriptlink.ClaudeProjectDirName(workingDir)
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
			if data, err := os.ReadFile(source); err == nil {
				writeNoFollow(targetJSONL, data)
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

// ClaudeHooksRev is the revision of the hooks Coral installs in Claude (coralHooks). It goes up
// whenever they change. Every coral-hook-agentic-state command carries it (--hooks-rev), so the
// server knows which set a running agent loaded at its launch: Claude reads its hooks only then,
// and agents outlive upgrades (#3655 F3). Revision 2 has the request and turn signals
// (PermissionRequest, Elicitation, UserPromptSubmit, StopFailure, SessionEnd: #3481); revision
// 3 names Coral's hook tools by absolute path (#3660); a command without a revision is from
// before 2.
const ClaudeHooksRev = 3

// ClaudeSignalsHooksRev is the first revision with the request and turn signals: an agent on an
// older set can't say it is idle, so the automated-input gate keeps the screen rule for it.
const ClaudeSignalsHooksRev = 2

// agenticStateCommand is Coral's state hook, with the hook set's revision.
var agenticStateCommand = "coral-hook-agentic-state --hooks-rev " + strconv.Itoa(ClaudeHooksRev)

// Coral hooks to inject into every Claude session.
var coralHooks = map[string][]map[string]interface{}{
	// Before a tool runs: lets the chat show what an open permission prompt,
	// AskUserQuestion or plan approval is asking (it reaches the transcript
	// only after it completes).
	"PreToolUse": {
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": agenticStateCommand},
		}},
	},
	"PostToolUse": {
		{"matcher": "TaskCreate|TaskUpdate", "hooks": []map[string]interface{}{
			{"type": "command", "command": "coral-hook-task-sync"},
		}},
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": agenticStateCommand},
		}},
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": "coral-hook-message-check"},
		}},
	},
	// A tool that errors or is interrupted fires this instead of PostToolUse.
	"PostToolUseFailure": {
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": agenticStateCommand},
		}},
	},
	"Stop": {
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": agenticStateCommand},
		}},
	},
	"Notification": {
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": agenticStateCommand},
		}},
	},
	"SessionStart": {
		{"hooks": []map[string]interface{}{
			{"type": "command", "command": agenticStateCommand},
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
	{"type": "command", "command": agenticStateCommand},
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

	// Append Coral hooks, each naming its tool by absolute path (#3660)
	for event, groups := range coralHooks {
		existing := mergedHooks[event]
		for _, group := range groups {
			group := resolvedHookGroup(group)
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

// readSettingsFile reads the user's settings file: a regular file (a symlink to one is fine,
// for dotfiles setups; a FIFO or a device isn't: it's opened non-blocking, then checked on
// the handle), at most maxSettingsFile bytes. Anything else, or invalid JSON, is an empty map.
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
			continue
		}
		// Only real files are copied (#3582): a symlink in the source session dir would
		// otherwise be read through, and O_NOFOLLOW stops a planted symlink at the target.
		if !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			slog.Warn("copyDir: failed to read file", "src", srcPath, "error", err)
			continue
		}
		if err := writeNoFollow(dstPath, data); err != nil {
			slog.Warn("copyDir: failed to write file", "dst", dstPath, "error", err)
		}
	}
}

// writeNoFollow writes data to a new file, failing if it exists or is a symlink (O_EXCL and, on
// unix, O_NOFOLLOW), so copying a resumed session never follows or overwrites a planted path
// (#3582). Readable by this user only (#4381): a session's transcript and files.
func writeNoFollow(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
