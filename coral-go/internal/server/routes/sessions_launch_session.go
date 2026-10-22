// Starting one agent session: the CLI, the backend and the command (moved out of sessions_launch.go, #5003; no behaviour change).

package routes

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentmodels"
	"github.com/subgentic/coral/internal/agentsignin"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/logsafe"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/proxy"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safefs"
	"github.com/subgentic/coral/internal/safego"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/tmux"
	"github.com/subgentic/coral/internal/tracking"
)

// errLaunchSettings is what a launch answers when Coral's settings could not be read.
var errLaunchSettings = errors.New("Coral's settings could not be read, so the agent was not started (it would have run the CLI found on PATH instead of the one set in Settings > CLI paths); try again")

// launchSettings reads the settings a launch builds its command from: the CLI path, the default
// model and permission mode. It reads with a context a client's hang-up cannot cancel (a team launch
// loops over its agents inside one request: when the request ended partway, every later agent read
// no settings and started the CLI found on PATH, the real one, instead of the configured one), and
// a read that fails fails the launch: it never falls back to PATH (#4350).
func (h *SessionsHandler) launchSettings(ctx context.Context) (map[string]string, error) {
	s, err := h.ss.GetSettingsForCLI(ctx)
	if err != nil || s == nil {
		slog.Error("reading settings for a launch failed", "error", err)
		return nil, errLaunchSettings
	}
	return s, nil
}

// launchSpec is one agent to launch.
type launchSpec struct {
	WorkDir, AgentType, DisplayName string
	ResumeSessionID                 string // a conversation to continue ("" for a new one)
	Flags                           []string
	Prompt                          string
	BoardName, BoardServer          string
	Backend                         string // the launch path asked for: "pty", "tmux" or "" (launchPath)
	BoardType, Model                string
	Thinking                        string // the thinking level to start with: launchSession gives the CLI its flag (#4969)
	Capabilities                    *agent.Capabilities
	Tools                           []string
	Hooks                           map[string]interface{}
	PermissionMode                  string // effective permission mode if already decided (team reset, etc.) (#5188)
	// AutoByDefault: this is a new launch that may start Claude in Auto when it names no permission mode (#5836). Set only by the launch and the team launch, and only when
	// autoDefaultAllowed (a Coral Hub request the owner does not let skip prompts is not). A restart, resume, wake or team reset restores a session and never sets it.
	AutoByDefault bool
}

