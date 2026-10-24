package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"github.com/subgentic/coral/internal/clihelp"
	"github.com/subgentic/coral/internal/taskcli"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func cmdTask() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, `Usage: coral-board task <subcommand> [args]

Subcommands:
  add "title" [--body "details"] [--priority P]
  list [--json]
  claim
  submit-review <id> --reason "review pending" [--revision N] [--candidate-revision REVISION] [--message "note"] [--outcome success|failed] [--artifacts manifest.json]
  release-review <id> --reason "slot released pending review" (orchestrator only)
  review <id> --to "Security Reviewer,QA Engineer"   (orchestrator: delegate a task in review)
  approve <id> [--round N] [--note "..."] / reject <id> [--round N] --note "why"   (a delegated reviewer)
  attach <id> --artifacts manifest.json / send-back <id> --note "why" / mark-landed <id> --digest D [--commit SHA[:subject]]   (the merge queue)
  complete <id> [--message "note"]`)
		clierr.Exit1()
	}

	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board. Run: coral-board join <project> --as <role>")
		clierr.Exit1()
	}

	sub := os.Args[2]
	taskArgs := os.Args[3:]

	switch sub {
	case "add":
		cmdTaskAdd(st, taskArgs)
	case "list":
		asJSON, goal := taskListArgs(taskArgs)
		cmdTaskListGoal(st, asJSON, goal)
	case "goal":
		cmdTaskGoal(st, taskArgs)
	case "claim":
		cmdTaskClaim(st, taskArgs...)
	case "current":
		cmdTaskCurrent(st, hasFlag(taskArgs, "--json"))
	case "detail":
		wantJSON := hasFlag(taskArgs, "--json")
		taskArgs = withoutFlag(taskArgs, "--json") // task detail always prints JSON; --json is accepted so a script can say so
		if len(taskArgs) == 0 {
			cmdTaskCurrent(st, wantJSON)
			return
		}
		id, err := strconv.ParseInt(taskArgs[0], 10, 64)
		if err != nil || id <= 0 {
			fmt.Fprintln(os.Stderr, "task detail requires a positive task ID")
			clierr.Exit1()
		}
		data, status, err := apiCallRaw("GET", fmt.Sprintf("/%s/tasks/%d", url.PathEscape(st.Project), id), nil)
		if err != nil || status != http.StatusOK {
			fmt.Fprintln(os.Stderr, "Unable to read task: "+clierr.Describe(err, status, data))
			clierr.Exit1()
		}
		var task map[string]any
		if err := json.Unmarshal(data, &task); err != nil {
			fmt.Fprintln(os.Stderr, err)
			clierr.Exit1()
		}
		noteSeenRound(st.Project, task)
		pretty, _ := json.MarshalIndent(task, "", "  ")
		fmt.Println(string(pretty))
	case "show":
		cmdTaskShow(st, taskArgs)
	case "submit-review", "release-review":
		cmdTaskReview(st, taskArgs, sub)
	case "review", "approve", "reject":
		cmdTaskReviewFlow(st, taskArgs, sub)
	case "attach", "send-back", "mark-landed":
		cmdTaskQueueVerb(st, taskArgs, sub)
	case "complete":
		cmdTaskComplete(st, taskArgs)
	case "cancel":
		cmdTaskCancel(st, taskArgs)
	case "reassign":
		cmdTaskReassign(st, taskArgs)
	case "unblock":
		cmdTaskUnblock(st, taskArgs)
	case "amend":
		cmdTaskAmend(st, taskArgs)
	case "park", "unpark":
		cmdTaskPark(st, taskArgs, sub == "unpark")
	case "--help", "-h", "help":
		fmt.Println(taskUsageText)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "Unknown task subcommand: %s\n", sub)
		os.Exit(2) // usage
	}
}

