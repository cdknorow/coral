package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// waitArgs is a coral-board wait command line.
type waitArgs struct {
	project, from, task, commit, operator, reason, timeout string
	status, cancel, block                                  bool
}

// parseWaitArgs reads wait's arguments (project: the joined board, the default), or says what is wrong with them:
// a flag without its value, an unknown option, a second reason, more than one target, or --status or --cancel
// with anything else.
func parseWaitArgs(args []string, project string) (waitArgs, string) {
	w := waitArgs{project: project}
	for i := 0; i < len(args); i++ {
		var dest *string
		switch args[i] {
		case "--board", "--project":
			dest = &w.project
		case "--from":
			dest = &w.from
		case "--task":
			dest = &w.task
		case "--commit":
			dest = &w.commit
		case "--operator":
			dest = &w.operator
		case "--reason":
			dest = &w.reason
		case "--timeout":
			dest = &w.timeout
		case "--status":
			w.status = true
		case "--cancel":
			w.cancel = true
		case "--block":
			w.block = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return w, fmt.Sprintf("unknown option %s", args[i])
			}
			if w.reason != "" {
				return w, fmt.Sprintf("unexpected argument %q", args[i])
			}
			w.reason = args[i]
		}
		if dest != nil {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return w, fmt.Sprintf("%s requires a value", args[i])
			}
			*dest = args[i+1]
			i++
		}
	}
	if w.timeout != "" {
		// A value the server cannot read used to register the default two-hour wait without a word.
		if d, err := time.ParseDuration(w.timeout); err != nil || d <= 0 {
			return w, fmt.Sprintf("--timeout needs a duration such as 30m or 2h, got %q", w.timeout)
		}
	}
	targets := 0
	for _, t := range []string{w.from, w.task, w.commit, w.operator} {
		if t != "" {
			targets++
		}
	}
	if targets > 1 {
		return w, "choose only one of --from, --task, --commit, or --operator"
	}
	if (w.status || w.cancel) && (targets > 0 || w.reason != "" || w.block) {
		return w, "--status and --cancel cannot be combined with wait registration options"
	}
	if w.status && w.cancel {
		return w, "--status and --cancel are mutually exclusive"
	}
	return w, ""
}

// target is what the wait is for: a message (from someone, or any), a task, a commit, or the Operator's reply. An
// Operator question is the wait's reason; Coral lists it in the orchestrator's check until the Operator posts.
func (w waitArgs) target() (waitType, targetID, reason string) {
	switch {
	case w.from != "":
		return "message", w.from, w.reason
	case w.task != "":
		return "task", w.task, w.reason
	case w.commit != "":
		return "commit", w.commit, w.reason
	case w.operator != "":
		return "operator", "Operator", w.operator
	}
	return "message", "", w.reason
}

func cmdWait() {
	project := ""
	if st := loadState(); st != nil {
		project = st.Project
	}
	w, problem := parseWaitArgs(os.Args[2:], project)
	if problem != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", problem)
		os.Exit(2)
	}
	if w.project == "" {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board. Run: coral-board join <project> --as <role> or specify --board <project>")
		os.Exit(1)
	}
	subscriberID := resolveSubscriberID()
	sessionName := resolveSessionName()
	switch {
	case w.status:
		showWait(w.project, subscriberID)
	case w.cancel:
		cancelWait(w.project, subscriberID)
	default:
		registerWait(w, subscriberID, sessionName)
	}
}

// waitPath is the subscriber's wait on project, for GET and DELETE.
func waitPath(project, subscriberID string) string {
	return fmt.Sprintf("/%s/waits?subscriber_id=%s", url.PathEscape(project), url.QueryEscape(subscriberID))
}