// launchSession creates a new agent session using the specified backend (tmux or pty).
func (h *SessionsHandler) launchSession(ctx context.Context, spec launchSpec) (_ map[string]any, retErr error) {
	if msg := launchDirError(spec.WorkDir); msg != "" {
		return nil, errors.New(msg)
	}
	absDir, _ := filepath.Abs(spec.WorkDir)
	agentType, displayName := spec.AgentType, spec.DisplayName
	if agentType == "" {
		agentType = at.Claude
	}
	// A role another running agent already holds on this board gets a numbered name
	// ("Backend Dev 2"), so the two never share a subscription, mentions or read state
	// (#3543). On this Coral's boards only; a remote board's Coral decides its own.
	if spec.BoardName != "" && (spec.BoardServer == "" || spec.BoardServer == "local") {
		displayName = h.uniqueBoardRole(ctx, spec.BoardName, displayName, agentType)
		picked := naming.SubscriberID(displayName, agentType)
		defer func() {
			if retErr != nil {
				h.releaseRoleReservation(spec.BoardName, picked) // the launch failed: the name is free again
			}
		}()
	}
	if err := validateAgentModel(agentType, spec.Model); err != nil {
		return nil, err
	}
	if !agentmodels.ValidThinking(agentType, spec.Thinking) {
		return nil, errors.New(errInvalidThinking(agentType))
	}
	if spec.Thinking != "" { // the level is the CLI's one --effort: a stored or given one is replaced, never doubled
		spec.Flags = append(agentmodels.StripThinkingFlags(spec.Flags), agentmodels.ThinkingArgs(agentType, spec.Thinking)...)
	}
	userSettings, err := h.launchSettings(ctx)
	if err != nil {
		return nil, err
	}
	cliPath, err := resolveLaunchCLI(agentType, agent.ResolveCLIPath(userSettings, agentType))
	if err != nil {
		return nil, err
	}
	if err := agent.ValidateAgentType(agentType); err != nil {
		return nil, err
	}
	backend := h.launchPath(spec.Backend)
	folderName := filepath.Base(absDir)
	sessionID := h.uuid()
	sessionName := naming.SessionName(agentType, sessionID)
	logFile := naming.LogFile(h.cfg.LogDir, agentType, sessionID)

	isTerminal := agentType == at.Terminal
	agentImpl := agent.GetAgent(agentType)
	if spec.ResumeSessionID != "" && !isTerminal {
		agent.TryPrepareResume(agentImpl, spec.ResumeSessionID, absDir)
	}
	launchParams := agent.LaunchParams{
		SessionID:       sessionID,
		SessionName:     sessionName,
		ProtocolPath:    h.protocolPath(),
		ResumeSessionID: spec.ResumeSessionID,
		Flags:           spec.Flags,
		WorkingDir:      absDir,
		BoardName:       spec.BoardName,
		BoardLabel:      h.boardLabel(spec.BoardName),
		Role:            naming.SubscriberID(displayName, agentType),
		Prompt:          spec.Prompt,
		PromptOverrides: promptOverrides(userSettings),
		BoardType:       spec.BoardType,
		Capabilities:    spec.Capabilities,
		Tools:           spec.Tools,
		Hooks:           spec.Hooks,
		CLIPath:         cliPath,
		PermissionMode: func() string {
			if spec.PermissionMode != "" {
				return spec.PermissionMode
			}
			if m := agent.PermissionModeFromFlags(spec.Flags); m != "" {
				return m
			}
			if def := setreg.Enum(userSettings, "default_permission_mode"); def != "" {
				return def
			}
			if spec.AutoByDefault {
				return agent.DefaultLaunchPermissionMode(agentType) // Claude: Auto (#5836)
			}
			return "default"
		}(),
		CoralDir:  h.cfg.CoralDir(),
		CoralHost: h.cfg.AgentHost(),
		CoralPort: h.cfg.Port,
	}
	if cliPath != "" {
		log.Printf("[launch] using custom CLI path: %s", logsafe.Path(cliPath))
	}

	// The launch command is built before any terminal is made: when its prompt or settings
	// files can't be written, the launch fails with nothing left behind (#3601 W12).
	var cmd string
	if !isTerminal {
		if cmd, err = launchCommand(agentImpl, launchParams); err != nil {
			return nil, err
		}
	}
	if backend == "pty" && h.backend != nil {
		if logFile, err = h.startOnBackend(ctx, spec.BoardName, sessionName, agentType, absDir, sessionID, cmd); err != nil {
			return nil, err
		}
	} else {
		backend = "tmux" // normalize if pty requested but no backend available
		if err := h.startOnTmux(ctx, launchParams, spec.BoardName, sessionName, agentType, absDir, folderName, logFile, cmd); err != nil {
			return nil, err
		}
	}

	// Capture shell PID for process-tree-based identity resolution.
	// Must happen after CreateSession (tmux session exists) but the pane PID
	// is available immediately — it's the shell spawned by tmux new-session.
	shellPID, pidErr := launchedSessionPID(ctx, backend, h.backend, h.terminal, sessionName)
	if pidErr != nil {
		log.Printf("[launch] failed to capture PID for session %s: %v", sessionName, pidErr)
	} else if shellPID > 0 {
		log.Printf("[launch] captured PID %d for session %s", shellPID, sessionName)
	}

	// Register in DB
	h.chatMeta.forget(sessionID, sessionName, folderName)
	h.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:     sessionID,
		AgentType:     agentType,
		AgentName:     folderName,
		WorkingDir:    absDir,
		DisplayName:   ptr.NonEmpty(displayName),
		ResumeFromID:  ptr.NonEmpty(spec.ResumeSessionID),
		Flags:         store.MarshalFlags(spec.Flags),
		Prompt:        ptr.NonEmpty(spec.Prompt),
		BoardName:     ptr.NonEmpty(spec.BoardName),
		BoardServer:   ptr.NonEmpty(spec.BoardServer),
		Backend:       ptr.NonEmpty(backend),
		BoardType:     ptr.NonEmpty(spec.BoardType),
		Capabilities:  store.MarshalCapabilities(spec.Capabilities),
		Model:         ptr.NonEmpty(spec.Model),
		Thinking:      ptr.NonEmpty(spec.Thinking),
		ContextWindow: proxy.LookupContextWindow(spec.Model),
		Tools:         store.MarshalCapabilities(spec.Tools),
		PID:           shellPID,
		PermissionMode: func() *string {
			if launchParams.PermissionMode != "" && agent.ValidPermissionMode(launchParams.PermissionMode) {
				return &launchParams.PermissionMode
			}
			return nil
		}(),
	})
	h.noteLaunchedHooks(ctx, sessionID, agentType) // #3665
	if agentType == at.Claude {
		probeSignInOnce(cliPath, absDir) // the first launch of this Claude asks once whether it is signed in (#5838)
	}
	if !isTerminal {
		go func() {
			defer safego.Recover("server/routes/sessions_launch:h.recordCLIVersion")
			h.recordCLIVersion(sessionID, agentType, userSettings)
		}() // #3481
	}
	if displayName != "" {
		h.ss.SetDisplayName(ctx, sessionID, displayName)
	}
	return map[string]any{
		"ok": true, "session_id": sessionID, "session_name": sessionName,
		"agent_type": agentType, "working_dir": absDir,
		"display_name": displayName, // numbered when the role was taken on the board (#3543)
		"log_file":     logFile, "launch_path": backend,
		// launch_path names the launch path (the stored label, live sessions' launch_path) and reads as "pty" even for a tmux session: it means "through the
		// TerminalBackend". terminal names what the session actually runs on. It was called "backend" until #6146, which read as the terminal.
		"terminal": terminalKind(h.backend),
	}, nil
}

