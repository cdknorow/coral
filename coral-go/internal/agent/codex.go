package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/transcriptlink"
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
	f, err := openHistoryFile(fpath) // regular file, never a link or FIFO (#3601, W21)
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

// extractCodexFirstText is the text of a message for the search index: what was
// typed or the image labels, never a data URL or a path.
func extractCodexFirstText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		text, _ := CodexUserContent(c)
		return text
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
	if imgs, ok := payload["images"].([]any); ok {
		message = CodexUserMessageText(message, CodexPayloadImages(imgs))
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

func (a *CodexAgent) LaunchCommand(params LaunchParams) (string, error) {
	files := &launchFiles{}
	// raw: the shell syntax this builder makes itself; every other part is quoted (#3362).
	parts, raw := codexStart(params)
	if instructions := codexInstructions(files, params); instructions != "" {
		raw = append(raw, instructions)
		parts = append(parts, "-c", instructions)
	}
	parts = append(parts, codexHookArgs()...)
	// Note: Codex's sandbox may strip env vars from child processes.
	// coral-board handles this via board_state file fallback (reads job_title
	// from ~/.coral/board_state_{session}.json when CORAL_SUBSCRIBER_ID is unavailable).
	parts, bypassSandbox, permissionApplied := codexCapabilityFlags(parts, params.Capabilities)
	presetApplied := permissionApplied
	parts, bypassSandbox, permissionApplied = codexUserFlags(parts, params.Flags, bypassSandbox, permissionApplied)
	if !permissionApplied {
		parts, _, _ = appendCodexPermissionMode(parts, bypassSandbox, params.PermissionMode)
	}
	parts = collapseCodexSandboxFlags(parts, presetApplied)
	if arg := codexActionPrompt(files, params); arg != "" {
		raw = append(raw, arg)
		// "--": a prompt that is only a flag ("--dangerously-skip-permissions") is the
		// prompt, not that option (#3690). The prompt is the last argument.
		parts = append(parts, "--", arg)
	}
	parts, raw = withSessionToken(parts, raw, params, "codex")
	return files.done(ShellJoin(parts, raw...))
}

// CodexSessionSaved reports whether Codex has a saved session to resume for a Coral session: the thread its link names (or, with no link, the id itself) has a
// rollout file under <CODEX_HOME>/sessions. Codex writes one only once a turn has been taken: an agent that never completed one (no credits, a prompt that
// failed, one killed at its start) has none, and "codex resume <id>" ends at "No saved session found" and a bare shell (#5222).
func CodexSessionSaved(coralSessionID string) bool {
	thread := coralSessionID
	if link := transcriptlink.Codex(coralSessionID); link.ThreadID != "" {
		if link.Path != "" { // the link's own rollout file, checked by the link reader
			return true
		}
		thread = link.ThreadID
	}
	if !transcriptlink.IsSessionID(thread) { // joined into a glob below: only a UUID
		return false
	}
	found, _ := filepath.Glob(filepath.Join((&CodexAgent{}).HistoryBasePath(), "*", "*", "*", "rollout-*-"+thread+".jsonl"))
	return len(found) > 0
}

// codexStart is the command's start: Coral's environment exported, so child processes (coral-board, hooks)
// inherit it, then the binary, resuming the conversation's own Codex thread when it is known. The exports are raw
// shell syntax (formatEnvExport quotes their values for the platform's shell, so it never expands them).
// PATH injection is handled by callers via WrapWithBundlePath().
func codexStart(params LaunchParams) (parts, raw []string) {
	for _, kv := range CoralEnv(params) {
		export := formatEnvExport(kv[0], kv[1])
		raw = append(raw, export)
		parts = append(parts, export)
	}
	bin := resolveBinary(params.CLIPath, "codex")
	if params.ResumeSessionID == "" {
		return append(parts, bin), raw
	}
	resumeID := params.ResumeSessionID
	if link := transcriptlink.Codex(resumeID); link.ThreadID != "" {
		resumeID = link.ThreadID
	}
	return append(parts, bin, "resume", resumeID), raw
}

// codexInstructions is the -c developer_instructions value: the protocol file and the board system prompt (CLI
// usage, role instructions) with Coral's session marker, read from a temporary file; "" when there is nothing.
// The temp file path is from os.TempDir() (safe), and the content sources are trusted internal strings.
func codexInstructions(files *launchFiles, params LaunchParams) string {
	var sysParts []string
	if proto := readProtocolFile(params.ProtocolPath); proto != "" {
		sysParts = append(sysParts, proto)
	}
	if boardSysPrompt := BuildBoardSystemPrompt(params.BoardName, params.Role, "", params.PromptOverrides, params.BoardType); boardSysPrompt != "" {
		sysParts = append(sysParts, boardSysPrompt)
	}
	sysParts = appendCoralSessionMarker(sysParts, params.SessionID)
	if len(sysParts) == 0 {
		return ""
	}
	return files.promptArg("codex_instructions", params.SessionID, "md", "developer_instructions=", strings.Join(sysParts, "\n\n"))
}

// codexHookEvents are the Codex hooks Coral's activity hook is installed for.
var codexHookEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"}

// codexHookArgs install Coral's activity hook in Codex's config: Codex does not read Claude's settings.json, and
// without these hooks a Codex session can still appear active, but no tool, thinking or stop events reach the
// activity view.
func codexHookArgs() []string {
	var args []string
	for _, event := range codexHookEvents {
		args = append(args, "-c", fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command=%s}]}]`, event, tomlString(ResolveHookCommand("coral-hook-agentic-state"))))
	}
	return args
}

// codexCapabilityFlags are the permission flags from the agent's capabilities, when it has them: bypass, full
// auto, or a sandbox mode and approval policy, and web search. bypass and applied say what was set.
func codexCapabilityFlags(parts []string, caps *Capabilities) (out []string, bypass, applied bool) {
	perms := TranslateToCodexPermissions(caps)
	if perms == nil {
		return parts, false, false
	}
	switch {
	case perms.BypassSandbox:
		bypass = true
		parts = append(parts, "--dangerously-bypass-approvals-and-sandbox")
	case perms.FullAuto:
		parts = appendCodexFullAuto(parts)
	default:
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
	return parts, bypass, true
}

// codexUserFlags adds the user's flags, translated for Codex: permission flags only when no permission was set
// yet (Claude's --permission-mode and --dangerously-skip-permissions, Codex's bypass and the removed --full-auto
// alias), approval policies normalized (the former "untrusted" is on-request), and Claude-only flags dropped.
func codexUserFlags(parts, flags []string, bypass, applied bool) ([]string, bool, bool) {
	claudeOnlyFlags := map[string]bool{
		"--settings": true, "--session-id": true, "--resume": true,
	}
	for i := 0; i < len(flags); i++ {
		flag := flags[i]
		switch {
		case flag == "--permission-mode":
			if i+1 < len(flags) {
				i++
				if !applied {
					parts, bypass, applied = appendCodexPermissionMode(parts, bypass, flags[i])
				}
			}
		case strings.HasPrefix(flag, "--permission-mode="):
			if !applied {
				parts, bypass, applied = appendCodexPermissionMode(parts, bypass, strings.TrimPrefix(flag, "--permission-mode="))
			}
		case flag == "--dangerously-skip-permissions", flag == "--full-auto":
			// Codex's equivalent (newer Codex versions removed the --full-auto alias), unless a permission is set.
			if !bypass && !applied {
				parts = appendCodexFullAuto(parts)
				applied = true
			}
		case flag == "--dangerously-bypass-approvals-and-sandbox":
			if !bypass && !applied {
				parts = append(parts, flag)
				bypass, applied = true, true
			}
		case (flag == "-a" || flag == "--approval-mode") && i+1 < len(flags):
			// Older Coral permission profiles used Codex's former "untrusted" policy. Current Codex accepts only
			// on-request or never.
			parts = append(parts, flag, normalizeCodexApprovalPolicy(flags[i+1]))
			i++
			applied = true
		case strings.HasPrefix(flag, "-a="), strings.HasPrefix(flag, "--approval-mode="):
			name, value, _ := strings.Cut(flag, "=")
			parts = append(parts, name+"="+normalizeCodexApprovalPolicy(value))
			applied = true
		case claudeOnlyFlags[flag]:
			slog.Warn("dropping Claude-specific flag for Codex agent", "flag", flag)
		default:
			if flag == "--sandbox" || strings.HasPrefix(flag, "--sandbox=") {
				applied = true
			}
			parts = append(parts, flag)
		}
	}
	return parts, bypass, applied
}

// codexActionPrompt is the action prompt's argument, read from a temporary file: the board's action prompt, or the
// launch prompt itself; "" when there is neither.
func codexActionPrompt(files *launchFiles, params LaunchParams) string {
	actionPrompt := BuildBoardActionPrompt(params.boardPromptName(), params.Role, params.Prompt, params.PromptOverrides, params.BoardType)
	if actionPrompt == "" {
		actionPrompt = params.Prompt
	}
	if actionPrompt == "" {
		return ""
	}
	return files.promptArg("codex_prompt", params.SessionID, "txt", "", actionPrompt)
}

// codexPermissionFlag says which setting the token at parts[i] sets, 's' for the sandbox (--sandbox X, -s X, --sandbox=X, -s=X) or 'a' for the approval policy (-a X,
// --ask-for-approval X, --approval-mode X and the = forms), and how many tokens it takes (the flag and its value, or the one token of the = form); 0, 0 for anything else.
// A flag with no value (the next token is itself a flag: "--sandbox -a=never") is passed as it is.
func codexPermissionFlag(parts []string, i int) (kind byte, n int) {
	switch parts[i] {
	case "--sandbox", "-s":
		kind = 's'
	case "-a", "--ask-for-approval", "--approval-mode":
		kind = 'a'
	default:
		for _, f := range []struct {
			prefix string
			kind   byte
		}{{"--sandbox=", 's'}, {"-s=", 's'}, {"-a=", 'a'}, {"--ask-for-approval=", 'a'}, {"--approval-mode=", 'a'}} {
			if strings.HasPrefix(parts[i], f.prefix) {
				return f.kind, 1
			}
		}
		return 0, 0
	}
	if i+1 < len(parts) && !strings.HasPrefix(parts[i+1], "-") {
		return kind, 2
	}
	return 0, 0
}

// collapseCodexSandboxFlags keeps one sandbox setting and one approval setting, whatever the spelling (--sandbox X, -s X, --sandbox=X; -a X, --ask-for-approval X, -a=X): Codex
// refuses either given twice ("the argument '--sandbox <SANDBOX_MODE>' cannot be used multiple times"), and the same pair reaching the builder twice (stored by a restart, added
// again by a wake) left the agent at a bare shell (#5222). The later one is the newer, so it is the one kept.
//
// keepFirst: the capabilities preset typed them first, and a flag stored from an earlier launch (or typed by hand) must not widen it: the preset's pair wins (#5247).
func collapseCodexSandboxFlags(parts []string, keepFirst bool) []string {
	keep := map[byte]int{}
	for i := 0; i < len(parts); i++ {
		kind, n := codexPermissionFlag(parts, i)
		if n == 0 {
			continue
		}
		if _, seen := keep[kind]; !seen || !keepFirst {
			keep[kind] = i
		}
		i += n - 1
	}
	out := make([]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		if kind, n := codexPermissionFlag(parts, i); n > 0 {
			if keep[kind] != i {
				i += n - 1 // an earlier (or, with keepFirst, a later) one: dropped with its value
				continue
			}
			out = append(out, parts[i:i+n]...)
			i += n - 1
			continue
		}
		out = append(out, parts[i])
	}
	return out
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
