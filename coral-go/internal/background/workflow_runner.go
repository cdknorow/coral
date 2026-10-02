// Package background — workflow_runner.go provides the workflow execution engine.
// It runs multi-step workflows (shell commands + agent prompts) sequentially,
// capturing output, managing timeouts, and tracking progress in the database.
package background

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/store"
)

// StepDef represents a single step definition parsed from the workflow's steps_json.
type StepDef struct {
	Name              string           `json:"name"`
	Type              string           `json:"type"` // "shell" or "agent"
	Command           string           `json:"command,omitempty"`
	Prompt            string           `json:"prompt,omitempty"`
	TimeoutS          int              `json:"timeout_s,omitempty"`
	ContinueOnFailure bool             `json:"continue_on_failure,omitempty"`
	Agent             *AgentStepConfig `json:"agent,omitempty"`
	Connections       []string         `json:"connections,omitempty"`
	Interactive       bool             `json:"interactive,omitempty"`
	OutputArtifact    string           `json:"output_artifact,omitempty"`
	Hooks             json.RawMessage  `json:"hooks,omitempty"` // per-step hooks (Claude-native events + Coral events)
}

// AgentStepConfig holds the agent configuration for an agent step.
type AgentStepConfig struct {
	AgentType    string          `json:"agent_type,omitempty"`
	Model        string          `json:"model,omitempty"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
	Tools        []string        `json:"tools,omitempty"`
	MCPServers   json.RawMessage `json:"mcpServers,omitempty"`
	Flags        []string        `json:"flags,omitempty"`
}

// StepResult tracks the outcome of a single step execution.
type StepResult struct {
	Index       int      `json:"index"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Status      string   `json:"status"` // pending, running, completed, failed, skipped
	ExitCode    *int     `json:"exit_code,omitempty"`
	OutputTail  string   `json:"output_tail,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`
	SessionName string   `json:"session_name,omitempty"`
	Files       []string `json:"files"`
	StartedAt   *string  `json:"started_at,omitempty"`
	FinishedAt  *string  `json:"finished_at,omitempty"`
}

// activeChild tracks the currently executing process or session for kill dispatch.
type activeChild struct {
	stepType    string    // "shell" or "agent"
	cmd         *exec.Cmd // non-nil for shell steps
	sessionName string    // non-empty for agent steps
}

// WorkflowRunner executes workflow runs step-by-step.
type WorkflowRunner struct {
	store    *store.WorkflowStore
	launcher *AgentLauncher
	runtime  AgentRuntime
	logger   *slog.Logger
	dataDir  string // Coral data directory (~/.coral) for workflow run artifacts
	host     string // Server host for CORAL_HOST env var
	port     int    // Server port for CORAL_PORT env var

	// mu protects activeChildren and runCancels
	mu             sync.Mutex
	activeChildren map[int64]*activeChild       // runID -> active child
	runCancels     map[int64]context.CancelFunc // runID -> cancel func
}

// NewWorkflowRunner creates a new WorkflowRunner.
func NewWorkflowRunner(wfStore *store.WorkflowStore, launcher *AgentLauncher, runtime AgentRuntime, _ any, _ any, dataDir, host string, port int) *WorkflowRunner {
	return &WorkflowRunner{
		store:          wfStore,
		launcher:       launcher,
		runtime:        runtime,
		dataDir:        dataDir,
		host:           host,
		port:           port,
		logger:         slog.Default().With("service", "workflow_runner"),
		activeChildren: make(map[int64]*activeChild),
		runCancels:     make(map[int64]context.CancelFunc),
	}
}

// TriggerWorkflow creates a run record and starts execution in a goroutine.
// Returns the created run. Callers can track progress via GetWorkflowRun.
func (wr *WorkflowRunner) TriggerWorkflow(ctx context.Context, workflow *store.Workflow, triggerType string, triggerContext *string) (*store.WorkflowRun, error) {
	run, err := wr.store.CreateWorkflowRun(ctx, workflow.ID, triggerType, triggerContext)
	if err != nil {
		return nil, fmt.Errorf("create workflow run: %w", err)
	}

	go wr.executeRun(run.ID, workflow)
	return run, nil
}

