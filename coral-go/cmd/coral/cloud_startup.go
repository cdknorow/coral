package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/safego"
)

// The startup "Connect to Coral Hub" step (#3285). Off unless asked for (#3459):
// with --cloud-connect (or CORAL_CLOUD_CONNECT) prompt or auto, or with a Coral Hub
// URL given explicitly (--cloud-url or CORAL_CLOUD_URL; unset --cloud-connect then
// means prompt), on a server that has a Coral Hub URL and is not enrolled yet, it
// connects it, with the same flow as `coral cloud connect`:
//   - prompt on a terminal: the question first, and only the word yes connects (Enter, s or
//     anything else skips, as Enter declines the Terms: one rule, #6381); a skip is stored
//     (welcome_choice, the answer the dashboard's first screen saves), so the browser does
//     not ask again; then the link and code, a live wait and the access question; Ctrl-C
//     skips the wait, and the server keeps running;
//   - otherwise (a service, a pipe, '&', or auto) it never blocks: it logs the
//     link and code, and a new code when one expires, for an hour;
//   - off, or unset with only the build's default URL: nothing. Coral Hub is still a
//     click away on the home screen and in Settings, and `coral cloud connect` works.
// setting is config.EffectiveCloudConnect.
// The dashboard is up meanwhile.

const (
	cloudConnectPrompt = "prompt"
	cloudConnectAuto   = "auto"
	cloudConnectOff    = "off"
)

// cloudLogWindow is how long a server without a terminal keeps offering codes.
var cloudLogWindow = time.Hour

// cloudStartupMode is what the startup step does: prompt, log, or off. setting is
// --cloud-connect or CORAL_CLOUD_CONNECT; haveURL is whether a Coral Hub URL is
// configured at all.
func cloudStartupMode(setting string, haveURL, tty bool) (string, error) {
	switch setting {
	case "", cloudConnectOff:
		return cloudConnectOff, nil
	case cloudConnectPrompt:
		if !haveURL {
			return cloudConnectOff, nil
		}
		if tty {
			return cloudConnectPrompt, nil
		}
		return "log", nil
	case cloudConnectAuto:
		if !haveURL {
			return cloudConnectOff, nil
		}
		return "log", nil
	}
	return "", fmt.Errorf("--cloud-connect (or CORAL_CLOUD_CONNECT) must be prompt, auto or off (got %q)", setting)
}

// interruptRouter turns SIGINT/SIGTERM into a shutdown, except that while a
// step has diverted it, Ctrl-C (SIGINT) ends only that step.
type interruptRouter struct {
	shutdown func()
	mu       sync.Mutex
	divert   func()
}

func newInterruptRouter(shutdown context.CancelFunc) *interruptRouter {
	ir := &interruptRouter{shutdown: shutdown}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer safego.Recover("coral/cloud_startup:81")
		for s := range ch {
			ir.handle(s)
		}
	}()
	return ir
}

func (ir *interruptRouter) handle(s os.Signal) {
	ir.mu.Lock()
	d := ir.divert
	ir.mu.Unlock()
	if s == os.Interrupt && d != nil {
		d()
		return
	}
	ir.shutdown()
}

// Divert sends Ctrl-C to cancel until the returned function is called.
func (ir *interruptRouter) Divert(cancel func()) (restore func()) {
	ir.mu.Lock()
	ir.divert = cancel
	ir.mu.Unlock()
	return func() {
		ir.mu.Lock()
		ir.divert = nil
		ir.mu.Unlock()
	}
}

// logLines writes each line as a [CLOUD] log line.
type logLines struct{}

func (logLines) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			log.Printf("[CLOUD] %s", line)
		}
	}
	return len(p), nil
}

// isYes is the one word that connects: the same rule as the Terms, where only the word accepts and Enter declines.
func isYes(ans string) bool {
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "y", "yes", "connect":
		return true
	}
	return false
}

