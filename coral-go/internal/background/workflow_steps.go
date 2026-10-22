package background

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/agentprio"
	"github.com/subgentic/coral/internal/safego"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentenv"
	"github.com/subgentic/coral/internal/agentmodels"
	"github.com/subgentic/coral/internal/agentsignin"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/store"
)

// runStep runs step i in its own new folder (one an earlier step made fails it): marked running, executed by its
// type, its files listed, its StepComplete or StepFailed hooks fired, and its result recorded. It reports whether
// the run goes on: a step that fails ends it unless the step may fail (a run KillRun already marked killed is not
// marked failed). A step that still names retired Connected Apps credentials fails before it runs.
func (wr *WorkflowRunner) runStep(ctx context.Context, runID int64, workflow *store.Workflow, steps []StepDef, i int, step StepDef, rf *runFiles, defaultAgent *AgentStepConfig, results []StepResult) bool {
	sf, serr := rf.step(i)
	stepDir := sf.dir
	if serr != nil {
		results[i].Status = "failed"
		results[i].OutputTail = serr.Error()
		wr.failRemaining(runID, i, results, fmt.Sprintf("step %d (%s) failed: %v", i, step.Name, serr), false)
		return false
	}
	env := wr.buildStepEnv(workflow, runID, rf.dir, i, stepDir, len(steps), steps)
	// Connected Apps and workflow token injection are retired. Preserve the workflow JSON, but fail this legacy
	// step before execution so it cannot silently run without the credentials it declared.
	if len(step.Connections) > 0 {
		err := fmt.Errorf("workflow step %q requires retired Connected Apps credentials; remove its connections before running", step.Name)
		errMsg := err.Error()
		wr.store.SetRunStatus(ctx, runID, "failed", &errMsg)
		results[i].Status = "failed"
		results[i].OutputTail = err.Error()
		wr.persistResults(ctx, runID, i, results)
		return false
	}
	now := time.Now().UTC().Format(store.ISOFormat)
	results[i].Status = "running"
	results[i].StartedAt = &now
	wr.persistResults(ctx, runID, i, results)

	var stepErr error
	switch step.Type {
	case "shell":
		stepErr = wr.executeShellStep(ctx, runID, step, sf, workflow.RepoPath, env, &results[i])
	case "agent":
		stepErr = wr.executeAgentStep(ctx, runID, step, sf, workflow.RepoPath, env, defaultAgent, &results[i])
	default:
		stepErr = fmt.Errorf("unknown step type: %s", step.Type)
	}
	finishedAt := time.Now().UTC().Format(store.ISOFormat)
	results[i].FinishedAt = &finishedAt
	results[i].Files = listStepFiles(stepDir)

	// Coral-level hooks (StepComplete/StepFailed), for every step type.
	if stepHooks := parseStepHooks(step.Hooks); stepHooks != nil {
		statusEnv := append(env, "CORAL_STEP_STATUS="+stepStatusStr(stepErr))
		event := "StepComplete"
		if stepErr != nil {
			event = "StepFailed"
		}
		wr.fireHooks(ctx, stepHooks, event, hookDir(workflow.RepoPath, stepDir), statusEnv)
	}

	if stepErr == nil {
		results[i].Status = "completed"
		wr.persistResults(ctx, runID, i, results)
		return true
	}
	results[i].Status = "failed"
	if step.ContinueOnFailure {
		wr.persistResults(ctx, runID, i, results)
		return true
	}
	wr.failRemaining(runID, i, results, fmt.Sprintf("step %d (%s) failed: %v", i, step.Name, stepErr), true)
	return false
}

