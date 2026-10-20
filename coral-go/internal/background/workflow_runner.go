// Package background — workflow_runner.go provides the workflow execution engine.
// It runs multi-step workflows (shell commands + agent prompts) sequentially,
// capturing output, managing timeouts, and tracking progress in the database.
package background

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/agentsignin"
	"github.com/cdknorow/coral/internal/safego"

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
	Flags        []string        `json:"flags,omitempty"`
	// mcpServers is not read: Coral doesn't configure MCP servers (#3644); saving a workflow
	// that names it is refused, and one stored before is run without it.
}

// StepResult tracks the outcome of a single step execution.
type StepResult struct {
	Index      int    `json:"index"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"` // pending, running, completed, failed, skipped
	ExitCode   *int   `json:"exit_code,omitempty"`
	OutputTail string `json:"output_tail,omitempty"`
	// ErrorTail is the end of a failed step's stderr, so the run view can show why it failed
	// without opening stderr.txt.
	ErrorTail string `json:"error_tail,omitempty"`
	// Note says why something the step asked for didn't happen although the step itself
	// ran (e.g. an output_artifact that was refused, #3423).
	Note        string   `json:"note,omitempty"`
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

	// mu protects activeChildren, runCancels, killed and running
	mu             sync.Mutex
	activeChildren map[int64]*activeChild       // runID -> active child
	runCancels     map[int64]context.CancelFunc // runID -> cancel func
	// killed: runs KillRun was called for, set before it kills the step's process, so
	// the step's failure (its process killed) is never recorded over "killed" (#3474).
	killed map[int64]bool
	// running counts runs in progress; at maxRuns a new trigger is refused (#3424, MED-02).
	running int
	maxRuns int
	// stopped: Shutdown ran, so no new run starts (#3997); runs waits for those in progress.
	stopped bool
	runs    sync.WaitGroup
}

// ErrRunnerStopped: Coral is stopping, so no workflow run starts.
var ErrRunnerStopped = errors.New("Coral is stopping: the workflow can't start now")

// maxConcurrentWorkflowRuns caps workflow runs in progress, like the job scheduler.
const maxConcurrentWorkflowRuns = 5

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
		maxRuns:        maxConcurrentWorkflowRuns,
		activeChildren: make(map[int64]*activeChild),
		runCancels:     make(map[int64]context.CancelFunc),
		killed:         make(map[int64]bool),
	}
}

// TriggerWorkflow creates a run record and starts execution in a goroutine.
// Returns the created run. Callers can track progress via GetWorkflowRun.
// A run beyond maxRuns in progress is refused with ConcurrencyLimitError (#3424, MED-02).
func (wr *WorkflowRunner) TriggerWorkflow(ctx context.Context, workflow *store.Workflow, triggerType string, triggerContext *string) (*store.WorkflowRun, error) {
	wr.mu.Lock()
	if wr.stopped {
		wr.mu.Unlock()
		return nil, ErrRunnerStopped
	}
	if wr.maxRuns > 0 && wr.running >= wr.maxRuns {
		limit := wr.maxRuns
		wr.mu.Unlock()
		return nil, &ConcurrencyLimitError{Limit: limit}
	}
	wr.running++
	wr.runs.Add(1)
	wr.mu.Unlock()
	done := func() {
		wr.mu.Lock()
		wr.running--
		wr.mu.Unlock()
		wr.runs.Done()
	}

	run, err := wr.store.CreateWorkflowRun(ctx, workflow.ID, triggerType, triggerContext)
	if err != nil {
		done()
		return nil, fmt.Errorf("create workflow run: %w", err)
	}

	wr.pruneRuns(ctx, workflow.ID)
	go func() {
		defer safego.Recover("background/workflow_runner:149")
		defer done()
		wr.executeRun(run.ID, workflow)
	}()
	return run, nil
}

// Shutdown ends every run in progress when Coral stops (#3997): no new run starts, each shell
// step's whole process group is killed (Unix: the step's group; Windows: its job), every run's
// context is cancelled and the run is recorded as killed. It then waits for the runs to finish,
// or for ctx. Before, nothing stopped them: a step in its own process group outlived the server.
// Agent steps' sessions are not killed: an agent survives a restart of Coral.
func (wr *WorkflowRunner) Shutdown(ctx context.Context) {
	wr.mu.Lock()
	wr.stopped = true
	ids := make([]int64, 0, len(wr.runCancels))
	for id, cancel := range wr.runCancels {
		wr.killed[id] = true
		if c := wr.activeChildren[id]; c != nil && c.stepType == "shell" && c.cmd != nil && c.cmd.Process != nil {
			killChildProcess(c.cmd)
		}
		cancel()
		ids = append(ids, id)
	}
	wr.mu.Unlock()
	for _, id := range ids {
		wr.markRunKilled(id)
	}
	done := make(chan struct{})
	go func() {
		defer safego.Recover("background/workflow_runner:stopRuns")
		wr.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
		if len(ids) > 0 {
			wr.logger.Info("stopped the workflow runs in progress", "runs", len(ids))
		}
	case <-ctx.Done():
		wr.logger.Warn("workflow runs still finishing at shutdown", "runs", len(ids))
	}
	// A step that ignores SIGTERM is killed by the escalation, which must run before Coral exits (#5366).
	waitChildKills(ctx)
}

