// Command coral-tray runs the Coral dashboard as a macOS/Windows/Linux system tray app.
//
// Usage:
//
//	coral-tray                     # Spawn a background tray process and exit
//	coral-tray --foreground        # Run in foreground (used internally by the background spawn)
//	coral-tray --stop              # Stop a running tray instance
//	coral-tray --port 9000         # Use a custom port
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/executil"
	"github.com/cdknorow/coral/internal/startup"
	"github.com/cdknorow/coral/internal/tracking"
	"github.com/gen2brain/beeep"
)

//go:embed icon.png
var iconData []byte

const (
	githubReleasesAPI = "https://api.github.com/repos/cdknorow/coral/releases/latest"
	githubReleasesURL = "https://github.com/cdknorow/coral/releases"
)

var version = "dev"

func main() {
	// macOS requires Cocoa calls on the main thread
	runtime.LockOSThread()

	host := flag.String("host", "0.0.0.0", "Host to bind to")
	port := flag.Int("port", 8420, "Port to bind to")
	homeDir := flag.String("home", "", "Home directory for Coral")
	foreground := flag.Bool("foreground", false, "Run in foreground (used internally)")
	stop := flag.Bool("stop", false, "Stop a running tray instance")
	noBrowser := flag.Bool("no-browser", false, "Don't open the browser on startup")
	devMode := flag.Bool("dev", false, "Development mode: skip license check")
	debugMode := flag.Bool("debug", false, "Enable debug logging to ~/.coral/tray.log")
	backendFlag := flag.String("backend", "tmux", "Terminal backend: pty or tmux")
	flag.Parse()

	// When launched from a .app bundle on macOS, run in foreground automatically
	// (macOS expects the main process to keep running)
	if !*foreground && runtime.GOOS == "darwin" && isInsideAppBundle() {
		*foreground = true
	}

	dataDir := getDataDir()

	// Handle --stop
	if *stop {
		pid := readPID(dataDir)
		if pid > 0 {
			if err := signalProcess(pid, syscall.SIGTERM); err != nil {
				fmt.Printf("Failed to stop Coral tray (PID %d): %v\n", pid, err)
			} else {
				fmt.Printf("Stopped Coral tray (PID %d)\n", pid)
			}
		} else {
			fmt.Println("No running Coral tray found.")
		}
		return
	}

	// If --foreground, run directly (this is the detached child)
	if *foreground {
		if *homeDir != "" {
			os.MkdirAll(*homeDir, 0755)
			os.Chdir(*homeDir)
		}
		runForeground(*host, *port, *noBrowser, *devMode, *debugMode, *backendFlag, dataDir)
		return
	}

	// Check if already running
	pid := readPID(dataDir)
	if pid > 0 {
		fmt.Printf("Coral tray is already running (PID %d). Use --stop to stop it.\n", pid)
		return
	}

	// Resolve home directory
	home := *homeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	// Spawn ourselves as a detached background process
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("Cannot find executable: %v", err)
	}
	args := []string{"--foreground", "--host", *host, "--port", strconv.Itoa(*port), "--home", home, "--backend", *backendFlag}
	if *noBrowser {
		args = append(args, "--no-browser")
	}
	if *devMode {
		args = append(args, "--dev")
	}
	if *debugMode {
		args = append(args, "--debug")
	}
	cmd := exec.Command(exe, args...)

	logFile := filepath.Join(dataDir, "tray.log")
	lf, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Cannot open log file: %v", err)
	}
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.Stdin = nil
	cmd.SysProcAttr = detachProcessAttrs()

	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to start background process: %v", err)
	}
	lf.Close() // Child has inherited the FD; parent no longer needs it.

	fmt.Printf("Coral tray started in background (dashboard on port %d)\n", *port)
	fmt.Printf("  Home: %s\n", home)
	fmt.Printf("  Logs: %s\n", logFile)
	fmt.Printf("  Stop: coral-tray --stop\n")
}