// executeShellStep runs a shell command, capturing stdout/stderr.
func (wr *WorkflowRunner) executeShellStep(ctx context.Context, runID int64, step StepDef, sf stepFiles, repoPath string, env []string, result *StepResult) error {
	timeout := step.TimeoutS
	if timeout <= 0 {
		timeout = 300 // default 5 min for shell
	}
	stepCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Expand {{var}} templates: paths as they are, step output as one quoted shell word
	// (#3423). Commands can also reference $CORAL_PREV_STDOUT etc. directly.
	expandedCmd, terr := expandShellTemplates(step.Command, env)
	if terr != nil {
		// Never an exec that fails with no message (E2BIG): say why, in the step (#3423 F3).
		msg := "coral: " + terr.Error() + "\n"
		sf.writeNew("stderr.txt", []byte(msg))
		sf.writeNew("stdout.txt", nil)
		code := 1
		result.ExitCode = &code
		sf.writeNew("exit_code", []byte("1"))
		result.ErrorTail = msg
		return terr
	}
	shell, serr := workflowShell()
	if serr != nil {
		msg := "coral: " + serr.Error() + "\n"
		sf.writeNew("stderr.txt", []byte(msg))
		sf.writeNew("stdout.txt", nil)
		code := 1
		result.ExitCode = &code
		sf.writeNew("exit_code", []byte("1"))
		result.ErrorTail = msg
		return serr
	}
	cmd := exec.CommandContext(stepCtx, shell, "-c", expandedCmd)
	cmd.Dir = repoPath
	cmd.Env = append(agentenv.Filter(os.Environ()), env...) // #3389: no server-only settings
	// Set process group for clean kill
	configureChildProcess(cmd)

	// Capture stdout and stderr to files, through a pipe and a capped copy (#5079): a runaway command cannot fill the disk before its timeout. The child gets
	// the pipe's write end as a file, so Wait returns when it exits (a process it left behind keeps its end and is never killed for it).
	output, err := newStepOutput(sf)
	if err != nil {
		return err
	}
	output.attach(cmd)

	// Start the process (not Run — we need separate Start/Wait for clean kill)
	if err := cmd.Start(); err != nil {
		output.release()
		return fmt.Errorf("start command: %w", err)
	}
	output.release()
	trackChildProcess(cmd)                // Windows: its job, so a kill ends everything it started (#3997)
	agentprio.ApplyGroup(cmd.Process.Pid) // the step's process group at the agents' priority (#3437)

	// Track active child for kill
	wr.mu.Lock()
	wr.activeChildren[runID] = &activeChild{stepType: "shell", cmd: cmd}
	wr.mu.Unlock()

	err = cmd.Wait()
	releaseChildProcess(cmd)
	output.finish(sf, result)

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
	sf.writeNew("exit_code", []byte(strconv.Itoa(exitCode)))

	// Capture output tail for the result
	result.OutputTail = sf.readTail("stdout.txt", 100)
	if exitCode != 0 {
		result.ErrorTail = sf.readTail("stderr.txt", 40)
	}

	// If output_artifact is set, copy stdout to it: inside the step's folder only, never
	// through a symlink (#3423, CRIT-02).
	if step.OutputArtifact != "" {
		if err := sf.writeArtifact(step.OutputArtifact); err != nil {
			slog.Warn("[workflow] output_artifact not written", "step", step.Name, "error", err)
			noteArtifactRefused(sf, step.OutputArtifact, err, result)
		}
	}

	if exitCode != 0 {
		return fmt.Errorf("exit code %d", exitCode)
	}
	return nil
}

// executeAgentStep runs an agent step. By default uses --print mode (non-interactive,
// no tmux session). When interactive: true, launches via tmux with workflow tagging.
func (wr *WorkflowRunner) executeAgentStep(ctx context.Context, runID int64, step StepDef, sf stepFiles, repoPath string, env []string, defaultAgent *AgentStepConfig, result *StepResult) error {
	stepDir := sf.dir
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
	if !step.Interactive {
		// A workflow saved before #3522 can still name a CLI print steps don't support.
		if msg := PrintStepError(agentCfg.AgentType); msg != "" {
			return fmt.Errorf("step %q: %s", step.Name, msg)
		}
	}
	if step.Interactive {
		stepErr = wr.executeAgentInteractive(stepCtx, runID, step, stepDir, repoPath, env, agentCfg, prompt, result)
	} else {
		stepErr = wr.executeAgentPrint(stepCtx, runID, step, sf, repoPath, env, agentCfg, prompt, stepHooks, result)
	}

	// For non-Claude agents, fire Stop hooks via the runner after process exit
	if stepHooks != nil && agentCfg.AgentType != "claude" {
		wr.fireHooks(stepCtx, stepHooks, "Stop", hookDir(repoPath, stepDir), env)
	}

	return stepErr
}

