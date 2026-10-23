package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/hooks"
	"golang.org/x/term"
)

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// coral cloud connect [--ticket-file <path|-> [--proof-file <path>]] [--url URL] [--home DIR]
// coral cloud status | disconnect [--url URL] [--home DIR]
//
// Connects the running Coral server on this computer to Coral Hub (#3149,
// #3285). Without --ticket-file it is interactive: it starts the enrollment,
// prints the link and code to approve in a browser (on any computer), waits,
// then asks for the most Coral Hub may do on this server. With --ticket-file it
// redeems a one-time credential instead (no browser).
//
// The command talks only to this computer's Coral, and only once that Coral has
// proved it knows the 0600 token in its data folder (cloud.CLIAuth): the token is
// never sent, and a program squatting on the Coral port gets nothing, not even
// the credential file read. Only Coral's own user can read the token, so another
// local user cannot link this machine to their account.
//
// Exit codes: 0 done, 2 a permanent problem (no Coral Hub configured, a bad
// file, a credential the Hub refused), 3 the terms must be accepted again, 4
// try again (the Hub, or this Coral, could not be reached or is busy; an expired
// code), 5 already enrolled (nothing was changed; --force connects again), 130
// cancelled, 1 otherwise.
func runCloud(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: coral cloud connect [--force] [--ticket-file <path|-> [--proof-file <path>]] [--url "+defaultCoralURL()+"] [--home DIR]")
		fmt.Fprintln(stderr, "       coral cloud status [--url ...] [--home DIR]")
		fmt.Fprintln(stderr, "       coral cloud disconnect [--url ...] [--home DIR]")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	cmd := args[0]
	if cmd != "connect" && cmd != "status" && cmd != "disconnect" {
		return usage()
	}
	fs := flag.NewFlagSet("cloud "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var ticket, proof *string
	force := new(bool)
	if cmd == "connect" {
		force = fs.Bool("force", false, "connect again even if this server is already enrolled: its enrollment here is forgotten first (the old entry stays in Coral Hub until you revoke it there)")
		ticket = fs.String("ticket-file", "", "a one-time Coral Hub credential file (owned by you, mode 0600; removed once the Hub has answered), or - for stdin")
		proof = fs.String("proof-file", "", "optional proof file from a provider helper (bootstrap credentials only)")
	}
	base := fs.String("url", hooks.CoralBase(), "the running Coral server, on this computer")
	home := fs.String("home", "", "Coral's data folder, if the server was started with --home (default: CORAL_DATA_DIR or ~/.coral)")
	if err := fs.Parse(args[1:]); err != nil {
		return usage()
	}
	*base = runningServerURL(fs, *base, *home)
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "coral cloud %s: unexpected argument %q (a credential is never taken from the command line; use --ticket-file)\n", cmd, fs.Arg(0))
		return 2
	}
	c, code := newCloudClient(*base, *home, cmd, stderr)
	if c == nil {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch cmd {
	case "status":
		return c.printStatus(ctx, stdout)
	case "disconnect":
		return c.disconnect(ctx, stdout)
	}
	if *ticket != "" {
		return c.redeemTicket(ctx, *ticket, *proof, stdin, stdout)
	}
	if *proof != "" {
		fmt.Fprintln(stderr, "coral cloud connect: --proof-file goes with --ticket-file")
		return 2
	}
	tty := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	return c.connectFlow(ctx, connectUI{out: stdout, in: bufio.NewReader(stdin), ask: tty, how: "coral cloud connect", force: *force})
}

// cloudClient is the command line's view of this computer's Coral.
type cloudClient struct {
	cli    cloud.CLIClient
	server string
	cmd    string
	// label names the command in messages ("coral cloud <cmd>" when empty).
	label  string
	stderr io.Writer
	// retryAfter is the last refusal's Retry-After (0 if none): the unattended startup
	// step waits at least this long before asking again.
	retryAfter time.Duration
}

