// Command coral starts the Coral dashboard web server.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/executil"
	"github.com/cdknorow/coral/internal/license"
	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/startup"
	"github.com/cdknorow/coral/internal/tracking"
)

const coralDevStartupMarker = "DEV-TMUX-DIAGNOSTIC-2026-06-03-02"

// setupCrashLogging logs to both terminal (stderr) and <coralDir>/coral.log.
// This ensures panics are captured in the file while keeping terminal output
// visible when running interactively.
func setupCrashLogging(coralDir string) {
	os.MkdirAll(coralDir, 0755)
	logFile := filepath.Join(coralDir, "coral.log")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
}

func main() {

	// Global panic recovery — log the full stack trace before exiting
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[FATAL] panic in main: %v\n%s", r, debug.Stack())
			os.Exit(1)
		}
	}()

	log.Printf("[STARTUP] coral server starting marker=%s pid=%d go=%s os=%s arch=%s exe=%q path=%q",
		coralDevStartupMarker, os.Getpid(), runtime.Version(), runtime.GOOS, runtime.GOARCH, mustExecutable(), os.Getenv("PATH"))

	// Parse --home early so config.Load() can use it
	homeDir := flag.String("home", "", "Data directory (default: ~/.coral)")
	host := flag.String("host", "", "Host to bind to")
	port := flag.Int("port", 0, "Port to bind to")
	noBrowser := flag.Bool("no-browser", false, "Don't open the browser on startup")
	defaultBackend := "tmux"
	if runtime.GOOS == "windows" {
		defaultBackend = "pty"
	}
	backendFlag := flag.String("backend", defaultBackend, "Terminal backend: pty or tmux")
	flag.Parse()

	cfg := config.Load(*homeDir)
	setupCrashLogging(cfg.CoralDir())

	// Warn if typing "coral" would run a different program. An abandoned PyPI
	// package ships the same six binary names, uses the same ~/.coral directory
	// and the same port, and pip --user installs ahead of us on a default PATH.
	// Silently running the wrong product looks exactly like running this one.
	if w := startup.CheckPATHShadowing(); w != nil {
		log.Printf("[WARNING] 'coral' on your PATH is NOT this program.")
		log.Printf("[WARNING]   typing 'coral' runs: %s", w.OnPath)
		log.Printf("[WARNING]   you are running:     %s", w.Running)
		log.Printf("[WARNING] Another program of the same name comes earlier on your PATH.")
		log.Printf("[WARNING] Check with: command -v coral")
	}
	// Point tracking at this instance's data directory before anything can read
	// it. The package default is ~/.coral, so a late call here would let the
	// production install's state be read by an instance run with --home.
	tracking.SetCoralDir(cfg.CoralDir())

	// Resolve license variant name for logging (no feature gating).
	variantName := ""
	if cfg.LicenseRequired() {
		lm := license.NewManager(cfg.CoralDir())
		variantName = lm.VariantName()
	}

	log.Printf("[STARTUP] build tier=%s eula=%v license=%v demo_limits=%v variant=%q max_teams=%d max_agents=%d",
		config.TierName, config.EULARequired(), cfg.LicenseRequired(), config.DemoLimitsEnforced(), variantName,
		cfg.MaxLiveTeams, cfg.MaxLiveAgents)

	if *host != "" {
		cfg.Host = *host
	}
	if *port != 0 {
		cfg.Port = *port
	}

	// Check EULA acceptance (terminal prompt on first launch)
	if config.EULARequired() && !license.CheckAndPromptEULA(license.TerminalEULADialog) {
		fmt.Fprintln(os.Stderr, "Terms of Service must be accepted to use Coral.")
		os.Exit(0)
	}

	// Ignore SIGHUP — macOS sends it during sleep/wake transitions and when
	// the controlling terminal closes. Without this, the default Go behavior
	// kills the process.
	signal.Ignore(syscall.SIGHUP)

	// Graceful shutdown on SIGINT/SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rs, err := startup.Start(ctx, cfg, startup.Options{
		BackendType: *backendFlag,
	})
	if err != nil {
		log.Fatalf("Failed to start: %v", err)
	}
	defer rs.Close()

	// Anonymous install/upgrade tracking (non-blocking)
	tracking.TrackInstallAsync()

	// Check for updates on startup (non-blocking, skip for license-free builds)
	if config.Version != "" && !config.TierSkipLicense {
		go func() {
			time.Sleep(5 * time.Second)
			latest := routes.FetchLatestVersion()
			if latest != "" && latest != config.Version {
				log.Printf("[UPDATE] New version available: v%s (you have v%s) — %s", latest, config.Version, "https://github.com/cdknorow/coral/releases")
			}
		}()
	}

	// Print dashboard URL to stdout so it's visible in the terminal
	fmt.Printf("\n  Coral dashboard: http://localhost:%d\n", cfg.Port)
	fmt.Printf("  Press Ctrl+C to stop\n\n")

	// Open browser unless --no-browser
	if !*noBrowser {
		go func() {
			time.Sleep(500 * time.Millisecond)
			executil.OpenBrowser(fmt.Sprintf("http://localhost:%d", cfg.Port))
		}()
	}

	<-ctx.Done()
	fmt.Println() // newline after ^C
	log.Println("[SHUTDOWN] shutting down...")
	rs.Shutdown(10 * time.Second)
	log.Println("[SHUTDOWN] done")
}

func mustExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown: " + err.Error()
	}
	return exe
}