const taskUsageText = `Usage: coral-board task <subcommand> [args]

Subcommands:
  add "title" [--body "details"] [--priority P] [--assignee "Agent Name"] [--remind-at TIME] [--goal ID]
  list [--json] [--goal ID]        Every task, or one goal's; --json prints the tasks as a JSON array
  goal <task-id> <goal-id|none>    Put a task on a goal of this board, move it, or take it off (coral-board goal ... manages the goals)
  claim [id]                       Claim a specific task or the next available one
  current                          Show your current in-progress task
  detail <id> [--json]             Read task instructions, inputs and results (always JSON; --json is accepted)
  show <id> [--json]               A task for a person: title, status, assignee, body, outcome, result message and its
                                   artifacts' coral:// links (--json: those fields as one JSON object)
  submit-review <id> --reason "review pending" [--revision N] [--message "note"] [--outcome success|failed] [--artifacts manifest.json]
                                   Hands the task to the team's orchestrator (frees your slot); with no orchestrator, completes it
  review <id> --to "Security Reviewer,QA Engineer"  The orchestrator delegates a task in review: one review task each
  approve <id> [--round N] [--revision N] [--note "..."]  A reviewer approves, any time the task is in review; the last approval completes it
  reject <id> [--round N] --note "why"     A reviewer rejects: back to its author; the reviewers stay on it and review the resubmission
  attach <id> --artifacts manifest.json    Add patch rows to a task in review (a next revision, a reviewer's fix); a reviewer who had approved reviews the new bytes again
  send-back <id> --note "why"              The orchestrator returns an approved, completed task to its author (a fix after approval, a patch that would not apply)
  mark-landed <id> --digest D [--digest D2] [--commit SHA[:subject]]  The orchestrator (or the Operator) records that the approved patches with these digests landed, and the commits that landed them; a digest its round did not approve is refused
  release-review <id> --reason "slot released pending review" (orchestrator only; for a candidate submitted before v1.4.56)
  complete <id> [--revision N] [--message "note"] [--outcome success|failed] [--candidate-revision REVISION] [--artifacts manifest.json]
  cancel <id> [--message "reason"]
  reassign <id> [--to "Agent Name"]
  unblock <id> (--blocker ID | --all) Remove one or all prerequisites (orchestrator only)
  park <id> --reason "why" [--until DATE]  Set a task aside: no reminders, nudges or check items for it until
                                   it is unparked or DATE passes (orchestrator or Operator only)
  unpark <id>                      Bring a parked task back
  amend <id> --revision N --reason "..." [--body "..." --workflow-instructions "..." --completion-gates JSON (experimental, disabled by default; "[]" clears)]

Workflow options for add:
  --workflow "name" --stage "Build" --outputs "build,test_report"
  --blocked-by '[{"task_id":1,"condition":"success","required_artifacts":["build"]}]'
  --workflow-instructions "project-specific guidance" --parent <id> --retry-of <id>
  --completion-gates '[{"type":"report","artifact":"report"}]'  (EXPERIMENTAL, disabled by default: the server rejects new gate declarations unless it enables the feature; existing gates are inactive)
Conditions: success (default), failure, termination. Completed results are immutable.`

