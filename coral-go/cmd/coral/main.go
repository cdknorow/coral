// Command coral starts the Coral dashboard web server.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/term"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/dbcrypt"
	"github.com/cdknorow/coral/internal/executil"
	"github.com/cdknorow/coral/internal/license"
	"github.com/cdknorow/coral/internal/logexit"
	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/startup"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tracking"
)

const coralDevStartupMarker = "DEV-TMUX-DIAGNOSTIC-2026-06-03-02"

// setupCrashLogging logs to <coralDir>/coral.log and copies each line to the
// terminal (stderr) when running interactively. The file comes first and the
// terminal copy never blocks: see logOutput (#3226). Exits go through logexit,
// which flushes the terminal copy first so the last lines are not lost.
func setupCrashLogging(coralDir string) {
	os.MkdirAll(coralDir, 0755)
	logFile := filepath.Join(coralDir, "coral.log")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	out, terminal := logOutput(f, os.Stderr)
	log.SetOutput(out)
	logexit.SetFlush(terminal.Flush)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
}

func main() {
	if code, ok := subcommand(os.Args[1:]); ok {
		os.Exit(code)
	}
	// Runs last on a normal return: the terminal gets "[SHUTDOWN] done".
	defer logexit.Flush()

	// Global panic recovery — log the full stack trace before exiting
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[FATAL] panic in main: %v\n%s", r, debug.Stack())
			logexit.Exit(1)
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
	remoteAccess := flag.Bool("remote", false, "Allow remote (non-loopback) connections without requiring the saved setting")
	hubMode := flag.Bool("hub", false, "Run as a multi-server hub: manage and view remote Coral servers (also CORAL_HUB=1)")
	cloudURL := flag.String("cloud-url", "", "Coral Hub origin this server may connect to (also CORAL_CLOUD_URL). Release builds default to https://app.coralai.ai; \"\" or off disables it. Nothing is sent until you connect")
	debugInput := flag.Bool("debug-input", false, "Record every write Coral makes to agent terminals in <data dir>/input-debug.log (debug; may contain secrets). Also CORAL_DEBUG_INPUT=1 or the Settings toggle")
	bootstrapFile := flag.String("bootstrap-file", "", "One-time Coral Hub credential file to enroll this server with at startup (owned by you, mode 0600, removed after reading). On Linux /run/coral/bootstrap.d/credential is read without the flag")
	proofFile := flag.String("proof-file", "", "Optional proof file to send with the bootstrap credential (at most 16 KiB of JSON from a provider helper)")
	cloudConnect := flag.String("cloud-connect", cloudConnectPrompt, "With --cloud-url/CORAL_CLOUD_URL on a server not yet connected: prompt (connect in the terminal at startup; without a terminal, log the link and code), auto (only log them, never prompt) or off")
	tlsCert := flag.String("tls-cert", os.Getenv("CORAL_TLS_CERT"), "PEM certificate file: serve HTTPS (and WSS) on the port, and accept only HTTPS from other computers. Needs --tls-key (also CORAL_TLS_CERT)")
	tlsKey := flag.String("tls-key", os.Getenv("CORAL_TLS_KEY"), "PEM private key file for --tls-cert (also CORAL_TLS_KEY)")
	selfTest := flag.Bool("encryption-self-test", false, "Run an isolated encrypted database round-trip and exit")
	flag.Parse()
	if *selfTest {
		if err := runEncryptionSelfTest(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("encrypted database self-test passed")
		return
	}

	cfg := config.Load(*homeDir)
	cfg.HubMode = *hubMode || envTruthy(os.Getenv("CORAL_HUB"))
	// --cloud-url wins over CORAL_CLOUD_URL and the tier default, and --cloud-url "" turns it off.
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "cloud-url" {
			cfg.CloudURL = config.CloudURLSetting(*cloudURL, true)
		}
	})
	cfg.BootstrapFile, cfg.ProofFile = *bootstrapFile, *proofFile
	setupCrashLogging(cfg.CoralDir())
	cloudMode, err := cloudStartupMode(*cloudConnect, explicitCloudURL(*cloudURL), isTerminal(os.Stdin) && isTerminal(os.Stdout))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

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
	tracking.SetEntrypoint("coral")
	tracking.SetCoralDir(cfg.CoralDir())

	log.Printf("[STARTUP] build tier=%s eula=%v", config.TierName, config.EULARequired())

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

	// Graceful shutdown on SIGINT/SIGTERM. While the Connect to Coral Hub step
	// waits at the terminal, Ctrl-C ends only that step (interruptRouter).
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	interrupts := newInterruptRouter(stop)

	// Without a terminal on stdin (a service, or a shell that started Coral in the
	// background) a prompt would block startup in read(0) forever. Skip the remote
	// access question then: remote access stays off and nothing is saved, so the
	// next interactive start still asks.
	var remotePrompt func() bool
	if term.IsTerminal(int(os.Stdin.Fd())) {
		tlsOn := strings.TrimSpace(*tlsCert) != "" && strings.TrimSpace(*tlsKey) != ""
		remotePrompt = func() bool { return promptRemoteAccess(os.Stdin, os.Stdout, tlsOn) }
	}
	rs, err := startup.Start(ctx, cfg, startup.Options{
		BackendType:        *backendFlag,
		PasswordPrompt:     promptDatabasePassword,
		UnlockSurface:      "tty",
		RemoteAccess:       *remoteAccess,
		RemoteAccessPrompt: remotePrompt,
		DebugInput:         *debugInput,
		TLSCert:            *tlsCert,
		TLSKey:             *tlsKey,
	})
	if err != nil {
		logexit.Fatalf("Failed to start: %v", err)
	}
	defer rs.Close()

	// Anonymous install/upgrade tracking (non-blocking)
	tracking.TrackInstallAsync()

	// Check for updates on startup (non-blocking; release tiers only)
	if config.Version != "" && config.TierChecksForUpdates {
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
	if cfg.TLS {
		// Other devices use https; this computer keeps plain http (#3355).
		fmt.Printf("  Other devices: https://<this computer>:%d (TLS)\n", cfg.Port)
	}
	fmt.Printf("  Press Ctrl+C to stop\n\n")

	// Open browser unless --no-browser. Always http://localhost: it works on this
	// computer with TLS on too, without a warning for a self-signed certificate.
	if !*noBrowser {
		go func() {
			time.Sleep(500 * time.Millisecond)
			executil.OpenBrowser(fmt.Sprintf("http://localhost:%d", cfg.Port))
		}()
	}

	// Connect to Coral Hub, when asked for and not connected yet (#3285).
	runCloudStartupStep(ctx, cfg, cloudMode, interrupts, os.Stdin, os.Stdout)

	<-ctx.Done()
	fmt.Println() // newline after ^C
	log.Println("[SHUTDOWN] shutting down...")
	rs.Shutdown(10 * time.Second)
	log.Println("[SHUTDOWN] done")
}