// signInProbed holds the Claude CLIs (by path) whose sign-in was asked about after a launch in this process.
var signInProbed sync.Map

// signInProbe is the one-time sign-in question after a launch (a variable so a test replaces it): the same non-interactive status check the background jobs make (#5469), whose answer
// /api/system/status then reports as claude_signed_in, so the launch card can say "Claude is not signed in" and what to do next on a fresh install, where no job has asked yet.
var signInProbe = func(ctx context.Context, bin, dir string) { _ = agentsignin.RequireIn(ctx, at.Claude, bin, dir) }

// probeSignInOnce asks, once per Claude CLI per process and in the background (a launch never waits for it), whether Claude is signed in (#5838). The check never signs in and
// never starts a terminal; an answer it cannot get changes nothing.
func probeSignInOnce(bin, dir string) {
	if bin == "" {
		return
	}
	if _, asked := signInProbed.LoadOrStore(bin, struct{}{}); asked {
		return
	}
	go func() {
		defer safego.Recover("server/routes/sessions_launch_session:signInProbe")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		signInProbe(ctx, bin, dir)
	}()
}

// resolveLaunchCLI is the CLI an agent of agentType starts with: cliPath (Settings > CLI paths), else its binary
// found on PATH or, failing that, in the common install places. A CLI found nowhere is an error naming how to
// install it. A plain terminal has none.
func resolveLaunchCLI(agentType, cliPath string) (string, error) {
	if agentType == at.Terminal {
		return cliPath, nil
	}
	if cliPath != "" {
		// A configured path that can't be run is an error, never another CLI found on PATH or in the common places (#115); the one
		// that runs is the resolved one, as for a restart (an extensionless claude is its claude.cmd).
		return agent.FindCLIForLaunch(map[string]string{agent.CLIPathSettingKey(agentType): cliPath}, agentType)
	}
	checkBin := ""
	if info := agent.GetCLIInfo(agentType); info != nil {
		checkBin = info.Binary
	}
	if checkBin == "" {
		return cliPath, nil
	}
	resolved, err := exec.LookPath(checkBin)
	if err == nil {
		if cliPath == "" {
			cliPath = resolved
		}
		return cliPath, nil
	}
	// LookPath failed — try common install locations
	if found := agent.FindCLIInCommonPaths(checkBin); found != "" {
		log.Printf("[launch] %s not on PATH, found at %s", logsafe.Path(checkBin), logsafe.Path(found))
		return found, nil
	}
	installCmd, installURL := "", ""
	if info := agent.GetCLIInfo(agentType); info != nil {
		installCmd, installURL = info.InstallCommand, info.InstallURL
	}
	return "", &agent.CLINotFoundError{AgentType: agentType, Binary: checkBin, InstallCommand: installCmd, InstallURL: installURL}
}

