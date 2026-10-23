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

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/loginlink"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/startup"
	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/tmux"
	"github.com/subgentic/coral/internal/tracking"

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

	// The web server's instance lock before config.Load touches the data folder (#6388): a second launch-coral against a folder another server holds writes nothing there.
	var instance *startup.Instance
	if !*skipWeb {
		var lockErr error
		if instance, lockErr = startup.LockInstance(config.ResolveCoralDir("")); lockErr != nil {
			fmt.Fprintf(os.Stderr, "launch-coral: %v\n", lockErr)
			os.Exit(1)
		}
	}
	// Agent pipe logs go where the server looks for them: cfg.LogDir (the data dir's
	// logs folder for an explicit data dir, the temp dir otherwise).
	cfg := config.Load()
	logDir := strings.TrimRight(cfg.LogDir, "/")
	if logDir == "" {
		logDir = filepath.Join(cfg.CoralDir(), "logs") // never the shared temp dir (#3601, W17)
	}
	os.MkdirAll(logDir, 0o700) // agents' terminal output: this user only (#3601, W17)
	os.Chmod(logDir, 0o700)

	// Clean up old coral logs (only in a folder of our own, never the shared temp dir)
	cleanOldLogs(logDir)

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

	// The browser signs in with a fresh single-use link from the server started here (#3394).
	var mintLink func() (string, error)
	if !*skipWeb {
		rs := startWebServer(ctx, cfg, instance)
		mintLink = func() (string, error) { return rs.NewLoginLink(cfg.Port) }
	}

	// Open the browser
	if !*noBrowser && !isSSH() {
		go func() {
			defer safego.Recover("launch-coral/main:119")
			time.Sleep(time.Second)
			executil.OpenBrowser(loginlink.Or(fmt.Sprintf("http://localhost:%d", cfg.Port), mintLink))
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

// oldLogAge is how old an agent pipe log must be before launch-coral removes it: a server
// already running on the same data dir keeps its live agents' logs fresh, so they stay.
const oldLogAge = 24 * time.Hour

// cleanOldLogs removes the agent pipe logs in logDir that were last written more than
// oldLogAge ago. It never cleans the shared system temp dir: the default install keeps
// its live agents' logs there, and a launch from here must not delete another Coral's (#3259).
func cleanOldLogs(logDir string) {
	if sameDir(logDir, os.TempDir()) {
		return
	}
	pattern := filepath.Join(logDir, "*_coral_*.log")
	matches, _ := filepath.Glob(pattern)
	cutoff := time.Now().Add(-oldLogAge)
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(m)
		}
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
	protocolPath := background.FindProtocolMD()

	var sessionNames []string
	for _, dir := range worktrees {
		folderName := filepath.Base(dir)
		sessionID := uuid.New().String()
		sessionName := fmt.Sprintf("%s-%s", agentType, sessionID)
		logFile := filepath.Join(logDir, fmt.Sprintf("%s_coral_%s.log", agentType, sessionID))

		// Clear old log
		ptymanager.ResetAgentLog(logFile) // 0600, never through a link (#3601, W17)

		// Build the launch command first: its prompt and settings files must be written
		// before a session is made for it (#3601 W12).
		cmd, err := ag.LaunchCommand(agent.LaunchParams{
			SessionID:    sessionID,
			ProtocolPath: protocolPath,
			WorkingDir:   dir,
		})
		if err != nil {
			log.Printf("Warning: not launching %s: %v", sessionName, err)
			continue
		}
		launchCmd := agent.WrapWithBundlePath(cmd)

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

		// Send the launch command
		if err := tc.SendKeysToTarget(ctx, terminalinput.Internal, sessionName+".0", launchCmd); err != nil {
			fmt.Fprintf(os.Stderr, "warning: the start command could not be typed into %s: %v\n", sessionName, err) // the terminal stays at its shell (#5171)
		}

		// Open a terminal window attached to this session
		if !isSSH() {
			openAgentTerminal(tc, sessionName, fmt.Sprintf("%s - %s", folderName, agentType))
		}

		fmt.Printf("  [+] Session : %s\n", sessionName)
		fmt.Printf("      Dir     : %s\n", dir)
		fmt.Printf("      Log     : %s\n", logFile)
		fmt.Printf("      Attach  : %s\n\n", tc.AttachCommand(sessionName))

		sessionNames = append(sessionNames, sessionName)
	}

	return sessionNames, nil
}

func startWebServer(ctx context.Context, cfg *config.Config, instance *startup.Instance) *startup.RunningServer {
	tracking.SetEntrypoint("launch-coral")
	tracking.SetCoralDir(cfg.CoralDir())
	rs, err := startup.Start(ctx, cfg, startup.Options{
		Instance: instance,
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
		defer safego.Recover("launch-coral/main:278")
		<-ctx.Done()
		rs.Shutdown(10 * time.Second)
		rs.Close()
	}()
	return rs
}

func isSSH() bool {
	return os.Getenv("SSH_CONNECTION") != ""
}

func openAgentTerminal(tc *tmux.Client, session, title string) {
	cmds := terminalCommands(runtime.GOOS, tc.AttachCommand(session), session, title, exec.LookPath)
	if len(cmds) == 0 {
		fmt.Printf("  [~] No supported terminal emulator found (use: %s)\n", tc.AttachCommand(session))
		return
	}
	if runtime.GOOS == "darwin" {
		// iTerm2 first, then Terminal.app.
		for _, argv := range cmds {
			if exec.Command(argv[0], argv[1:]...).Run() == nil {
				return
			}
		}
		return
	}
	exec.Command(cmds[0][0], cmds[0][1:]...).Start()
}

// terminalCommands builds the commands that open a terminal window attached to session, in
// the order to try them (#3588). The title comes from a folder name, which is anything the
// target folder holds (a cloned repo's directories), so no script or command line is ever
// built from it: osascript gets the attach command and the title as arguments ('on run
// argv'), and every title is reduced to [A-Za-z0-9 _.-] first.
func terminalCommands(goos, attachCmd, session, title string, lookPath func(string) (string, error)) [][]string {
	title = safeTitle(title)
	switch goos {
	case "darwin":
		return [][]string{
			{"osascript",
				"-e", "on run argv",
				"-e", `tell application "iTerm2" to create window with default profile command (item 1 of argv)`,
				"-e", "end run",
				attachCmd},
			{"osascript",
				"-e", "on run argv",
				"-e", `tell application "Terminal"`,
				"-e", "do script (item 1 of argv)",
				"-e", "set custom title of front window to (item 2 of argv)",
				"-e", "end tell",
				"-e", "end run",
				attachCmd, title},
		}
	case "windows":
		echo := "echo Attached to session " + safeText(session)
		if wtPath, err := lookPath("wt.exe"); err == nil {
			return [][]string{{wtPath, "--title", title, "cmd", "/k", echo}}
		}
		// cmd re-parses this line: the title is safe text with a space in it, so it is quoted
		// and start takes it as the window title, never as a program.
		return [][]string{{"cmd.exe", "/c", "start", title, "cmd", "/k", echo}}
	default:
		for _, term := range []string{"x-terminal-emulator", "gnome-terminal", "konsole", "xfce4-terminal"} {
			if path, err := lookPath(term); err == nil {
				return [][]string{{path, "-e", attachCmd}}
			}
		}
		return nil
	}
}

// safeTitle is the window title: "Coral " and the safe text of s, so it is never empty and
// always has a space.
func safeTitle(s string) string {
	return "Coral " + safeText(s)
}

// safeText keeps letters, digits, space, '_', '.' and '-' (anything else becomes '_'), at
// most 80 of them.
func safeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 80 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ' ', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// sameDir reports whether a and b name the same directory (symlinks resolved when they exist).
func sameDir(a, b string) bool {
	resolve := func(p string) string {
		p = filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	return resolve(a) == resolve(b)
}