func cmdTaskAdd(st *boardState, args []string) {
	// Reorder args: pull the title (first non-flag arg) out so flags can appear after it.
	// Go's flag package stops at the first non-flag arg, so we move flags before the title.
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

	fs := flag.NewFlagSet("task-add", flag.ContinueOnError)
	priority := fs.String("priority", "medium", "Task priority (critical, high, medium, low)")
	taskBody := fs.String("body", "", "Detailed description/instructions")
	assignee := fs.String("assignee", "", "Assign task to a specific agent")
	blockedBy := fs.String("blocked-by", "", "Dependency JSON: [1] or [{\"task_id\":1,\"condition\":\"success\",\"required_artifacts\":[\"build\"]}]")
	outputs := fs.String("outputs", "", "Comma-separated required output artifact names")
	workflow := fs.String("workflow", "", "Workflow name (e.g. Build -> Test -> Release)")
	stage := fs.String("stage", "", "Stage name")
	instructions := fs.String("workflow-instructions", "", "Additional instructions appended to Coral's default task workflow")
	reviewers := fs.String("reviewers", "", "Who reviews it, comma separated (\"Security Reviewer,QA Engineer\"): Coral delegates the review to them when the task is submitted (used when the orchestrator or the Operator adds the task)")
	completionGates := fs.String("completion-gates", "", "JSON completion gates (experimental and disabled by default)")
	remindAt := fs.String("remind-at", "", "A one-time reminder to the assignee (or the orchestrator) at that time: \"2026-10-19 21:58\", 21:58, 2026-10-19, 30m, 2h or 3d")
	parent := fs.Int64("parent", 0, "Parent task ID")
	retryOf := fs.Int64("retry-of", 0, "Finished task ID this new task retries")
	goal := fs.Int64("goal", 0, "The goal of this board the task serves")
	clihelp.ParseFlags(fs, reordered, func() string { return commandUsage(os.Args[1], os.Args[2:]) })
	*taskBody = flagText(*taskBody, "task body")

	if title == "" {
		fmt.Fprintln(os.Stderr, `Usage: coral-board task add "title" [--body "details"] [--priority P] [--assignee "Agent Name"] [--reviewers "A,B"]`)
		clierr.Exit1()
	}
	subscriberID := resolveSubscriberID()

	reqBody := map[string]any{
		"title":         title,
		"priority":      *priority,
		"subscriber_id": subscriberID,
	}
	if *taskBody != "" {
		reqBody["body"] = *taskBody
	}
	if *assignee != "" {
		reqBody["assigned_to"] = *assignee
	}
	if *goal < 0 {
		fmt.Fprintln(os.Stderr, "--goal needs a goal ID, a whole number")
		clierr.Exit1()
	}
	if *goal > 0 {
		reqBody["goal_id"] = *goal
	}
	if *remindAt != "" {
		at, err := parseWhen(*remindAt, time.Now())
		if err != nil {
			fmt.Fprintln(os.Stderr, "--remind-at:", err)
			clierr.Exit1()
		}
		reqBody["remind_at"] = at.UTC().Format(time.RFC3339)
	}
	if *blockedBy != "" {
		var deps any
		if err := json.Unmarshal([]byte(*blockedBy), &deps); err != nil {
			fmt.Fprintln(os.Stderr, "Invalid --blocked-by JSON:", err)
			clierr.Exit1()
		}
		reqBody["blocked_by"] = deps
	}
	var required []string
	for _, name := range strings.Split(*outputs, ",") {
		if name = strings.TrimSpace(name); name != "" {
			required = append(required, name)
		}
	}
	workflowBody := map[string]any{"name": *workflow, "stage": *stage, "instructions": *instructions, "required_outputs": required, "parent_task_id": *parent, "retry_of": *retryOf}
	if *completionGates != "" {
		var gates any
		if err := json.Unmarshal([]byte(*completionGates), &gates); err != nil {
			fmt.Fprintln(os.Stderr, "Invalid --completion-gates JSON:", err)
			clierr.Exit1()
		}
		workflowBody["completion_gates"] = gates
	}
	if *reviewers != "" {
		var names []string
		for _, r := range strings.Split(*reviewers, ",") {
			if r = strings.TrimSpace(r); r != "" {
				names = append(names, r)
			}
		}
		workflowBody["reviewers"] = names
	}
	reqBody["workflow"] = workflowBody

	data, status, err := apiCallRaw("POST", "/"+url.PathEscape(st.Project)+"/tasks", reqBody)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusCreated && status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error creating task: %s\n", clierr.Describe(nil, status, data))
		clierr.Exit1()
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	id := task["id"]
	fmt.Printf("Created Task #%.0f: %s\n", id, title)
}

func cmdTaskList(st *boardState, asJSON bool) { cmdTaskListGoal(st, asJSON, 0) }