// launchPath is the launch path for a requested one.
//
// NOTE: this value selects the LAUNCH PATH, not the terminal a session ends
// up on. "pty" means "go through the TerminalBackend abstraction", which is
// how tmux-backed sessions are launched too — h.backend is a TmuxBackend in
// that case. It is persisted and drives the same branch on wake, so it must
// keep meaning exactly this. Use terminalKind for what actually runs.
// When the server is using a PTY backend, always use PTY regardless of
// what the frontend requests — tmux is not available on this platform.
func (h *SessionsHandler) launchPath(requested string) string {
	if _, isPTY := h.backend.(*ptymanager.PTYBackend); isPTY {
		return "pty"
	}
	if requested != "" {
		return requested
	}
	if h.backend != nil {
		return "pty"
	}
	return "tmux"
}

// startOnBackend starts the agent through the TerminalBackend: a shell first (empty command), then the agent's
// start command typed into it once the shell is ready. This matches the tmux pattern and works cross-platform:
// the shell interprets bash syntax like $(cat ...) correctly. It returns the log file the backend keeps.
func (h *SessionsHandler) startOnBackend(ctx context.Context, team, sessionName, agentType, dir, sessionID, cmd string) (string, error) {
	if err := h.backend.Spawn(sessionName, agentType, dir, sessionID, "", 200, 50); err != nil {
		return "", spawnError(err)
	}
	logFile := h.backend.LogPath(sessionName) // PTY backend manages its own log file
	if cmd == "" {
		return logFile, nil
	}
	log.Printf("[launch] pty session=%s agent=%s cmd=%s", sessionName, agentType, cmd)
	if pb, ok := h.backend.(*ptymanager.PTYBackend); ok {
		if !pb.WaitReady(sessionName, 5*time.Second) {
			log.Printf("[launch] pty shell not ready after 5s, sending anyway: %s", sessionName)
		}
	} else {
		time.Sleep(500 * time.Millisecond)
	}
	if err := h.startAgentCLI(ctx, team, sessionName, cmd); err != nil {
		return "", fmt.Errorf("pty launch command failed: %w", err)
	}
	return logFile, nil
}