// KillRun terminates a running workflow. Returns true if the run was killed.
func (wr *WorkflowRunner) KillRun(ctx context.Context, runID int64) bool {
	run, err := wr.store.GetWorkflowRunDirect(ctx, runID)
	if err != nil || run == nil {
		return false
	}
	if run.Status != "pending" && run.Status != "running" {
		return false
	}

	wr.mu.Lock()
	child := wr.activeChildren[runID]
	cancel := wr.runCancels[runID]
	wr.mu.Unlock()

	// Kill the active child based on step type
	if child != nil {
		switch child.stepType {
		case "shell":
			if child.cmd != nil && child.cmd.Process != nil {
				// Look up the actual process group ID
				pgid, err := syscall.Getpgid(child.cmd.Process.Pid)
				if err != nil {
					pgid = child.cmd.Process.Pid
				}
				// Send SIGTERM to process group
				syscall.Kill(-pgid, syscall.SIGTERM)
				// Escalate to SIGKILL after grace period in background
				// to avoid blocking the HTTP handler goroutine.
				go func(pgid int) {
					time.Sleep(5 * time.Second)
					if err := syscall.Kill(-pgid, 0); err == nil {
						syscall.Kill(-pgid, syscall.SIGKILL)
					}
				}(pgid)
			}
		case "agent":
			if child.sessionName != "" && wr.runtime != nil {
				wr.runtime.KillAgent(ctx, child.sessionName)
			}
		}
	}

	// Cancel the run's context to stop the execution loop
	if cancel != nil {
		cancel()
	}

	// Mark remaining steps as skipped and run as killed
	wr.markRunKilled(runID)
	return true
}