func runForeground(host string, port int, noBrowser, devMode, debugMode bool, backendType, dataDir string) {
	// Setup log file FIRST — when launched from .app there's no terminal,
	// so log.Fatalf messages would disappear silently without this.
	logFile := filepath.Join(dataDir, "tray.log")
	if lf, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		log.SetOutput(lf)
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

		// Redirect OS-level stderr (fd 2) to the log file. CGO crashes
		// (systray, Cocoa) write to stderr, not Go's log package. Without
		// this, those crash messages vanish when running as a .app bundle
		// or background process with no terminal attached.
		redirectStderr(lf)
		// lf intentionally NOT closed — log.SetOutput and dup2'd stderr
		// need the FD open for the process lifetime. OS cleans up on exit.
	}

	// Global panic recovery — log the full stack trace before exiting
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[FATAL] panic in coral-tray: %v\n%s", r, debug.Stack())
			os.Exit(1)
		}
	}()

	log.Printf("[STARTUP] coral-tray starting pid=%d go=%s os=%s arch=%s",
		os.Getpid(), runtime.Version(), runtime.GOOS, runtime.GOARCH)

	if debugMode {
		os.Setenv("CORAL_DEBUG", "1")
		log.Println("Debug mode enabled")
	}

	// Write PID
	writePID(dataDir)
	defer removePID(dataDir)

	// Ignore SIGHUP — macOS sends it during sleep/wake transitions and when
	// the controlling terminal closes. Without this, the default Go behavior
	// kills the process.
	signal.Ignore(syscall.SIGHUP)

	// Catch SIGABRT — CGO/Mach exception crashes on macOS send SIGABRT
	// before terminating. Log the stack trace so we have post-mortem data.
	abrtCh := make(chan os.Signal, 1)
	signal.Notify(abrtCh, syscall.SIGABRT)
	go func() {
		<-abrtCh
		log.Printf("[FATAL] received SIGABRT (possible CGO/Mach crash)")
		log.Printf("[FATAL] goroutines: %d", runtime.NumGoroutine())
		log.Printf("[FATAL] stack trace:\n%s", debug.Stack())
		os.Exit(1)
	}()

	// Setup signal handling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start the HTTP server and all background services
	cfg := config.Load()
	cfg.Host = host
	cfg.Port = port
	rs, err := startup.Start(ctx, cfg, startup.Options{
		BackendType: backendType,
		PasswordPrompt: func() (string, error) {
			return nativeDatabasePassword()
		},
		UnlockSurface: func() string {
			if runtime.GOOS == "darwin" {
				return "native"
			}
			return "headless"
		}(),
		OnServerError: func(err error) {
			log.Printf("Server error: %v", err)
			beeep.Notify("Coral", "Server failed to start: "+err.Error(), "")
		},
	})
	if err != nil {
		log.Printf("Failed to start: %v", err)
		beeep.Notify("Coral", "Failed to start: "+err.Error(), "")
		// Still launch tray so user sees the error
		systray.Run(func() {
			systray.SetTemplateIcon(iconData, iconData)
			systray.SetTooltip("Coral — Error")
			mQuit := systray.AddMenuItem("Quit", "Exit Coral")
			go func() { <-mQuit.ClickedCh; systray.Quit() }()
		}, func() {})
		return
	}
	defer rs.Close()
	httpServer := rs.HTTPServer

	// Heartbeat — write timestamp+PID to ~/.coral/heartbeat every 10s.
	// This is the only way to detect SIGKILL (which can't be caught).
	// Post-mortem, the last heartbeat timestamp shows when the process died.
	// Also logs resource usage every 5 minutes to help diagnose memory
	// pressure kills (macOS Jetsam).
	go func() {
		heartbeatPath := filepath.Join(dataDir, "heartbeat")
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		resourceLogCounter := 0
		writeHeartbeat := func() {
			data := fmt.Sprintf("%d %d\n", time.Now().Unix(), os.Getpid())
			os.WriteFile(heartbeatPath, []byte(data), 0644)

			// Log resource usage every ~5 minutes (30 ticks × 10s).
			// ReadMemStats is a stop-the-world call — only run it here.
			resourceLogCounter++
			if resourceLogCounter%30 == 0 {
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				log.Printf("[HEALTH] goroutines=%d heap_alloc=%dMB sys=%dMB",
					runtime.NumGoroutine(), m.HeapAlloc/1024/1024, m.Sys/1024/1024)
			}
		}
		writeHeartbeat() // Write immediately on start
		for {
			select {
			case <-ctx.Done():
				os.Remove(heartbeatPath)
				return
			case <-ticker.C:
				writeHeartbeat()
			}
		}
	}()

	// Open dashboard (try native app first, fall back to browser)
	if !noBrowser {
		go func() {
			time.Sleep(time.Second)
			dashURL := fmt.Sprintf("http://localhost:%d", port)
			if err := launchCoralApp(dashURL); err != nil {
				executil.OpenBrowser(dashURL)
			}
		}()
	}

	// Check for updates in background
	go checkForUpdatesOnStartup(port)

	url := fmt.Sprintf("http://localhost:%d", port)

	// Kill any orphaned coral-app from previous sessions
	killOrphanedCoralApp()

	// Anonymous install/upgrade tracking (non-blocking)
	tracking.SetCoralDir(dataDir)
	tracking.TrackInstallAsync()

	// Run systray (blocks until quit)
	systray.Run(func() {
		systray.SetTemplateIcon(iconData, iconData)
		systray.SetTitle("")
		systray.SetTooltip("Coral Dashboard")

		// Set activation policy to Accessory (not Prohibited from LSUIElement).
		// This raises Jetsam priority so macOS is less likely to kill us under
		// memory pressure, while still hiding from the Dock.
		hideTrayFromDock()

		systray.AddSeparator() // workaround: macOS clips the first item on some configurations
		mOpenApp := systray.AddMenuItem("Open in App", "Open Coral in native window")
		mOpenBrowser := systray.AddMenuItem("Open in Browser", "Open Coral in web browser")
		mUpdate := systray.AddMenuItem("Check for Updates", "Check for new versions")
		systray.AddSeparator()
		mShutdown := systray.AddMenuItem("Shutdown — Kill Agents & Stop Server", "Kill all agents and stop the server")
		mQuit := systray.AddMenuItem("Quit — Exit Coral", "Exit the tray app")

		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[CRASH] systray event loop panicked: %v\n%s", r, debug.Stack())
				}
			}()
			for {
				select {
				case <-mOpenApp.ClickedCh:
					go launchCoralApp(url)
				case <-mOpenBrowser.ClickedCh:
					executil.OpenBrowser(url)
				case <-mUpdate.ClickedCh:
					go checkForUpdates()
				case <-mShutdown.ClickedCh:
					go func() {
						killed := killAllAgents(url)
						killCoralApp()
						shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						httpServer.Shutdown(shutdownCtx)
						beeep.Notify("Coral", fmt.Sprintf("Shut down %d agent(s) and dashboard server.", killed), "")
						systray.Quit()
					}()
				case <-mQuit.ClickedCh:
					killCoralApp()
					func() {
						shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						httpServer.Shutdown(shutdownCtx)
					}()
					systray.Quit()
				case <-ctx.Done():
					func() {
						shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						httpServer.Shutdown(shutdownCtx)
					}()
					systray.Quit()
					return
				}
			}
		}()
	}, func() {
		// onExit — kill coral-app subprocess and stop server
		killCoralApp()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
		log.Println("coral-tray exited")
	})
}