// printStepCLIs lists the agent CLIs a non-interactive ("print") workflow step may use, and
// what verifying each remaining one takes (#3522). A CLI is enabled only after a real run
// shows how it reads the prompt (stdin, never argv) AND its headless permission defaults,
// with the permission flags then pinned explicitly: a guessed headless mode could run with a
// different sandbox or approval default than the workflow's author expects, unnoticed.
// Until then such a step is refused at save and at run with a clear message.
var printStepCLIs = map[string]bool{
	at.Claude: true,  // verified: --print with the prompt on stdin (#3519, #3521)
	at.Codex:  false, // to verify: "codex exec" reads stdin with no prompt argument; pin --sandbox and the approval policy
	at.Gemini: false, // to verify: "gemini -p" appends to stdin input; pin --approval-mode (headless can auto-approve)
	at.Agy:    false, // to verify: "agy --print" takes the prompt as its value (stdin only with stream-json); pin --mode
	// agy's older name, accepted everywhere else in Coral (QA's review of #3522).
	at.Antigravity: false,
	at.Pi:          false, // to verify: not installed on the test machines yet
}

// PrintStepError says why a non-interactive step can't run on agentType, or "" when it can:
// an unknown agent type, or a CLI not verified for print steps yet (#3522).
func PrintStepError(agentType string) string {
	supported, known := printStepCLIs[agentType]
	if !known {
		return fmt.Sprintf("unknown agent_type %q", agentType)
	}
	if !supported {
		return fmt.Sprintf("print (non-interactive) steps aren't supported for %s yet; use claude, or make the step interactive", agentType)
	}
	return ""
}

// KnownStepAgentType reports whether agentType can run a workflow agent step at all.
func KnownStepAgentType(agentType string) bool {
	_, known := printStepCLIs[agentType]
	return known
}

