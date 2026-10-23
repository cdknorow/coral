// Package background — launcher.go provides the agent launch function
// used by the scheduler to spawn agents in tmux sessions.
package background

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/store"
)

// AgentLauncher creates agent sessions and launches agents.
type AgentLauncher struct {
	runtime AgentRuntime
	sessDB  *store.SessionStore
	logger  *slog.Logger
	port    int
	// host and coralDir say which Coral launched the agent: they go to its environment (CORAL_URL, CORAL_PORT, CORAL_DIR) so its hooks and coral-board reach THIS server, not whatever owns
	// :8420 (#6767: a scheduled agent's events went to the default port, so a server on any other port stored none).
	host     string
	coralDir string
}

// runTagger is a runtime whose agents carry a run tag (the tmux one): the agent's processes get CORAL_PTY_RUN=<tag> so a Kill, a sleep or a quit finds what they detached. The built-in terminal
// tags its own processes, so a tag exported by the launch line would hide them from it.
type runTagger interface{ TagsRuns() bool }

// newRunTag is a fresh random tag for an agent about to start on a tagging runtime, "" otherwise or when none can be made.
func (l *AgentLauncher) newRunTag() string {
	if t, ok := l.runtime.(runTagger); !ok || !t.TagsRuns() {
		return ""
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		slog.Warn("could not make a tag for a scheduled agent: its detached helpers will not be found at Kill", "error", err)
		return ""
	}
	return hex.EncodeToString(b)
}

// SetServer tells the launcher the host agents dial and the data folder of this Coral (the startup passes cfg.AgentHost() and cfg.CoralDir()).
func (l *AgentLauncher) SetServer(host, coralDir string) {
	l.host, l.coralDir = host, coralDir
}

// NewAgentLauncher creates a new AgentLauncher.
func NewAgentLauncher(runtime AgentRuntime, sessDB *store.SessionStore, port int) *AgentLauncher {
	return &AgentLauncher{
		runtime: runtime,
		sessDB:  sessDB,
		logger:  slog.Default().With("service", "agent_launcher"),
		port:    port,
	}
}

// LaunchResult contains the result of launching an agent.
type LaunchResult struct {
	SessionID   string
	SessionName string
	LogFile     string
	WorkingDir  string
}