func newCloudClient(base, home, cmd string, stderr io.Writer) (*cloudClient, int) {
	server := strings.TrimRight(base, "/")
	if !loopbackURL(server) {
		fmt.Fprintf(stderr, "%s: %s is not this computer: this command only talks to the Coral running here\n", commandName(cmd), server)
		return nil, 2
	}
	token, err := cloud.ReadCLIToken(config.ResolveCoralDir(home))
	if err != nil {
		fmt.Fprintf(stderr, "%s: could not read Coral's command-line token (%v): run this as the user Coral runs as, with Coral started (and --home if it uses one)\n", commandName(cmd), err)
		return nil, 2
	}
	return &cloudClient{cli: cloud.CLIClient{Base: server, Token: token}, server: server, cmd: cmd, stderr: stderr}, 0
}

// who names the command in messages.
// exitCannotReach is the exit code for "Coral is not running there": 3, as #101's clierr table has it (2 usage, 3 cannot reach,
// 4 timed out); to become clierr.ExitUnreachable once that lands (#235).
const exitCannotReach = 3

func (c *cloudClient) who() string {
	if c.label != "" {
		return c.label
	}
	return commandName(c.cmd)
}

// commandName is how a message names the command: "coral cloud status", but "coral open", "coral set-password" ... by their own
// name, never "coral cloud open" (#235).
func commandName(cmd string) string {
	switch cmd {
	case "connect", "status", "disconnect":
		return "coral cloud " + cmd
	}
	return "coral " + cmd
}

// call sends one request. code is -1 when out was filled from a 200, else the
// exit code for a failure (already reported).
func (c *cloudClient) call(ctx context.Context, method, path string, body any, hdr http.Header, timeout time.Duration, out any) int {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	resp, err := c.cli.Do(ctx, method, path, b, hdr, timeout)
	switch {
	case errors.Is(err, cloud.ErrNotCoral):
		fmt.Fprintf(c.stderr, "%s: %s: %v. Nothing was sent. Check --url (and --home), and that Coral is running.\n", c.who(), c.server, err)
		return 4
	case ctx.Err() != nil:
		return 130
	case err != nil:
		fmt.Fprintf(c.stderr, "%s: Coral is not running at %s: start it and run this again (or give --url, or --home for its data folder)\n", c.who(), c.server)
		return exitCannotReach
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	c.retryAfter = 0
	if resp.StatusCode != http.StatusOK {
		if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
			c.retryAfter = time.Duration(secs) * time.Second
		}
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		fmt.Fprintf(c.stderr, "%s: %s\n", c.who(), e.Error)
		switch resp.StatusCode {
		case http.StatusBadRequest:
			return 2
		case http.StatusTooManyRequests, http.StatusServiceUnavailable: // Coral Hub is busy, unreachable or failing: try again later
			return 4
		}
		return 1
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			fmt.Fprintf(c.stderr, "%s: unexpected answer from Coral\n", c.who())
			return 1
		}
	}
	return -1
}

type cloudStatusReply struct {
	cloud.Status
	AllowHubAutoAccept bool `json:"allow_hub_auto_accept"`
}

func (c *cloudClient) status(ctx context.Context) (cloudStatusReply, int) {
	var st cloudStatusReply
	return st, c.call(ctx, http.MethodGet, "/api/cloud/status", nil, nil, 10*time.Second, &st)
}

// notConfiguredHelp is what to do when the server has no Coral Hub URL.
const notConfiguredHelp = "Coral Hub isn't configured on this server. Start Coral with --cloud-url https://app.coralhub.dev (or CORAL_CLOUD_URL=https://app.coralhub.dev), then run: coral cloud connect"

func (c *cloudClient) printStatus(ctx context.Context, out io.Writer) int {
	st, code := c.status(ctx)
	if code >= 0 {
		return code
	}
	if !st.Configured {
		fmt.Fprintln(out, notConfiguredHelp)
		return 0
	}
	fmt.Fprintf(out, "Coral Hub: %s\nState:     %s\n", st.HubURL, st.State)
	if st.Label != "" {
		fmt.Fprintf(out, "Server:    %s\n", st.Label)
	}
	fmt.Fprintf(out, "Access:    %s\n", st.MaxMode)
	if st.LastError != "" {
		fmt.Fprintf(out, "Note:      %s\n", st.LastError)
	}
	return 0
}