// taskListArgs reads task list's options: --json and --goal ID (a whole number, 1 or more).
func taskListArgs(args []string) (asJSON bool, goal int64) {
	asJSON = hasFlag(args, "--json")
	for i, a := range args {
		name, value, inline := strings.Cut(a, "=")
		if name != "--goal" {
			continue
		}
		if !inline && i+1 < len(args) {
			value = args[i+1]
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n <= 0 {
			fmt.Fprintf(os.Stderr, "--goal needs a goal ID, a whole number (got %q)\n", value)
			clierr.Exit1()
		}
		goal = n
	}
	return asJSON, goal
}

// cmdTaskListGoal lists the board's tasks, or one goal's when goal is not 0.
func cmdTaskListGoal(st *boardState, asJSON bool, goal int64) {
	tasks := fetchTasks(st, goal)
	if asJSON {
		if tasks == nil {
			tasks = []map[string]any{} // an empty board is [], never null
		}
		out, _ := json.MarshalIndent(tasks, "", "  ")
		fmt.Println(string(out))
		return
	}
	if len(tasks) == 0 {
		fmt.Println("No tasks found.")
		return
	}

	fmt.Printf("%-4s %-12s %-9s %-15s %s\n", "ID", "Status", "Priority", "Assignee", "Title")
	for _, task := range tasks {
		id, _ := task["id"].(float64)
		tStatus, _ := task["status"].(string)
		if taskReviewRequested(task) && tStatus == "in_progress" {
			tStatus = "review_requested"
		}
		tPriority, _ := task["priority"].(string)
		tAssignee := "—"
		if a, ok := task["assigned_to"].(string); ok && a != "" {
			tAssignee = a
		}
		tTitle, _ := task["title"].(string)

		fmt.Printf("#%-3.0f %-12s %-9s %-15s %s\n", id, tStatus, tPriority, tAssignee, tTitle)
	}
}

// fetchAllTasks reads every task of the board, a page at a time (#3269), in the order
// the full list always had: priority, then id. A server from before pagination ignores
// the parameters and answers with every task and no next_cursor, which ends the loop.
func fetchAllTasks(st *boardState) []map[string]any { return fetchTasks(st, 0) }

// fetchTasks is fetchAllTasks for one goal's tasks when goal is not 0.
func fetchTasks(st *boardState, goal int64) []map[string]any {
	var tasks []map[string]any
	cursor := ""
	for {
		path := "/" + url.PathEscape(st.Project) + "/tasks?status=all&limit=200"
		if goal > 0 {
			path += "&goal=" + strconv.FormatInt(goal, 10)
		}
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		data, statusCode, err := apiCallRaw("GET", path, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			clierr.Exit1()
		}
		if statusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "Error listing tasks: %s\n", clierr.Describe(nil, statusCode, data))
			clierr.Exit1()
		}
		var page struct {
			Tasks      []map[string]any `json:"tasks"`
			NextCursor string           `json:"next_cursor"`
		}
		json.Unmarshal(data, &page)
		tasks = append(tasks, page.Tasks...)
		if page.NextCursor == "" || page.NextCursor == cursor {
			break
		}
		cursor = page.NextCursor
	}
	rank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	sort.SliceStable(tasks, func(i, j int) bool {
		pi, pj := rank[fmt.Sprint(tasks[i]["priority"])], rank[fmt.Sprint(tasks[j]["priority"])]
		if pi != pj {
			return pi < pj
		}
		ii, _ := tasks[i]["id"].(float64)
		ij, _ := tasks[j]["id"].(float64)
		return ii < ij
	})
	return tasks
}

// taskReviewRequested: a list item says review_requested; a full task (an older
// server) has workflow.completion_review.
func taskReviewRequested(task map[string]any) bool {
	if r, ok := task["review_requested"].(bool); ok && r {
		return true
	}
	workflow, ok := task["workflow"].(map[string]any)
	return ok && workflow["completion_review"] != nil
}

