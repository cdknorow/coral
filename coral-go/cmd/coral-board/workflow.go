package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// workflowAPICall performs an HTTP request against /api/workflows (not /api/board).
func workflowAPICall(method, path string, body any) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, serverURL+"/api/workflows"+path, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	wait := clierr.Timeout(10 * time.Second)
	client := &http.Client{Timeout: wait}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, clierr.CannotReach(redactedServerURL(), err, wait)
	}
	defer resp.Body.Close()
	clierr.NoteStatus(resp.StatusCode)
	respData, _ := readServer(resp.Body)
	return respData, resp.StatusCode, nil
}

func cmdWorkflow() {
	if len(os.Args) < 3 {
		printWorkflowUsage()
		clierr.Exit1()
	}

	sub := os.Args[2]
	switch sub {
	case "create":
		cmdWorkflowCreate()
	case "list":
		cmdWorkflowList()
	case "trigger":
		cmdWorkflowTrigger()
	case "status":
		cmdWorkflowStatus()
	case "runs":
		cmdWorkflowRuns()
	case "kill":
		cmdWorkflowKill()
	case "--help", "-h", "help":
		printWorkflowUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "Unknown workflow subcommand: %s\n", sub)
		printWorkflowUsage()
		clierr.Exit1()
	}
}

func printWorkflowUsage() {
	fmt.Fprintln(os.Stderr, workflowUsageText)
}

const workflowUsageText = `Usage: coral-board workflow <subcommand> [args]

Every subcommand is the Operator's: an agent's session token is refused (403 forbidden_for_agent), reads included.

Subcommands:
  create --file <path>                    Create a workflow from a JSON file
  list                                    List all workflows
  trigger <name> [--context '{"k":"v"}']  Trigger a workflow by name
  status <run-id>                         Check run status with step details
  runs [--workflow <name>] [--status <s>] List recent runs
  kill <run-id>                           Kill a running workflow`

func cmdWorkflowCreate() {
	args := os.Args[3:]
	filePath := ""
	for i, arg := range args {
		if (arg == "--file" || arg == "-f") && i+1 < len(args) {
			filePath = args[i+1]
		}
	}
	if filePath == "" {
		fmt.Fprintln(os.Stderr, "Usage: coral-board workflow create --file <path>")
		clierr.Exit1()
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		clierr.Exit1()
	}

	// Validate it's valid JSON
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid JSON: %v\n", err)
		clierr.Exit1()
	}

	respData, status, err := workflowAPICall("POST", "", json.RawMessage(data))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusCreated && status != http.StatusOK {
		var errResp map[string]string
		json.Unmarshal(respData, &errResp)
		fmt.Fprintf(os.Stderr, "Error creating workflow: %s\n", errResp["error"])
		clierr.Exit1()
	}

	var wf map[string]any
	json.Unmarshal(respData, &wf)
	name, _ := wf["name"].(string)
	id, _ := wf["id"].(float64)
	fmt.Printf("Created workflow '%s' (id: %.0f)\n", name, id)
}

func cmdWorkflowList() {
	respData, status, err := workflowAPICall("GET", "", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error listing workflows: %s\n", string(respData))
		clierr.Exit1()
	}

	var result map[string]any
	json.Unmarshal(respData, &result)
	workflows, _ := result["workflows"].([]any)

	if len(workflows) == 0 {
		fmt.Println("No workflows found.")
		return
	}

	fmt.Printf("%-4s %-20s %-7s %-5s %-12s %s\n", "ID", "Name", "Enabled", "Steps", "Last Run", "Description")
	for _, w := range workflows {
		wf, _ := w.(map[string]any)
		id, _ := wf["id"].(float64)
		name, _ := wf["name"].(string)
		desc, _ := wf["description"].(string)
		enabled, _ := wf["enabled"].(float64)
		stepCount, _ := wf["step_count"].(float64)

		enabledStr := "yes"
		if enabled == 0 {
			enabledStr = "no"
		}

		lastRunStr := "—"
		if lr, ok := wf["last_run"].(map[string]any); ok && lr != nil {
			lrStatus, _ := lr["status"].(string)
			lastRunStr = lrStatus
		}

		// Truncate description
		if len(desc) > 40 {
			desc = desc[:37] + "..."
		}

		fmt.Printf("#%-3.0f %-20s %-7s %-5.0f %-12s %s\n", id, name, enabledStr, stepCount, lastRunStr, desc)
	}
}