// LaunchAgent creates a tmux session and launches an agent.
// This is the core function that the scheduler's launchFn should call.
func (l *AgentLauncher) LaunchAgent(ctx context.Context, workingDir, agentType, displayName string,
	resumeSessionID string, flags []string, isJob bool,
	prompt, boardName, boardServer string) (*LaunchResult, error) {

	// A scheduled job's or a caller's agent type names the log file and the CLI (#3602 W13).
	if err := agent.ValidateAgentType(agentType); err != nil {
		return nil, err
	}
	// Not given (an older scheduled job, a workflow agent step): Claude, as GetAgent("") and the
	// dashboard's launch path do. Defaulted before any name or log path is built, so the log
	// path (naming.LogFile refuses an empty type) is always made (Security's #3602 question).
	if agentType == "" {
		agentType = at.Claude
	}
	workingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}
	if info, err := os.Stat(workingDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("directory not found: %s", workingDir)
	}

	folderName := filepath.Base(workingDir)
	logDir := naming.DefaultLogDir()
	sessionID := uuid.New().String()
	sessionName := naming.SessionName(agentType, sessionID)
	logFile := naming.LogFile(logDir, agentType, sessionID)

	isTerminal := agentType == at.Terminal
	ag := agent.GetAgent(agentType)

	// If resuming, let the agent prepare (e.g. copy session files)
	if resumeSessionID != "" && !isTerminal {
		agent.TryPrepareResume(ag, resumeSessionID, workingDir)
	}

	// Clear old log
	if err := ptymanager.ResetAgentLog(logFile); err != nil { // 0600, never through a link (#3601, W17)
		slog.Warn("failed to clear agent log file", "path", logsafe.Dir(logFile), "error", err)
	}

	// Build the launch command (empty for terminal sessions)
	var launchCmd, runTag string
	if !isTerminal {
		protocolPath := FindProtocolMD()
		// Settings > CLI paths applies to job and scheduled runs like a dashboard
		// launch: the macOS app started from Finder has no Homebrew PATH (#3205).
		var settings map[string]string
		if l.sessDB != nil {
			// A read that fails fails the launch: without the settings the command would run the
			// CLI found on PATH instead of the configured one (#4350). The context is not the
			// caller's cancellation either.
			st, err := l.sessDB.GetSettingsForCLI(ctx)
			if err != nil {
				slog.Error("reading settings for the agent CLI path failed", "error", err)
				return nil, fmt.Errorf("reading Coral's settings for the agent CLI path: %w", err)
			}
			settings = st
		}
		// A configured cli_path_<agent> that can't be run refuses the launch; it never runs another CLI (#115).
		cliPath, err := agent.LaunchCLI(settings, agentType)
		if err != nil {
			return nil, err
		}
		runTag = l.newRunTag()
		launchCmd, err = ag.LaunchCommand(agent.LaunchParams{
			SessionID:       sessionID,
			SessionName:     sessionName, // CORAL_SESSION_NAME: the agent's hooks and coral-board name their session by it
			CoralHost:       l.host,
			CoralPort:       l.port,
			CoralDir:        l.coralDir,
			RunTag:          runTag,
			ProtocolPath:    protocolPath,
			ResumeSessionID: resumeSessionID,
			Flags:           flags,
			WorkingDir:      workingDir,
			CLIPath:         cliPath,
		})
		if err != nil {
			return nil, fmt.Errorf("launch command: %w", err)
		}
		// Ensure coral-hook-* binaries are reachable from app bundles
		launchCmd = agent.WrapWithBundlePath(agent.WrapWithCLINode(launchCmd, cliPath)) // an npm-installed CLI runs with its own node (#5899)
	}

	// Spawn agent session via the runtime
	if resumeSessionID != "" {
		slog.Info("resuming agent", "session_id", sessionID, "agent_type", agentType, "agent_name", folderName, "resume_from", resumeSessionID)
	} else {
		slog.Info("spawning agent", "session_id", sessionID, "agent_type", agentType, "agent_name", folderName, "workdir", logsafe.Dir(workingDir))
	}
	if err := l.runtime.SpawnAgent(ctx, sessionName, workingDir, logFile, launchCmd); err != nil {
		return nil, fmt.Errorf("spawn agent failed: %w", err)
	}

	// Register the live session in the DB
	ls := &store.LiveSession{
		SessionID:  sessionID,
		AgentType:  agentType,
		AgentName:  folderName,
		WorkingDir: workingDir,
		IsJob:      boolToInt(isJob),
	}
	if displayName != "" {
		ls.DisplayName = &displayName
	}
	if resumeSessionID != "" {
		ls.ResumeFromID = &resumeSessionID
	}
	if len(flags) > 0 {
		flagsJSON, _ := json.Marshal(flags)
		s := string(flagsJSON)
		ls.Flags = &s
	}
	if prompt != "" {
		ls.Prompt = &prompt
	}
	if boardName != "" {
		ls.BoardName = &boardName
	}
	if boardServer != "" {
		ls.BoardServer = &boardServer
	}

	if l.sessDB != nil {
		if displayName != "" {
			l.sessDB.SetDisplayName(ctx, sessionID, displayName)
		}
		l.sessDB.RegisterLiveSession(ctx, ls)
		if runTag != "" {
			if err := l.sessDB.SetRunTagIfEmpty(ctx, sessionID, runTag); err != nil { // the tag a Kill looks up by session id
				slog.Warn("could not record a scheduled agent's run tag", "session_id", sessionID, "error", err)
			}
		}
		if !isTerminal {
			go func() {
				defer safego.Recover("background/launcher:recordCLIVersion")
				recordCLIVersion(l.sessDB, sessionID, agentType)
			}()
		}
	}

	l.logger.Info("launched agent",
		"session_id", sessionID[:8],
		"agent_type", agentType,
		"working_dir", logsafe.Dir(workingDir),
	)

	return &LaunchResult{
		SessionID:   sessionID,
		SessionName: sessionName,
		LogFile:     logFile,
		WorkingDir:  workingDir,
	}, nil
}

// promptStartWait bounds how long SendPrompt waits for the agent CLI to take the
// foreground of its terminal after the initial delay.
var promptStartWait = 30 * time.Second

// promptInitialDelay is the pause before SendPrompt first looks at the terminal, and promptPollEvery how often it looks until the CLI is in
// the foreground (variables so the tests need not wait real seconds, #4982).
var (
	promptInitialDelay = 3 * time.Second
	promptPollEvery    = 250 * time.Millisecond
)

