// Command coral-hook-agentic-state reads Claude Code hook JSON from stdin,
// parses agent events (tool use, stop, notification, prompt), and posts
// them to the Coral dashboard activity timeline.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/hooks"
	"github.com/cdknorow/coral/internal/transcriptlink"
)

func main() {
	hooks.InstallIdentity("coral-hook-agentic-state") // the agent session's token, sent with every request (#3387)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[CRASH] hook panicked: %v\n%s", r, debug.Stack())
		}
	}()

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		hooks.DebugLog(fmt.Sprintf("AGENTIC_STATE STDIN_ERROR: %v", err))
		return
	}

	hooks.DebugLog(fmt.Sprintf("AGENTIC_STATE RAW(%d)", len(raw))) // the size only: never the payload (#3571)

	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		hooks.DebugLog("AGENTIC_STATE JSON_ERROR")
		return
	}

	// --session-clear flag injects marker for /clear detection
	opts := parseArgs(os.Args[1:])
	if opts.sessionClear {
		d["_coral_session_clear"] = true
	}
	agyEvent, hooksRev := opts.agyEvent, opts.hooksRev
	if agyEvent != "" {
		// Antigravity CLI (agy) hooks (#3481): its payload names neither the event nor
		// the tool the way Claude's does; it is translated, and an event Coral doesn't
		// record is dropped here.
		var ok bool
		if d, ok = fromAgy(d, agyEvent); !ok {
			hooks.DebugLog(fmt.Sprintf("AGENTIC_STATE AGY_SKIPPED: %s", agyEvent))
			return
		}
	}

	hookType, _ := d["hook_event_name"].(string)
	if hookType == "" {
		hookType, _ = d["type"].(string)
	}
	if hookType == "" {
		hookType, _ = d["event"].(string)
	}
	if hookType == "" {
		hookType, _ = d["event_name"].(string)
	}
	hooks.DebugLog(fmt.Sprintf("AGENTIC_STATE INPUT: hook_type=%s argv=%v", hooks.LogID(hookType), os.Args[1:]))

	base := hooks.CoralBase()
	sessionID := hooks.ResolveSessionID(hooks.StrVal(d, "session_id"))
	if strings.HasPrefix(os.Getenv("CORAL_SESSION_NAME"), "codex-") {
		threadID := os.Getenv("CODEX_THREAD_ID")
		if threadID == "" {
			threadID = hooks.StrVal(d, "session_id")
		}
		if threadID != "" && threadID != sessionID {
			if err := transcriptlink.BindCodex(sessionID, threadID); err != nil {
				hooks.DebugLog(fmt.Sprintf("CODEX_LINK_FAILED: %v", err))
			}
		}
	}
	agentName := hooks.ResolveAgentName(d)
	if agentName == "" {
		hooks.DebugLog(fmt.Sprintf("DROPPED (no agent_name): hook_type=%s", hookType))
		return
	}

	// PreToolUse: record the tool call that is starting so the chat can show
	// what a permission prompt, question or plan approval is asking. It is not
	// an activity event; its PostToolUse is.
	if hookType == "PreToolUse" {
		pending := parsePendingTool(d, sessionID)
		if pending == nil {
			return
		}
		if _, err := hooks.CoralAPI(base, "POST", hooks.LivePath(agentName, "/pending-tool"), pending); err != nil {
			hooks.DebugLog(fmt.Sprintf("PENDING_POST_FAILED: agent=%s tool=%v err=%v", agentName, pending["tool_name"], err))
		}
		return
	}

	// Claude's turn ended: if the agent has unread board messages, Coral hands them over
	// here and the agent goes on (#3481, #3475 3A); its turn didn't end, so no stop is
	// recorded. Any failure means no hand-off: the stop is recorded as usual.
	if hookType == "Stop" && agyEvent == "" && strings.HasPrefix(os.Getenv("CORAL_SESSION_NAME"), "claude-") {
		active, _ := d["stop_hook_active"].(bool)
		if out := turnEndHandoff(base, agentName, active); out != nil {
			os.Stdout.Write(out)
			hooks.DebugLog(fmt.Sprintf("TURN_END_HANDOFF: agent=%s", agentName))
			return
		}
	}

	event := parseAgenticEvent(d, hookType, sessionID)
	if event == nil {
		hooks.DebugLog(fmt.Sprintf("DROPPED (parse returned nil): hook_type=%s agent=%s", hookType, agentName))
		return
	}

	if hooksRev > 0 {
		event["hooks_rev"] = hooksRev // which hook set this agent loaded at launch (#3655 F3)
	}
	if _, err := hooks.CoralAPI(base, "POST", hooks.LivePath(agentName, "/events"), event); err != nil {
		hooks.DebugLog(fmt.Sprintf("POST_FAILED: base=%s agent=%s event_type=%s err=%v", base, agentName, event["event_type"], err))
		return
	}

	// Forward token usage data on Stop events
	if hookType == "Stop" && (d["total_input_tokens"] != nil || d["total_output_tokens"] != nil || d["total_cost_usd"] != nil) {
		tokenData := map[string]any{
			"session_id":    sessionID,
			"input_tokens":  d["total_input_tokens"],
			"output_tokens": d["total_output_tokens"],
			"cost_usd":      d["total_cost_usd"],
			"num_turns":     d["num_turns"],
		}
		hooks.CoralAPI(base, "POST", hooks.LivePath(agentName, "/token-usage"), tokenData)
	}

	hooks.DebugLog(fmt.Sprintf("DONE: agent=%s event_type=%s", agentName, event["event_type"]))
}

