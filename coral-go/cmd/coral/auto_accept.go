package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/hooks"
)

// coral auto-accept off --all [--url URL]
//
// Turns auto-accept off for every agent on the running Coral server (#3182):
// the owner's "Stop all auto-accept" from a terminal. The server is CORAL_URL,
// or http://localhost:$CORAL_PORT (default 8420), unless --url is given.
func runAutoAccept(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: coral auto-accept off --all [--url http://localhost:8420]")
		return 2
	}
	if len(args) == 0 || args[0] != "off" {
		return usage()
	}
	fs := flag.NewFlagSet("auto-accept off", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "turn auto-accept off for every agent")
	base := fs.String("url", hooks.CoralBase(), "Coral server URL")
	if err := fs.Parse(args[1:]); err != nil || !*all || fs.NArg() > 0 {
		return usage()
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(*base, "/")+"/api/auto-accept/disable-all", strings.NewReader("{}"))
	if err != nil {
		fmt.Fprintln(stderr, "coral auto-accept:", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "coral auto-accept: could not reach Coral at %s: %v\n", *base, err)
		return 1
	}
	defer resp.Body.Close()
	var out struct {
		Stopped int    `json:"stopped"`
		Error   string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "coral auto-accept: %s %s\n", resp.Status, out.Error)
		return 1
	}
	fmt.Fprintf(stdout, "Auto-accept is off for every agent (%d turned off).\n", out.Stopped)
	return 0
}

// subcommand runs a `coral <subcommand>` and reports whether there was one.
func subcommand(args []string) (int, bool) {
	if len(args) > 0 && args[0] == "auto-accept" {
		return runAutoAccept(args[1:], os.Stdout, os.Stderr), true
	}
	if len(args) > 0 && args[0] == "db" {
		return runDB(args[1:], os.Stdout, os.Stderr), true
	}
	if len(args) > 0 && args[0] == "cloud" {
		return runCloud(args[1:], os.Stdin, os.Stdout, os.Stderr), true
	}
	return 0, false
}
