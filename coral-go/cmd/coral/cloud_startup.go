package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/config"
)

// The startup "Connect to Coral Hub" step (#3285). When Coral is started with a
// Coral Hub URL given explicitly (--cloud-url or CORAL_CLOUD_URL) and this
// server is not enrolled yet, it connects it, with the same flow as
// `coral cloud connect`:
//   - prompt (the default) on a terminal: the link and code, a live wait, then
//     the access question; s or Ctrl-C skips it, and the server keeps running;
//   - otherwise (a service, a pipe, '&', or --cloud-connect=auto) it never
//     blocks: it logs the link and code, and a new code when one expires, for an
//     hour;
//   - --cloud-connect=off: nothing.
// The dashboard is up meanwhile.

const (
	cloudConnectPrompt = "prompt"
	cloudConnectAuto   = "auto"
	cloudConnectOff    = "off"
)

// cloudLogWindow is how long a server without a terminal keeps offering codes.
var cloudLogWindow = time.Hour

// explicitCloudURL reports whether a Coral Hub URL was given on purpose (the
// flag, or the environment), as opposed to a build default.
func explicitCloudURL(flagValue string) bool {
	if strings.TrimSpace(flagValue) != "" {
		return true
	}
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("CORAL_CLOUD_URL"))); v {
	case "", "off", "none", "0", "false":
		return false
	}
	return true
}

// cloudStartupMode is what the startup step does: prompt, log, or off.
func cloudStartupMode(flagValue string, explicit, tty bool) (string, error) {
	switch flagValue {
	case cloudConnectPrompt, "":
		if !explicit {
			return cloudConnectOff, nil
		}
		if tty {
			return cloudConnectPrompt, nil
		}
		return "log", nil
	case cloudConnectAuto:
		if !explicit {
			return cloudConnectOff, nil
		}
		return "log", nil
	case cloudConnectOff:
		return cloudConnectOff, nil
	}
	return "", fmt.Errorf("--cloud-connect must be prompt, auto or off (got %q)", flagValue)
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
		log.Printf("[CLOUD] Coral Hub: this server is connected as %s", firstNonEmpty(st.Label, "this server"))
		return
	case st.State == cloud.StateRevoked:
		log.Printf("[CLOUD] Coral Hub revoked this server. To connect it again, run: coral cloud connect --force")
		return
	}

	if mode == "log" {
		go func() {
			ui := connectUI{out: logLines{}, in: bufio.NewReader(strings.NewReader("")), how: "coral cloud connect"}
			end := time.Now().Add(cloudLogWindow)
			for ctx.Err() == nil {
				if c.connectFlow(ctx, ui) != 4 || !time.Now().Before(end) {
					break
				}
				// A new code, after a pause so an unreachable Hub is not hammered.
				select {
				case <-ctx.Done():
				case <-time.After(30 * time.Second):
				}
			}
			if ctx.Err() == nil && !time.Now().Before(end) {
				log.Printf("[CLOUD] Not connected to Coral Hub. To connect later, run: coral cloud connect")
			}
		}()
		return
	}

	ui := connectUI{out: out, in: bufio.NewReader(in), ask: true, how: "coral cloud connect"}
	ans, ok := ui.prompt(fmt.Sprintf("\nConnect this server to Coral Hub (%s) now? Enter to continue, s to skip: ", st.HubURL))
	if !ok || strings.EqualFold(ans, "s") || strings.EqualFold(ans, "skip") {
		fmt.Fprintln(out, "Skipped. This server runs on its own; to connect it later, run: coral cloud connect")
		return
	}
	step, cancel := context.WithCancel(ctx)
	restore := ir.Divert(cancel)
	defer restore()
	defer cancel()
	c.connectFlow(step, ui)
	fmt.Fprintln(out)
}