// SendPrompt sends a prompt to an agent session after a delay for initialization.
// The CLI is typed into a shell at launch, so the prompt is sent only once the CLI
// is in the foreground: typed into the shell, it would run as a command (#3219).
func (l *AgentLauncher) SendPrompt(ctx context.Context, sessionID, sessionName, agentType, prompt string) error {
	// Wait for agent to initialize
	select {
	case <-time.After(promptInitialDelay):
	case <-ctx.Done():
		return ctx.Err()
	}
	for deadline := time.Now().Add(promptStartWait); ; {
		exited, _ := ptymanager.AgentExited(ctx, l.runtime, sessionName, agentType, sessionID)
		if !exited {
			break
		}
		if time.Now().After(deadline) {
			l.logger.Warn("agent CLI is not running in its terminal; prompt not sent", "session", sessionID[:8])
			return nil
		}
		select {
		case <-time.After(promptPollEvery):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Through the automated-input gate (#3431), and only once the agent shows
	// its normal input box: a freshly started CLI may be on its trust-this-folder
	// screen or a login or model picker, and the prompt plus Enter would answer
	// it. Held until the user deals with that screen.
	if _, err := deliverAutomated(ctx, l.runtime, l.logger, sessionName, agentType, sessionID, "initial-prompt", prompt, "initial prompt", ptymanager.InputBoxKnown(agentType), false); err != nil {
		l.logger.Warn("prompt send failed", "session", sessionID[:8], "error", err)
	}

	return nil
}

// startPromptDelivery sends prompt in the background and returns a channel that closes when the
// delivery is over. It runs on ctx, so cancelling ctx (the run ended) ends it too.
func (l *AgentLauncher) startPromptDelivery(ctx context.Context, sessionID, sessionName, agentType, prompt string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer safego.Recover("background/launcher:startPromptDelivery")
		defer close(done)
		_ = l.SendPrompt(ctx, sessionID, sessionName, agentType, prompt)
	}()
	return done
}

// BuildSchedulerLaunchFn returns a function suitable for scheduler.SetLaunchFn.
// It integrates agent launching with the scheduler's job/run lifecycle.
func (l *AgentLauncher) BuildSchedulerLaunchFn(schedStore *store.ScheduleStore) func(ctx context.Context, job store.ScheduledJob, runID int64) error {
	return func(ctx context.Context, job store.ScheduledJob, runID int64) error {
		flagsList := strings.Fields(job.Flags)

		result, err := l.LaunchAgent(ctx, job.RepoPath, job.AgentType, job.Name,
			"", flagsList, true, job.Prompt, "", "")
		if err != nil {
			return err
		}

		// Update the run record with the session_id
		schedStore.UpdateScheduledRun(ctx, runID, map[string]interface{}{
			"session_id": result.SessionID,
		})

		// Register auto-accept if applicable (checked by the run config)
		// Note: auto-accept is set per-run in FireOneshot, not per-job

		// Send prompt to the agent after delay, on the run's own context: when the run ends the
		// delivery (and an initial prompt held for a screen nobody answers) ends with it (#3999).
		l.startPromptDelivery(ctx, result.SessionID, result.SessionName, job.AgentType, job.Prompt)

		// Monitor the tmux session — return when agent finishes or context expires
		return l.watchSession(ctx, result.SessionName)
	}
}

// watchSession polls for session existence until it exits or context is cancelled.
func (l *AgentLauncher) watchSession(ctx context.Context, sessionName string) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !l.runtime.IsAlive(context.Background(), sessionName) {
				return nil // Session finished
			}
		}
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// findProtocolMD locates PROTOCOL.md relative to the running binary or source.
func FindProtocolMD() string {
	// Check next to the executable
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "PROTOCOL.md")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// The current folder only in a dev build (running from the source tree): a release server
	// started from a repository would otherwise give every agent that repository's
	// PROTOCOL.md as its system prompt (#3601, weak site W16).
	if config.TierName != "dev" {
		return ""
	}
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

// recordCLIVersion records which CLI version a launched agent runs (#3481). It asks
// the CLI (--version, cached per binary), so it runs after the launch, not before.
func recordCLIVersion(ss *store.SessionStore, sessionID, agentType string) {
	ctx := context.Background()
	settings, err := ss.GetSettingsForCLI(ctx)
	if err != nil { // without the settings the version would be asked of the CLI on PATH, not the configured one (#4350)
		slog.Warn("could not read settings; the agent's CLI version is not recorded", "session_id", sessionID, "error", err)
		return
	}
	if v := agent.CLIVersion(settings, agentType); v != "" {
		if err := ss.SetCLIVersion(ctx, sessionID, v); err != nil {
			slog.Warn("could not record the agent's CLI version", "session_id", sessionID, "error", err)
		}
	}
}