// startOnTmux starts the agent in a new tmux session: the session (tmux checked first, on the real tmux
// terminal), Coral's environment in it, its log and title, then the start command (none for a plain terminal).
func (h *SessionsHandler) startOnTmux(ctx context.Context, params agent.LaunchParams, team, sessionName, agentType, dir, folderName, logFile, cmd string) error {
	// Only check real tmux availability when the terminal is the real
	// tmux terminal — tests inject a mock that should bypass this.
	tmuxTerm, isTmux := h.terminal.(*ptymanager.TmuxSessionTerminal)
	if isTmux {
		tmuxPath, ok := tmux.IsAvailable()
		slog.Info("launch tmux availability",
			"available", ok,
			"resolved_path", logsafe.Path(tmuxPath),
			"client_tmux_bin_before", logsafe.Path(tmuxTerm.Client().TmuxBin),
			"socket", logsafe.Path(tmuxTerm.Client().SocketPath),
		)
		if !ok {
			// What was missing and where it was looked for, without the PATH itself (it is logged once, at server start, with the home folder as "~").
			logTmuxNotFound()
			return errors.New(tmux.MissingMessage())
		}
		if tmuxPath != "" && tmuxTerm.Client().TmuxBin != tmuxPath {
			slog.Info("launch updating tmux client binary",
				"from", logsafe.Path(tmuxTerm.Client().TmuxBin),
				"to", logsafe.Path(tmuxPath),
			)
			tmuxTerm.Client().TmuxBin = tmuxPath
		}
	}

	ptymanager.ResetAgentLog(logFile) // 0600, never through a link (#3601, W17)
	slog.Info("launch creating tmux session",
		"session_name", sessionName,
		"working_dir", logsafe.Dir(dir),
		"terminal_type", fmt.Sprintf("%T", h.terminal),
	)
	if err := h.terminal.CreateSession(ctx, sessionName, dir); err != nil {
		slog.Error("launch tmux CreateSession failed",
			"session_name", sessionName,
			"working_dir", logsafe.Dir(dir),
			"terminal_type", fmt.Sprintf("%T", h.terminal),
			"error", err,
		)
		return fmt.Errorf("tmux new-session failed: %w", err)
	}
	// Put the Coral environment into the tmux session so that anything the
	// user runs by hand in that pane — coral-board especially — reaches the
	// server that launched the agent. Same source as the agent's own env.
	if isTmux {
		for _, kv := range agent.CoralEnv(params) {
			if err := tmuxTerm.Client().SetEnvironment(ctx, sessionName, kv[0], kv[1]); err != nil {
				log.Printf("[launch] failed to set %s for %s: %v", kv[0], sessionName, err)
			}
		}
		// Prepend Coral tools dir to PATH so coral-board and hooks are
		// discoverable by subshells Claude spawns (e.g. Bash tool).
		if binDir := agent.CoralToolsDir(); binDir != "" {
			currentPath := os.Getenv("PATH")
			if !strings.Contains(currentPath, binDir) {
				currentPath = binDir + ":" + currentPath
			}
			if err := tmuxTerm.Client().SetEnvironment(ctx, sessionName, "PATH", currentPath); err != nil {
				log.Printf("[launch] failed to set PATH for %s: %v", sessionName, err)
			}
		}
	}
	h.terminal.StartLogging(ctx, sessionName, logFile)
	// Set pane title using native tmux command (avoids shell echo)
	h.terminal.SetPaneTitle(ctx, sessionName+".0", fmt.Sprintf("%s — %s", folderName, agentType))
	if agentType == at.Terminal {
		return nil
	}
	log.Printf("[launch] tmux session=%s agent=%s cmd=%s", sessionName, agentType, cmd)
	return h.startAgentCLI(ctx, team, sessionName+".0", cmd)
}

// validateAgentModel rejects only known Coral model IDs whose provider is
// incompatible with the selected agent implementation. Unknown IDs remain
// valid for custom/provider-specific configurations.
func validateAgentModel(agentType, model string) error {
	provider, known := proxy.KnownModelProvider(model)
	if !known {
		return nil
	}
	var expected string
	switch agentType {
	case at.Claude:
		expected = "anthropic"
	case at.Codex:
		expected = "openai"
	case at.Agy, at.Antigravity, at.Gemini:
		expected = "google"
	default:
		return nil
	}
	if provider != expected {
		return fmt.Errorf("model %q belongs to %s provider and cannot launch with %s agent; choose a compatible model or leave custom IDs unchanged", model, provider, agentType)
	}
	return nil
}

// launchedSessionPID resolves the process identifier from what actually owns
// the launched session. The persisted backend label describes the launch path
// and can be "pty" even when h.backend is a TmuxBackend, so it must not be the
// sole condition for PID capture.
func launchedSessionPID(ctx context.Context, backend string, terminalBackend ptymanager.TerminalBackend, terminal ptymanager.SessionTerminal, sessionName string) (int, error) {
	if provider, ok := terminalBackend.(ptymanager.SessionPIDProvider); ok {
		return provider.SessionPID(ctx, sessionName)
	}
	if backend == "tmux" {
		if tmuxTerm, ok := terminal.(*ptymanager.TmuxSessionTerminal); ok {
			return tmuxTerm.Client().GetPanePID(ctx, sessionName)
		}
	}
	return 0, nil
}

// terminalKind names the terminal a session actually runs on, as opposed to
// the launch path taken to get there.
func terminalKind(b ptymanager.TerminalBackend) string {
	switch b.(type) {
	case *ptymanager.PTYBackend:
		return "pty"
	case *ptymanager.TmuxBackend:
		return "tmux"
	case nil:
		return "tmux"
	}
	return "unknown"
}

