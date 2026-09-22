// Command coral-agent is the command-line interface an agent uses to work
// with its own state in Coral, starting with the tasks the operator gives
// it from the dashboard:
//
//	coral-agent task claim          claim the next pending task and show it
//	coral-agent task current        show the task in progress
//	coral-agent task list           list your tasks
//	coral-agent task complete <id>  mark a task done
//
// The agent is identified by its Coral session (tmux session name or the
// CORAL_SESSION_NAME variable Coral exports), the same way Coral's hooks
// resolve it. Team boards keep using coral-board.
package main

import (
	"bytes"
	"encoding/json"
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

const usage = `Usage: coral-agent <command> [args]

Commands:
  task claim            Claim your next pending task and show its details
  task current          Show your task in progress
  task list             List your tasks
  task complete <id>    Mark a task done

Environment:
  CORAL_URL   Server URL (default http://localhost:8420)
  CORAL_PORT  Server port (overrides CORAL_URL)`

var serverURL = hooks.CoralBase()

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		fmt.Fprintln(os.Stderr, usage)
		if len(os.Args) < 2 {
			os.Exit(1)
		}
		return
	}
	switch os.Args[1] {
	case "task", "tasks":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(1)
		}
		cmdTask(os.Args[2], os.Args[3:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n%s\n", os.Args[1], usage)
		os.Exit(1)
	}
}

func sessionID() string {
	sid := hooks.ResolveSessionID("")
	if sid == "" {
		fmt.Fprintln(os.Stderr, "Not inside a Coral agent session (no tmux session name or CORAL_SESSION_NAME).")
		os.Exit(1)
	}
	return sid
}

func cmdTask(sub string, args []string) {
	switch sub {
	case "claim":
		sid := sessionID()
		data, status, err := api("POST", "/api/agent-tasks/claim", map[string]string{"session_id": sid})
		check(data, status, err)
		var t task
		json.Unmarshal(data, &t)
		fmt.Printf("Claimed task #%d: %s\n", t.ID, t.Title)
		if t.Body != "" {
			fmt.Printf("\n%s\n", t.Body)
		}
		fmt.Printf("\nWhen it is done, run: coral-agent task complete %d\n", t.ID)
	case "current", "detail":
		for _, t := range list(sessionID()) {
			if t.Completed == 2 {
				fmt.Printf("Task #%d (in progress): %s\n", t.ID, t.Title)
				if t.Body != "" {
					fmt.Printf("\n%s\n", t.Body)
				}
				return
			}
		}
		fmt.Println("No task in progress. Run: coral-agent task claim")
	case "list":
		tasks := list(sessionID())
		if len(tasks) == 0 {
			fmt.Println("No tasks.")
			return
		}
		labels := map[int]string{0: "pending", 1: "done", 2: "in progress"}
		for _, t := range tasks {
			fmt.Printf("#%d  [%s]  %s\n", t.ID, labels[t.Completed], t.Title)
		}
	case "complete", "done":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "Usage: coral-agent task complete <id>")
			os.Exit(1)
		}
		id, err := strconv.Atoi(strings.TrimPrefix(args[0], "#"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "Task id must be a number")
			os.Exit(1)
		}
		data, status, err := api("POST", fmt.Sprintf("/api/agent-tasks/%d/complete", id), map[string]string{"session_id": sessionID()})
		check(data, status, err)
		fmt.Printf("Task #%d completed\n", id)
	default:
		fmt.Fprintf(os.Stderr, "Unknown task command: %s (use claim, current, list or complete)\n", sub)
		os.Exit(1)
	}
}

type task struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Completed int    `json:"completed"`
}

func list(sid string) []task {
	data, status, err := api("GET", "/api/agent-tasks?session_id="+url.QueryEscape(sid), nil)
	check(data, status, err)
	var tasks []task
	json.Unmarshal(data, &tasks)
	return tasks
}

func api(method, path string, body any) ([]byte, int, error) {
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
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot reach Coral server at %s: %v", serverURL, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

func check(data []byte, status int, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("HTTP %d", status)
		}
		fmt.Fprintln(os.Stderr, e.Error)
		os.Exit(1)
	}
}
