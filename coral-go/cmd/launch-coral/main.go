// Command launch-coral discovers worktrees, launches agent tmux sessions,
// and starts the Coral web server. It is the Go equivalent of launch_agents.sh.
//
// Usage:
//
//	launch-coral [flags] [target-dir] [agent-type] [agents]
//
// Examples:
//
//	launch-coral .                     # Start web server only
//	launch-coral . claude agents       # Launch Claude agents + web server
//	launch-coral /path/to/root agy agents
package main

import (
	"context"
	"flag"
	"fmt"
	"golang.org/x/term"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/executil"
	"github.com/cdknorow/coral/internal/startup"
	"github.com/cdknorow/coral/internal/tmux"
	"github.com/cdknorow/coral/internal/tracking"

	"github.com/google/uuid"
)

const maxAgents = 5

func main() {
	portFlag := flag.Int("port", 0, "Override web server port (default: $CORAL_PORT or 8420)")
	skipWeb := flag.Bool("skip-web-server", false, "Skip launching the web server")
	noBrowser := flag.Bool("no-browser", false, "Don't open the browser on startup")
	flag.Parse()

	args := flag.Args()

	targetDir := "."
	agentType := "claude"
	launchAgents := false

	if len(args) >= 1 {
		targetDir = args[0]
	}
	if len(args) >= 2 {
		agentType = args[1]
	}
	if len(args) >= 3 && args[2] == "agents" {
		launchAgents = true
	}

	// Resolve target directory to absolute path
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		log.Fatalf("Error resolving target directory: %v", err)
	}
	targetDir = absTarget

	logDir := os.TempDir()
	logDir = strings.TrimRight(logDir, "/")

	// Clean up old coral logs
	cleanOldLogs(logDir)

	cfg := config.Load()
	if *portFlag > 0 {
		cfg.Port = *portFlag
	}
	cfg.CoralRoot = targetDir

	tc := tmux.NewClient()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var sessionNames []string

	if launchAgents {
		sessionNames, err = launchAgentSessions(ctx, tc, targetDir, agentType, logDir)
		if err != nil {
			log.Fatalf("Error launching agents: %v", err)
		}
	} else {
		fmt.Println("=== Coral Dashboard ===")
		fmt.Println("Starting web server only (launch agents from the dashboard).")
		fmt.Println("To also launch agents: launch-coral <path> <agent-type> agents")
		fmt.Println()
	}

	if !*skipWeb {
		startWebServer(ctx, cfg)
	}

	// Open the browser
	if !*noBrowser && !isSSH() {
		go func() {
			time.Sleep(time.Second)
			executil.OpenBrowser(fmt.Sprintf("http://localhost:%d", cfg.Port))
		}()
	}

	fmt.Println("=== All sessions launched ===")
	fmt.Println()
	if len(sessionNames) > 0 {
		fmt.Println("Quick attach commands:")
		for _, sn := range sessionNames {
			fmt.Printf("  %s\n", tc.AttachCommand(sn))
		}
		fmt.Println()
		fmt.Println("Kill all agents:")
		socketFlag := ""
		if tc.SocketPath != "" {
			socketFlag = fmt.Sprintf("-S %s ", tc.SocketPath)
		}
		fmt.Printf("  for sn in %s; do tmux %skill-session -t $sn 2>/dev/null; done\n",
			strings.Join(sessionNames, " "), socketFlag)
	}

	<-ctx.Done()
	log.Println("Shutting down...")
}

func cleanOldLogs(logDir string) {
	pattern := filepath.Join(logDir, "*_coral_*.log")
	matches, _ := filepath.Glob(pattern)
	for _, m := range matches {
		os.Remove(m)
	}
}

