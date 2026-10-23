package main

import (
	"flag"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"github.com/subgentic/coral/internal/clihelp"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// cmdTaskPark: coral-board task park <id> --reason "..." [--until DATE], and task unpark <id>
// (#4175 step 3). The orchestrator or the Operator only; the server decides.
func cmdTaskPark(st *boardState, args []string, unpark bool) {
	usage := `Usage: coral-board task park <id> --reason "why" [--until "2026-10-19 21:58" | 2026-10-19 | 3d]
       coral-board task unpark <id>`
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, usage)
		clierr.Exit1()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintf(os.Stderr, "Invalid task ID: %s\n", args[0])
		clierr.Exit1()
	}
	path := fmt.Sprintf("/%s/tasks/%d/park", url.PathEscape(st.Project), id)
	if unpark {
		data, status, err := apiCallRaw("DELETE", path, nil)
		if err != nil || status != http.StatusOK {
			fmt.Fprintf(os.Stderr, "Error unparking task: %v %s\n", err, data)
			clierr.Exit1()
		}
		fmt.Printf("Unparked Task #%d\n", id)
		return
	}
	fs := flag.NewFlagSet("task-park", flag.ContinueOnError)
	reason := fs.String("reason", "", "Why it is set aside (required)")
	until := fs.String("until", "", "When it comes back by itself (optional)")
	clihelp.ParseFlags(fs, args[1:], func() string { return commandUsage(os.Args[1], os.Args[2:]) })
	if *reason == "" {
		fmt.Fprintln(os.Stderr, usage)
		clierr.Exit1()
	}
	body := map[string]any{"reason": flagText(*reason, "park reason")}
	if *until != "" {
		at, err := parseWhen(*until, time.Now())
		if err != nil {
			fmt.Fprintln(os.Stderr, "--until:", err)
			clierr.Exit1()
		}
		body["until"] = at.UTC().Format(time.RFC3339)
	}
	data, status, err := apiCallRaw("POST", path, body)
	if err != nil || status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error parking task: %v %s\n", err, data)
		clierr.Exit1()
	}
	if *until != "" {
		fmt.Printf("Parked Task #%d until %s\n", id, body["until"])
	} else {
		fmt.Printf("Parked Task #%d until it is unparked\n", id)
	}
}
