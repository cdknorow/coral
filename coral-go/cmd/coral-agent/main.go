// Command coral-agent is the command-line interface an agent uses to work
// with its own tasks in Coral: the tasks the operator gives an agent that is
// not on a team board.
//
// Its task commands mirror `coral-board task` (same subcommands, flags and
// output), minus reassign, and call the matching agent task API
// (/api/agent/tasks/...). The agent is identified by its Coral session (tmux
// session name or the CORAL_SESSION_NAME variable Coral exports), the same way
// Coral's hooks resolve it. Team boards keep using coral-board.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/hooks"
)

var serverURL = hooks.CoralBase()

func printUsage() {
	fmt.Println(`coral-agent - your own tasks in Coral (agents not on a board)

Commands:
  task add "title" [--body "details"] [--priority P]  Create a task
  task list                      List all tasks
  task claim                     Claim next available task
  task current                   Show your current in-progress task
  task complete <id> [--message "note"]  Complete a task
  task cancel <id> [--message "reason"]  Cancel a task

Environment:
  CORAL_URL   Server URL (default http://localhost:8420)
  CORAL_PORT  Server port (overrides CORAL_URL)`)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "task":
		cmdTask()
	case "--help", "-h", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func cmdTask() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, `Usage: coral-agent task <subcommand> [args]

Subcommands:
  add "title" [--body "details"] [--priority P]
  list
  claim
  complete <id> [--message "note"]`)
		os.Exit(1)
	}

	sub := os.Args[2]
	taskArgs := os.Args[3:]

	switch sub {
	case "add":
		cmdTaskAdd(taskArgs)
	case "list":
		cmdTaskList()
	case "claim":
		cmdTaskClaim()
	case "current", "detail":
		cmdTaskCurrent()
	case "complete":
		cmdTaskFinish(taskArgs, "complete")
	case "cancel":
		cmdTaskFinish(taskArgs, "cancel")
	case "--help", "-h", "help":
		fmt.Fprintln(os.Stderr, `Usage: coral-agent task <subcommand> [args]

Subcommands:
  add "title" [--body "details"] [--priority P]
  list
  claim
  current                          Show your current in-progress task
  complete <id> [--message "note"]
  cancel <id> [--message "reason"]`)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "Unknown task subcommand: %s\n", sub)
		os.Exit(1)
	}
}

// resolveSessionID identifies the calling agent (as coral-board's
// resolveSubscriberID identifies a board member).
func resolveSessionID() string {
	sid := hooks.ResolveSessionID("")
	if sid == "" {
		fmt.Fprintln(os.Stderr, "Not inside a Coral agent session (no tmux session name or CORAL_SESSION_NAME).")
		os.Exit(1)
	}
	return sid
}

func cmdTaskAdd(args []string) {
	// Reorder args: pull the title (first non-flag arg) out so flags can appear after it.
	var reordered []string
	var title string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			reordered = append(reordered, args[i])
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				reordered = append(reordered, args[i+1])
				i++ // skip flag value
			}
		} else if title == "" {
			title = args[i]
		}
	}

	fs := flag.NewFlagSet("task-add", flag.ExitOnError)
	priority := fs.String("priority", "medium", "Task priority (critical, high, medium, low)")
	taskBody := fs.String("body", "", "Detailed description/instructions")
	fs.Parse(reordered)

	if title == "" {
		fmt.Fprintln(os.Stderr, `Usage: coral-agent task add "title" [--body "details"] [--priority P]`)
		os.Exit(1)
	}

	reqBody := map[string]any{
		"title":      title,
		"priority":   *priority,
		"session_id": resolveSessionID(),
	}
	if *taskBody != "" {
		reqBody["body"] = *taskBody
	}

	data, status, err := apiCallRaw("POST", "/tasks", reqBody)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusCreated && status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error creating task: %s\n", string(data))
		os.Exit(1)
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	fmt.Printf("Created Task #%.0f: %s\n", task["id"], title)
}