// executeRun is the main execution loop for a workflow run.
func (wr *WorkflowRunner) executeRun(runID int64, workflow *store.Workflow) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(workflow.MaxDurationS)*time.Second)
	defer cancel()

	wr.mu.Lock()
	wr.runCancels[runID] = cancel
	wr.mu.Unlock()

	defer func() {
		wr.mu.Lock()
		delete(wr.runCancels, runID)
		delete(wr.activeChildren, runID)
		wr.mu.Unlock()
	}()

	// Parse step definitions
	var steps []StepDef
	if err := json.Unmarshal([]byte(workflow.StepsJSON), &steps); err != nil {
		errMsg := fmt.Sprintf("failed to parse steps: %v", err)
		wr.store.SetRunStatus(ctx, runID, "failed", &errMsg)
		return
	}

	// Parse default agent config
	var defaultAgent *AgentStepConfig
	if workflow.DefaultAgentJSON != "" {
		defaultAgent = &AgentStepConfig{}
		if err := json.Unmarshal([]byte(workflow.DefaultAgentJSON), defaultAgent); err != nil {
			errMsg := fmt.Sprintf("failed to parse default_agent: %v", err)
			wr.store.SetRunStatus(ctx, runID, "failed", &errMsg)
			return
		}
	}

	// Create the run directory
	runDir := filepath.Join(wr.dataDir, "workflows", "runs", strconv.FormatInt(runID, 10))
	if err := os.MkdirAll(runDir, 0755); err != nil {
		errMsg := fmt.Sprintf("failed to create run directory: %v", err)
		wr.store.SetRunStatus(ctx, runID, "failed", &errMsg)
		return
	}

	// Write context.json
	contextData, _ := json.MarshalIndent(map[string]interface{}{
		"workflow_name": workflow.Name,
		"workflow_id":   workflow.ID,
		"run_id":        runID,
		"steps":         steps,
	}, "", "  ")
	os.WriteFile(filepath.Join(runDir, "context.json"), contextData, 0644)

	// Initialize step results
	results := make([]StepResult, len(steps))
	for i, step := range steps {
		results[i] = StepResult{
			Index:  i,
			Name:   step.Name,
			Type:   step.Type,
			Status: "pending",
			Files:  []string{},
		}
	}

	// Mark run as running
	wr.store.SetRunStatus(ctx, runID, "running", nil)
	wr.persistResults(ctx, runID, 0, results)

	// Execute steps sequentially
	for i, step := range steps {
		// Check if context is cancelled (timeout or kill)
		if ctx.Err() != nil {
			// Mark remaining steps as skipped
			for j := i; j < len(steps); j++ {
				results[j].Status = "skipped"
			}
			wr.persistResults(context.Background(), runID, i, results)

			status := "killed"
			var errMsg *string
			if ctx.Err() == context.DeadlineExceeded {
				msg := "workflow timeout exceeded"
				errMsg = &msg
			}
			wr.store.SetRunStatus(context.Background(), runID, status, errMsg)
			return
		}

		// Create step directory
		stepDir := filepath.Join(runDir, fmt.Sprintf("step_%d", i))
		os.MkdirAll(filepath.Join(stepDir, "artifacts"), 0755)

		// Build environment variables
		env := wr.buildStepEnv(workflow, runID, runDir, i, stepDir, len(steps), steps)

		// Connected Apps and workflow token injection are retired. Preserve the
		// workflow JSON, but fail this legacy step before execution so it cannot
		// silently run without the credentials it declared.
		if len(step.Connections) > 0 {
			err := fmt.Errorf("workflow step %q requires retired Connected Apps credentials; remove its connections before running", step.Name)
			errMsg := err.Error()
			wr.store.SetRunStatus(ctx, runID, "failed", &errMsg)
			results[i].Status = "failed"
			results[i].OutputTail = err.Error()
			wr.persistResults(ctx, runID, i, results)
			return
		}

		// Mark step as running
		now := time.Now().UTC().Format(store.ISOFormat)
		results[i].Status = "running"
		results[i].StartedAt = &now
		wr.persistResults(ctx, runID, i, results)

		// Execute step
		var stepErr error
		switch step.Type {
		case "shell":
			stepErr = wr.executeShellStep(ctx, runID, step, stepDir, workflow.RepoPath, env, &results[i])
		case "agent":
			stepErr = wr.executeAgentStep(ctx, runID, step, stepDir, workflow.RepoPath, env, defaultAgent, &results[i])
		default:
			stepErr = fmt.Errorf("unknown step type: %s", step.Type)
		}

		// Record step completion
		finishedAt := time.Now().UTC().Format(store.ISOFormat)
		results[i].FinishedAt = &finishedAt

		// List files in step directory
		results[i].Files = listStepFiles(stepDir)

		// Fire Coral-level hooks (StepComplete/StepFailed) — works for all step types
		stepHooks := parseStepHooks(step.Hooks)
		if stepHooks != nil {
			statusEnv := append(env, "CORAL_STEP_STATUS="+stepStatusStr(stepErr))
			if stepErr != nil {
				wr.fireHooks(ctx, stepHooks, "StepFailed", statusEnv)
			} else {
				wr.fireHooks(ctx, stepHooks, "StepComplete", statusEnv)
			}
		}

		if stepErr != nil {
			results[i].Status = "failed"

			if !step.ContinueOnFailure {
				// Skip remaining steps
				for j := i + 1; j < len(steps); j++ {
					results[j].Status = "skipped"
				}
				wr.persistResults(context.Background(), runID, i, results)
				// Only set failed if not already killed by KillRun
				if !wr.isRunKilled(runID) {
					errMsg := fmt.Sprintf("step %d (%s) failed: %v", i, step.Name, stepErr)
					wr.store.SetRunStatus(context.Background(), runID, "failed", &errMsg)
				}
				return
			}
		} else {
			results[i].Status = "completed"
		}

		wr.persistResults(ctx, runID, i, results)
	}

	// All steps completed — only set if not already killed
	if !wr.isRunKilled(runID) {
		wr.store.SetRunStatus(context.Background(), runID, "completed", nil)
	}
}

