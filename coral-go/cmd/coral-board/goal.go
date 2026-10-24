package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/subgentic/coral/internal/clierr"
	"github.com/subgentic/coral/internal/clihelp"
)

// Board goals (#7087, #7116): what a team is working toward, kept on the board with the tasks that serve it. Not the session "goal line" an agent's status shows (POST /api/sessions/live/{name}/goal):
// these are `coral-board goal ...` and a task's `--goal`.

const goalUsageText = `Usage: coral-board goal <subcommand> [args]

Subcommands:
  add "title" [--outcome "what is true when done"] [--done-when "a check a person can make"] [--state active|proposed] [--driver "Agent Name"]
  list [--state open|all|proposed,active,blocked,done,dropped] [--json]    Goals still open by default
  show <id> [--json]               A goal, its fields, how its tasks stand and the tasks
  update <id> [--title T] [--outcome T] [--done-when T] [--state proposed|active|blocked] [--driver NAME | --driver ""]
              [--blocked-on "who or what it waits for"] [--decision "what is needed"] [--recommendation "what you suggest"] [--if-silent "what happens if nobody answers"]
                                   A goal that is blocked needs --blocked-on; --decision, --recommendation and --if-silent say what the block needs
  close <id> --reason "why" [--state done|dropped]   Close a goal (done by default); its open tasks are left as they are and counted

A task joins a goal when it is added (task add --goal <id>) or later (task goal <task> <goal|none>); task list --goal <id> lists a goal's tasks.`

// goalTextFlags are the goal options whose values are free text.
var goalTextFlags = []string{"title", "outcome", "done-when", "blocked-on", "decision", "recommendation", "if-silent"}

func cmdGoal() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, goalUsageText)
		clierr.Exit1()
	}
	sub, args := os.Args[2], os.Args[3:]
	if sub == "--help" || sub == "-h" || sub == "help" {
		fmt.Println(goalUsageText)
		os.Exit(0)
	}
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board. Run: coral-board join <project> --as <role>")
		clierr.Exit1()
	}
	switch sub {
	case "add":
		cmdGoalAdd(st, args)
	case "list":
		cmdGoalList(st, args)
	case "show":
		cmdGoalShow(st, args)
	case "update":
		cmdGoalUpdate(st, args)
	case "close":
		cmdGoalClose(st, args)
	default:
		fmt.Fprintf(os.Stderr, "Unknown goal subcommand: %s\n", sub)
		os.Exit(2)
	}
}

func goalPath(st *boardState, id int64, tail string) string {
	p := "/" + url.PathEscape(st.Project) + "/goals"
	if id > 0 {
		p += "/" + strconv.FormatInt(id, 10)
	}
	return p + tail
}

// goalID reads a goal id argument: a whole number, 1 or more.
func goalID(arg, usage string) int64 {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintf(os.Stderr, "Invalid goal ID: %s\n%s\n", arg, usage)
		clierr.Exit1()
	}
	return id
}

// goalCall makes one goal request and returns its answer, or ends the command with the server's sentence.
func goalCall(method, path string, body any, what string, ok ...int) []byte {
	data, status, err := apiCallRaw(method, path, body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	for _, s := range ok {
		if status == s {
			return data
		}
	}
	fmt.Fprintf(os.Stderr, "Error %s: %s\n", what, clierr.Describe(nil, status, data))
	clierr.Exit1()
	return nil
}

// splitTitle pulls the first argument that is not an option (or an option's value) out, so options may stand after the title as they do for task add.
func splitTitle(args []string, valueFlags map[string]bool) (title string, rest []string) {
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			rest = append(rest, args[i])
			if !strings.Contains(args[i], "=") && valueFlags[strings.TrimLeft(args[i], "-")] && i+1 < len(args) {
				rest = append(rest, args[i+1])
				i++
			}
		} else if title == "" {
			title = args[i]
		} else {
			rest = append(rest, args[i])
		}
	}
	return title, rest
}

var goalValueFlags = map[string]bool{"outcome": true, "done-when": true, "state": true, "driver": true, "title": true, "blocked-on": true, "decision": true, "recommendation": true, "if-silent": true, "reason": true}