func cmdTaskClaim(st *boardState, args ...string) {
	subscriberID := resolveSubscriberID()
	body := map[string]any{"subscriber_id": subscriberID}
	if len(args) > 0 {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil || id <= 0 {
			fmt.Fprintln(os.Stderr, "task claim requires a positive task ID")
			clierr.Exit1()
		}
		body["task_id"] = id
	}

	data, status, err := apiCallRaw("POST", "/"+url.PathEscape(st.Project)+"/tasks/claim", body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}

	if status == http.StatusNotFound {
		fmt.Println("No available tasks")
		taskcli.PrintBlocked(os.Stdout, data)
		return
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error claiming task: %s\n", clierr.Describe(nil, status, data))
		taskcli.PrintBlocked(os.Stderr, data)
		clierr.Exit1()
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	noteSeenRound(st.Project, task)
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

// cmdTaskCurrent prints the task in progress; with asJSON the task as JSON, or null when there is none.
func cmdTaskCurrent(st *boardState, asJSON bool) {
	subscriberID := resolveSubscriberID()
	body := map[string]string{"subscriber_id": subscriberID}

	data, status, err := apiCallRaw("POST", "/"+url.PathEscape(st.Project)+"/tasks/current", body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}

	if status == http.StatusNotFound {
		if asJSON {
			fmt.Println("null")
			return
		}
		fmt.Println("No active task")
		return
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", clierr.Describe(nil, status, data))
		clierr.Exit1()
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	if asJSON {
		pretty, _ := json.MarshalIndent(task, "", "  ")
		fmt.Println(string(pretty))
		return
	}
	id, _ := task["id"].(float64)
	title, _ := task["title"].(string)
	taskBody, _ := task["body"].(string)
	priority, _ := task["priority"].(string)
	status_, _ := task["status"].(string)
	revision, _ := task["revision"].(float64)
	fmt.Printf("Task #%.0f (revision %.0f, %s) [%s]: %s\n", id, revision, priority, status_, title)
	printTaskWorkflow(task)
	if taskBody != "" {
		fmt.Printf("\n%s\n", taskBody)
	}
}

func cmdTaskComplete(st *boardState, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board task complete <id> [--revision N] [--message \"note\"]")
		clierr.Exit1()
	}

	taskID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid task ID: %s\n", args[0])
		clierr.Exit1()
	}

	fs := flag.NewFlagSet("task-complete", flag.ContinueOnError)
	message := fs.String("message", "", "Completion message")
	outcome := fs.String("outcome", "success", "success or failed")
	artifactsFile := fs.String("artifacts", "", "JSON file containing named artifacts (name, uri or content, revision, digest)")
	expectedRevision := fs.Int("revision", 0, "Expected task revision after amendments")
	candidateRevision := fs.String("candidate-revision", "", "Exact source/build revision represented by completion evidence")
	clihelp.ParseFlags(fs, args[1:], func() string { return commandUsage(os.Args[1], os.Args[2:]) })
	*message = flagText(*message, "completion message")

	subscriberID := resolveSubscriberID()
	body := map[string]any{
		"subscriber_id": subscriberID,
		"outcome":       *outcome,
	}
	if *expectedRevision > 0 {
		body["expected_revision"] = *expectedRevision
	}
	if *candidateRevision != "" {
		body["candidate_revision"] = *candidateRevision
	}
	if *artifactsFile != "" {
		data, err := os.ReadFile(*artifactsFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			clierr.Exit1()
		}
		var artifacts []map[string]any
		if err := json.Unmarshal(data, &artifacts); err != nil {
			fmt.Fprintln(os.Stderr, "Invalid artifacts JSON:", err)
			clierr.Exit1()
		}
		body["artifacts"] = artifacts
	}
	if *message != "" {
		body["message"] = *message
	}

	data, status, err := apiCallRaw("POST", fmt.Sprintf("/%s/tasks/%d/complete", url.PathEscape(st.Project), taskID), body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error completing task: %s\n", clierr.Describe(nil, status, data))
		clierr.Exit1()
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	title, _ := task["title"].(string)
	fmt.Printf("Finished Task #%d (%s): %s\n", taskID, *outcome, title)
	printUnverifiedArtifacts(data)
}

// printUnverifiedArtifacts says which coral://artifacts links in the result the server kept as plain
// links because this team never uploaded those files (#4379).
func printUnverifiedArtifacts(data []byte) {
	var r struct {
		N     int      `json:"artifacts_unverified"`
		Names []string `json:"artifacts_unverified_names"`
	}
	if json.Unmarshal(data, &r) != nil || r.N == 0 {
		return
	}
	fmt.Printf("Note: %d artifact link(s) are not uploads of this team and were kept as plain links: %s. Upload the file with 'coral-agent artifact upload <file>' and use the link it prints.\n", r.N, strings.Join(r.Names, ", "))
}

func printTaskWorkflow(task map[string]any) {
	if workflow, ok := task["workflow"]; ok {
		data, err := json.MarshalIndent(workflow, "", "  ")
		if err == nil {
			fmt.Printf("\nWorkflow instructions, inputs and outputs:\n%s\n", data)
		}
	}
}

func cmdTaskCancel(st *boardState, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board task cancel <id> [--message \"reason\"]")
		clierr.Exit1()
	}

	taskID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid task ID: %s\n", args[0])
		clierr.Exit1()
	}

	fs := flag.NewFlagSet("task-cancel", flag.ContinueOnError)
	message := fs.String("message", "", "Cancellation reason")
	clihelp.ParseFlags(fs, args[1:], func() string { return commandUsage(os.Args[1], os.Args[2:]) })
	*message = flagText(*message, "cancellation reason")

	subscriberID := resolveSubscriberID()
	body := map[string]any{
		"subscriber_id": subscriberID,
	}
	if *message != "" {
		body["message"] = *message
	}

	data, status, err := apiCallRaw("POST", fmt.Sprintf("/%s/tasks/%d/cancel", url.PathEscape(st.Project), taskID), body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error cancelling task: %s\n", clierr.Describe(nil, status, data))
		clierr.Exit1()
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	title, _ := task["title"].(string)
	fmt.Printf("Cancelled Task #%d: %s\n", taskID, title)
}

// parseReassignAssignee reads the --to flag. A bare name is rejected rather
// than ignored: Go's flag package stops at the first positional argument, so
// "reassign 5 Orchestrator" used to leave --to empty and silently unassign the
// task, returning it to the pool every worker can claim from.
func parseReassignAssignee(args []string) (string, error) {
	fs := flag.NewFlagSet("task-reassign", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	assignee := fs.String("to", "", "New assignee (empty = unassigned)")
	if err := fs.Parse(args); err != nil {
		return "", fmt.Errorf("invalid reassign options: %v (usage: task reassign <id> [--to \"Agent Name\"])", err)
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected argument %q: use --to %q to assign; omit --to only to unassign", fs.Arg(0), fs.Arg(0))
	}
	return *assignee, nil
}

func cmdTaskReassign(st *boardState, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `Usage: coral-board task reassign <id> [--to "Agent Name"]`)
		clierr.Exit1()
	}

	taskID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid task ID: %s\n", args[0])
		clierr.Exit1()
	}

	newAssignee, err := parseReassignAssignee(args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	assignee := &newAssignee

	subscriberID := resolveSubscriberID()
	body := map[string]any{
		"subscriber_id": subscriberID,
		"assignee":      *assignee,
	}

	data, status, err := apiCallRaw("POST", fmt.Sprintf("/%s/tasks/%d/reassign", url.PathEscape(st.Project), taskID), body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error reassigning task: %s\n", clierr.Describe(nil, status, data))
		clierr.Exit1()
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	title, _ := task["title"].(string)
	if *assignee != "" {
		fmt.Printf("Reassigned Task #%d to %s: %s\n", taskID, *assignee, title)
	} else {
		fmt.Printf("Reassigned Task #%d (now unassigned): %s\n", taskID, title)
	}
}

func cmdTaskAmend(st *boardState, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `Usage: coral-board task amend <id> --revision N --reason "..." [--body "..."]`)
		clierr.Exit1()
	}
	taskID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid task ID: %s\n", args[0])
		clierr.Exit1()
	}
	fs := flag.NewFlagSet("task-amend", flag.ContinueOnError)
	revision := fs.Int("revision", 0, "Expected current task revision")
	reason := fs.String("reason", "", "Why this amendment is authoritative")
	bodyText := fs.String("body", "", "Replacement task body")
	instructions := fs.String("workflow-instructions", "", "Replacement task-specific instructions")
	completionGates := fs.String("completion-gates", "", "Replacement completion gates JSON (experimental and disabled by default)")
	clihelp.ParseFlags(fs, args[1:], func() string { return commandUsage(os.Args[1], os.Args[2:]) })
	*bodyText = flagText(*bodyText, "task body")
	if *revision <= 0 || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(os.Stderr, "--revision and --reason are required")
		clierr.Exit1()
	}
	changes := map[string]any{}
	if *bodyText != "" {
		changes["body"] = *bodyText
	}
	if *instructions != "" {
		changes["workflow_instructions"] = *instructions
	}
	if *completionGates != "" {
		var gates any
		if err := json.Unmarshal([]byte(*completionGates), &gates); err != nil {
			fmt.Fprintln(os.Stderr, "Invalid --completion-gates JSON:", err)
			clierr.Exit1()
		}
		changes["completion_gates"] = gates
	}
	if len(changes) == 0 {
		fmt.Fprintln(os.Stderr, "at least --body or --workflow-instructions is required")
		clierr.Exit1()
	}
	data, status, err := apiCallRaw("PATCH", fmt.Sprintf("/%s/tasks/%d/amend", url.PathEscape(st.Project), taskID), map[string]any{"subscriber_id": resolveSubscriberID(), "base_revision": *revision, "reason": *reason, "changes": changes})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error amending task: %s\n", clierr.Describe(nil, status, data))
		clierr.Exit1()
	}
	var task map[string]any
	_ = json.Unmarshal(data, &task)
	fmt.Printf("Amended Task #%d to revision %.0f: %s\n", taskID, task["revision"], task["title"])
}

