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

	"github.com/cdknorow/coral/internal/hooks"
)

func main() {
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
	for _, arg := range os.Args[1:] {
		if arg == "--session-clear" {
			d["_coral_session_clear"] = true
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
		return map[string]any{
			"event_type": "notification",
			"summary":    "Notification: " + hooks.Truncate(message, 100),
			"session_id": sessionID,
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