func cmdTaskList() {
	data, statusCode, err := apiCallRaw("GET", "/tasks?session_id="+url.QueryEscape(resolveSessionID()), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if statusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error listing tasks: %s\n", string(data))
		os.Exit(1)
	}

	var result map[string]any
	json.Unmarshal(data, &result)
	tasks, _ := result["tasks"].([]any)

	if len(tasks) == 0 {
		fmt.Println("No tasks found.")
		return
	}

	fmt.Printf("%-4s %-12s %-9s %-15s %s\n", "ID", "Status", "Priority", "Assignee", "Title")
	for _, t := range tasks {
		task, _ := t.(map[string]any)
		id, _ := task["id"].(float64)
		tStatus, _ := task["status"].(string)
		tPriority, _ := task["priority"].(string)
		tAssignee := "—"
		if a, ok := task["assigned_to"].(string); ok && a != "" {
			tAssignee = a
		}
		tTitle, _ := task["title"].(string)

		fmt.Printf("#%-3.0f %-12s %-9s %-15s %s\n", id, tStatus, tPriority, tAssignee, tTitle)
	}
}

func cmdTaskClaim() {
	body := map[string]string{"session_id": resolveSessionID()}

	data, status, err := apiCallRaw("POST", "/tasks/claim", body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if status == http.StatusNotFound && isNoTask(data) {
		fmt.Println("No available tasks")
		return
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error claiming task: %s\n", string(data))
		os.Exit(1)
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	id, _ := task["id"].(float64)
	title, _ := task["title"].(string)
	taskBody, _ := task["body"].(string)
	priority, _ := task["priority"].(string)
	fmt.Printf("Claimed Task #%.0f (%s): %s\n", id, priority, title)
	if taskBody != "" {
		fmt.Printf("\n%s\n", taskBody)
	}
}

func cmdTaskCurrent() {
	body := map[string]string{"session_id": resolveSessionID()}

	data, status, err := apiCallRaw("POST", "/tasks/current", body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if status == http.StatusNotFound && isNoTask(data) {
		fmt.Println("No active task")
		return
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", string(data))
		os.Exit(1)
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	id, _ := task["id"].(float64)
	title, _ := task["title"].(string)
	taskBody, _ := task["body"].(string)
	priority, _ := task["priority"].(string)
	status_, _ := task["status"].(string)
	fmt.Printf("Task #%.0f (%s) [%s]: %s\n", id, priority, status_, title)
	if taskBody != "" {
		fmt.Printf("\n%s\n", taskBody)
	}
}

// cmdTaskFinish is `task complete` / `task cancel`.
func cmdTaskFinish(args []string, verb string) {
	flagName, flagHelp, done, errWord := "message", "Completion message", "Completed", "completing"
	usageArg := `"note"`
	if verb == "cancel" {
		flagHelp, done, errWord, usageArg = "Cancellation reason", "Cancelled", "cancelling", `"reason"`
	}
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: coral-agent task %s <id> [--message %s]\n", verb, usageArg)
		os.Exit(1)
	}

	taskID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid task ID: %s\n", args[0])
		os.Exit(1)
	}

	fs := flag.NewFlagSet("task-"+verb, flag.ExitOnError)
	message := fs.String(flagName, "", flagHelp)
	fs.Parse(args[1:])

	body := map[string]any{"session_id": resolveSessionID()}
	if *message != "" {
		body["message"] = *message
	}

	data, status, err := apiCallRaw("POST", fmt.Sprintf("/tasks/%d/%s", taskID, verb), body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error %s task: %s\n", errWord, string(data))
		os.Exit(1)
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	title, _ := task["title"].(string)
	fmt.Printf("%s Task #%d: %s\n", done, taskID, title)
}

// isNoTask tells an empty queue (404 "No available tasks"/"No active task")
// apart from an unknown session (also 404), which is an error.
func isNoTask(data []byte) bool {
	var e struct {
		Error string `json:"error"`
	}
	json.Unmarshal(data, &e)
	return e.Error == "No available tasks" || e.Error == "No active task"
}

func apiCallRaw(method, path string, body any) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, serverURL+"/api/agent"+path, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot reach Coral server at %s: %v", serverURL, err)
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)
	return respData, resp.StatusCode, nil
}