// hookArgs are the hook's own arguments, from the command Coral installed.
type hookArgs struct {
	sessionClear bool   // --session-clear: a /clear
	agyEvent     string // --agy <event>: an Antigravity hook
	hooksRev     int    // --hooks-rev N: the hook set's revision (#3655 F3); 0 when absent or not a positive number
}

func parseArgs(args []string) hookArgs {
	var o hookArgs
	for i, arg := range args {
		switch {
		case arg == "--session-clear":
			o.sessionClear = true
		case arg == "--agy" && i+1 < len(args):
			o.agyEvent = args[i+1]
		case arg == "--hooks-rev" && i+1 < len(args):
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
				o.hooksRev = n
			}
		}
	}
	return o
}

// turnEndHandoff asks Coral whether to keep the agent going with its board messages,
// and returns the Stop hook output that does, or nil. Claude shows a blocking reason as
// "Stop hook error: <reason>" whatever it says, so the reason is short and the
// instruction is the additionalContext ("Stop hook feedback"); verified on 2.1.300.
func turnEndHandoff(base, agentName string, stopHookActive bool) []byte {
	if stopHookActive {
		return nil // Claude's own loop guard: hand off at most once per stop chain
	}
	raw, err := hooks.CoralAPI(base, "POST", hooks.LivePath(agentName, "/turn-end"), map[string]any{"stop_hook_active": false})
	if err != nil {
		hooks.DebugLog(fmt.Sprintf("TURN_END_FAILED: %v", err))
		return nil
	}
	var r struct {
		Handoff bool   `json:"handoff"`
		Reason  string `json:"reason"`
		Context string `json:"context"`
	}
	if json.Unmarshal(raw, &r) != nil || !r.Handoff || r.Reason == "" {
		return nil
	}
	out, err := json.Marshal(map[string]any{
		"decision": "block",
		"reason":   r.Reason,
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "Stop",
			"additionalContext": r.Context,
		},
	})
	if err != nil {
		return nil
	}
	return out
}

// parsePendingTool builds the pending-tool payload for a PreToolUse hook.
func parsePendingTool(d map[string]any, sessionID string) map[string]any {
	tool, _ := d["tool_name"].(string)
	if tool == "" {
		return nil
	}
	return map[string]any{
		"session_id":  sessionID,
		"tool_use_id": hooks.StrVal(d, "tool_use_id"),
		"tool_name":   tool,
		"tool_input":  hooks.GetToolInput(d),
	}
}

