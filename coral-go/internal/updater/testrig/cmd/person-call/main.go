//go:build unix

// person-call is the update rehearsal's stand-in for the person at this computer (#6297). It is NOT a released program: no list in tests/release names it and a test runs go list over every
// released program to show none links the rehearsal package. It makes one request to the Coral of a data folder the way Coral's own command-line tools do: signed with that folder's command-line
// token, which is the one credential the server takes for a person besides the dashboard's cookie (an API key, an agent's session token or an unsigned request is not a person's yes).
//
//	person-call --data-dir D --url http://127.0.0.1:PORT --method POST --path /api/system/update/relaunch-local --body '{"ack":"..."}'
//
// It prints the status code, a space and the body on one line, and exits 0 when it got any answer.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/cliauth"
)

func main() { os.Exit(run()) }

func run() int {
	dataDir := flag.String("data-dir", "", "Coral's data folder (the command-line token is read from it)")
	base := flag.String("url", "", "the server's address")
	method := flag.String("method", "GET", "the method")
	path := flag.String("path", "", "the path")
	body := flag.String("body", "", "the request body")
	flag.Parse()
	if *dataDir == "" || *base == "" || *path == "" {
		fmt.Fprintln(os.Stderr, "usage: person-call --data-dir D --url U --method M --path P [--body B]")
		return 2
	}
	os.Setenv("CORAL_DATA_DIR", *dataDir)
	if !cliauth.InstallOperatorSigning() {
		fmt.Fprintln(os.Stderr, "person-call: could not sign (no command-line token in the data folder)")
		return 2
	}
	req, err := http.NewRequest(*method, strings.TrimRight(*base, "/")+*path, strings.NewReader(*body))
	if err != nil {
		fmt.Fprintln(os.Stderr, "person-call:", err)
		return 2
	}
	if *body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "person-call:", err)
		return 1
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	fmt.Printf("%d %s\n", resp.StatusCode, strings.ReplaceAll(strings.TrimSpace(string(b)), "\n", " "))
	return 0
}