// rememberSkip stores the answer the dashboard's first screen stores when its Skip is pressed (welcome_choice and welcome_pending, in one request), so a skip typed in the terminal is the
// answer for every browser. Best effort: a failure is said by the client and the step goes on.
func (c *cloudClient) rememberSkip(ctx context.Context) {
	_ = c.call(ctx, http.MethodPut, "/api/settings", map[string]any{"welcome_choice": "skipped", "welcome_pending": "False"}, nil, 10*time.Second, nil)
}

// runCloudStartupStep runs the step against this server (its own loopback
// port, its own command-line token). In prompt mode it returns when the step is
// done or skipped; in log mode it returns at once.
func runCloudStartupStep(ctx context.Context, cfg *config.Config, mode string, ir *interruptRouter, in io.Reader, out io.Writer) {
	if mode == cloudConnectOff {
		return
	}
	base := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port))
	c, _ := newCloudClient(base, cfg.CoralDir(), "connect", logLines{})
	if c == nil {
		return // already logged (no token: the command line is off)
	}
	// The connector reads the stored enrollment (and a startup credential) just
	// after the server starts: wait for that before deciding anything.
	deadline := time.Now().Add(2 * time.Minute)
	st, code := c.status(ctx)
	for code < 0 && st.Loading && time.Now().Before(deadline) && ctx.Err() == nil {
		time.Sleep(200 * time.Millisecond)
		st, code = c.status(ctx)
	}
	switch {
	case code >= 0 || ctx.Err() != nil:
		return
	case !st.Configured:
		return // a bad URL: already logged by the connector
	case st.State == cloud.StateConnected || st.State == cloud.StateConnecting || st.State == cloud.StateRetrying:
		log.Printf("[CLOUD] Coral Hub: this server is connected") // the Hub label names the person's machine: not in the log (#6053)
		return
	case st.State == cloud.StateRevoked:
		log.Printf("[CLOUD] Coral Hub revoked this server. To connect it again, run: coral cloud connect --force")
		return
	case st.HubMoved || st.LastErrorCode == cloud.ErrCodeHubMoved:
		// Enrolled with the hub's old host (#3546): say so, and start nothing. No request goes to
		// either hub until the operator enrolls again, and the message stays as it is.
		log.Printf("[CLOUD] Coral Hub: %s", st.LastError)
		return
	}

	if mode == "log" {
		go func() {
			defer safego.Recover("coral/cloud_startup:163")
			ui := connectUI{out: logLines{}, in: bufio.NewReader(strings.NewReader("")), how: "coral cloud connect"}
			end := time.Now().Add(cloudLogWindow)
			for ctx.Err() == nil {
				if c.connectFlow(ctx, ui) != 4 || !time.Now().Before(end) {
					break
				}
				// A new code, after a pause so an unreachable Hub is not hammered: at least
				// 30 s, and as long as Coral says (its Retry-After: the hub's, or this
				// machine's own limit on new codes).
				wait := 30 * time.Second
				if c.retryAfter > wait {
					wait = c.retryAfter
				}
				if left := time.Until(end); wait > left {
					wait = left
				}
				select {
				case <-ctx.Done():
				case <-time.After(wait):
				}
			}
			if ctx.Err() == nil && !time.Now().Before(end) {
				log.Printf("[CLOUD] Not connected to Coral Hub. To connect later, run: coral cloud connect")
			}
		}()
		return
	}

	ui := connectUI{out: out, in: bufio.NewReader(in), ask: true, how: "coral cloud connect"}
	ans, ok := ui.prompt(fmt.Sprintf("\nConnect this server to Coral Hub (%s)? Type yes to connect, or press Enter to skip: ", st.HubURL))
	if !ok || !isYes(ans) {
		if ok { // somebody answered: the dashboard does not ask again (settings, and the Coral Hub card, still connect it)
			c.rememberSkip(ctx)
		}
		fmt.Fprintln(out, "Skipped. This server runs on its own; to connect it later, use Settings > Coral Hub, or run: coral cloud connect")
		return
	}
	step, cancel := context.WithCancel(ctx)
	restore := ir.Divert(cancel)
	defer restore()
	defer cancel()
	c.connectFlow(step, ui)
	fmt.Fprintln(out)
}