// killAllAgents calls the REST API to kill all running agent sessions.
func killAllAgents(baseURL string) int {
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(baseURL + "/api/sessions/live")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var sessions []map[string]any
	if err := json.Unmarshal(body, &sessions); err != nil {
		return 0
	}

	killed := 0
	for _, s := range sessions {
		name, _ := s["name"].(string)
		if name == "" {
			continue
		}
		payload := map[string]any{
			"session_id": s["session_id"],
			"agent_type": s["agent_type"],
		}
		data, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST",
			fmt.Sprintf("%s/api/sessions/live/%s/kill", baseURL, name),
			strings.NewReader(string(data)))
		req.Header.Set("Content-Type", "application/json")
		if r, err := client.Do(req); err == nil {
			r.Body.Close()
			killed++
		}
	}
	return killed
}

// checkForUpdatesOnStartup checks for updates silently on startup.
func checkForUpdatesOnStartup(port int) {
	time.Sleep(5 * time.Second) // Wait for server to be ready
	latest := fetchLatestVersion()
	if latest != "" && latest != version && version != "dev" {
		beeep.Notify("Coral", fmt.Sprintf("Update available: v%s (you have v%s)", latest, version), "")
	}
}

// checkForUpdates checks for updates and notifies.
func checkForUpdates() {
	latest := fetchLatestVersion()
	if latest == "" {
		beeep.Notify("Coral", "Could not check for updates.", "")
		return
	}
	if latest != version && version != "dev" {
		beeep.Notify("Coral", fmt.Sprintf("Update available: v%s\nYou have v%s", latest, version), "")
		executil.OpenBrowser(githubReleasesURL)
	} else {
		beeep.Notify("Coral", "You're on the latest version.", "")
	}
}

// fetchLatestVersion queries GitHub for the latest release version.
func fetchLatestVersion() string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(githubReleasesAPI)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var data struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return ""
	}
	return strings.TrimPrefix(data.TagName, "v")
}