// executeShellStep runs a shell command, capturing stdout/stderr.
func (wr *WorkflowRunner) executeShellStep(ctx context.Context, runID int64, step StepDef, stepDir, repoPath string, env []string, result *StepResult) error {
	timeout := step.TimeoutS
	if timeout <= 0 {
		timeout = 300 // default 5 min for shell
	}
	stepCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Expand {{var}} templates in shell commands using pre-computed absolute paths.
	// These are safe (not user-controlled content). Commands can also reference
	// $CORAL_PREV_STDOUT etc. directly via environment variables.
	expandedCmd := expandTemplates(step.Command, env)
	cmd := exec.CommandContext(stepCtx, "sh", "-c", expandedCmd)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), env...)
	// Set process group for clean kill
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Capture stdout and stderr to files
	stdoutFile, err := os.Create(filepath.Join(stepDir, "stdout.txt"))
	if err != nil {
		return fmt.Errorf("create stdout file: %w", err)
	}
	defer stdoutFile.Close()

	stderrFile, err := os.Create(filepath.Join(stepDir, "stderr.txt"))
	if err != nil {
		return fmt.Errorf("create stderr file: %w", err)
	}
	defer stderrFile.Close()

	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	// Start the process (not Run — we need separate Start/Wait for clean kill)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start command: %w", err)
	}

	// Track active child for kill
	wr.mu.Lock()
	wr.activeChildren[runID] = &activeChild{stepType: "shell", cmd: cmd}
	wr.mu.Unlock()

	err = cmd.Wait()

	wr.mu.Lock()
	delete(wr.activeChildren, runID)
	wr.mu.Unlock()

	// Write exit code
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	result.ExitCode = &exitCode
	os.WriteFile(filepath.Join(stepDir, "exit_code"), []byte(strconv.Itoa(exitCode)), 0644)

	// Capture output tail for the result
	result.OutputTail = readTail(filepath.Join(stepDir, "stdout.txt"), 100)

	// If output_artifact is set, copy stdout to the specified artifact path
	if step.OutputArtifact != "" {
		artifactPath := filepath.Join(stepDir, filepath.Clean(step.OutputArtifact))
		os.MkdirAll(filepath.Dir(artifactPath), 0755)
		if data, err := os.ReadFile(filepath.Join(stepDir, "stdout.txt")); err == nil {
			os.WriteFile(artifactPath, data, 0644)
		}
	}

	if exitCode != 0 {
		return fmt.Errorf("exit code %d", exitCode)
	}
	return nil
}

// executeAgentStep runs an agent step. By default uses --print mode (non-interactive,
// no tmux session). When interactive: true, launches via tmux with workflow tagging.
func (wr *WorkflowRunner) executeAgentStep(ctx context.Context, runID int64, step StepDef, stepDir, repoPath string, env []string, defaultAgent *AgentStepConfig, result *StepResult) error {
	timeout := step.TimeoutS
	if timeout <= 0 {
		timeout = 600 // default 10 min for agent
	}
	stepCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Resolve agent config: step overrides default
	agentCfg := mergeAgentConfig(defaultAgent, step.Agent)
	if agentCfg == nil || agentCfg.AgentType == "" {
		return fmt.Errorf("agent step missing agent_type")
	}

	// Expand template variables in prompt (safe — not shell-interpreted)
	prompt := expandTemplates(step.Prompt, env)

	// Parse step hooks for agent-level routing
	stepHooks := parseStepHooks(step.Hooks)

	var stepErr error
	if step.Interactive {
		stepErr = wr.executeAgentInteractive(stepCtx, runID, step, stepDir, repoPath, env, agentCfg, prompt, result)
	} else {
		stepErr = wr.executeAgentPrint(stepCtx, runID, step, stepDir, repoPath, env, agentCfg, prompt, stepHooks, result)
	}

	// For non-Claude agents, fire Stop hooks via the runner after process exit
	if stepHooks != nil && agentCfg.AgentType != "claude" {
		wr.fireHooks(stepCtx, stepHooks, "Stop", env)
	}

	return stepErr
}

