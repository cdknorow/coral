package main

import (
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"net/http"
	"net/url"
	"os"
)

const healthUsageText = `Usage: coral-board health ack <id>

  ack <id>   Mark an item of the health check handled: the check prints it as [ack <id>].
             It stays off the list until its state changes (the orchestrator or the Operator only).`

// cmdHealth: coral-board health ack <id> (#4175 step 3).
func cmdHealth() {
	if len(os.Args) < 4 || os.Args[2] != "ack" {
		if len(os.Args) >= 3 && (os.Args[2] == "-h" || os.Args[2] == "--help" || os.Args[2] == "help") {
			fmt.Println(healthUsageText)
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, healthUsageText)
		clierr.Exit1()
	}
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board. Run: coral-board join <project> --as <role>")
		clierr.Exit1()
	}
	item := os.Args[3]
	data, status, err := apiCallRaw("POST", "/"+url.PathEscape(st.Project)+"/health/ack", map[string]any{"item": item})
	if err != nil || status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error acknowledging %s: %v %s\n", item, err, data)
		clierr.Exit1()
	}
	fmt.Printf("Acknowledged %s: it stays off the health check until it changes\n", item)
}