func (c *cloudClient) disconnect(ctx context.Context, out io.Writer) int {
	if code := c.call(ctx, http.MethodPost, "/api/cloud/disconnect", map[string]any{}, nil, 30*time.Second, nil); code >= 0 {
		return code
	}
	fmt.Fprintln(out, "Disconnected from Coral Hub. Run 'coral cloud connect' to connect again.")
	return 0
}

// connectUI is where the interactive flow talks: the terminal for `coral cloud
// connect` and a startup on a TTY (ask), or the log for a startup without one.
type connectUI struct {
	out   io.Writer
	in    *bufio.Reader
	ask   bool   // a person is at a terminal: offer a retry, ask for the access mode
	how   string // how to do it later, for the messages
	force bool   // --force: forget an existing enrollment here, then enroll again
}

func (u connectUI) prompt(q string) (string, bool) {
	fmt.Fprint(u.out, q)
	line, err := u.in.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimSpace(line), true
}

func labelSuffix(label string) string {
	if label == "" {
		return ""
	}
	return " (" + label + ")"
}

// connectPoll is how often the flow reads the status while waiting (a var for tests).
var connectPoll = 2 * time.Second

// connectFlow links the running server to Coral Hub with a device code: the
// same flow for `coral cloud connect` and the startup step (#3285).
func (c *cloudClient) connectFlow(ctx context.Context, ui connectUI) int {
	st, code := c.status(ctx)
	if code >= 0 {
		return code
	}
	if !st.Configured {
		fmt.Fprintln(ui.out, notConfiguredHelp)
		return 2
	}
	switch st.State {
	case cloud.StateConnected, cloud.StateConnecting, cloud.StateRetrying, cloud.StateRevoked:
		if !ui.force {
			if st.State == cloud.StateRevoked {
				fmt.Fprintln(ui.out, "Coral Hub revoked this server. Run 'coral cloud connect --force' to connect it again.")
			} else {
				fmt.Fprintf(ui.out, "This server is already connected to Coral Hub (%s) as %s. Nothing was changed: 'coral cloud connect --force' connects it again.\n", st.HubURL, st.Label)
			}
			return 5
		}
		// The Hub keeps the old server entry (and its token stays valid there) until the
		// user revokes it: forgetting it here is all this computer can do.
		if code := c.call(ctx, http.MethodPost, "/api/cloud/disconnect", map[string]any{}, nil, 30*time.Second, nil); code >= 0 {
			return code
		}
		fmt.Fprintf(ui.out, "Forgot this server's enrollment%s. Its old entry stays in Coral Hub until you revoke it there.\n", labelSuffix(st.Label))
	}
	if ui.ask {
		fmt.Fprintf(ui.out, "\nConnect to Coral Hub (%s)\n", st.HubURL)
	}
	for {
		if code := c.call(ctx, http.MethodPost, "/api/cloud/connect", map[string]any{}, nil, 30*time.Second, &st); code >= 0 {
			if code == 130 {
				return c.cancelled(ui)
			}
			return code
		}
		if st.State != cloud.StateEnrolling || st.UserCode == "" {
			fmt.Fprintf(ui.out, "Could not start connecting: %s\n", firstNonEmpty(st.LastError, "the Hub did not answer"))
			return 4
		}
		if ui.ask {
			fmt.Fprintf(ui.out, "\nOpen %s\nYour code %s should already be filled in. Approve it there (from any computer).\n", st.VerificationURL, st.UserCode)
			fmt.Fprintln(ui.out, "\nWaiting for you to approve… (Ctrl-C to cancel)")
		} else {
			// One line, for a log or a script (#3285).
			fmt.Fprintf(ui.out, "To connect this server to Coral Hub, open %s and enter code %s\n", st.VerificationURL, st.UserCode)
		}
		st, code = c.waitEnrollment(ctx)
		switch {
		case code == 130:
			return c.cancelled(ui)
		case code >= 0:
			return code
		}
		if st.State == cloud.StateConnecting || st.State == cloud.StateConnected || st.State == cloud.StateRetrying {
			break
		}
		// The code expired, the request was denied, or the Hub stopped answering.
		if st.LastErrorCode == cloud.ErrCodeExpired {
			// coral-hub's text, a full sentence ending with a link: printed as it is.
			fmt.Fprintf(ui.out, "Not connected. %s\n", st.LastError)
		} else {
			fmt.Fprintf(ui.out, "Not connected: %s.\n", firstNonEmpty(st.LastError, "the enrollment ended"))
		}
		if !ui.ask {
			return 4
		}
		if ans, ok := ui.prompt("Press Enter for a new code, or type q to stop: "); !ok || strings.EqualFold(ans, "q") {
			fmt.Fprintf(ui.out, "Not connected. Run '%s' to try again.\n", ui.how)
			return 4
		}
	}
	fmt.Fprintf(ui.out, "Connected to Coral Hub as %s.\n", firstNonEmpty(st.Label, "this server"))
	if ui.ask {
		if code := c.askAccess(ctx, ui); code >= 0 {
			return code
		}
	} else {
		fmt.Fprintf(ui.out, "Coral Hub can view this server. To allow more, open Settings > Coral Hub in the dashboard on this computer.\n")
	}
	return 0
}