// executeAgentPrint runs an agent in non-interactive --print mode.
// When the agent config includes tools/capabilities, those are passed via a settings
// file (same as BuildLaunchCommand) so the agent has tool access in headless mode.
// Captures stdout/stderr to step files. No tmux session.
func (wr *WorkflowRunner) executeAgentPrint(ctx context.Context, runID int64, step StepDef, stepDir, repoPath string, env []string, agentCfg *AgentStepConfig, prompt string, stepHooks map[string]interface{}, result *StepResult) error {
	binPath, err := exec.LookPath(agentCfg.AgentType)
	if err != nil {
		return fmt.Errorf("agent binary %q not found: %w", agentCfg.AgentType, err)
	}

	// Build the full launch command via the agent package when tools/capabilities
	// are present, so settings (permissions, tools, MCP) are properly translated.
	hasCfg := len(agentCfg.Tools) > 0 || agentCfg.Capabilities != nil || agentCfg.MCPServers != nil
	var args []string

	if hasCfg && agentCfg.AgentType == "claude" {
		// Use BuildLaunchCommand for full settings support, then inject --print
		ag := agent.GetAgent(agentCfg.AgentType)
		params := agent.LaunchParams{
			WorkingDir: repoPath,
			Flags:      agentCfg.Flags,
			Prompt:     prompt,
			Hooks:      filterHooksForClaude(stepHooks),
		}
		if agentCfg.Capabilities != nil {
			var caps agent.Capabilities
			json.Unmarshal(agentCfg.Capabilities, &caps)
			params.Capabilities = &caps
		}
		if len(agentCfg.Tools) > 0 {
			params.Tools = agentCfg.Tools
		}
		if agentCfg.MCPServers != nil {
			var servers map[string]any
			json.Unmarshal(agentCfg.MCPServers, &servers)
			params.MCPServers = servers
		}
		// BuildLaunchCommand returns a full command string; parse it to extract args
		fullCmd := ag.BuildLaunchCommand(params)
		parts := strings.Fields(fullCmd)
		if len(parts) > 1 {
			// Filter out --session-id and its value (not valid for --print mode)
			var filtered []string
			for i := 1; i < len(parts); i++ {
				if parts[i] == "--session-id" || parts[i] == "-s" {
					i++ // skip the value too
					continue
				}
				if strings.HasPrefix(parts[i], "--session-id=") {
					continue
				}
				filtered = append(filtered, parts[i])
			}
			args = filtered
		}
		// Insert --print --no-session-persistence at the beginning
		args = append([]string{"--print", "--no-session-persistence"}, args...)
		if agentCfg.Model != "" {
			args = append([]string{"--model", agentCfg.Model}, args...)
		}
	} else {
		// Simple --print mode (no tools needed)
		args = []string{"--print", "--no-session-persistence"}
		if agentCfg.Model != "" {
			args = append(args, "--model", agentCfg.Model)
		}
		args = append(args, agentCfg.Flags...)
		args = append(args, prompt)
	}

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Capture stdout and stderr to files
	stdoutFile, err := os.Create(filepath.Join(stepDir, "stdout.txt"))
	if err != nil {
		return fmt.Errorf("create stdout file: %w", err)
	}
	defer stdoutFile.Close()

	stderrFile, err := os.Create(filepath.Join(stepDir, "stderr.txt"))
	if err != nil {
		return fmt.Errorf("create stderr file: %w", err)
	}
	defer stderrFile.Close()

	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	// Track active child for kill
	wr.mu.Lock()
	wr.activeChildren[runID] = &activeChild{stepType: "shell", cmd: cmd}
	wr.mu.Unlock()

	err = cmd.Wait()

	wr.mu.Lock()
	delete(wr.activeChildren, runID)
	wr.mu.Unlock()

	// Write exit code
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	result.ExitCode = &exitCode
	os.WriteFile(filepath.Join(stepDir, "exit_code"), []byte(strconv.Itoa(exitCode)), 0644)
	result.OutputTail = readTail(filepath.Join(stepDir, "stdout.txt"), 100)

	// If output_artifact is set, copy stdout to the specified artifact path
	if step.OutputArtifact != "" {
		artifactPath := filepath.Join(stepDir, filepath.Clean(step.OutputArtifact))
		os.MkdirAll(filepath.Dir(artifactPath), 0755)
		if data, err := os.ReadFile(filepath.Join(stepDir, "stdout.txt")); err == nil {
			os.WriteFile(artifactPath, data, 0644)
		}
	}

	if exitCode != 0 {
		return fmt.Errorf("agent exit code %d", exitCode)
	}
	return nil
}

// executeAgentInteractive launches an agent in a tmux session (interactive mode).
// The session is tagged with workflow metadata for frontend grouping.
func (wr *WorkflowRunner) executeAgentInteractive(ctx context.Context, runID int64, step StepDef, stepDir, repoPath string, env []string, agentCfg *AgentStepConfig, prompt string, result *StepResult) error {
	var flags []string
	if agentCfg.Model != "" {
		flags = append(flags, "--model", agentCfg.Model)
	}
	flags = append(flags, agentCfg.Flags...)

	displayName := fmt.Sprintf("workflow-run-%d-step-%s", runID, step.Name)
	launchResult, err := wr.launcher.LaunchAgent(
		ctx, repoPath, agentCfg.AgentType, displayName,
		"", flags, true, prompt, "", "",
	)
	if err != nil {
		return fmt.Errorf("launch agent: %w", err)
	}

	result.SessionID = launchResult.SessionID
	result.SessionName = launchResult.SessionName

	// Track active child for kill
	wr.mu.Lock()
	wr.activeChildren[runID] = &activeChild{stepType: "agent", sessionName: launchResult.SessionName}
	wr.mu.Unlock()

	// Send prompt after initialization delay
	go func() {
		wr.launcher.SendPrompt(ctx, launchResult.SessionID, launchResult.SessionName, agentCfg.AgentType, prompt)
	}()

	// Poll for session completion — 5s interval for workflow steps
	err = wr.watchSessionFast(ctx, launchResult.SessionName)

	wr.mu.Lock()
	delete(wr.activeChildren, runID)
	wr.mu.Unlock()

	if err != nil {
		return fmt.Errorf("agent session: %w", err)
	}
	return nil
}

