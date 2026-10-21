package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/startup"
	"github.com/subgentic/coral/internal/tracking"
)

// sayTelemetry writes the line the tray reads (childSaysTelemetryOn or childSaysTelemetryOff).
func sayTelemetry(w io.Writer, on bool) {
	if on {
		fmt.Fprintln(w, childSaysTelemetryOn)
	} else {
		fmt.Fprintln(w, childSaysTelemetryOff)
	}
}

// watchTelemetry tells the tray about each later change of the Usage analytics setting: the tray never reads the setting, and a switch-off after startup
// would otherwise leave its tracking on until the next start.
func watchTelemetry(w io.Writer) {
	tracking.OnTelemetryChange(func(on bool) { sayTelemetry(w, on) })
}

// runServerChild is the server half of the tray (#4163): coral-tray --server-child runs
// Coral's server and nothing else, under the tray's supervision (supervise.go).
//
//   - It logs to standard error, which the tray writes to tray.log.
//   - It says "coral-child ready" on standard output once it serves, and asks for the
//     database password ("coral-child need-password") when the database needs one: the
//     tray's native prompt answers with one line on standard input.
//   - Its standard input closing is the tray asking it to stop, as is SIGTERM/interrupt; a
//     tray that dies closes it too, so a server never outlives its tray.
//   - It exits 0 after a stop, childExitStartFailed when the server could not start and
//     childExitRestart when Settings > Restart asked for a new server.
func runServerChild(host string, port int, backendType string) {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Printf("[STARTUP] coral server (tray child) starting pid=%d go=%s os=%s arch=%s", os.Getpid(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
	signal.Ignore(syscall.SIGHUP)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Standard input: password lines when asked, and EOF as the stop request.
	lines := make(chan string, 1)
	go func() {
		defer safego.Recover("coral-tray/child:stdin")
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			default: // nothing asked for a line
			}
		}
		log.Printf("[SHUTDOWN] the tray asked the server to stop")
		cancel()
	}()

	var restart atomic.Bool
	cfg := config.Load()
	if host != "" {
		cfg.Host, cfg.HostExplicit = host, true
	}
	cfg.Port = port
	rs, err := startup.Start(ctx, cfg, startup.Options{
		BackendType: backendType,
		// Settings > Run as a hub's Restart button (#3460): this server stops and the tray
		// starts a new one at once.
		Restart: func() error {
			restart.Store(true)
			go func() {
				defer safego.Recover("coral-tray/child:restart")
				time.Sleep(500 * time.Millisecond) // let the request that asked be answered
				cancel()
			}()
			return nil
		},
		PasswordPrompt: func() (string, error) {
			fmt.Println(childSaysNeedPassword)
			select {
			case pw := <-lines:
				return strings.TrimRight(pw, "\r"), nil
			case <-ctx.Done():
				return "", errors.New("no database password: the tray closed the prompt")
			}
		},
		UnlockSurface: func() string {
			if runtime.GOOS == "darwin" {
				return "native" // the tray's prompt
			}
			return "headless"
		}(),
		OnServerError: func(err error) {
			log.Printf("Server error: %v", err)
		},
	})
	if err != nil {
		log.Printf("Failed to start: %v", err)
		os.Exit(childExitStartFailed)
	}
	sayTelemetry(os.Stdout, tracking.TelemetryOn()) // the setting startup applied (fail closed) and the environment's opt-out
	watchTelemetry(os.Stdout)                       // and every later switch in Settings, so the tray follows it
	fmt.Println(childSaysReady)

	<-ctx.Done()
	log.Println("[SHUTDOWN] shutting down...")
	rs.Shutdown(10 * time.Second)
	rs.Close()
	log.Println("[SHUTDOWN] done")
	if restart.Load() {
		os.Exit(childExitRestart)
	}
}