// launchBackendLabel reports the terminal backend for analytics as a closed
// enum: the requested one when valid, otherwise the one this server runs.
func (h *SessionsHandler) launchBackendLabel(requested string) string {
	switch requested {
	case "tmux", "pty":
		return requested
	}
	switch h.terminal.(type) {
	case *ptymanager.TmuxSessionTerminal:
		return "tmux"
	case *ptymanager.PTYSessionTerminal:
		return "pty"
	}
	return "unknown"
}

// classifyLaunchError maps a launch error to a controlled analytics category.
// It inspects the message only to choose a category; the text is never sent.
func classifyLaunchError(err error) string {
	if err == nil {
		return ""
	}
	// A configured CLI path that can't be run (#115's agent.CLIPathError) is a setup problem like a missing CLI. It is recognised by the text it
	// has for an answer through Coral Hub rather than by name, so this compiles with or without that type.
	var forHub interface{ HubText() string }
	if errors.As(err, &forHub) {
		return tracking.FailProviderUnrunnable
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "cli not found"):
		return tracking.FailProviderMissing
	case strings.Contains(msg, "tmux is required") || strings.Contains(msg, "terminal backend"):
		return tracking.FailBackendUnavailable
	case strings.Contains(msg, "directory not found") || strings.Contains(msg, "no such file") || strings.Contains(msg, "not a directory") || strings.Contains(msg, "working directory"):
		return tracking.FailWorkdirUnavailable
	case strings.Contains(msg, "spawn failed") || strings.Contains(msg, "launch command failed") || strings.Contains(msg, "launch command delivery") || strings.Contains(msg, "new-session failed"):
		return tracking.FailSpawnFailed
	}
	return tracking.FailInternal
}

// launchDirError is the error for a launch working directory that is not an
// existing directory, or "" when it is usable.
func launchDirError(dir string) string {
	// A network path is refused before anything stats it: on Windows the stat alone connects to
	// the host as the user (#3671).
	if safefs.RefuseRemote(dir) != nil {
		return fmt.Sprintf("a network path can't be a working folder: %s", dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil || !isDir(abs) {
		return fmt.Sprintf("directory not found: %s", dir)
	}
	// Windows: CreateProcess refuses a working folder of 259+ characters; say so here (the dialog marks the field) instead of a 500 from the spawn.
	if _, err := executil.StartDir(abs); err != nil {
		return err.Error()
	}
	return ""
}

// launchCommand is the agent's launch command with the bundled tools on PATH. It fails when
// the agent's prompt, settings or instructions files can't be written to the temp folder: the
// launch fails with that error instead of starting an agent without them (#3601 W12).
func launchCommand(ag agent.Agent, p agent.LaunchParams) (string, error) {
	cmd, err := ag.LaunchCommand(p)
	if err != nil {
		return "", err
	}
	return agent.WrapWithBundlePath(agent.WrapWithCLINode(cmd, p.CLIPath)), nil // an npm-installed CLI runs with its own node (#5899)
}

// logTmuxNotFound says what a launch could not find and where it looked, without the PATH itself: that is logged once, at server start, with the home folder as "~" (#6018).
func logTmuxNotFound() {
	slog.Warn("launch: tmux not found", "binary", "tmux", "searched", "the PATH logged at server start, then the common tmux locations and a login shell", "path_entries", logsafe.Count())
}

// tmuxAvailable is a variable so a test can stand in for a computer without tmux.
var tmuxAvailable = func() bool { _, ok := tmux.IsAvailable(); return ok }

// spawnError is a failed terminal start as the launch says it: when tmux is what is missing, that and the one command for this computer (#6046), not "pty spawn failed: tmux new-session: exec: not found".
func spawnError(err error) error {
	// Only a failure of the tmux backend is blamed on a missing tmux: the pure pty backend never runs tmux, and its start failures keep their own words.
	if errors.Is(err, ptymanager.ErrTmuxBackend) && !tmuxAvailable() {
		return errors.New(tmux.MissingMessage())
	}
	return fmt.Errorf("pty spawn failed: %w", err)
}