// Review operations are explicit: a rejected completion is never automatically
// retried through another endpoint or treated as accepted.
func cmdTaskReview(st *boardState, args []string, action string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board task", action, "<id> --reason <reason> [--message <message>] [--artifacts manifest.json]")
		clierr.Exit1()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Invalid task ID")
		os.Exit(2) // usage
	}
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	reason := fs.String("reason", "", "Reason review is needed or capacity is being released")
	message := fs.String("message", "", "Candidate completion message")
	revision := fs.Int("revision", 0, "Expected task revision after amendments (submit-review)")
	outcome := fs.String("outcome", "success", "Proposed outcome: success or failed")
	manifest := fs.String("artifacts", "", "Candidate artifact manifest")
	candidateRevision := fs.String("candidate-revision", "", "Exact source/build revision represented by candidate evidence")
	clihelp.ParseFlags(fs, args[1:], func() string { return commandUsage(os.Args[1], os.Args[2:]) })
	*message = flagText(*message, "completion message")
	if strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(os.Stderr, "--reason is required")
		clierr.Exit1()
	}
	body := map[string]any{"subscriber_id": resolveSubscriberID(), "reason": *reason, "message": *message, "outcome": *outcome}
	if *candidateRevision != "" {
		body["candidate_revision"] = *candidateRevision
	}
	if *revision > 0 {
		body["expected_revision"] = *revision
	}
	if *manifest != "" {
		data, err := os.ReadFile(*manifest)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			clierr.Exit1()
		}
		var artifacts []map[string]any
		if err := json.Unmarshal(data, &artifacts); err != nil {
			fmt.Fprintln(os.Stderr, err)
			clierr.Exit1()
		}
		body["artifacts"] = artifacts
	}
	data, status, err := apiCallRaw("POST", fmt.Sprintf("/%s/tasks/%d/%s", url.PathEscape(st.Project), id, action), body)
	if err != nil || status != http.StatusOK {
		fmt.Fprintln(os.Stderr, "Review action failed: "+clierr.Describe(err, status, data))
		clierr.Exit1()
	}
	if action == "submit-review" {
		var t struct {
			Status     string  `json:"status"`
			AssignedTo *string `json:"assigned_to"`
		}
		_ = json.Unmarshal(data, &t)
		switch {
		case t.Status == "completed":
			fmt.Printf("Task #%d completed: the team has no orchestrator to review it.\n", id)
		case t.AssignedTo != nil:
			fmt.Printf("Task #%d is in review with %s; your slot is free.\n", id, *t.AssignedTo)
		default:
			fmt.Printf("Task #%d: submit-review recorded.\n", id)
		}
		printUnverifiedArtifacts(data)
		return
	}
	fmt.Printf("Task #%d: %s recorded. Completion has not been accepted.\n", id, action)
}

