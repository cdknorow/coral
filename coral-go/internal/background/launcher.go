// Package background — launcher.go provides the agent launch function
// used by the scheduler to spawn agents in tmux sessions.
package background

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/agent"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
	"github.com/google/uuid"
)

// AgentLauncher creates agent sessions and launches agents.
type AgentLauncher struct {
	runtime AgentRuntime
	sessDB  *store.SessionStore
	logger  *slog.Logger
	port    int // retained for constructor compatibility
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

	workingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}
	if info, err := os.Stat(workingDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("directory not found: %s", workingDir)
	}

	folderName := filepath.Base(workingDir)
	logDir := os.TempDir()
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
	if err := os.WriteFile(logFile, nil, 0644); err != nil {
		slog.Warn("failed to clear agent log file", "path", logFile, "error", err)
	}

	// Build the launch command (empty for terminal sessions)
	var launchCmd string
	if !isTerminal {
		protocolPath := findProtocolMD()
		launchCmd = ag.BuildLaunchCommand(agent.LaunchParams{
			SessionID:       sessionID,
			ProtocolPath:    protocolPath,
			ResumeSessionID: resumeSessionID,
			Flags:           flags,
			WorkingDir:      workingDir,
		})
		// Ensure coral-hook-* binaries are reachable from app bundles
		launchCmd = agent.WrapWithBundlePath(launchCmd)
	}

	// Spawn agent session via the runtime
	if resumeSessionID != "" {
		slog.Info("resuming agent", "session_id", sessionID, "agent_type", agentType, "agent_name", folderName, "resume_from", resumeSessionID)
	} else {
		slog.Info("spawning agent", "session_id", sessionID, "agent_type", agentType, "agent_name", folderName, "workdir", workingDir)
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
	}

	l.logger.Info("launched agent",
		"session_id", sessionID[:8],
		"agent_type", agentType,
		"working_dir", workingDir,
	)

	return &LaunchResult{
		SessionID:   sessionID,
		SessionName: sessionName,
		LogFile:     logFile,
		WorkingDir:  workingDir,
	}, nil
}

// SendPrompt sends a prompt to an agent session after a delay for initialization.
func (l *AgentLauncher) SendPrompt(ctx context.Context, sessionID, sessionName, agentType, prompt string) error {
	// Wait for agent to initialize
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}

	if err := l.runtime.SendInput(ctx, sessionName, prompt); err != nil {
		l.logger.Warn("prompt send failed", "session", sessionID[:8], "error", err)
	}

	return nil
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

		// Send prompt to the agent after delay
		go func() {
			sendCtx := context.Background()
			l.SendPrompt(sendCtx, result.SessionID, result.SessionName, job.AgentType, job.Prompt)
		}()

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
