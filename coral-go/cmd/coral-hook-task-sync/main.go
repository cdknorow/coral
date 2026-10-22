// Command coral-hook-task-sync reads Claude Code hook JSON from stdin,
// detects TaskCreate/TaskUpdate tool calls, and syncs them to the Coral
// dashboard task list.
package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/subgentic/coral/internal/hooks"
)

func main() {
	hooks.InstallIdentity("coral-hook-task-sync") // the agent session's token, sent with every request (#3387)
	defer func() {
		if r := recover(); r != nil {
			hooks.LogCrash(r)
		}
	}()

	raw, ok, err := hooks.ReadStdin(os.Stdin)
	if err != nil || !ok {
		return
	}
	run(raw)
}

// run handles one hook event. The debug log names events and IDs only: never the
// payload, a subject or a title (#3571).
func run(raw []byte) {
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		return
	}

	base := hooks.CoralBase()
	sessionID := hooks.ResolveSessionID(hooks.StrVal(d, "session_id"))
	agentName := hooks.ResolveAgentName(d)
	if agentName == "" {
		return
	}

	tool, _ := d["tool_name"].(string)
	inp := hooks.GetToolInput(d)
	hooks.DebugLog(fmt.Sprintf("TASK_SYNC INPUT: tool=%s", hooks.LogID(tool)))

	switch tool {
	case "TaskCreate":
		subject, _ := inp["subject"].(string)
		if subject == "" {
			return
		}

		payload := map[string]any{"title": subject}
		if sessionID != "" {
			payload["session_id"] = sessionID
		}
		hooks.CoralAPI(base, "POST", hooks.LivePath(agentName, "/tasks"), payload) // (not CoralPost: this post is the agent's own task reaching Coral, and a busy server must not lose it)

		// Cache task ID for later TaskUpdate lookups
		taskID := parseTaskIDFromResponse(d)
		if taskID == "" {
			taskID = fmt.Sprintf("%v", inp["taskId"])
		}
		hooks.DebugLog(fmt.Sprintf("TaskCreate: cache_id=%s", hooks.LogID(taskID)))
		cacheWrite(taskID, subject)

	case "TaskUpdate":
		taskID := fmt.Sprintf("%v", inp["taskId"])
		subject, _ := inp["subject"].(string)
		status, _ := inp["status"].(string)

		// Cache subject if provided
		if subject != "" {
			cacheWrite(taskID, subject)
		}

		if status != "completed" && status != "in_progress" {
			return
		}

		// Resolve title from event or cache
		title := subject
		if title == "" {
			title = hooks.TaskCacheRead(taskID)
		}

		hooks.DebugLog(fmt.Sprintf("TaskUpdate %s: task_id=%s resolved=%t", status, hooks.LogID(taskID), title != ""))

		if title == "" {
			return
		}

		completedValue := 1 // completed
		if status == "in_progress" {
			completedValue = 2
		}

		// Find the dashboard task by title and update it
		qs := ""
		if sessionID != "" {
			qs = "?session_id=" + url.QueryEscape(sessionID)
		}
		resp, err := hooks.CoralAPI(base, "GET", hooks.LivePath(agentName, "/tasks"+qs), nil)
		if err != nil {
			return
		}

		var tasks []map[string]any
		if err := json.Unmarshal(resp, &tasks); err != nil {
			return
		}

		for _, t := range tasks {
			tTitle, _ := t["title"].(string)
			tCompleted, _ := t["completed"].(float64)
			tID, _ := t["id"].(float64)
			if tTitle == title && int(tCompleted) != 1 {
				hooks.DebugLog(fmt.Sprintf("Setting %s: dashboard_id=%d", status, int(tID)))
				hooks.CoralAPI(base, "PATCH",
					hooks.LivePath(agentName, fmt.Sprintf("/tasks/%d", int(tID))),
					map[string]any{"completed": completedValue})
				break
			}
		}
	}
}

func parseTaskIDFromResponse(d map[string]any) string {
	resp, ok := d["tool_response"].(map[string]any)
	if !ok {
		return ""
	}
	if task, ok := resp["task"].(map[string]any); ok {
		if id, ok := task["id"]; ok {
			return fmt.Sprintf("%v", id)
		}
	}
	if id, ok := resp["taskId"]; ok {
		return fmt.Sprintf("%v", id)
	}
	return ""
}

// cacheWrite caches a task's subject under the sha256 of its ID (hooks.TaskCacheWrite).
func cacheWrite(taskID, subject string) {
	if err := hooks.TaskCacheWrite(taskID, subject); err != nil {
		hooks.DebugLog(fmt.Sprintf("TASK_CACHE_SKIPPED: task_id=%s", hooks.LogID(taskID)))
	}
}
