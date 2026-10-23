package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/transcriptlink"
)

// CodexAgent implements the Agent interface for OpenAI Codex CLI.
type CodexAgent struct {
	openHistory func(string) (*os.File, error) // nil uses the regular-file-only history opener

	// failures counts, per rollout file, the passes in a row that could not parse it (#6098): after codexParseGiveUp the file is left alone until its mtime changes.
	mu       sync.Mutex
	failures map[string]parseFailure
}

type parseFailure struct {
	mtime float64
	n     int
}

// codexParseGiveUp is how many passes in a row may fail to parse one file before it is left alone until it changes: a file that cannot be read (a permission, a half-written
// rollout) was opened again on every pass, forever.
const codexParseGiveUp = 3

// noteParseFailure records a failed parse of path at mtime and reports whether this was the pass that gave it up.
func (a *CodexAgent) noteParseFailure(path string, mtime float64) (gaveUp bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failures == nil {
		a.failures = map[string]parseFailure{}
	}
	f := a.failures[path]
	if f.mtime != mtime {
		f = parseFailure{mtime: mtime}
	}
	f.n++
	a.failures[path] = f
	return f.n == codexParseGiveUp
}

// givenUp reports whether path, at this mtime, failed enough passes in a row to be left alone.
func (a *CodexAgent) givenUp(path string, mtime float64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.failures[path]
	return ok && f.mtime == mtime && f.n >= codexParseGiveUp
}

// forgetFailures drops the marks of files a pass did not see (deleted since): the map never outgrows the rollouts that exist.
func (a *CodexAgent) forgetFailures(seen map[string]bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for path := range a.failures {
		if !seen[path] {
			delete(a.failures, path)
		}
	}
}

const codexHistoryIndexVersion = HistoryIndexVersion

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
	seen := map[string]bool{}
	defer func() { a.forgetFailures(seen) }()
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
		seen[path] = true
		if prev, ok := knownMtimes[path]; ok && prev == mtime {
			return nil // file unchanged since last index
		}
		if a.givenUp(path, mtime) {
			return nil // it failed to parse enough passes in a row, and has not changed since (#6098)
		}
		open := a.openHistory
		if open == nil {
			open = openHistoryFile
		}
		sess, err := parseCodexSessionWithOpen(path, mtime, open)
		if err != nil {
			slog.Debug("codex: failed to parse session file", "path", logsafe.Dir(path), "error", err)
			if a.noteParseFailure(path, mtime) {
				slog.Info("codex: a rollout failed to parse in several passes in a row; it is left alone until it changes", "file", logsafe.Dir(path))
			}
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
	return parseCodexSessionWithOpen(fpath, mtime, openHistoryFile)
}

func parseCodexSessionWithOpen(fpath string, mtime float64, open func(string) (*os.File, error)) (*IndexedSession, error) {
	f, err := open(fpath) // regular file, never a link or FIFO (#3601, W21)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if IsCodexGuardianRollout(f) {
		return &IndexedSession{Excluded: true, SourceType: "codex", SourceFile: fpath, FileMtime: mtime, IndexVersion: codexHistoryIndexVersion}, nil
	}

	// Derive session ID from filename: rollout-<timestamp>-<id>.jsonl
	sessionID := strings.TrimSuffix(filepath.Base(fpath), ".jsonl")

	var firstTS, lastTS *string
	var msgCount int
	var summary string
	var firstTyped, firstItem string // the first user message: from the event the CLI records a typed message as, else from a user message item (#6207)
	var previousRole, previousText, previousTS, previousKind string
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
		kind, _ := entry["type"].(string)
		if role != "" && kind != previousKind && role == previousRole && text == previousText {
			prev, e1 := time.Parse(time.RFC3339Nano, previousTS)
			now, e2 := time.Parse(time.RFC3339Nano, ts)
			if e1 == nil && e2 == nil && now.Sub(prev).Abs() <= time.Second {
				previousKind = kind
				continue
			}
		}
		if role != "" {
			previousRole, previousText, previousTS, previousKind = role, text, ts, kind
		}

		if role == "user" || role == "assistant" {
			msgCount++
			fts.Add(text)
		}
		if role == "user" {
			if t, ok := typedText(text); ok {
				if kind == "event_msg" && firstTyped == "" {
					firstTyped = clipFirstMessage(t)
				} else if firstItem == "" {
					firstItem = clipFirstMessage(t)
				}
			}
		}
		if summary == "" && role == "assistant" {
			summary = redactedCut(text, 200)
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
		IndexVersion:   codexHistoryIndexVersion,
		SourceFile:     fpath,
		FileMtime:      mtime,
		FirstTimestamp: firstTS,
		LastTimestamp:  lastTS,
		MessageCount:   msgCount,
		DisplaySummary: summary,
		FirstUserMessage: func() string {
			if firstTyped != "" {
				return firstTyped
			}
			return firstItem
		}(),
		FTSBody: buildFTSBody(summary, &fts),
	}, nil
}