func cmdGoalAdd(st *boardState, args []string) {
	title, rest := splitTitle(args, goalValueFlags)
	fs := flag.NewFlagSet("goal-add", flag.ContinueOnError)
	outcome := fs.String("outcome", "", "What is true when the goal is done")
	doneWhen := fs.String("done-when", "", "A check a person can make")
	state := fs.String("state", "", "active (default) or proposed")
	driver := fs.String("driver", "", "The agent driving the goal")
	clihelp.ParseFlags(fs, rest, func() string { return goalUsageText })
	if title == "" {
		fmt.Fprintln(os.Stderr, `Usage: coral-board goal add "title" [--outcome "..."] [--done-when "..."] [--state active|proposed] [--driver "Agent Name"]`)
		clierr.Exit1()
	}
	body := map[string]any{"title": title, "subscriber_id": resolveSubscriberID()}
	set := func(k, v, what string) {
		if v != "" {
			body[k] = flagText(v, what)
		}
	}
	set("outcome", *outcome, "goal outcome")
	set("done_when", *doneWhen, "goal done-when")
	set("state", *state, "goal state")
	set("driver", *driver, "goal driver")
	data := goalCall("POST", goalPath(st, 0, ""), body, "creating the goal", http.StatusCreated, http.StatusOK)
	var g map[string]any
	_ = json.Unmarshal(data, &g)
	id, _ := g["id"].(float64)
	fmt.Printf("Created Goal #%.0f: %s\n", id, title)
}