// cmdTaskReviewFlow: the orchestrator delegates a task in review (review --to, which also changes
// the reviewers of an open round); a reviewer approves or rejects it any time it is in review,
// naming the round they reviewed with --round (#3725, #3787).
func cmdTaskReviewFlow(st *boardState, args []string, action string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `Usage: coral-board task review <id> --to "Reviewer A,Reviewer B" | approve <id> [--round N] [--note "..."] | reject <id> [--round N] --note "why"`)
		clierr.Exit1()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Invalid task ID")
		os.Exit(2) // usage
	}
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	to := fs.String("to", "", "Reviewers, comma-separated (review)")
	note := fs.String("note", "", "Your note (approve; required for reject)")
	revision := fs.Int("revision", -1, "The revision of the round you read (approve): patches that joined it since it opened; default: the one task detail last showed you")
	round := fs.Int("round", 0, "The review round you reviewed (default: the one task detail or your review task last showed you); a verdict on an earlier round is refused")
	clihelp.ParseFlags(fs, args[1:], func() string { return commandUsage(os.Args[1], os.Args[2:]) })
	explicitRound := false
	fs.Visit(func(f *flag.Flag) { explicitRound = explicitRound || f.Name == "round" })
	if !explicitRound && (action == "approve" || action == "reject") {
		// The round this reviewer last saw (#3805): a verdict typed after the task was
		// resubmitted is then refused instead of applying to a revision they haven't read.
		*round = lastSeenRound(st.Project, id)
	}
	*note = flagText(*note, "note")
	body := map[string]any{"subscriber_id": resolveSubscriberID()}
	switch action {
	case "review":
		var reviewers []string
		for _, r := range strings.Split(*to, ",") {
			if r = strings.TrimSpace(r); r != "" {
				reviewers = append(reviewers, r)
			}
		}
		if len(reviewers) == 0 {
			fmt.Fprintln(os.Stderr, `--to "Reviewer A,Reviewer B" is required`)
			os.Exit(2) // usage
		}
		body["reviewers"] = reviewers
	case "reject":
		if strings.TrimSpace(*note) == "" {
			fmt.Fprintln(os.Stderr, `--note "why" is required`)
			os.Exit(2) // usage
		}
		body["note"] = *note
		body["round"] = *round
	default:
		body["note"] = *note
		body["round"] = *round
		if *revision >= 0 {
			body["revision"] = *revision
		} else {
			body["revision"] = lastSeenRevision(st.Project, id)
		}
	}
	data, status, err := apiCallRaw("POST", fmt.Sprintf("/%s/tasks/%d/%s", url.PathEscape(st.Project), id, action), body)
	if err != nil || status != http.StatusOK {
		fmt.Fprintln(os.Stderr, "Review action failed: "+clierr.Describe(err, status, data))
		exitFn(1)
		return
	}
	var out struct {
		Completed bool `json:"completed"`
		Rejected  bool `json:"rejected"`
		Reviews   []struct {
			ID         int64   `json:"id"`
			AssignedTo *string `json:"assigned_to"`
		} `json:"reviews"`
	}
	_ = json.Unmarshal(data, &out)
	switch {
	case action == "review":
		for _, r := range out.Reviews {
			if r.AssignedTo != nil {
				fmt.Printf("Review task #%d for %s\n", r.ID, *r.AssignedTo)
			}
		}
	case out.Completed:
		fmt.Printf("Task #%d approved by every reviewer: completed.\n", id)
	case out.Rejected:
		fmt.Printf("Task #%d rejected: back to its author.\n", id)
	default:
		fmt.Printf("Task #%d approved; waiting for the other reviewers.\n", id)
	}
}