// watchSessionFast polls for session existence every 5 seconds (faster than default 30s).
// Includes a startup grace period to allow the tmux session to become visible.
func (wr *WorkflowRunner) watchSessionFast(ctx context.Context, sessionName string) error {
	// Wait for session to start before polling (grace period for tmux visibility)
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !wr.runtime.IsAlive(context.Background(), sessionName) {
				return nil
			}
		}
	}
}

// markRunKilled marks remaining pending steps as skipped and the run as killed.
// Uses context.Background() because the run's context may already be cancelled.
func (wr *WorkflowRunner) markRunKilled(runID int64) {
	run, err := wr.store.GetWorkflowRunDirect(context.Background(), runID)
	if err != nil || run == nil {
		return
	}

	var results []StepResult
	if run.StepResults != "" && run.StepResults != "[]" {
		json.Unmarshal([]byte(run.StepResults), &results)
	}

	now := time.Now().UTC().Format(store.ISOFormat)
	for i := range results {
		if results[i].Status == "pending" || results[i].Status == "running" {
			results[i].Status = "skipped"
			results[i].FinishedAt = &now
		}
	}

	resultsJSON, _ := json.Marshal(results)
	wr.store.UpdateStepResults(context.Background(), runID, run.CurrentStep, string(resultsJSON))
	wr.store.SetRunStatus(context.Background(), runID, "killed", nil)
}

// persistResults marshals step results to JSON and updates the database.
func (wr *WorkflowRunner) persistResults(ctx context.Context, runID int64, currentStep int, results []StepResult) {
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		wr.logger.Error("failed to marshal step results", "run_id", runID, "error", err)
		return
	}
	if err := wr.store.UpdateStepResults(ctx, runID, currentStep, string(resultsJSON)); err != nil {
		wr.logger.Error("failed to persist step results", "run_id", runID, "error", err)
	}
}

// buildStepEnv constructs environment variables for a workflow step.
func (wr *WorkflowRunner) buildStepEnv(workflow *store.Workflow, runID int64, runDir string, stepIndex int, stepDir string, totalSteps int, steps []StepDef) []string {
	runIDStr := strconv.FormatInt(runID, 10)
	portStr := strconv.Itoa(wr.port)
	coralURL := fmt.Sprintf("http://%s:%s", wr.host, portStr)
	env := []string{
		// Server-level Coral variables
		"CORAL_PORT=" + portStr,
		"CORAL_HOST=" + wr.host,
		"CORAL_URL=" + coralURL,
		"CORAL_DIR=" + wr.dataDir,
		"CORAL_DATA_DIR=" + wr.dataDir,
		// Workflow-specific variables
		"CORAL_WORKFLOW_RUN_DIR=" + runDir,
		"CORAL_WORKFLOW_STEP=" + strconv.Itoa(stepIndex),
		"CORAL_WORKFLOW_STEP_DIR=" + stepDir,
		"CORAL_WORKFLOW_NAME=" + workflow.Name,
		"CORAL_WORKFLOW_RUN_ID=" + runIDStr,
		"CORAL_WORKFLOW_REPO_PATH=" + workflow.RepoPath,
	}

	// Ensure Coral tools (coral-board, hooks, etc.) and agent CLIs are in PATH
	if toolsDir := agent.CoralToolsDir(); toolsDir != "" {
		env = append(env, "PATH="+toolsDir+":"+os.Getenv("PATH"))
	}

	// Previous step references
	if stepIndex > 0 {
		prevDir := filepath.Join(runDir, fmt.Sprintf("step_%d", stepIndex-1))
		env = append(env,
			"CORAL_PREV_DIR="+prevDir,
			"CORAL_PREV_STDOUT="+filepath.Join(prevDir, "stdout.txt"),
			"CORAL_PREV_STDERR="+filepath.Join(prevDir, "stderr.txt"),
		)
		// If previous step has an output_artifact, set path and read content
		if stepIndex-1 < len(steps) && steps[stepIndex-1].OutputArtifact != "" {
			artifactPath := filepath.Join(prevDir, steps[stepIndex-1].OutputArtifact)
			env = append(env, "CORAL_PREV_ARTIFACT="+artifactPath)
			if data, err := os.ReadFile(artifactPath); err == nil {
				env = append(env, "CORAL_PREV_ARTIFACT_CONTENT="+string(data))
			}
		}
	}

	// All previous step directories as CORAL_STEP_N_DIR / CORAL_STEP_N_STDOUT
	for n := 0; n < stepIndex; n++ {
		nDir := filepath.Join(runDir, fmt.Sprintf("step_%d", n))
		env = append(env,
			fmt.Sprintf("CORAL_STEP_%d_DIR=%s", n, nDir),
			fmt.Sprintf("CORAL_STEP_%d_STDOUT=%s", n, filepath.Join(nDir, "stdout.txt")),
		)
	}

	return env
}

