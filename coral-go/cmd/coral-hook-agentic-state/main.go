// Command coral-hook-agentic-state reads Claude Code hook JSON from stdin,
// parses agent events (tool use, stop, notification, prompt), and posts
// them to the Coral dashboard activity timeline.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"runtime/debug"
	"strings"

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

	hooks.DebugLog(fmt.Sprintf("AGENTIC_STATE RAW(%d): %s", len(raw), hooks.Truncate(string(raw), 300)))

	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		hooks.DebugLog(fmt.Sprintf("AGENTIC_STATE JSON_ERROR: %v", err))
		return
	}

	// --session-clear flag injects marker for /clear detection
	agyEvent := ""
	args := os.Args[1:]
	for i, arg := range args {
		if arg == "--session-clear" {
			d["_coral_session_clear"] = true
		}
		if arg == "--agy" && i+1 < len(args) {
			agyEvent = args[i+1]
		}
	}
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
	hooks.DebugLog(fmt.Sprintf("AGENTIC_STATE INPUT: hook_type=%s argv=%v", hookType, os.Args[1:]))

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
		if _, err := hooks.CoralAPI(base, "POST", fmt.Sprintf("/api/sessions/live/%s/pending-tool", agentName), pending); err != nil {
			hooks.DebugLog(fmt.Sprintf("PENDING_POST_FAILED: agent=%s tool=%v err=%v", agentName, pending["tool_name"], err))
		}
		return
	}

	event := parseAgenticEvent(d, hookType, sessionID)
	if event == nil {
		hooks.DebugLog(fmt.Sprintf("DROPPED (parse returned nil): hook_type=%s agent=%s", hookType, agentName))
		return
	}

	if _, err := hooks.CoralAPI(base, "POST", fmt.Sprintf("/api/sessions/live/%s/events", agentName), event); err != nil {
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
		hooks.CoralAPI(base, "POST", fmt.Sprintf("/api/sessions/live/%s/token-usage", agentName), tokenData)
	}

	hooks.DebugLog(fmt.Sprintf("DONE: agent=%s event_type=%s", agentName, event["event_type"]))
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

	// SessionStart / /clear
	if hookType == "SessionStart" || d["_coral_session_clear"] != nil {
		return map[string]any{
			"event_type": "session_reset",
			"summary":    "Session reset: /clear",
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

// withDetail sets a key on a detail map that may still be nil (tools with no
// input detail).
func withDetail(detail map[string]any, key string, val any) map[string]any {
	if detail == nil {
		detail = map[string]any{}
	}
	detail[key] = val
	return detail
}
