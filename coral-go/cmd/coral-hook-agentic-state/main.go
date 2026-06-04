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

	event := parseAgenticEvent(d, hookType, sessionID)
	if event == nil {
		hooks.DebugLog(fmt.Sprintf("DROPPED (parse returned nil): hook_type=%s agent=%s", hookType, agentName))
		return
	}

	hooks.CoralAPI(base, "POST", fmt.Sprintf("/api/sessions/live/%s/events", agentName), event)

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
		return map[string]any{
			"event_type":  "tool_use",
			"tool_name":   tool,
			"summary":     hooks.MakeToolSummary(tool, inp),
			"detail_json": makeToolDetail(tool, inp),
			"session_id":  sessionID,
		}
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
		if strings.Contains(strings.ToLower(message), "waiting for your input") {
			return map[string]any{
				"event_type": "stop",
				"summary":    "Agent stopped: waiting for input",
				"session_id": sessionID,
			}
		}
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
	default:
		return nil
	}
	return detail
}