// templatePattern matches {{variable_name}} patterns for template expansion.
var templatePattern = regexp.MustCompile(`\{\{(\w+)\}\}`)

// envKeyForTemplate maps template variable names to their environment variable equivalents.
var envKeyForTemplate = map[string]string{
	"run_dir":               "CORAL_WORKFLOW_RUN_DIR",
	"run_id":                "CORAL_WORKFLOW_RUN_ID",
	"step_dir":              "CORAL_WORKFLOW_STEP_DIR",
	"prev_dir":              "CORAL_PREV_DIR",
	"prev_stdout":           "CORAL_PREV_STDOUT",
	"prev_stderr":           "CORAL_PREV_STDERR",
	"prev_artifact":         "CORAL_PREV_ARTIFACT",
	"prev_artifact_content": "CORAL_PREV_ARTIFACT_CONTENT",
}

// stepNPattern matches {{step_N_dir}} and {{step_N_stdout}} patterns.
var stepNPattern = regexp.MustCompile(`^step_(\d+)_(dir|stdout)$`)

// expandTemplates replaces {{var}} placeholders with values from environment variables.
// This uses pre-computed absolute paths — no user-controlled content is interpolated.
func expandTemplates(text string, env []string) string {
	// Build lookup from env slice
	envMap := make(map[string]string, len(env))
	for _, e := range env {
		if idx := strings.IndexByte(e, '='); idx >= 0 {
			envMap[e[:idx]] = e[idx+1:]
		}
	}

	return templatePattern.ReplaceAllStringFunc(text, func(match string) string {
		varName := match[2 : len(match)-2] // strip {{ and }}

		// Check direct mapping
		if envKey, ok := envKeyForTemplate[varName]; ok {
			if val, found := envMap[envKey]; found {
				return val
			}
			return match // leave unexpanded if env var not set
		}

		// Check step_N_dir / step_N_stdout patterns
		if m := stepNPattern.FindStringSubmatch(varName); m != nil {
			suffix := "DIR"
			if m[2] == "stdout" {
				suffix = "STDOUT"
			}
			envKey := fmt.Sprintf("CORAL_STEP_%s_%s", m[1], suffix)
			if val, found := envMap[envKey]; found {
				return val
			}
			return match
		}

		// Check _content suffix — reads file content instead of returning path
		// e.g., {{prev_stdout_content}} reads the file at CORAL_PREV_STDOUT
		// e.g., {{step_0_stdout_content}} reads the file at CORAL_STEP_0_STDOUT
		if strings.HasSuffix(varName, "_content") {
			baseVar := varName[:len(varName)-len("_content")]
			var envKey string
			if ek, ok := envKeyForTemplate[baseVar]; ok {
				envKey = ek
			} else if m := stepNPattern.FindStringSubmatch(baseVar); m != nil {
				suffix := "DIR"
				if m[2] == "stdout" {
					suffix = "STDOUT"
				}
				envKey = fmt.Sprintf("CORAL_STEP_%s_%s", m[1], suffix)
			}
			if envKey != "" {
				if filePath, found := envMap[envKey]; found {
					if data, err := os.ReadFile(filePath); err == nil {
						return string(data)
					}
				}
			}
			return match
		}

		return match // unknown template, leave as-is
	})
}