// WorkflowRunsKept is how many runs (records and run directories) each
// workflow keeps; older finished runs are removed when a new one starts.
const WorkflowRunsKept = 50

// pruneRuns removes a workflow's runs beyond the newest WorkflowRunsKept, with
// their directories (audit MED-04). Best effort: a failure only logs.
func (wr *WorkflowRunner) pruneRuns(ctx context.Context, workflowID int64) {
	ids, err := wr.store.PruneWorkflowRuns(ctx, workflowID, WorkflowRunsKept)
	if err != nil {
		wr.logger.Warn("could not prune old workflow runs", "workflow_id", workflowID, "error", err)
		return
	}
	RemoveRunDirs(wr.dataDir, ids)
}

// RemoveRunDirs deletes <dataDir>/workflows/runs/<id> for each run id.
func RemoveRunDirs(dataDir string, ids []int64) {
	if dataDir == "" {
		return
	}
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		runs := filepath.Join(dataDir, "workflows", "runs")
		name := strconv.FormatInt(id, 10)
		_ = os.RemoveAll(filepath.Join(runs, name))
		// And any folder an earlier database left for the same id, moved aside (#3587). Listed
		// and matched by prefix, not globbed: the data folder's path may hold '[' or '*'.
		entries, _ := os.ReadDir(runs)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), name+".stale-") {
				_ = os.RemoveAll(filepath.Join(runs, e.Name()))
			}
		}
	}
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
	if cancel != nil {
		wr.killed[runID] = true
	}
	wr.mu.Unlock()

	// Kill the active child based on step type
	if child != nil {
		switch child.stepType {
		case "shell":
			if child.cmd != nil && child.cmd.Process != nil {
				killChildProcess(child.cmd)
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

	if killRunPause != nil {
		killRunPause()
	}
	// Mark remaining steps as skipped and run as killed
	wr.markRunKilled(runID)
	return true
}

// killRunPause and afterKillCheck, for tests: the first runs in KillRun after the
// step's process is killed and before the run is marked killed, the second in the run
// loop between its killed check and its "failed" write. Together they force the
// order seen under -race (#3474): checked, then marked killed, then written failed.
var (
	killRunPause   func()
	afterKillCheck func(runID int64)
)

// executeRun is the main execution loop for a workflow run: its definition read, its folder made, then each step
// in turn, until one fails (unless it may), the run is cancelled (a kill or its time limit), or all are done.
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
		delete(wr.killed, runID)
		wr.mu.Unlock()
	}()

	steps, defaultAgent, errMsg := parseRunDefinition(workflow)
	if errMsg != "" {
		wr.store.SetRunStatus(ctx, runID, "failed", &errMsg)
		return
	}
	rf, results, ok := wr.startRun(ctx, runID, workflow, steps)
	if !ok {
		return
	}
	defer rf.Close()
	// Before the first step (#5469): when an agent step needs a CLI that is signed out, the whole run fails at once, no step starts, and nothing tries to sign in.
	if nsi := wr.signInPreflightIn(ctx, steps, defaultAgent, workflow.RepoPath); nsi != nil {
		for i := range results {
			results[i].Status = "skipped"
		}
		wr.persistResults(context.Background(), runID, 0, results)
		wr.store.SetRunSignInRefused(context.Background(), runID, nsi.Error(), nsi.Code(), nsi.Agent)
		return
	}
	for i, step := range steps {
		if ctx.Err() != nil {
			wr.endCancelledRun(ctx, runID, i, results)
			return
		}
		if !wr.runStep(ctx, runID, workflow, steps, i, step, rf, defaultAgent, results) {
			return
		}
	}
	// All steps completed — only set if not already killed
	if !wr.isRunKilled(runID) {
		wr.store.SetRunStatus(context.Background(), runID, "completed", nil)
	}
}

// parseRunDefinition reads a workflow's steps and its default agent config; errMsg says what failed to parse.
func parseRunDefinition(workflow *store.Workflow) (steps []StepDef, defaultAgent *AgentStepConfig, errMsg string) {
	if err := json.Unmarshal([]byte(workflow.StepsJSON), &steps); err != nil {
		return nil, nil, fmt.Sprintf("failed to parse steps: %v", err)
	}
	if workflow.DefaultAgentJSON != "" {
		defaultAgent = &AgentStepConfig{}
		if err := json.Unmarshal([]byte(workflow.DefaultAgentJSON), defaultAgent); err != nil {
			return nil, nil, fmt.Sprintf("failed to parse default_agent: %v", err)
		}
	}
	return steps, defaultAgent, ""
}