func parseAgenticEvent(d map[string]any, hookType, sessionID string) map[string]any {
	// The agent's own signals that it waits on the user, or stopped waiting (#3481,
	// #3475 phase 0). They are matched by event name first: PermissionRequest carries a
	// tool_name and would otherwise read as a tool call.
	if event := parseRequestEvent(d, hookType, sessionID); event != nil {
		return event
	}

	// SessionStart / /clear: a turn boundary whatever started the session; its summary says what did, from
	// the hook's source (Claude and Codex both send it; a fresh launch was shown as "/clear", #4545).
	if hookType == "SessionStart" || d["_coral_session_clear"] != nil {
		return map[string]any{
			"event_type": "session_reset",
			"summary":    sessionStartSummary(d),
			"session_id": sessionID,
		}
	}

	// UserPromptSubmit
	if hookType == "UserPromptSubmit" || (d["prompt"] != nil && d["tool_name"] == nil && d["stop_hook_active"] == nil) {
		return map[string]any{
			"event_type": "prompt_submit",
			"summary":    "User submitted prompt",
			"session_id": sessionID,
		}
	}

	// Tool use
	tool, _ := d["tool_name"].(string)
	if tool == "" {
		if toolData, _ := d["tool"].(map[string]any); toolData != nil {
			tool, _ = toolData["name"].(string)
		}
	}
	if tool == "" {
		tool, _ = d["name"].(string)
	}
	if tool != "" {
		inp := hooks.GetToolInput(d)
		summary := hooks.MakeToolSummary(tool, inp)
		detail := makeToolDetail(tool, inp)
		if resp := makeResponseDetail(d["tool_response"]); resp != nil {
			detail = withDetail(detail, "response", resp)
		}
		// Codex's PostToolUse gives a shell command's output as a plain string, with no exit code: its
		// transcript has it (#4545).
		codexFailed := false
		if _, isText := d["tool_response"].(string); isText && hookType == "PostToolUse" {
			if code, status, ok := codexToolResult(hooks.StrVal(d, "transcript_path"), hooks.StrVal(d, "tool_use_id")); ok {
				detail = withDetail(detail, "exit_code", code)
				codexFailed = status == "failed" || code != 0
			}
		}
		if codexFailed {
			summary = "Failed: " + summary
			detail = withDetail(detail, "failed", true)
		}
		// PostToolUseFailure: the tool errored or was interrupted.
		if hookType == "PostToolUseFailure" || d["error"] != nil {
			summary = "Failed: " + summary
			detail = withDetail(detail, "failed", true)
			if errMsg, _ := d["error"].(string); errMsg != "" {
				detail = withDetail(detail, "error", hooks.Truncate(errMsg, 300))
			}
			if interrupted, _ := d["is_interrupt"].(bool); interrupted {
				detail = withDetail(detail, "interrupted", true)
			}
		}
		event := map[string]any{
			"event_type":  "tool_use",
			"tool_name":   tool,
			"summary":     summary,
			"detail_json": detail,
			"session_id":  sessionID,
		}
		if id, _ := d["tool_use_id"].(string); id != "" {
			event["tool_use_id"] = id
		}
		// Measured by the agent around the tool call itself.
		if ms, ok := d["duration_ms"].(float64); ok && ms >= 0 {
			event["duration_ms"] = int64(ms)
		}
		return event
	}

	// Stop
	if hookType == "Stop" || d["stop_hook_active"] != nil {
		reason, _ := d["reason"].(string)
		if reason == "" {
			reason = "unknown"
		}
		return map[string]any{
			"event_type": "stop",
			"summary":    "Agent stopped: " + reason,
			"session_id": sessionID,
		}
	}

	// Notification
	if hookType == "Notification" || d["message"] != nil {
		message, _ := d["message"].(string)
		event := map[string]any{
			"event_type": "notification",
			"summary":    "Notification: " + hooks.Truncate(message, 100),
			"session_id": sessionID,
		}
		// Claude Code names the kind (permission_prompt, idle_prompt, elicitation_dialog,
		// ...): kept so the state needn't rely on the message's wording (#3475).
		if t, _ := d["notification_type"].(string); t != "" {
			event["detail_json"] = map[string]any{"notification_type": t}
		}
		return event
	}

	return nil
}