// mergeAgentConfig merges step-level agent config over workflow-level default.
// Step fields take precedence.
func mergeAgentConfig(defaultCfg, stepCfg *AgentStepConfig) *AgentStepConfig {
	if defaultCfg == nil && stepCfg == nil {
		return nil
	}
	if defaultCfg == nil {
		return stepCfg
	}
	if stepCfg == nil {
		// Return a copy of default
		cfg := *defaultCfg
		return &cfg
	}

	// Merge: step overrides default
	merged := *defaultCfg
	if stepCfg.AgentType != "" {
		merged.AgentType = stepCfg.AgentType
	}
	if stepCfg.Model != "" {
		merged.Model = stepCfg.Model
	}
	if stepCfg.Capabilities != nil {
		merged.Capabilities = stepCfg.Capabilities
	}
	if len(stepCfg.Tools) > 0 {
		merged.Tools = stepCfg.Tools
	}
	if stepCfg.MCPServers != nil {
		merged.MCPServers = stepCfg.MCPServers
	}
	if len(stepCfg.Flags) > 0 {
		merged.Flags = stepCfg.Flags
	}
	return &merged
}

// readTail reads the last N lines from a file.
func readTail(path string, maxLines int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// listStepFiles returns relative file paths found in a step directory.
func listStepFiles(stepDir string) []string {
	var files []string
	filepath.Walk(stepDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(stepDir, path)
		if err != nil {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if files == nil {
		return []string{}
	}
	return files
}

// stepStatusStr returns "failed" if err is non-nil, "completed" otherwise.
func stepStatusStr(err error) string {
	if err != nil {
		return "failed"
	}
	return "completed"
}

// isRunKilled checks if the run has been killed (context cancelled by KillRun).
// Used to avoid overwriting "killed" status with "failed" or "completed".
func (wr *WorkflowRunner) isRunKilled(runID int64) bool {
	wr.mu.Lock()
	cancel, exists := wr.runCancels[runID]
	wr.mu.Unlock()
	// If cancel doesn't exist, the run was already cleaned up (killed)
	if !exists {
		return true
	}
	// Check if the run's DB status is already killed
	_ = cancel
	run, err := wr.store.GetWorkflowRunDirect(context.Background(), runID)
	if err != nil || run == nil {
		return false
	}
	return run.Status == "killed"
}

// IsRunActive returns true if the given run ID is currently being executed.
func (wr *WorkflowRunner) IsRunActive(runID int64) bool {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	_, ok := wr.runCancels[runID]
	return ok
}

// parseStepHooks unmarshals the hooks JSON from a step definition.
func parseStepHooks(raw json.RawMessage) map[string]interface{} {
	if raw == nil {
		return nil
	}
	var hooks map[string]interface{}
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return nil
	}
	return hooks
}

// claudeNativeEvents are hook events that Claude Code handles natively via settings.json.
var claudeNativeEvents = map[string]bool{
	"PreToolUse":   true,
	"PostToolUse":  true,
	"Stop":         true,
	"Notification": true,
	"SubagentStop": true,
}

// filterHooksForClaude returns only Claude-native events from the hooks map.
func filterHooksForClaude(hooks map[string]interface{}) map[string]interface{} {
	if hooks == nil {
		return nil
	}
	filtered := make(map[string]interface{})
	for event, groups := range hooks {
		if claudeNativeEvents[event] {
			filtered[event] = groups
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// fireHooks executes hook commands for a given event. Best-effort: hook failures
// are logged but don't fail the step. Timeout per hook: 60s (matches Claude Code default).
func (wr *WorkflowRunner) fireHooks(ctx context.Context, hooks map[string]interface{}, event string, env []string) {
	eventHooks, ok := hooks[event]
	if !ok {
		return
	}
	groups, ok := eventHooks.([]interface{})
	if !ok {
		return
	}

	for _, g := range groups {
		group, ok := g.(map[string]interface{})
		if !ok {
			continue
		}
		hookList, ok := group["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range hookList {
			hook, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			command, _ := hook["command"].(string)
			if command == "" {
				continue
			}

			hookCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			cmd := exec.CommandContext(hookCtx, "sh", "-c", command)
			cmd.Env = append(os.Environ(), env...)
			if err := cmd.Run(); err != nil {
				wr.logger.Warn("hook execution failed", "event", event, "command", command, "error", err)
			}
			cancel()
		}
	}
}
