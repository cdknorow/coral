// Command coral-agent is the command-line interface to Coral agents. `launch`
// starts a new agent from any terminal, the same way + New Agent does. The
// task commands are what an agent uses to work with its own tasks in Coral:
// the tasks the operator gives an agent that is not on a team board.
//
// Its task commands use the same workflow engine as coral-board task, scoped
// to a personal session, and call the matching agent task API
// (/api/agent/tasks/...). The agent is identified by its Coral session (tmux
// session name or the CORAL_SESSION_NAME variable Coral exports), the same way
// Coral's hooks resolve it. Team boards keep using coral-board.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/cdknorow/coral/internal/taskcli"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/hooks"
	"github.com/cdknorow/coral/internal/mediatype"
	"github.com/cdknorow/coral/internal/transcriptlink"
)

var serverURL = hooks.CoralBase()

func printUsage() {
	fmt.Println(`coral-agent - launch Coral agents and work with your own tasks

Commands:
  bind-codex <coral-id> <thread-id> Link a verified native Codex transcript
  ui <publish|list|events|remove>  Publish interactive sidebar panels
  artifact upload <file>           Store a durable Coral artifact
  artifact download <uri> [--output FILE] Download and verify an artifact; prints local path
  launch [dir] [--type T] [--name N] [--model M] [--prompt P]
                                 Launch an agent in dir (default: current
                                 directory) with your default settings
  task add "title" [--body "details"] [--priority P]  Create a task
  task list                      List all tasks
  task detail <id>               Read requirements, dependencies and results
  task claim [id]                Claim a named ready task or next ready work
  task edit <id>                 Edit unstarted task text/dependencies
  task current                   Show your current in-progress task
  task complete <id> [--message "note"]  Complete with outcome/evidence
  task cancel <id> [--message "reason"]  Cancel a task
  history search <query> [flags] Search conversation history (post-compaction)
  history context [sess] <idx>   Retrieve surrounding conversation context
  search <query> [flags]         Shortcut for 'history search'

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
	case "bind-codex":
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "Usage: coral-agent bind-codex <coral-id> <thread-id>")
			os.Exit(1)
		}
		if err := transcriptlink.BindCodex(os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("Codex transcript linked")
	case "ui":
		cmdUI(os.Args[2:])
	case "launch":
		cmdLaunch(os.Args[2:])
	case "artifact":
		cmdArtifact(os.Args[2:])
	case "task":
		cmdTask()
	case "history":
		cmdHistory(os.Args[2:])
	case "search":
		cmdHistorySearch(os.Args[2:])
	case "--help", "-h", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func cmdArtifact(args []string) {
	if len(args) >= 2 && args[0] == "download" {
		fs := flag.NewFlagSet("artifact-download", flag.ExitOnError)
		output := fs.String("output", "", "Local destination (must not exist; default: temporary file with media extension)")
		fs.Parse(args[2:])
		path, err := downloadArtifact(serverURL, args[1], *output)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(path)
		return
	}
	if len(args) < 2 || args[0] != "upload" {
		fmt.Fprintln(os.Stderr, "Usage: coral-agent artifact upload <file> [--name NAME] [--media-type TYPE]\n       coral-agent artifact download <coral://artifacts/digest> [--output FILE]")
		os.Exit(1)
	}
	file := args[1]
	fs := flag.NewFlagSet("artifact-upload", flag.ExitOnError)
	name := fs.String("name", filepath.Base(file), "Artifact filename")
	mediaType := fs.String("media-type", "", "Artifact media type (default: inferred from the file extension)")
	fs.Parse(args[2:])
	*name = mediatype.EnsureExtension(*name, file)
	*mediaType = mediatype.Resolve(*mediaType, file, *name)
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sid := resolveSessionID()
	req, err := http.NewRequest(http.MethodPost, serverURL+"/api/agent/artifacts?session_id="+url.QueryEscape(sid), bytes.NewReader(data))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	req.Header.Set("X-Artifact-Name", *name)
	if *mediaType != "" {
		req.Header.Set("X-Artifact-Media-Type", *mediaType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "Artifact upload failed: %s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}
	fmt.Println(string(body))
}

func taskUsageText() string {
	return `Usage: coral-agent task <subcommand> [args]

Subcommands:
  add "title" [--body "details"] [--priority P]
  list
  claim [id]                     Claim a specific ready task or next ready work
  current                        Show your current in-progress task
  detail <id>                    Read instructions, prerequisites and artifacts
  edit <id> [options]             Edit unstarted task text/dependencies
  publish <id>                   Publish a draft
  complete <id> [options]         Complete with outcome/evidence
  cancel <id> [--message "reason"]

Add options:
  --blocked-by '[{"task_id":1,"condition":"success","required_artifacts":["build"]}]'
  --outputs "build,report" --workflow NAME --stage NAME
  --workflow-instructions TEXT --parent ID --retry-of ID
  --completion-gates '[{"type":"report","artifact":"report"}]'  (EXPERIMENTAL, disabled by default: the server rejects new gate declarations unless it enables the feature; existing gates are inactive)
Completion options:
  --outcome success|failed --artifacts manifest.json --candidate-revision REVISION
Completed results are immutable. Dependencies must belong to this agent session.`
}

func printTaskUsage() {
	fmt.Fprintln(os.Stderr, taskUsageText())
}

func cmdTask() {
	if len(os.Args) < 3 {
		printTaskUsage()
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
		cmdTaskClaim(taskArgs...)
	case "current":
		cmdTaskCurrent()
	case "detail":
		cmdTaskDetail(taskArgs)
	case "edit":
		cmdTaskEdit(taskArgs)
	case "publish":
		cmdTaskPublish(taskArgs)
	case "complete":
		cmdTaskFinish(taskArgs, "complete")
	case "cancel":
		cmdTaskFinish(taskArgs, "cancel")
	case "--help", "-h", "help":
		printTaskUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "Unknown task subcommand: %s\n", sub)
		os.Exit(1)
	}
}

// cmdLaunch starts an agent through the server's launch API, so it gets the
// same defaults (agent type, model, permission mode) and shows up in the UI
// like one started from + New Agent.
func cmdLaunch(args []string) {
	var dir string
	var flagArgs []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flagArgs = append(flagArgs, args[i])
			if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flagArgs = append(flagArgs, args[i+1])
				i++
			}
		} else if dir == "" {
			dir = args[i]
		}
	}

	fs := flag.NewFlagSet("launch", flag.ExitOnError)
	agentType := fs.String("type", "", "Agent type: claude, codex, agy, pi or terminal (default: your Coral setting)")
	name := fs.String("name", "", "Display name")
	model := fs.String("model", "", "Model (default: your Coral setting for the agent type)")
	prompt := fs.String("prompt", "", "Initial prompt")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, `Usage: coral-agent launch [dir] [--type T] [--name N] [--model M] [--prompt P]`)
		fs.PrintDefaults()
	}
	fs.Parse(flagArgs)

	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid directory %s: %v\n", dir, err)
		os.Exit(1)
	}
	if info, err := os.Stat(absDir); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Not a directory: %s\n", absDir)
		os.Exit(1)
	}

	body := map[string]any{"working_dir": absDir}
	for key, val := range map[string]string{
		"agent_type": *agentType, "display_name": *name, "model": *model, "prompt": *prompt,
	} {
		if val != "" {
			body[key] = val
		}
	}

	// Launching checks the agent CLI and starts its session, which can take a
	// few seconds, so this call gets longer than the task calls.
	data, status, err := apiCall("POST", "/api/sessions/launch", body, 60*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var result map[string]any
	json.Unmarshal(data, &result)
	if status != http.StatusOK {
		msg, _ := result["error"].(string)
		if msg == "" {
			msg = string(data)
		}
		fmt.Fprintf(os.Stderr, "Error launching agent: %s\n", msg)
		os.Exit(1)
	}

	// Older servers don't echo agent_type back.
	launchedType, _ := result["agent_type"].(string)
	if launchedType == "" {
		launchedType = *agentType
	}
	sessionName, _ := result["session_name"].(string)
	label := strings.TrimSpace(launchedType + " agent")
	if *name != "" {
		label = *name
		if launchedType != "" {
			label += " (" + launchedType + ")"
		}
	}
	fmt.Printf("Launched %s in %s\n", label, absDir)
	fmt.Printf("  Session: %s\n", sessionName)
	fmt.Printf("  Open Coral to work with it: %s\n", serverURL)
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
	blockedBy := fs.String("blocked-by", "", "Dependency IDs or rule objects as JSON")
	outputs := fs.String("outputs", "", "Comma-separated required artifact names")
	workflow := fs.String("workflow", "", "Workflow name")
	stage := fs.String("stage", "", "Workflow stage")
	instructions := fs.String("workflow-instructions", "", "Additional workflow instructions")
	parent := fs.Int64("parent", 0, "Parent task ID")
	retry := fs.Int64("retry-of", 0, "Finished task ID to retry")
	completionGates := fs.String("completion-gates", "", "JSON completion gates (experimental and disabled by default)")
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
	if *blockedBy != "" {
		var deps any
		if err := json.Unmarshal([]byte(*blockedBy), &deps); err != nil {
			fmt.Fprintln(os.Stderr, "Invalid --blocked-by JSON:", err)
			os.Exit(1)
		}
		reqBody["blocked_by"] = deps
	}
	var names []string
	for _, name := range strings.Split(*outputs, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	workflowBody := map[string]any{"name": *workflow, "stage": *stage, "instructions": *instructions, "required_outputs": names, "parent_task_id": *parent, "retry_of": *retry}
	if *completionGates != "" {
		var gates any
		if err := json.Unmarshal([]byte(*completionGates), &gates); err != nil {
			fmt.Fprintln(os.Stderr, "Invalid --completion-gates JSON:", err)
			os.Exit(1)
		}
		workflowBody["completion_gates"] = gates
	}
	reqBody["workflow"] = workflowBody

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

func cmdTaskClaim(args ...string) {
	body := map[string]any{"session_id": resolveSessionID()}
	if len(args) > 0 {
		body["task_id"] = positiveTaskID(args[0])
	}

	data, status, err := apiCallRaw("POST", "/tasks/claim", body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if status == http.StatusNotFound && isNoTask(data) {
		fmt.Println("No available tasks")
		taskcli.PrintBlocked(os.Stdout, data)
		return
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error claiming task: %s\n", string(data))
		taskcli.PrintBlocked(os.Stderr, data)
		os.Exit(1)
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	id, _ := task["id"].(float64)
	title, _ := task["title"].(string)
	taskBody, _ := task["body"].(string)
	priority, _ := task["priority"].(string)
	fmt.Printf("Claimed Task #%.0f (%s): %s\n", id, priority, title)
	printTaskWorkflow(task)
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
	printTaskWorkflow(task)
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
	outcome := fs.String("outcome", "success", "success or failed")
	artifactsFile := fs.String("artifacts", "", "JSON artifact manifest")
	candidateRevision := fs.String("candidate-revision", "", "Exact source/build revision represented by completion evidence")
	fs.Parse(args[1:])

	body := map[string]any{"session_id": resolveSessionID()}
	if verb == "complete" {
		body["outcome"] = *outcome
		if *candidateRevision != "" {
			body["candidate_revision"] = *candidateRevision
		}
		if *artifactsFile != "" {
			data, err := os.ReadFile(*artifactsFile)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			var artifacts []map[string]any
			if err := json.Unmarshal(data, &artifacts); err != nil {
				fmt.Fprintln(os.Stderr, "Invalid artifacts JSON:", err)
				os.Exit(1)
			}
			body["artifacts"] = artifacts
		}
	}
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
	if verb == "complete" {
		fmt.Printf("Finished Task #%d (%s): %s\n", taskID, *outcome, title)
	} else {
		fmt.Printf("%s Task #%d: %s\n", done, taskID, title)
	}
}

func printTaskWorkflow(task map[string]any) {
	if workflow, ok := task["workflow"]; ok {
		data, _ := json.MarshalIndent(workflow, "", "  ")
		fmt.Printf("\nWorkflow instructions, inputs and outputs:\n%s\n", data)
	}
}

func positiveTaskID(arg string) int64 {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintln(os.Stderr, "Task ID must be a positive integer")
		os.Exit(1)
	}
	return id
}

func cmdTaskDetail(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-agent task detail <id>")
		os.Exit(1)
	}
	id := positiveTaskID(args[0])
	data, status, err := apiCallRaw("GET", fmt.Sprintf("/tasks/%d?session_id=%s", id, url.QueryEscape(resolveSessionID())), nil)
	if err != nil || status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error reading task: %s %v\n", data, err)
		os.Exit(1)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(pretty.String())
}

func cmdTaskEdit(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-agent task edit <id> [--title TEXT] [--body TEXT] [--priority P] [--blocked-by JSON]")
		os.Exit(1)
	}
	id := positiveTaskID(args[0])
	fs := flag.NewFlagSet("task-edit", flag.ExitOnError)
	fs.String("title", "", "Task title")
	fs.String("body", "", "Task details")
	fs.String("priority", "", "Task priority")
	fs.String("blocked-by", "", "Dependency JSON")
	fs.Parse(args[1:])
	body := map[string]any{"session_id": resolveSessionID()}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "blocked-by" {
			var deps any
			if err := json.Unmarshal([]byte(f.Value.String()), &deps); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			body["blocked_by"] = deps
		} else {
			body[f.Name] = f.Value.String()
		}
	})
	data, status, err := apiCallRaw("PATCH", fmt.Sprintf("/tasks/%d", id), body)
	if err != nil || status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error editing task: %s %v\n", data, err)
		os.Exit(1)
	}
	fmt.Printf("Updated Task #%d\n", id)
}

func cmdTaskPublish(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-agent task publish <id>")
		os.Exit(1)
	}
	id := positiveTaskID(args[0])
	data, status, err := apiCallRaw("POST", fmt.Sprintf("/tasks/%d/publish", id), map[string]string{"session_id": resolveSessionID()})
	if err != nil || status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error publishing task: %s %v\n", data, err)
		os.Exit(1)
	}
	fmt.Printf("Published Task #%d\n", id)
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
	return apiCall(method, "/api/agent"+path, body, 10*time.Second)
}

func apiCall(method, path string, body any, timeout time.Duration) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, serverURL+path, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot reach Coral server at %s: %v", serverURL, err)
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)
	return respData, resp.StatusCode, nil
}
