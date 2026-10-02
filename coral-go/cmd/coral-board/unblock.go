package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
)

func cmdTaskUnblock(st *boardState, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board task unblock <id> (--blocker ID | --all)")
		os.Exit(1)
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintln(os.Stderr, "task ID must be positive")
		os.Exit(1)
	}
	fs := flag.NewFlagSet("task-unblock", flag.ExitOnError)
	blocker := fs.Int64("blocker", 0, "Remove this prerequisite task ID")
	all := fs.Bool("all", false, "Remove all prerequisites")
	fs.Parse(args[1:])
	if fs.NArg() != 0 || *blocker < 0 || (*all == (*blocker > 0)) {
		fmt.Fprintln(os.Stderr, "choose exactly one of --blocker ID or --all")
		os.Exit(1)
	}
	body := map[string]any{"subscriber_id": resolveSubscriberID()}
	if *all {
		body["clear_blockers"] = true
	} else {
		body["remove_blocker"] = *blocker
	}
	data, status, err := apiCallRaw(http.MethodPatch, fmt.Sprintf("/%s/tasks/%d", st.Project, id), body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error removing blocker: %s\n", data)
		os.Exit(1)
	}
	var task struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &task); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Updated blockers for task #%d; status: %s\n", id, task.Status)
}