func launchAgentSessions(ctx context.Context, tc *tmux.Client, targetDir, agentType, logDir string) ([]string, error) {
	// Collect valid subdirectories (up to maxAgents)
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return nil, fmt.Errorf("cannot read target directory %s: %w", targetDir, err)
	}

	var worktrees []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		worktrees = append(worktrees, filepath.Join(targetDir, entry.Name()))
		if len(worktrees) >= maxAgents {
			break
		}
	}

	if len(worktrees) == 0 {
		return nil, fmt.Errorf("no subdirectories found in %s", targetDir)
	}

	fmt.Printf("=== %s Coral Launcher (Independent Sessions) ===\n", agentType)
	fmt.Printf("Target directory: %s\n", targetDir)
	fmt.Printf("Found %d workspace(s):\n\n", len(worktrees))

	ag := agent.GetAgent(agentType)

	// Locate PROTOCOL.md relative to the executable
	protocolPath := findProtocolMD()

	var sessionNames []string
	for _, dir := range worktrees {
		folderName := filepath.Base(dir)
		sessionID := uuid.New().String()
		sessionName := fmt.Sprintf("%s-%s", agentType, sessionID)
		logFile := filepath.Join(logDir, fmt.Sprintf("%s_coral_%s.log", agentType, sessionID))

		// Clear old log
		os.WriteFile(logFile, nil, 0644)

		// Create a new detached tmux session rooted in the worktree
		if err := tc.NewSession(ctx, sessionName, dir); err != nil {
			log.Printf("Warning: failed to create tmux session %s: %v", sessionName, err)
			continue
		}

		// Stream stdout to log file
		if err := tc.PipePane(ctx, sessionName, logFile); err != nil {
			log.Printf("Warning: failed to set up pipe-pane for %s: %v", sessionName, err)
		}

		// Set pane title using native tmux command (avoids shell echo issues)
		tc.SetPaneTitle(ctx, sessionName+".0", fmt.Sprintf("%s — %s", folderName, agentType))

		// Build and send the launch command
		launchCmd := agent.WrapWithBundlePath(ag.BuildLaunchCommand(agent.LaunchParams{
			SessionID:    sessionID,
			ProtocolPath: protocolPath,
			WorkingDir:   dir,
		}))
		tc.SendKeysToTarget(ctx, sessionName+".0", launchCmd)

		// Open a terminal window attached to this session
		if !isSSH() {
			openAgentTerminal(tc, sessionName, fmt.Sprintf("%s — %s", folderName, agentType))
		}

		fmt.Printf("  [+] Session : %s\n", sessionName)
		fmt.Printf("      Dir     : %s\n", dir)
		fmt.Printf("      Log     : %s\n", logFile)
		fmt.Printf("      Attach  : %s\n\n", tc.AttachCommand(sessionName))

		sessionNames = append(sessionNames, sessionName)
	}

	return sessionNames, nil
}

func startWebServer(ctx context.Context, cfg *config.Config) {
	tracking.SetEntrypoint("launch-coral")
	tracking.SetCoralDir(cfg.CoralDir())
	rs, err := startup.Start(ctx, cfg, startup.Options{
		BackendType: func() string {
			if runtime.GOOS == "windows" {
				return "pty"
			}
			return "tmux"
		}(),
		PasswordPrompt: func() (string, error) {
			fmt.Fprint(os.Stderr, "Coral database password: ")
			value, promptErr := term.ReadPassword(int(syscall.Stdin))
			fmt.Fprintln(os.Stderr)
			return string(value), promptErr
		},
		UnlockSurface: "tty",
	})
	if err != nil {
		log.Fatalf("Failed to start: %v", err)
	}
	// Startup has applied the saved privacy setting before analytics can run.
	tracking.TrackInstallAsync()

	go func() {
		<-ctx.Done()
		rs.Shutdown(10 * time.Second)
		rs.Close()
	}()
}

// findProtocolMD locates PROTOCOL.md relative to the running binary or source.
func findProtocolMD() string {
	// Check next to the executable
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "PROTOCOL.md")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Check common source locations
	for _, rel := range []string{
		"src/coral/PROTOCOL.md",
		"PROTOCOL.md",
	} {
		if _, err := os.Stat(rel); err == nil {
			abs, _ := filepath.Abs(rel)
			return abs
		}
	}

	return ""
}

func isSSH() bool {
	return os.Getenv("SSH_CONNECTION") != ""
}

func openAgentTerminal(tc *tmux.Client, session, title string) {
	attachCmd := tc.AttachCommand(session)
	switch runtime.GOOS {
	case "darwin":
		// macOS: try iTerm2 first, then Terminal.app
		script := fmt.Sprintf(`tell application "iTerm2"
    create window with default profile command "%s"
end tell`, attachCmd)
		cmd := exec.Command("osascript", "-e", script)
		if err := cmd.Run(); err == nil {
			return
		}
		script = fmt.Sprintf(`tell application "Terminal"
    do script "%s"
    set custom title of front window to "%s"
end tell`, attachCmd, title)
		exec.Command("osascript", "-e", script).Run()

	case "windows":
		// Windows: try Windows Terminal first, then conhost
		if wtPath, err := exec.LookPath("wt.exe"); err == nil {
			exec.Command(wtPath, "--title", title, "cmd", "/k",
				fmt.Sprintf("echo Attached to session %s", session)).Start()
			return
		}
		exec.Command("cmd.exe", "/c", "start", title, "cmd", "/k",
			fmt.Sprintf("echo Attached to session %s", session)).Start()

	default:
		// Linux: try terminal emulators
		for _, term := range []string{"x-terminal-emulator", "gnome-terminal", "konsole", "xfce4-terminal"} {
			if path, err := exec.LookPath(term); err == nil {
				cmd := exec.Command(path, "-e", attachCmd)
				cmd.Start()
				return
			}
		}
		fmt.Printf("  [~] No supported terminal emulator found (use: %s)\n", attachCmd)
	}
}
