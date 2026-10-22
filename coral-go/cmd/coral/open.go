package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"time"

	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/hooks"
	"github.com/subgentic/coral/internal/loginlink"
)

// canOpenBrowser reports whether this machine can show a browser: false on a Linux
// box with no display (SSH, a container, a service).
func canOpenBrowser() bool {
	if runtime.GOOS == "linux" {
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
	return true
}

// openDashboard opens the dashboard sign-in link in a browser, or prints it when there
// is none to open (#3387).
func openDashboard(out io.Writer, link string, browse bool) {
	if browse && canOpenBrowser() {
		executil.OpenBrowser(link)
		return
	}
	fmt.Fprintf(out, "  Open the dashboard (sign-in link, single use, 10 minutes):\n  %s\n\n", link)
	if inAgentTerminal() {
		fmt.Fprintln(out, "  Warning: this looks like an agent's terminal (or a tmux pane an agent can read). Anything shown here can read this link and sign in as you: run 'coral open' in your own terminal instead.")
		fmt.Fprintln(out)
	}
}

// inAgentTerminal: a Coral agent session's variables, or tmux (where an agent's pane can be read
// back), are set (#3970, Security's #3967 finding 4).
func inAgentTerminal() bool {
	return cliauth.InsideAgentSession() || os.Getenv("TMUX") != ""
}

// runOpen is `coral open`: a fresh dashboard sign-in link from the running Coral on
// this computer (#3387), opened in the browser, or printed with --print or without a
// display. It asks Coral through the same handshake as `coral cloud`.
func runOpen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	printOnly := fs.Bool("print", false, "print the link instead of opening a browser")
	base := fs.String("url", hooks.CoralBase(), "the running Coral server, on this computer")
	home := fs.String("home", "", "Coral's data folder, if the server was started with --home")
	page := fs.String("path", "/", "the dashboard page to open, e.g. /chats or /team/<board>")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: coral open [--print] [--path /page] [--url http://localhost:8420] [--home DIR]")
		return 2
	}
	*base = runningServerURL(fs, *base, *home)
	if !loginlink.ValidPage(*page) {
		fmt.Fprintln(stderr, "coral open: --path must be a page of the dashboard, like /chats or /team/<board>")
		return 2
	}
	c, code := newCloudClient(*base, *home, "open", stderr)
	if c == nil {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var reply struct {
		Path string `json:"path"`
	}
	if code := c.call(ctx, http.MethodPost, "/api/system/login-token", map[string]any{}, nil, 10*time.Second, &reply); code >= 0 {
		return code
	}
	openDashboard(stdout, loginlink.OnPage(c.server+reply.Path, *page), !*printOnly)
	return 0
}
