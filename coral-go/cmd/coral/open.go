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

	"github.com/cdknorow/coral/internal/executil"
	"github.com/cdknorow/coral/internal/hooks"
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
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: coral open [--print] [--url http://localhost:8420] [--home DIR]")
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
	openDashboard(stdout, c.server+reply.Path, !*printOnly)
	return 0
}