// extractCodexFirstText is the text of a message for the search index: what was
// typed or the image labels, never a data URL or a path.
func extractCodexFirstText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var outputs []string
		for _, raw := range c {
			if block, ok := raw.(map[string]any); ok && block["type"] == "output_text" {
				if text, ok := block["text"].(string); ok {
					outputs = append(outputs, text)
				}
			}
		}
		if len(outputs) > 0 {
			return strings.Join(outputs, "\n")
		}
		text, _ := CodexUserContent(c)
		return text
	}
	return ""
}

func codexIndexMessage(entry map[string]any) (role, text string) {
	if role, _ := entry["role"].(string); role == "user" || role == "assistant" {
		return role, extractCodexFirstText(entry["content"])
	}

	if entry["type"] == "response_item" {
		payload, _ := entry["payload"].(map[string]any)
		if payload["type"] == "message" {
			role, _ := payload["role"].(string)
			if role == "user" || role == "assistant" {
				return role, extractCodexFirstText(payload["content"])
			}
		}
		return "", ""
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
	parts = append(parts, codexHookArgs(params.SessionID)...)
	// Note: Codex's sandbox may strip env vars from child processes.
	// coral-board handles this via board_state file fallback (reads job_title
	// from ~/.coral/board_state_{session}.json when CORAL_SUBSCRIBER_ID is unavailable).
	parts, bypassSandbox, permissionApplied := codexCapabilityFlags(parts, params.Capabilities, params.PermissionMode)
	presetApplied := permissionApplied
	parts, bypassSandbox, permissionApplied = codexUserFlags(parts, params.Flags, bypassSandbox, permissionApplied)
	if !permissionApplied {
		parts, _, _ = appendCodexPermissionMode(parts, bypassSandbox, params.PermissionMode)
	}
	parts = collapseCodexSandboxFlags(parts, presetApplied)
	parts = codexCoralSandboxNetwork(parts, params)
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
	link := transcriptlink.Codex(coralSessionID)
	if link.ThreadID == "" && transcriptlink.BindCodexByMarker(coralSessionID) { // no hook wrote a link (Codex's hooks not trusted): the rollout the session's own marker names is its thread (#6325)
		link = transcriptlink.Codex(coralSessionID)
	}
	if link.ThreadID != "" {
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
//
// On Windows, with no absolute path to the hook (no tools folder, or no hook in it), there are
// none: Codex runs without Coral's state tracking, logged once for the launch, rather than with
// a bare name a repository could shadow (#49).
func codexHookArgs(sessionID string) []string {
	command, ok := ResolvePowerShellHookCommand("coral-hook-agentic-state")
	if !ok {
		slog.Warn("codex runs without Coral's hooks, so its state isn't tracked: the hook tool has no absolute path (no tools folder, or no tool in it)",
			"session", sessionID, "tools", CoralToolsDir())
		return nil
	}
	var args []string
	for _, event := range codexHookEvents {
		args = append(args, "-c", fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command=%s}]}]`, event, tomlString(command)))
	}
	// Trusted for this launch when the hooks can't be changed by anyone but an administrator (#153, codex_trust.go):
	// the hash is of this same command, so the two can't drift.
	return append(args, codexHookTrustFor(sessionID, command)...)
}

// codexCapabilityFlags are the permission flags from the agent's capabilities, when it has them: bypass, full
// auto, or a sandbox mode and approval policy, and web search. bypass and applied say what was set.
//
// An explicit permission mode (the member's own, else the team's: anything but "" and "default") is always applied, and the capabilities may only narrow it (#6425): Plan with Shell,
// Read and Write stays read-only, Bypass with a read-only capability set is read-only.
func codexCapabilityFlags(parts []string, caps *Capabilities, mode string) (out []string, bypass, applied bool) {
	perms := TranslateToCodexPermissions(caps)
	if perms == nil {
		return parts, false, false
	}
	pair := codexPairOf(perms)
	if m, ok := codexPairForMode(mode); ok {
		if perms.FullAuto {
			pair = codexPair{codexSandboxNone, "never"} // Shell with no deny restricts nothing: against an explicit mode it is the widest set, so the mode stands as chosen
		}
		pair = narrowCodexPair(m, pair)
	}
	switch {
	case pair.sandbox == codexSandboxNone && pair.approval == "never":
		bypass = true
		parts = append(parts, "--dangerously-bypass-approvals-and-sandbox")
	case pair.sandbox == "workspace-write" && pair.approval == "never":
		parts = appendCodexFullAuto(parts)
	default:
		if pair.sandbox != "" {
			parts = append(parts, "--sandbox", pair.sandbox)
		}
		if pair.approval != "" {
			parts = append(parts, "-a", normalizeCodexApprovalPolicy(pair.approval))
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
		case (flag == "-a" || flag == "--ask-for-approval" || flag == "--approval-mode") && i+1 < len(flags):
			// Older Coral permission profiles used Codex's former "untrusted" policy (and on-failure). Current Codex accepts only on-request or never, and
			// no longer has --approval-mode (its flag is -a / --ask-for-approval): the old name is emitted as -a (#5890).
			parts = append(parts, codexApprovalFlagName(flag), normalizeCodexApprovalPolicy(flags[i+1]))
			i++
			applied = true
		case strings.HasPrefix(flag, "-a="), strings.HasPrefix(flag, "--ask-for-approval="), strings.HasPrefix(flag, "--approval-mode="):
			name, value, _ := strings.Cut(flag, "=")
			parts = append(parts, codexApprovalFlagName(name)+"="+normalizeCodexApprovalPolicy(value))
			applied = true
		case claudeOnlyFlags[flag]:
			slog.Warn("dropping Claude-specific flag for Codex agent", "flag", flag)
		case (flag == "-c" || flag == "--config") && i+1 < len(flags) && isCodexPermissionConfig(flags[i+1]):
			// -c sandbox_mode=... and -c approval_policy=... set what Codex may do without asking: a mode's flags are not added after them (#6423).
			parts = append(parts, flag, flags[i+1])
			i++
			applied = true
		default:
			if flag == "--sandbox" || flag == "-s" || strings.HasPrefix(flag, "--sandbox=") || strings.HasPrefix(flag, "-s=") {
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

// normalizeCodexApprovalPolicy maps the policies Codex 0.163 no longer takes to the nearest one it does, never to a wider one: untrusted and on-failure ask (on-request). The rest
// (on-request, never, anything else) is passed as it is, so a value Codex does not know is Codex's to refuse.
func normalizeCodexApprovalPolicy(policy string) string {
	switch policy {
	case "untrusted", "on-failure":
		return "on-request"
	}
	return policy
}

// codexApprovalFlagName is the flag Codex 0.163 has for an approval policy: -a and --ask-for-approval as they are, and the former --approval-mode, which it rejects, as -a.
func codexApprovalFlagName(flag string) string {
	if flag == "--approval-mode" {
		return "-a"
	}
	return flag
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

// codexCoralSandboxNetwork lets the autonomous workspace sandbox reach Coral's
// HTTP API. Command hooks run in Codex's host process, outside this sandbox, so
// they need no writable roots. The token and board state are read-only inputs;
// prompt handoffs are read before Codex starts, and artifact downloads use temp.
// Do not grant write access to Coral's data directory or session token files.
//
// Codex's network_access switch permits outbound networking, including loopback;
// it is not a localhost-only firewall. Keep explicit flags/config authoritative:
// this default is inserted before the user's -c overrides. Restrictive capability
// presets and the other permission modes do not acquire network access here.
func codexCoralSandboxNetwork(parts []string, params LaunchParams) []string {
	if !params.Capabilities.IsEmpty() || (params.PermissionMode != "auto" && params.PermissionMode != "dontAsk") {
		return parts
	}
	sandbox, approval := "", ""
	insert := -1
	for i := 0; i < len(parts); i++ {
		if strings.HasPrefix(parts[i], "--config=") || strings.HasPrefix(parts[i], "-c=") {
			if insert < 0 {
				insert = i
			}
			continue
		}
		if parts[i] == "-c" || parts[i] == "--config" {
			if insert < 0 {
				insert = i
			}
			i++ // a config value is never a permission flag
			continue
		}
		if parts[i] == "--dangerously-bypass-approvals-and-sandbox" || parts[i] == "--yolo" {
			return parts
		}
		if kind, n := codexPermissionFlag(parts, i); n > 0 {
			value := ""
			if n == 2 {
				value = parts[i+1]
			} else {
				_, value, _ = strings.Cut(parts[i], "=")
			}
			if kind == 's' {
				sandbox = value
			} else {
				approval = value
			}
			i += n - 1
		}
	}
	if sandbox != "workspace-write" || approval != "never" {
		return parts
	}
	if insert < 0 {
		insert = len(parts)
	}
	out := append([]string(nil), parts[:insert]...)
	out = append(out, "-c", "sandbox_workspace_write.network_access=true")
	return append(out, parts[insert:]...)
}