// PID file management

func getDataDir() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".coral")
	os.MkdirAll(dir, 0755)
	return dir
}

func writePID(dataDir string) {
	pidFile := filepath.Join(dataDir, "tray.pid")
	os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644)
}

func removePID(dataDir string) {
	os.Remove(filepath.Join(dataDir, "tray.pid"))
}

func readPID(dataDir string) int {
	data, err := os.ReadFile(filepath.Join(dataDir, "tray.pid"))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	// Check if process is alive
	if err := signalProcess(pid, 0); err != nil {
		removePID(dataDir)
		return 0
	}
	return pid
}

// coralAppProcess tracks the running coral-app subprocess.
// Protected by coralAppMu.
var (
	coralAppProcess *os.Process
	coralAppMu      sync.Mutex
)

// killCoralApp sends SIGTERM, waits briefly, then SIGKILL if still alive.
func killCoralApp() {
	coralAppMu.Lock()
	p := coralAppProcess
	coralAppProcess = nil
	coralAppMu.Unlock()

	if p == nil {
		return
	}
	p.Signal(syscall.SIGTERM)
	// Give WKWebView time to exit cleanly
	time.Sleep(500 * time.Millisecond)
	if err := p.Signal(syscall.Signal(0)); err == nil {
		// Still alive — force kill
		log.Println("coral-app did not exit after SIGTERM, sending SIGKILL")
		p.Kill()
	}
}

// killOrphanedCoralApp finds and kills any stale coral-app processes from previous sessions.
func killOrphanedCoralApp() {
	out, err := exec.Command("pgrep", "-x", "coral-app").Output()
	if err != nil || len(out) == 0 {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid == os.Getpid() {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			log.Printf("killing orphaned coral-app (PID %d)", pid)
			proc.Signal(syscall.SIGTERM)
		}
	}
}

// launchCoralApp launches coral-app or brings the existing one to front.
// Returns an error if coral-app is not found or fails to start.
func launchCoralApp(url string) error {
	coralAppMu.Lock()
	defer coralAppMu.Unlock()

	// If already running, bring it to front
	if coralAppProcess != nil {
		// Check if still alive (signal 0 = no-op, just checks existence)
		if err := coralAppProcess.Signal(syscall.Signal(0)); err == nil {
			raiseCoralApp()
			return nil
		}
		// Process exited — clear the reference
		coralAppProcess = nil
	}

	// Find the coral-app binary
	appPath := findCoralApp()
	if appPath == "" {
		return fmt.Errorf("coral-app not found")
	}

	cmd := exec.Command(appPath, "--url", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	coralAppProcess = cmd.Process

	// Reap the process in background and clear the reference when it exits
	go func() {
		cmd.Wait()
		coralAppMu.Lock()
		if coralAppProcess == cmd.Process {
			coralAppProcess = nil
			log.Println("coral-app process exited")
		}
		coralAppMu.Unlock()
	}()

	return nil
}

// findCoralApp looks for the coral-app binary next to the executable, then in PATH.
// On macOS, it resolves symlinks to handle App Translocation, where the OS
// moves quarantined apps to a random /private/var/folders path at runtime.
func findCoralApp() string {
	exe, err := os.Executable()
	if err == nil {
		// Resolve symlinks to handle macOS App Translocation
		resolved, resolveErr := filepath.EvalSymlinks(exe)
		if resolveErr == nil {
			if resolved != exe {
				log.Printf("[WARN] App Translocation detected: exe=%s resolved=%s", exe, resolved)
			}
			exe = resolved
		}
		candidate := filepath.Join(filepath.Dir(exe), "coral-app")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if p, err := exec.LookPath("coral-app"); err == nil {
		return p
	}
	return ""
}

// raiseCoralApp brings the running coral-app window to the front.
// Caller must hold coralAppMu.
func raiseCoralApp() {
	if coralAppProcess == nil {
		return
	}
	if runtime.GOOS == "darwin" {
		script := fmt.Sprintf(
			`tell application "System Events" to set frontmost of (first process whose unix id is %d) to true`,
			coralAppProcess.Pid,
		)
		exec.Command("osascript", "-e", script).Run()
	}
}

// isInsideAppBundle detects if the binary is running inside a macOS .app bundle
// by checking if the executable path contains ".app/Contents/MacOS/".
// Resolves symlinks to handle App Translocation.
func isInsideAppBundle() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return strings.Contains(exe, ".app/Contents/MacOS/")
}