func cmdWorkflowTrigger() {
	args := os.Args[3:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board workflow trigger <name> [--context '{...}']")
		clierr.Exit1()
	}

	name := args[0]
	var triggerCtx string
	for i, arg := range args {
		if arg == "--context" && i+1 < len(args) {
			triggerCtx = args[i+1]
		}
	}

	body := map[string]any{
		"trigger_type": "cli",
	}
	if triggerCtx != "" {
		var ctx json.RawMessage
		if err := json.Unmarshal([]byte(triggerCtx), &ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Invalid context JSON: %v\n", err)
			clierr.Exit1()
		}
		body["context"] = ctx
	}

	respData, status, err := workflowAPICall("POST", "/by-name/"+url.PathEscape(name)+"/trigger", body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusOK {
		var errResp map[string]string
		json.Unmarshal(respData, &errResp)
		fmt.Fprintf(os.Stderr, "Error triggering workflow: %s\n", errResp["error"])
		clierr.Exit1()
	}

	var result map[string]any
	json.Unmarshal(respData, &result)
	runID, _ := result["run_id"].(float64)
	wfName, _ := result["workflow_name"].(string)
	fmt.Printf("Triggered workflow '%s' — run #%.0f (status: pending)\n", wfName, runID)
}

func cmdWorkflowStatus() {
	args := os.Args[3:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board workflow status <run-id>")
		clierr.Exit1()
	}

	runID := args[0]
	respData, status, err := workflowAPICall("GET", "/runs/"+runID, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusOK {
		var errResp map[string]string
		json.Unmarshal(respData, &errResp)
		fmt.Fprintf(os.Stderr, "Error: %s\n", errResp["error"])
		clierr.Exit1()
	}

	var run map[string]any
	json.Unmarshal(respData, &run)

	runIDFloat, _ := run["id"].(float64)
	wfName, _ := run["workflow_name"].(string)
	runStatus, _ := run["status"].(string)
	currentStep, _ := run["current_step"].(float64)
	triggerType, _ := run["trigger_type"].(string)
	startedAt, _ := run["started_at"].(string)
	finishedAt, _ := run["finished_at"].(string)
	errorMsg, _ := run["error_msg"].(string)

	fmt.Printf("Run #%.0f — %s\n", runIDFloat, wfName)
	fmt.Printf("  Status:       %s\n", runStatus)
	fmt.Printf("  Trigger:      %s\n", triggerType)
	fmt.Printf("  Current step: %.0f\n", currentStep)
	if startedAt != "" {
		fmt.Printf("  Started:      %s\n", startedAt)
	}
	if finishedAt != "" {
		fmt.Printf("  Finished:     %s\n", finishedAt)
	}
	if errorMsg != "" {
		fmt.Printf("  Error:        %s\n", errorMsg)
	}

	// Print step details
	steps, _ := run["steps"].([]any)
	if len(steps) > 0 {
		fmt.Println("\n  Steps:")
		for _, s := range steps {
			step, _ := s.(map[string]any)
			idx, _ := step["index"].(float64)
			sName, _ := step["name"].(string)
			sType, _ := step["type"].(string)
			sStatus, _ := step["status"].(string)

			statusIcon := statusSymbol(sStatus)
			line := fmt.Sprintf("    %s [%.0f] %s (%s)", statusIcon, idx, sName, sType)

			// Add timing info if available
			sStarted, _ := step["started_at"].(string)
			sFinished, _ := step["finished_at"].(string)
			if sStarted != "" && sFinished != "" {
				line += fmt.Sprintf(" — %s to %s", truncTime(sStarted), truncTime(sFinished))
			} else if sStarted != "" {
				line += fmt.Sprintf(" — started %s", truncTime(sStarted))
			}

			fmt.Println(line)

			// Show output tail for completed/failed steps
			if tail, ok := step["output_tail"].(string); ok && tail != "" {
				// Indent and truncate output
				lines := strings.Split(strings.TrimSpace(tail), "\n")
				maxLines := 5
				start := 0
				if len(lines) > maxLines {
					start = len(lines) - maxLines
					fmt.Printf("      ... (%d lines truncated)\n", start)
				}
				for _, l := range lines[start:] {
					fmt.Printf("      %s\n", l)
				}
			}
		}
	}
}

func cmdWorkflowRuns() {
	args := os.Args[3:]
	workflowName := ""
	statusFilter := ""
	limit := "20"
	for i, arg := range args {
		if arg == "--workflow" && i+1 < len(args) {
			workflowName = args[i+1]
		}
		if arg == "--status" && i+1 < len(args) {
			statusFilter = args[i+1]
		}
		if arg == "--limit" && i+1 < len(args) {
			limit = args[i+1]
		}
	}

	var path string
	if workflowName != "" {
		// First, look up the workflow by name to get its ID
		wfData, wfStatus, err := workflowAPICall("GET", "/by-name/"+url.PathEscape(workflowName), nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			clierr.Exit1()
		}
		if wfStatus != http.StatusOK {
			fmt.Fprintf(os.Stderr, "Workflow '%s' not found\n", workflowName)
			clierr.Exit1()
		}
		var wf map[string]any
		json.Unmarshal(wfData, &wf)
		wfID, _ := wf["id"].(float64)
		path = fmt.Sprintf("/%.0f/runs?limit=%s", wfID, limit)
		if statusFilter != "" {
			path += "&status=" + url.QueryEscape(statusFilter)
		}
	} else {
		path = "/runs/recent?limit=" + limit
		if statusFilter != "" {
			path += "&status=" + url.QueryEscape(statusFilter)
		}
	}

	respData, status, err := workflowAPICall("GET", path, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", string(respData))
		clierr.Exit1()
	}

	var result map[string]any
	json.Unmarshal(respData, &result)
	runs, _ := result["runs"].([]any)

	if len(runs) == 0 {
		fmt.Println("No runs found.")
		return
	}

	fmt.Printf("%-6s %-20s %-10s %-8s %-20s %s\n", "Run", "Workflow", "Status", "Trigger", "Started", "Finished")
	for _, r := range runs {
		run, _ := r.(map[string]any)
		id, _ := run["id"].(float64)
		wfName, _ := run["workflow_name"].(string)
		rStatus, _ := run["status"].(string)
		trigger, _ := run["trigger_type"].(string)
		started, _ := run["started_at"].(string)
		finished, _ := run["finished_at"].(string)

		if wfName == "" {
			wfName = "—"
		}

		fmt.Printf("#%-5.0f %-20s %-10s %-8s %-20s %s\n",
			id, wfName, rStatus, trigger, truncTime(started), truncTime(finished))
	}
}

func cmdWorkflowKill() {
	args := os.Args[3:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board workflow kill <run-id>")
		clierr.Exit1()
	}

	runID := args[0]
	respData, status, err := workflowAPICall("POST", "/runs/"+runID+"/kill", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusOK {
		var errResp map[string]string
		json.Unmarshal(respData, &errResp)
		fmt.Fprintf(os.Stderr, "Error: %s\n", errResp["error"])
		clierr.Exit1()
	}

	fmt.Printf("Killed workflow run #%s\n", runID)
}

// statusSymbol returns a unicode symbol for a step status.
func statusSymbol(s string) string {
	switch s {
	case "completed":
		return "+"
	case "running":
		return ">"
	case "failed":
		return "x"
	case "skipped":
		return "-"
	case "killed":
		return "!"
	default:
		return "."
	}
}

// truncTime truncates a timestamp to the first 19 chars (YYYY-MM-DDTHH:MM:SS).
func truncTime(t string) string {
	if len(t) > 19 {
		return t[:19]
	}
	return t
}