// executeAgentPrint runs an agent in non-interactive --print mode.
// When the agent config includes tools/capabilities, those are passed via a settings
// file (same as LaunchCommand) so the agent has tool access in headless mode.
// Captures stdout/stderr to step files. No tmux session.
func (wr *WorkflowRunner) executeAgentPrint(ctx context.Context, runID int64, step StepDef, sf stepFiles, repoPath string, env []string, agentCfg *AgentStepConfig, prompt string, stepHooks map[string]interface{}, result *StepResult) error {
	stepSettings, err := wr.settings(ctx)
	if err != nil {
		return fmt.Errorf("step %q: reading Coral's settings for the CLI path: %w", step.Name, err)
	}
	binPath, err := agent.FindCLI(stepSettings, agentCfg.AgentType)
	if err != nil {
		return fmt.Errorf("step %q: %w", step.Name, err)
	}
	// The run checked this at its start; a step that begins much later asks again (the answer is reused for a minute), so a CLI signed out meanwhile is not started (#5469).
	if err := agentsignin.RequireIn(ctx, agentCfg.AgentType, binPath, repoPath); err != nil {
		return fmt.Errorf("step %q: %w", step.Name, err)
	}

	// Claude gets the step's prompt on stdin, never in argv, where any local user could
	// read it in the process list: it can carry earlier steps' output or board content
	// (#3521). With tools or capabilities, the settings come from the agent
	// package as real argv (ClaudeAgent.PrintArgs); splitting the launch command's shell
	// line on spaces gave Claude env-var names and a "$(cat ...)" fragment instead.
	hasCfg := len(agentCfg.Tools) > 0 || agentCfg.Capabilities != nil
	var args []string
	var stdin io.Reader

	if agentCfg.AgentType == "claude" {
		if hasCfg {
			params := agent.LaunchParams{
				WorkingDir: repoPath,
				Flags:      agentCfg.Flags,
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
			var settingsFile string
			args, settingsFile, err = (&agent.ClaudeAgent{}).PrintArgs(params)
			if err != nil {
				return fmt.Errorf("step %q: %w", step.Name, err)
			}
			// The step's settings temp file goes once the step has run (Security's review).
			defer os.Remove(settingsFile)
		} else {
			args = append([]string{"--print", "--no-session-persistence"}, agentCfg.Flags...)
		}
		modelArgs, merr := agentmodels.ModelArgs(agentCfg.Model)
		if merr != nil {
			return fmt.Errorf("step %q: the model %w", step.Name, merr)
		}
		args = append(args, modelArgs...)
		stdin = strings.NewReader(prompt)
	} else {
		// Other CLIs: the argument list and where the prompt goes are the agent package's (StepPrintArgs): the prompt
		// can start with "-", and each CLI is given it where it can only be the prompt (#36).
		modelArgs, merr := agentmodels.ModelArgs(agentCfg.Model)
		if merr != nil {
			return fmt.Errorf("step %q: the model %w", step.Name, merr)
		}
		var in string
		args, in = agent.StepPrintArgs(agentCfg.AgentType, agentCfg.Flags, modelArgs, prompt)
		if in != "" {
			stdin = strings.NewReader(in)
		}
	}

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Stdin = stdin
	cmd.Dir = repoPath
	cmd.Env = append(agentenv.Filter(os.Environ()), env...) // #3389: no server-only settings
	configureChildProcess(cmd)

	// Capture stdout and stderr to files, through a pipe and a capped copy (#5079): a runaway command cannot fill the disk before its timeout. The child gets
	// the pipe's write end as a file, so Wait returns when it exits (a process it left behind keeps its end and is never killed for it).
	output, err := newStepOutput(sf)
	if err != nil {
		return err
	}
	output.attach(cmd)

	if err := cmd.Start(); err != nil {
		output.release()
		return fmt.Errorf("start agent: %w", err)
	}
	output.release()
	trackChildProcess(cmd)
	agentprio.ApplyGroup(cmd.Process.Pid) // #3437

	// Track active child for kill
	wr.mu.Lock()
	wr.activeChildren[runID] = &activeChild{stepType: "shell", cmd: cmd}
	wr.mu.Unlock()

	err = cmd.Wait()
	releaseChildProcess(cmd)
	output.finish(sf, result)

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
	sf.writeNew("exit_code", []byte(strconv.Itoa(exitCode)))
	result.OutputTail = sf.readTail("stdout.txt", 100)
	if exitCode != 0 {
		result.ErrorTail = sf.readTail("stderr.txt", 40)
	}

	// If output_artifact is set, copy stdout to it: inside the step's folder only, never
	// through a symlink (#3423, CRIT-02).
	if step.OutputArtifact != "" {
		if err := sf.writeArtifact(step.OutputArtifact); err != nil {
			slog.Warn("[workflow] output_artifact not written", "step", step.Name, "error", err)
			noteArtifactRefused(sf, step.OutputArtifact, err, result)
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
	modelArgs, merr := agentmodels.ModelArgs(agentCfg.Model)
	if merr != nil {
		return fmt.Errorf("step %q: the model %w", step.Name, merr)
	}
	flags := append([]string{}, modelArgs...)
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
		defer safego.Recover("background/workflow_runner:779")
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
	if len(stepCfg.Flags) > 0 {
		merged.Flags = stepCfg.Flags
	}
	return &merged
}
