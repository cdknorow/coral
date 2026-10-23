package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
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
	tracking.OnTelemetryChange(telemetryHook(w))
}

// telemetryHook is what watchTelemetry registers. The hook is called after each switch from the goroutine that made it, so two quick switches could call it out of
// order, and the second call would say a stale "on" after the "off". It therefore says what the setting is NOW (not the value it was called with), one line at a
// time: the last line the tray reads is always the current setting, so a switch-off can never be undone by a late call (#5702 review).
func telemetryHook(w io.Writer) func(on bool) {
	var mu sync.Mutex
	return func(bool) {
		mu.Lock()
		defer mu.Unlock()
		sayTelemetry(w, tracking.TelemetryOn())
	}
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
//   - It exits 0 after a stop, childExitStartFailed when the server could not start,
//     childExitRestart when Settings > Restart asked for a new server and childExitQuitForUpdate
//     when the local "Install update" started the updater (#166).
func runServerChild(host string, port int, backendType string) {
	log.SetOutput(logsafe.Writer(os.Stderr)) // tray.log carries what this writes: no home folder, user or computer name (#6053)
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

	var restart, quitForUpdate atomic.Bool
	// The instance lock before config.Load touches the data folder (#6388): a second server child against a folder another holds writes nothing there.
	instance, lockErr := startup.LockInstance(config.ResolveCoralDir(""))
	if lockErr != nil {
		log.Printf("Failed to start: %v", lockErr)
		os.Exit(childExitStartFailed)
	}
	cfg := config.Load()
	if host != "" {
		cfg.Host, cfg.HostExplicit = host, true
	}
	cfg.Port = port
	rs, err := startup.Start(ctx, cfg, startup.Options{
		Instance:    instance,
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
		// The local "Install update" (#166): the updater has just been started and must have Coral to itself, so this server stops and exits with
		// childExitQuitForUpdate, which the tray's supervisor takes as "do not start it again".
		QuitForUpdate: func() error {
			quitForUpdate.Store(true)
			go func() {
				defer safego.Recover("coral-tray/child:quit-for-update")
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
	if quitForUpdate.Load() {
		os.Exit(childExitQuitForUpdate)
	}
}