func runEncryptionSelfTest() error {
	if !dbcrypt.FeatureAvailable() {
		return fmt.Errorf("database encryption is not included in this standard build; experimental encryption requires a build with CGO_ENABLED=1 and -tags sqlcipher,fts5")
	}
	dir, err := os.MkdirTemp("", "coral-encryption-self-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	key := "self-test-key-material-2026"
	path := filepath.Join(dir, "sessions.db")
	db, err := store.OpenWithKey(context.Background(), path, key)
	if err != nil {
		return fmt.Errorf("open encrypted test database: %w", err)
	}
	if _, err := db.Exec("INSERT INTO user_settings(key,value) VALUES('self_test','ok')"); err != nil {
		db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	if encrypted, err := dbcrypt.EncryptedFile(path); err != nil || !encrypted {
		return fmt.Errorf("encrypted test database check failed: %v", err)
	}
	reopened, err := store.OpenWithKey(context.Background(), path, key)
	if err != nil {
		return fmt.Errorf("reopen encrypted test database: %w", err)
	}
	var value string
	if err := reopened.Get(&value, "SELECT value FROM user_settings WHERE key='self_test'"); err != nil {
		reopened.Close()
		return err
	}
	reopened.Close()
	if value != "ok" {
		return fmt.Errorf("encrypted test sentinel = %q", value)
	}
	if wrong, err := store.OpenWithKey(context.Background(), path, "wrong-self-test-key"); err == nil {
		wrong.Close()
		return fmt.Errorf("wrong key unexpectedly opened encrypted database")
	}
	boardPath := filepath.Join(dir, "messageboard.db")
	boardStore, err := board.NewStoreWithKey(boardPath, key)
	if err != nil {
		return fmt.Errorf("open encrypted board test database: %w", err)
	}
	if err := boardStore.EncryptionSelfTest(context.Background()); err != nil {
		boardStore.Close()
		return err
	}
	if err := boardStore.Close(); err != nil {
		return err
	}
	if reopenedBoard, err := board.NewStoreWithKey(boardPath, key); err != nil {
		return fmt.Errorf("reopen encrypted board test database: %w", err)
	} else if err := reopenedBoard.Close(); err != nil {
		return err
	}
	if wrongBoard, err := board.NewStoreWithKey(boardPath, "wrong-self-test-key"); err == nil {
		wrongBoard.Close()
		return fmt.Errorf("wrong key unexpectedly opened encrypted board database")
	}
	if encrypted, err := dbcrypt.EncryptedFile(boardPath); err != nil || !encrypted {
		return fmt.Errorf("encrypted board test check failed: %v", err)
	}
	return nil
}

// promptRemoteAccess asks once, on first launch, whether other devices may connect. It
// says plainly that without TLS the connection is unencrypted (#3355).
func promptRemoteAccess(in io.Reader, out io.Writer, tlsOn bool) bool {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Remote Access")
	fmt.Fprintln(out, "  ─────────────")
	fmt.Fprintln(out, "  Allow connections from other devices on your network (phones, tablets, etc.)?")
	fmt.Fprintln(out)
	if tlsOn {
		fmt.Fprintln(out, "  They connect over HTTPS (--tls-cert/--tls-key).")
	} else {
		fmt.Fprintln(out, "  Warning: remote access is plain HTTP, NOT encrypted. Your API key, login cookie")
		fmt.Fprintln(out, "  and everything typed into agent terminals cross the network readable by anyone")
		fmt.Fprintln(out, "  on it. For use beyond a network you trust, use Coral Hub (encrypted), a TLS")
		fmt.Fprintln(out, "  reverse proxy, or start Coral with --tls-cert and --tls-key.")
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Note: If you're running inside WSL2, you'll need to either enable remote")
	fmt.Fprintln(out, "  access here or set up port forwarding from Windows to reach the dashboard.")
	fmt.Fprintln(out)
	fmt.Fprint(out, "  Enable remote access? [y/N]: ")
	scanner := bufio.NewScanner(in)
	if scanner.Scan() {
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if answer == "y" || answer == "yes" {
			fmt.Fprintln(out, "  Remote access enabled. You can change this later in Settings > Privacy.")
			fmt.Fprintln(out)
			return true
		}
	}
	fmt.Fprintln(out, "  Remote access disabled. You can change this later in Settings > Privacy.")
	fmt.Fprintln(out)
	return false
}

func promptDatabasePassword() (string, error) {
	if !term.IsTerminal(int(syscall.Stdin)) {
		return "", errors.New("the database is password-protected and stdin is not a terminal: start Coral from a terminal to unlock it")
	}
	fmt.Fprint(os.Stderr, "Coral database password: ")
	password, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	return string(password), err
}

func mustExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown: " + err.Error()
	}
	return exe
}

// envTruthy reports whether an environment value turns a switch on.
func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