// waitEnrollment polls until the enrollment ends. code is -1 with the final
// status, 130 when cancelled, else an exit code.
func (c *cloudClient) waitEnrollment(ctx context.Context) (cloudStatusReply, int) {
	for {
		select {
		case <-ctx.Done():
			return cloudStatusReply{}, 130
		case <-time.After(connectPoll):
		}
		st, code := c.status(ctx)
		if code >= 0 {
			return st, code
		}
		if st.State != cloud.StateEnrolling {
			return st, -1
		}
	}
}

// cancelled stops the enrollment in the server (never a connection) after Ctrl-C.
func (c *cloudClient) cancelled(ui connectUI) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var st cloudStatusReply
	c.call(ctx, http.MethodPost, "/api/cloud/connect/cancel", map[string]any{}, nil, 10*time.Second, &st)
	if st.State == cloud.StateConnecting || st.State == cloud.StateConnected {
		fmt.Fprintf(ui.out, "\nThe code was approved just before the cancel: this server is connected to Coral Hub as %s. 'coral cloud disconnect' to undo it.\n", st.Label)
		return 0
	}
	fmt.Fprintf(ui.out, "\nCancelled: this server was not connected. Run '%s' to connect it later.\n", ui.how)
	return 130
}

// askAccess asks the most Coral Hub may do on this server (default View) and,
// for Launch, whether it may turn on auto-accept.
func (c *cloudClient) askAccess(ctx context.Context, ui connectUI) int {
	fmt.Fprint(ui.out, `
The most Coral Hub may do on this server:
  1) View      read everything: terminals, chats, all board chat (default)
  2) Interact  also type into agents and answer their prompts
  3) Launch    also start, restart and stop agents
`)
	ans, ok := ui.prompt("Choose 1, 2 or 3 [1]: ")
	mode := map[string]string{"": "view", "1": "view", "2": "interact", "3": "launch", "view": "view", "interact": "interact", "launch": "launch"}[strings.ToLower(ans)]
	if !ok || mode == "" {
		mode = "view"
	}
	if code := c.call(ctx, http.MethodPut, "/api/cloud/max-mode", map[string]string{"mode": mode}, nil, 10*time.Second, nil); code >= 0 {
		return code
	}
	if mode == "launch" {
		ans, ok = ui.prompt("Let Coral Hub turn on auto-accept for agents here? [y/N]: ")
		allow := ok && (strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes"))
		if code := c.call(ctx, http.MethodPut, "/api/cloud/allow-auto-accept", map[string]bool{"allow": allow}, nil, 10*time.Second, nil); code >= 0 {
			return code
		}
	}
	fmt.Fprintf(ui.out, "Coral Hub access: %s. Change it any time in Settings > Coral Hub on this computer.\n", mode)
	return -1
}

// redeemTicket enrolls with a one-time credential (#3149). It asks first whether
// a bootstrap could proceed and reads nothing when it could not. The credential
// file stays on disk until the Hub has answered: removed when it enrolled or
// refused the credential, kept when the Hub could not be reached (exit 4: run
// it again with the same file).
func (c *cloudClient) redeemTicket(ctx context.Context, ticket, proof string, stdin io.Reader, stdout io.Writer) int {
	var out redeemReply
	if code := c.call(ctx, http.MethodPost, "/api/cloud/redeem", map[string]any{"check": true}, nil, 10*time.Second, &out); code >= 0 {
		return code
	}
	if out.Status != cloud.BootstrapReady {
		fmt.Fprintf(c.stderr, "%s: %s (nothing was read)\n", out.Status, out.Detail)
		return out.exitCode()
	}

	// The proof, then the credential: a bad proof leaves the credential unread.
	body := map[string]any{}
	if proof != "" {
		p, err := cloud.ReadProofFile(proof)
		if err == nil {
			err = cloud.ValidateProof(p)
		}
		if err != nil {
			fmt.Fprintln(c.stderr, "coral cloud connect: proof file:", err)
			return 2
		}
		body["proof"] = json.RawMessage(p)
	}
	var raw []byte
	var held *cloud.HeldCredential
	if ticket == "-" {
		var err error
		raw, err = io.ReadAll(io.LimitReader(stdin, 4097))
		if err != nil || len(raw) > 4096 {
			fmt.Fprintln(c.stderr, "coral cloud connect: could not read one credential from stdin")
			return 2
		}
	} else {
		var notTmpfs bool
		var err error
		raw, notTmpfs, held, err = cloud.HoldCredentialFile(ticket)
		if err != nil {
			fmt.Fprintln(c.stderr, "coral cloud connect:", err)
			return 2
		}
		if notTmpfs {
			fmt.Fprintln(c.stderr, "coral cloud connect: warning: the credential file is not on a tmpfs")
		}
	}
	cred, err := cloud.ParseCredential(raw)
	clear(raw)
	if err != nil {
		if held != nil {
			held.Keep()
		}
		fmt.Fprintln(c.stderr, "coral cloud connect:", err)
		return 2
	}

	// The server redeems (it retries the Hub within the credential's window).
	hdr := http.Header{"Authorization": {"Bearer " + cred}}
	code := c.call(ctx, http.MethodPost, "/api/cloud/redeem", body, hdr, 5*time.Minute, &out)
	answered := code < 0 && out.Status != cloud.BootstrapRetryLater && out.Status != cloud.BootstrapTermsReconsent
	if held != nil {
		if answered {
			if err := held.Remove(); err != nil {
				fmt.Fprintln(c.stderr, "coral cloud connect: warning: the credential file", err)
			}
		} else {
			held.Keep()
		}
	}
	if code >= 0 {
		if held != nil {
			fmt.Fprintln(c.stderr, "coral cloud connect: the credential file was left in place: run this again to retry")
		}
		return code
	}
	w := stdout
	if out.exitCode() != 0 {
		w = c.stderr
	}
	fmt.Fprintf(w, "%s: %s\n", out.Status, out.Detail)
	if held != nil && !answered {
		fmt.Fprintln(c.stderr, "coral cloud connect: the credential file was left in place: run this again to retry")
	}
	return out.exitCode()
}

type redeemReply struct {
	Status   string `json:"status"`
	Detail   string `json:"detail"`
	ExitCode *int   `json:"exit_code"`
}

func (r redeemReply) exitCode() int {
	if r.ExitCode == nil {
		return 1
	}
	return *r.ExitCode
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// loopbackURL reports whether u is an http(s) URL for this computer.
func loopbackURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.User != nil {
		return false
	}
	h := p.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
