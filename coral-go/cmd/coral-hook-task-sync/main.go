// Command coral-hook-task-sync reads Claude Code hook JSON from stdin,
// detects TaskCreate/TaskUpdate tool calls, and syncs them to the Coral
// dashboard task list.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
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
		return
	}

	hooks.DebugLog(fmt.Sprintf("TASK_SYNC INPUT: %s", hooks.Truncate(string(raw), 500)))

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
		hooks.CoralAPI(base, "POST", fmt.Sprintf("/api/sessions/live/%s/tasks", agentName), payload)

		// Cache task ID for later TaskUpdate lookups
		taskID := parseTaskIDFromResponse(d)
		if taskID == "" {
			taskID = fmt.Sprintf("%v", inp["taskId"])
		}
		hooks.DebugLog(fmt.Sprintf("TaskCreate: cache_id=%s subject=%s", taskID, subject))
		if taskID != "" && taskID != "<nil>" {
			cacheWrite(taskID, subject)
		}

	case "TaskUpdate":
		taskID := fmt.Sprintf("%v", inp["taskId"])
		subject, _ := inp["subject"].(string)
		status, _ := inp["status"].(string)

		// Cache subject if provided
		if taskID != "" && taskID != "<nil>" && subject != "" {
			cacheWrite(taskID, subject)
		}

		if status != "completed" && status != "in_progress" {
			return
		}

		// Resolve title from event or cache
		title := subject
		if title == "" && taskID != "" && taskID != "<nil>" {
			title = cacheRead(taskID)
		}

		hooks.DebugLog(fmt.Sprintf("TaskUpdate %s: task_id=%s resolved_title=%s", status, taskID, title))

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
			qs = "?session_id=" + sessionID
		}
		resp, err := hooks.CoralAPI(base, "GET", fmt.Sprintf("/api/sessions/live/%s/tasks%s", agentName, qs), nil)
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
					fmt.Sprintf("/api/sessions/live/%s/tasks/%d", agentName, int(tID)),
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

func cacheWrite(taskID, subject string) {
	path := filepath.Join(hooks.CacheDir(), "task_"+taskID)
	os.WriteFile(path, []byte(subject), 0644)
}

func cacheRead(taskID string) string {
	path := filepath.Join(hooks.CacheDir(), "task_"+taskID)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
