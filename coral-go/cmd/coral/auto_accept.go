package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/clierr"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/hooks"
)

// coral auto-accept off --all [--url URL]
//
// Turns auto-accept off for every agent on the running Coral server (#3182):
// the owner's "Stop all auto-accept" from a terminal. The server is CORAL_URL,
// or http://localhost:$CORAL_PORT (default: the platform's port, 8450 on Windows), unless --url is given.
func runAutoAccept(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: coral auto-accept off --all [--url "+defaultCoralURL()+"]")
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
	*base = runningServerURL(fs, *base, "")
	url := strings.TrimRight(*base, "/") + "/api/auto-accept/disable-all"
	send := func(client *http.Client) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return client.Do(req)
	}
	// Coral rejects unsigned command-line requests (#3421 v2): the kill switch signs with
	// Coral's command-line token (never inside an agent session). A server that can't
	// prove it knows that token gets nothing, and the error says which folder was used.
	client := &http.Client{Timeout: clierr.Timeout(15 * time.Second)}
	dir := cliauth.DataDir()
	if tok, terr := cliauth.ReadToken(dir); terr == nil && !cliauth.InsideAgentSession() {
		client.Transport = cliauth.Transport{Base: http.DefaultTransport, Token: tok, Dir: dir}
	}
	resp, err := send(client)
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
	// Help first, before any command or the server parses anything (#235).
	if code, ok := helpRequest(args, os.Stdout, os.Stderr); ok {
		return code, true
	}
	if len(args) > 0 && isVersionArg(args[0]) {
		printVersion(os.Stdout)
		return 0, true
	}
	if len(args) > 0 && args[0] == "auto-accept" {
		return runAutoAccept(args[1:], os.Stdout, os.Stderr), true
	}
	if len(args) > 0 && args[0] == "db" {
		return runDB(args[1:], os.Stdout, os.Stderr), true
	}
	if len(args) > 0 && args[0] == "setup-code" {
		return runSetupCode(args[1:], os.Stdout, os.Stderr), true
	}
	if len(args) > 0 && args[0] == "set-password" {
		return runSetPassword(args[1:], os.Stdin, os.Stdout, os.Stderr), true
	}
	if len(args) > 0 && args[0] == "reset-password" {
		return runResetPassword(args[1:], os.Stdin, os.Stdout, os.Stderr), true
	}
	if len(args) > 0 && args[0] == "api-key" {
		return runAPIKey(args[1:], os.Stdout, os.Stderr), true
	}
	if len(args) > 0 && args[0] == "open" {
		return runOpen(args[1:], os.Stdout, os.Stderr), true
	}
	if len(args) > 0 && args[0] == "cloud" {
		return runCloud(args[1:], os.Stdin, os.Stdout, os.Stderr), true
	}
	if len(args) > 0 && args[0] == "artifacts" {
		return runArtifacts(args[1:], os.Stdin, os.Stdout, os.Stderr), true
	}
	// A bare word that is no command ("coral stop", "coral status") is reported as one, with a hint, not as a stray server
	// argument followed by every server flag (#235). A flag goes on to the server's parser.
	if len(args) > 0 && args[0] != "" && !strings.HasPrefix(args[0], "-") {
		return unknownCommand(os.Stderr, args[0]), true
	}
	return 0, false
}
