package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"net/http"
	"net/url"
	"os"
	"strconv"
)

func cmdTaskUnblock(st *boardState, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board task unblock <id> (--blocker ID | --all)")
		clierr.Exit1()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintln(os.Stderr, "task ID must be positive")
		clierr.Exit1()
	}
	fs := flag.NewFlagSet("task-unblock", flag.ExitOnError)
	blocker := fs.Int64("blocker", 0, "Remove this prerequisite task ID")
	all := fs.Bool("all", false, "Remove all prerequisites")
	fs.Parse(args[1:])
	if fs.NArg() != 0 || *blocker < 0 || (*all == (*blocker > 0)) {
		fmt.Fprintln(os.Stderr, "choose exactly one of --blocker ID or --all")
		clierr.Exit1()
	}
	body := map[string]any{"subscriber_id": resolveSubscriberID()}
	if *all {
		body["clear_blockers"] = true
	} else {
		body["remove_blocker"] = *blocker
	}
	data, status, err := apiCallRaw(http.MethodPatch, fmt.Sprintf("/%s/tasks/%d", url.PathEscape(st.Project), id), body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error removing blocker: %s\n", data)
		clierr.Exit1()
	}
	var task struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &task); err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
	fmt.Printf("Updated blockers for task #%d; status: %s\n", id, task.Status)
}