// startRun creates the run folder, fresh, and holds it open: every step file is made and read through it, never by
// a path an earlier step could have planted a symlink on (#3587). It writes context.json, marks every step pending
// and the run running. ok is false when the folder can't be made (the run is then failed).
func (wr *WorkflowRunner) startRun(ctx context.Context, runID int64, workflow *store.Workflow, steps []StepDef) (*runFiles, []StepResult, bool) {
	rf, err := openRunFolder(wr.dataDir, runID)
	if err != nil {
		errMsg := fmt.Sprintf("failed to create run directory: %v", err)
		wr.store.SetRunStatus(ctx, runID, "failed", &errMsg)
		return nil, nil, false
	}
	contextData, _ := json.MarshalIndent(map[string]interface{}{
		"workflow_name": workflow.Name,
		"workflow_id":   workflow.ID,
		"run_id":        runID,
		"steps":         steps,
	}, "", "  ")
	if err := rf.writeNew("context.json", contextData); err != nil {
		slog.Warn("[workflow] context.json not written", "run", runID, "error", err)
	}
	results := make([]StepResult, len(steps))
	for i, step := range steps {
		results[i] = StepResult{Index: i, Name: step.Name, Type: step.Type, Status: "pending", Files: []string{}}
	}
	wr.store.SetRunStatus(ctx, runID, "running", nil)
	wr.persistResults(ctx, runID, 0, results)
	return rf, results, true
}

// endCancelledRun ends a run whose context ended before step i (a kill, or its time limit): the steps left are
// skipped, and the run is killed (with "workflow timeout exceeded" when it ran out of time).
func (wr *WorkflowRunner) endCancelledRun(ctx context.Context, runID int64, i int, results []StepResult) {
	for j := i; j < len(results); j++ {
		results[j].Status = "skipped"
	}
	wr.persistResults(context.Background(), runID, i, results)
	var errMsg *string
	if ctx.Err() == context.DeadlineExceeded {
		msg := "workflow timeout exceeded"
		errMsg = &msg
	}
	wr.store.SetRunStatus(context.Background(), runID, "killed", errMsg)
}

// failRemaining ends the run at failed step i: the steps after it skipped, the results recorded, and the run failed
// with errMsg. unlessKilled keeps a run KillRun already marked killed as it is (its step failed because of the kill).
func (wr *WorkflowRunner) failRemaining(runID int64, i int, results []StepResult, errMsg string, unlessKilled bool) {
	for j := i + 1; j < len(results); j++ {
		results[j].Status = "skipped"
	}
	wr.persistResults(context.Background(), runID, i, results)
	if unlessKilled {
		killed := wr.isRunKilled(runID)
		if afterKillCheck != nil {
			afterKillCheck(runID)
		}
		if killed {
			return
		}
	}
	wr.store.SetRunStatus(context.Background(), runID, "failed", &errMsg)
}

// settings are the user's settings (for the CLI path); nil with no error when there is no store,
// and an error when the read fails (the step then fails instead of running the CLI on PATH, #4350).
func (wr *WorkflowRunner) settings(ctx context.Context) (map[string]string, error) {
	if wr.store == nil || wr.store.DB() == nil {
		return nil, nil
	}
	return store.NewSessionStore(wr.store.DB()).GetSettingsForCLI(ctx)
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
	killed := wr.killed[runID]
	wr.mu.Unlock()
	if killed {
		// KillRun marks it before killing the step's process: the database may not
		// say "killed" yet when the step's failure gets here.
		return true
	}
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

// signInPreflight is the run's one sign-in check (#5469): for each agent CLI the run's agent steps use (print or interactive) that has a non-interactive status command
// (agentsignin.Supports: Claude and Codex; agy, Gemini and Pi have none), it asks the CLI itself, fresh. It returns the sentence for the run history ("Claude is not signed in")
// as an error (the agent named) when one is signed out, nil when the run may start. An agent CLI that cannot be found or asked does not stop the run here: its step fails or goes on as it did before.
func (wr *WorkflowRunner) signInPreflight(ctx context.Context, steps []StepDef, defaultAgent *AgentStepConfig) *agentsignin.NotSignedInError {
	return wr.signInPreflightIn(ctx, steps, defaultAgent, "")
}

// signInPreflightIn is signInPreflight asked from dir, the run's repository: its steps run there, so the CLI reads that folder's project settings (#5476).
func (wr *WorkflowRunner) signInPreflightIn(ctx context.Context, steps []StepDef, defaultAgent *AgentStepConfig, dir string) *agentsignin.NotSignedInError {
	var types []string
	seen := map[string]bool{}
	for _, step := range steps {
		if step.Type != "agent" {
			continue
		}
		cfg := mergeAgentConfig(defaultAgent, step.Agent)
		if cfg == nil || !agentsignin.Supports(cfg.AgentType) || seen[cfg.AgentType] {
			continue
		}
		seen[cfg.AgentType] = true
		types = append(types, cfg.AgentType)
	}
	if len(types) == 0 {
		return nil
	}
	settings, err := wr.settings(ctx)
	if err != nil {
		return nil // the step reports the unreadable settings itself
	}
	for _, t := range types {
		bin, err := agent.FindCLI(settings, t)
		if err != nil {
			continue
		}
		var nsi *agentsignin.NotSignedInError
		if err := agentsignin.RequireFreshIn(ctx, t, bin, dir); errors.As(err, &nsi) {
			return nsi
		}
	}
	return nil
}