// seenRoundsPath holds, per board task, the review round this agent was last shown (#3805). The
// name comes from a hash of the session name, never the raw value (Security's NIT: an env value
// with a path separator or ".." can't place the file elsewhere).
func seenRoundsPath() string {
	name := resolveSessionName()
	if name == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(coralDir(), "board_review_rounds_"+hex.EncodeToString(sum[:8])+".json")
}

func loadSeenRounds() map[string]int {
	seen := map[string]int{}
	if path := seenRoundsPath(); path == "" {
		return seen
	} else if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &seen)
	}
	return seen
}

// noteSeenRound records the review round a task shows (task detail, a claim): the open round of
// a task under review, or a review task's own round (recorded for the task it reviews too).
func noteSeenRound(project string, task map[string]any) {
	id, _ := task["id"].(float64)
	w, _ := task["workflow"].(map[string]any)
	if id <= 0 || w == nil {
		return
	}
	seen := loadSeenRounds()
	changed := false
	if rounds, _ := w["review_rounds"].([]any); len(rounds) > 0 {
		if last, _ := rounds[len(rounds)-1].(map[string]any); last != nil {
			if n, _ := last["round"].(float64); n > 0 {
				seen[fmt.Sprintf("%s/%d", project, int64(id))] = int(n)
				rev, _ := last["revision"].(float64) // patches that joined the round (#5689); absent is 0
				seen[fmt.Sprintf("%s/%d#revision", project, int64(id))] = int(rev)
				changed = true
			}
		}
	}
	if of, _ := w["review_of"].(float64); of > 0 {
		if n, _ := w["review_round"].(float64); n > 0 {
			seen[fmt.Sprintf("%s/%d", project, int64(id))] = int(n)
			seen[fmt.Sprintf("%s/%d", project, int64(of))] = int(n)
			changed = true
		}
	}
	if !changed {
		return
	}
	data, _ := json.Marshal(seen)
	path := seenRoundsPath()
	if path == "" {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	// A temp file and a rename (Security's NIT): two commands at once never leave half a file.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".board_review_rounds_*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Chmod(tmp.Name(), 0o600) != nil || os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
	}
}

// lastSeenRound is the review round recorded for a task, 0 when none was (the server then takes
// the current round).
func lastSeenRound(project string, id int64) int {
	return loadSeenRounds()[fmt.Sprintf("%s/%d", project, id)]
}

// lastSeenRevision is the revision of the round recorded for a task: how many patches joined it after it opened (#5689), 0 when none did
// or none was recorded. An approval names it; the server refuses one that names an older revision.
func lastSeenRevision(project string, id int64) int {
	return loadSeenRounds()[fmt.Sprintf("%s/%d#revision", project, id)]
}