// fromAgy translates an agy (Antigravity CLI 1.3.5) hook payload into the Claude shape
// parseAgenticEvent reads. agy's hooks (installed per session through --add-dir) are
// SessionStart, PreInvocation, PreToolUse, PostToolUse, PostInvocation and Stop; its
// payload is camelCase: toolCall{name,args}, error, terminationReason, invocationNum.
//   - PreInvocation 0 (the model's first call of a turn) starts a turn, as a prompt;
//     later PreInvocations and every PostInvocation record nothing.
//   - PreToolUse goes to the pending tool, like Claude's: while it is pending a
//     permission prompt can be open (agy signals no prompt itself).
//   - An empty error is not a failure.
func fromAgy(d map[string]any, event string) (map[string]any, bool) {
	out := map[string]any{"hook_event_name": event}
	if call, _ := d["toolCall"].(map[string]any); call != nil {
		out["tool_name"], _ = call["name"].(string)
		if args, _ := call["args"].(map[string]any); args != nil {
			out["tool_input"] = args
		}
	}
	if step, ok := d["stepIdx"].(float64); ok {
		out["tool_use_id"] = fmt.Sprintf("agy-step-%d", int64(step))
	}
	if errMsg, _ := d["error"].(string); errMsg != "" && event == "PostToolUse" {
		out["error"] = errMsg
	}
	switch event {
	case "SessionStart", "PreToolUse", "PostToolUse":
	case "PreInvocation":
		if n, _ := d["invocationNum"].(float64); n != 0 {
			return nil, false
		}
		out["hook_event_name"] = "UserPromptSubmit"
	case "Stop":
		reason, _ := d["terminationReason"].(string)
		if reason == "" {
			reason = "unknown"
		}
		out["reason"] = strings.ToLower(reason)
	default:
		return nil, false
	}
	return out, true
}

// requestKinds maps the tool a Claude PermissionRequest is for to what the user is
// asked: a question (AskUserQuestion), a plan approval (ExitPlanMode), else a tool
// permission (all three fire PermissionRequest; verified on Claude Code 2.1.300).
var requestKinds = map[string]string{"AskUserQuestion": "question", "ExitPlanMode": "plan"}

// parseRequestEvent maps the hooks that open or close a request to the user, and the
// end of a session:
//   - PermissionRequest (a tool permission, question or plan approval) and Elicitation
//     (an MCP server asks for input): permission_request, with its kind.
//   - ElicitationResult: request_resolved. A permission prompt has no such hook (an
//     approval shows as the tool's PostToolUse, a denial as nothing at all).
//   - StopFailure (the turn ended on an API error): stop.
//   - SessionEnd: session_end.
func parseRequestEvent(d map[string]any, hookType, sessionID string) map[string]any {
	switch hookType {
	case "PermissionRequest":
		tool, _ := d["tool_name"].(string)
		kind := requestKinds[tool]
		if kind == "" {
			kind = "permission"
		}
		event := map[string]any{
			"event_type":  "permission_request",
			"summary":     "Waiting for the user: " + kind + " (" + hooks.Truncate(tool, 60) + ")",
			"detail_json": map[string]any{"kind": kind, "tool_name": tool},
			"session_id":  sessionID,
		}
		if tool != "" {
			event["tool_name"] = tool
		}
		return event
	case "Elicitation":
		server, _ := d["mcp_server_name"].(string)
		return map[string]any{
			"event_type":  "permission_request",
			"summary":     "Waiting for the user: input for " + hooks.Truncate(server, 60),
			"detail_json": map[string]any{"kind": "elicitation", "mcp_server": server},
			"session_id":  sessionID,
		}
	case "ElicitationResult":
		action, _ := d["action"].(string)
		return map[string]any{
			"event_type":  "request_resolved",
			"summary":     "Input request answered: " + hooks.Truncate(action, 20),
			"detail_json": map[string]any{"kind": "elicitation", "action": action},
			"session_id":  sessionID,
		}
	case "StopFailure":
		msg, _ := d["error"].(string)
		return map[string]any{
			"event_type":  "stop",
			"summary":     "Agent stopped: error",
			"detail_json": map[string]any{"failed": true, "error": hooks.Truncate(msg, 300)},
			"session_id":  sessionID,
		}
	case "SessionEnd":
		reason, _ := d["reason"].(string)
		if reason == "" {
			reason = "other"
		}
		return map[string]any{
			"event_type":  "session_end",
			"summary":     "Session ended: " + hooks.Truncate(reason, 40),
			"detail_json": map[string]any{"reason": reason},
			"session_id":  sessionID,
		}
	}
	return nil
}