func cmdGoalList(st *boardState, args []string) {
	asJSON := hasFlag(args, "--json")
	fs := flag.NewFlagSet("goal-list", flag.ContinueOnError)
	states := fs.String("state", "", "open (default), all, or a comma list of states")
	fs.Bool("json", false, "")
	clihelp.ParseFlags(fs, args, func() string { return goalUsageText })
	path := goalPath(st, 0, "")
	switch s := strings.TrimSpace(*states); s {
	case "", "open":
	case "all":
		path += "?state=proposed,active,blocked,done,dropped"
	default:
		path += "?state=" + url.QueryEscape(s)
	}
	data := goalCall("GET", path, nil, "listing goals", http.StatusOK)
	var out struct {
		Goals []map[string]any `json:"goals"`
	}
	_ = json.Unmarshal(data, &out)
	if asJSON {
		if out.Goals == nil {
			out.Goals = []map[string]any{}
		}
		b, _ := json.MarshalIndent(out.Goals, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(out.Goals) == 0 {
		fmt.Println("No goals found.")
		return
	}
	fmt.Printf("%-4s %-9s %-15s %-9s %s\n", "ID", "State", "Driver", "Tasks", "Title")
	for _, g := range out.Goals {
		id, _ := g["id"].(float64)
		driver := "—"
		if d, _ := g["driver"].(string); d != "" {
			driver = d
		}
		fmt.Printf("#%-3.0f %-9s %-15s %-9s %s\n", id, str(g["state"]), driver, rollupWords(g), str(g["title"]))
	}
}

func str(v any) string { s, _ := v.(string); return s }

// rollupWords is "2/5": the goal's tasks that are done out of all of them; "—" for a goal with none.
func rollupWords(g map[string]any) string {
	r, _ := g["rollup"].(map[string]any)
	total, _ := r["total"].(float64)
	if total == 0 {
		return "—"
	}
	done, _ := r["done"].(float64)
	return fmt.Sprintf("%.0f/%.0f", done, total)
}

func cmdGoalShow(st *boardState, args []string) {
	asJSON := hasFlag(args, "--json")
	args = withoutFlag(args, "--json")
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board goal show <id> [--json]")
		clierr.Exit1()
	}
	id := goalID(args[0], "Usage: coral-board goal show <id> [--json]")
	data := goalCall("GET", goalPath(st, id, ""), nil, "reading the goal", http.StatusOK)
	var g map[string]any
	if err := json.Unmarshal(data, &g); err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	tasks, truncated := fetchTasksPage(st, id)
	if asJSON {
		g["tasks"] = tasks
		if truncated {
			g["tasks_truncated"] = true
		}
		b, _ := json.MarshalIndent(g, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("Goal #%d: %s\n", id, str(g["title"]))
	line := func(label, key string) {
		if v := str(g[key]); v != "" {
			fmt.Printf("  %-15s %s\n", label+":", v)
		}
	}
	line("State", "state")
	line("Driver", "driver")
	line("Outcome", "outcome")
	line("Done when", "done_when")
	line("Blocked on", "blocked_on")
	line("Decision", "decision")
	line("Recommendation", "recommendation")
	line("If silent", "if_silent")
	line("Closed", "closed_reason")
	if r, ok := g["rollup"].(map[string]any); ok {
		fmt.Printf("  %-15s %.0f done, %.0f active, %.0f blocked, %.0f queued (%.0f)\n", "Tasks:", num(r["done"]), num(r["active"]), num(r["blocked"]), num(r["queued"]), num(r["total"]))
	}
	for _, t := range tasks {
		tid, _ := t["id"].(float64)
		assignee := "—"
		if a := str(t["assigned_to"]); a != "" {
			assignee = a
		}
		fmt.Printf("    #%-4.0f %-12s %-15s %s\n", tid, str(t["status"]), assignee, str(t["title"]))
	}
	if truncated {
		fmt.Printf("    (only the first %d tasks are listed; the goal has more: coral-board task list --goal %d reads the same)\n", len(tasks), id)
	}
}

func num(v any) float64 { f, _ := v.(float64); return f }

func cmdGoalUpdate(st *boardState, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `Usage: coral-board goal update <id> [--title T] [--outcome T] [--done-when T] [--state S] [--driver NAME] [--blocked-on T] [--decision T] [--recommendation T] [--if-silent T]`)
		clierr.Exit1()
	}
	id := goalID(args[0], "Usage: coral-board goal update <id> [options]")
	fs := flag.NewFlagSet("goal-update", flag.ContinueOnError)
	vals := map[string]*string{}
	for _, name := range []string{"title", "outcome", "done-when", "state", "driver", "blocked-on", "decision", "recommendation", "if-silent"} {
		vals[name] = fs.String(name, "", "")
	}
	clihelp.ParseFlags(fs, args[1:], func() string { return goalUsageText })
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	body := map[string]any{"subscriber_id": resolveSubscriberID()}
	for name, v := range vals {
		if !given[name] {
			continue
		}
		body[strings.ReplaceAll(name, "-", "_")] = flagText(*v, "goal "+name) // an empty value is sent: it clears the field (--driver "" frees the goal)
	}
	if len(body) == 1 {
		fmt.Fprintln(os.Stderr, "Nothing to update: give at least one option.")
		clierr.Exit1()
	}
	goalCall("PATCH", goalPath(st, id, ""), body, "updating the goal", http.StatusOK)
	fmt.Printf("Updated Goal #%d\n", id)
}

func cmdGoalClose(st *boardState, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `Usage: coral-board goal close <id> --reason "why" [--state done|dropped]`)
		clierr.Exit1()
	}
	id := goalID(args[0], `Usage: coral-board goal close <id> --reason "why" [--state done|dropped]`)
	fs := flag.NewFlagSet("goal-close", flag.ContinueOnError)
	reason := fs.String("reason", "", "Why it is closed")
	state := fs.String("state", "", "done (default) or dropped")
	clihelp.ParseFlags(fs, args[1:], func() string { return goalUsageText })
	*reason = flagText(*reason, "close reason")
	if strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(os.Stderr, `Usage: coral-board goal close <id> --reason "why" [--state done|dropped]`)
		clierr.Exit1()
	}
	body := map[string]any{"reason": *reason, "subscriber_id": resolveSubscriberID()}
	if *state != "" {
		body["state"] = *state
	}
	data := goalCall("POST", goalPath(st, id, "/close"), body, "closing the goal", http.StatusOK)
	var out struct {
		Goal      map[string]any `json:"goal"`
		OpenTasks int            `json:"open_tasks"`
	}
	_ = json.Unmarshal(data, &out)
	closedAs := str(out.Goal["state"])
	if closedAs == "" {
		closedAs = "done"
	}
	fmt.Printf("Closed Goal #%d (%s)\n", id, closedAs)
	if out.OpenTasks > 0 {
		plural := "s"
		if out.OpenTasks == 1 {
			plural = ""
		}
		fmt.Printf("%d task%s of this goal %s still open; they are left as they are (coral-board task goal <task> none unlinks one).\n", out.OpenTasks, plural, map[bool]string{true: "is", false: "are"}[out.OpenTasks == 1])
	}
}

// cmdTaskGoal is "task goal <task> <goal|none>": link a task to a goal of its board, move it to another, or clear the link.
func cmdTaskGoal(st *boardState, args []string) {
	const usage = "Usage: coral-board task goal <task-id> <goal-id|none>"
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, usage)
		clierr.Exit1()
	}
	taskID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || taskID <= 0 {
		fmt.Fprintf(os.Stderr, "Invalid task ID: %s\n%s\n", args[0], usage)
		clierr.Exit1()
	}
	var target any // null clears
	if args[1] != "none" {
		target = goalID(args[1], usage)
	}
	body := map[string]any{"goal_id": target, "subscriber_id": resolveSubscriberID()}
	data := goalCall("PUT", fmt.Sprintf("/%s/tasks/%d/goal", url.PathEscape(st.Project), taskID), body, "changing the task's goal", http.StatusOK)
	var task map[string]any
	_ = json.Unmarshal(data, &task)
	if target == nil {
		fmt.Printf("Task #%d is no longer linked to a goal\n", taskID)
		return
	}
	fmt.Printf("Task #%d is now on Goal #%d\n", taskID, target)
}