// showWait prints the subscriber's active wait on project, or that there is none.
func showWait(project, subscriberID string) {
	data, status, err := apiCallRaw("GET", waitPath(project, subscriberID), nil)
	if err != nil || status != http.StatusOK {
		failFromServer(data, status, err)
	}
	var result struct {
		Wait *struct {
			WaitType  string `json:"wait_type"`
			TargetID  string `json:"target_id"`
			Reason    string `json:"reason"`
			ExpiresAt string `json:"expires_at"`
			Status    string `json:"status"`
		} `json:"wait"`
	}
	json.Unmarshal(data, &result)
	if result.Wait == nil {
		fmt.Printf("No active wait for '%s' on '%s'.\n", subscriberID, project)
		return
	}
	fmt.Printf("Active wait on '%s':\n", project)
	fmt.Printf("  Waiting for: %s\n", formatWaitTarget(result.Wait.WaitType, result.Wait.TargetID))
	if result.Wait.Reason != "" {
		fmt.Printf("  Reason:      %s\n", result.Wait.Reason)
	}
	if result.Wait.ExpiresAt != "" {
		fmt.Printf("  Expires:     %s\n", result.Wait.ExpiresAt)
	}
}

// cancelWait cancels the subscriber's active wait on project.
func cancelWait(project, subscriberID string) {
	data, status, err := apiCallRaw("DELETE", waitPath(project, subscriberID), nil)
	if err != nil || status != http.StatusOK {
		failFromServer(data, status, err)
	}
	fmt.Printf("Cancelled active wait for '%s' on '%s'.\n", subscriberID, project)
}

// registerWait registers the wait and tells the agent to stop; with --block it waits for it here.
func registerWait(w waitArgs, subscriberID, sessionName string) {
	waitType, targetID, reason := w.target()
	data, status, err := apiCallRaw("POST", "/"+url.PathEscape(w.project)+"/waits", map[string]string{
		"subscriber_id": subscriberID,
		"session_name":  sessionName,
		"wait_type":     waitType,
		"target_id":     targetID,
		"reason":        reason,
		"timeout":       w.timeout,
	})
	if err != nil || (status != http.StatusCreated && status != http.StatusOK) {
		failFromRefusal(data, status, err)
	}
	var regWait struct {
		ExpiresAt string `json:"expires_at"`
	}
	json.Unmarshal(data, &regWait)
	fmt.Printf("Registered wait on '%s':\n", w.project)
	fmt.Printf("  Waiting for: %s\n", formatWaitTarget(waitType, targetID))
	if reason != "" {
		fmt.Printf("  Reason:      %s\n", reason)
	}
	if regWait.ExpiresAt != "" {
		fmt.Printf("  Expires:     %s\n", regWait.ExpiresAt)
	}
	fmt.Println()
	fmt.Println("Coral will automatically wake you up with a terminal notification when this happens.")
	fmt.Println("STOP now and yield your turn. Do NOT poll or call sleep in a loop.")
	if w.block {
		blockUntilResolved(w.project, subscriberID)
	}
}

// blockUntilResolved long-polls the wait until it resolves. A non-2xx (a key-protected Coral answering 401, a 500)
// makes the 30 s long-poll return at once: it exits instead of spinning against the server with no delay (#3589).
func blockUntilResolved(project, subscriberID string) {
	fmt.Println("\nBlocking until wait resolves...")
	pollClient := &http.Client{Timeout: 65 * time.Second}
	for {
		req, err := http.NewRequest("GET", fmt.Sprintf("%s/api/board/%s/waits/poll?subscriber_id=%s&timeout=30", serverURL, url.PathEscape(project), url.QueryEscape(subscriberID)), nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Poll error:", err)
			os.Exit(1)
		}
		resp, err := pollClient.Do(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Poll error:", err)
			os.Exit(1)
		}
		respBytes, _ := readServer(resp.Body)
		status := resp.StatusCode
		resp.Body.Close()
		var pollResult map[string]any
		json.Unmarshal(respBytes, &pollResult)
		if e := serverError(status, pollResult); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		if s, _ := pollResult["status"].(string); s == "resolved" {
			fmt.Println("Wait resolved.")
			return
		}
	}
}

func formatWaitTarget(waitType, targetID string) string {
	switch waitType {
	case "message":
		if targetID != "" {
			return fmt.Sprintf("message from '%s'", targetID)
		}
		return "any board message"
	case "task":
		if targetID != "" {
			return fmt.Sprintf("task #%s", targetID)
		}
		return "task update"
	case "commit":
		if targetID != "" {
			return fmt.Sprintf("commit %s", targetID)
		}
		return "commit landing"
	default:
		if targetID != "" {
			return targetID
		}
		return "board update"
	}
}