func makeToolDetail(tool string, inp map[string]any) map[string]any {
	detail := map[string]any{}
	switch tool {
	case "Bash":
		detail["command"], _ = inp["command"].(string)
	case "Read":
		detail["file_path"], _ = inp["file_path"].(string)
	case "Write":
		detail["file_path"], _ = inp["file_path"].(string)
	case "Edit":
		detail["file_path"], _ = inp["file_path"].(string)
	case "Grep":
		detail["pattern"], _ = inp["pattern"].(string)
	case "Glob":
		detail["pattern"], _ = inp["pattern"].(string)
	case "WebFetch":
		detail["url"], _ = inp["url"].(string)
	case "WebSearch":
		detail["query"], _ = inp["query"].(string)
	case "Agent", "Task":
		detail["description"], _ = inp["description"].(string)
		detail["subagent_type"], _ = inp["subagent_type"].(string)
	default:
		return nil
	}
	return detail
}

// responseStringKeys are the short string fields worth keeping from a tool
// response. Free-text output (stdout, file contents, page bodies) is not.
var responseStringKeys = map[string]bool{"codeText": true, "status": true, "type": true}

// makeResponseDetail keeps the scalar metadata of a tool response: HTTP status
// and byte counts for WebFetch, search timings, interrupt flags, counters, and
// whatever numeric or boolean fields an MCP tool reports. Bulk content is dropped.
func makeResponseDetail(v any) map[string]any {
	resp, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for k, val := range resp {
		switch t := val.(type) {
		case float64, bool:
			out[k] = t
		case string:
			if responseStringKeys[k] && len(t) <= 64 {
				out[k] = t
			}
		}
		if len(out) >= 24 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sessionStartSummary is a SessionStart's summary, from its source ("startup", "resume", "clear",
// "compact", "fork"); Coral's own /clear marker, or no source at all, is a reset.
func sessionStartSummary(d map[string]any) string {
	if d["_coral_session_clear"] != nil {
		return "Session reset: /clear"
	}
	switch hooks.StrVal(d, "source") {
	case "startup":
		return "Session started"
	case "resume":
		return "Session resumed"
	case "compact":
		return "Session compacted"
	case "fork":
		return "Session forked"
	}
	return "Session reset: /clear"
}

// codexToolResult is the exit code and status of a Codex tool call, from the item_completed record its
// transcript (the rollout file Codex names in transcript_path) holds for the call. Codex writes that record
// just before it runs PostToolUse, so a record not there yet is looked for again, briefly.
var codexToolResult = func(path, callID string) (int, string, bool) {
	if callID == "" || !strings.HasSuffix(path, ".jsonl") || !strings.HasPrefix(filepath.Base(path), "rollout-") {
		return 0, "", false
	}
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			time.Sleep(25 * time.Millisecond)
		}
		if code, status, ok := findCodexToolResult(path, callID); ok {
			return code, status, true
		}
	}
	return 0, "", false
}

// codexTranscriptTail: how much of the transcript's end is read for a call's record.
const codexTranscriptTail = 256 << 10

// findCodexToolResult reads the end of a Codex rollout file for the item_completed record of callID.
func findCodexToolResult(path, callID string) (int, string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return 0, "", false
	}
	start := fi.Size() - codexTranscriptTail
	if start < 0 {
		start = 0
	}
	buf := make([]byte, fi.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return 0, "", false
	}
	quoted, _ := json.Marshal(callID)
	lines := bytes.Split(buf, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !bytes.Contains(line, []byte(`"item_completed"`)) || !bytes.Contains(line, quoted) {
			continue
		}
		var rec struct {
			Payload struct {
				Type string `json:"type"`
				Item struct {
					Type     string   `json:"type"`
					ID       string   `json:"id"`
					Status   string   `json:"status"`
					ExitCode *float64 `json:"exit_code"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Payload.Type != "item_completed" || rec.Payload.Item.ID != callID ||
			rec.Payload.Item.Type != "CommandExecution" || rec.Payload.Item.ExitCode == nil {
			continue
		}
		return int(*rec.Payload.Item.ExitCode), rec.Payload.Item.Status, true
	}
	return 0, "", false
}

// withDetail sets a key on a detail map that may still be nil (tools with no
// input detail).
func withDetail(detail map[string]any, key string, val any) map[string]any {
	if detail == nil {
		detail = map[string]any{}
	}
	detail[key] = val
	return detail
}
