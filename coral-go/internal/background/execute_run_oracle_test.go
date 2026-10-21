package background

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

var runStamp = regexp.MustCompile(`\d{4}-\d\d-\d\dT[0-9:.]+(Z|[+-]\d\d:\d\d)?`)

// The outcomes the executeRun oracle must reach, each counted from the finished run, with a floor.
var executeRunOutcomes = map[string]int{
	"steps that don't parse":                  5,
	"a default agent that doesn't parse":      5,
	"completed":                               40,
	"a step failed, the rest skipped":         30,
	"a step failed and the run went on":       20,
	"an unknown step type":                    10,
	"retired connections":                     10,
	"StepComplete hooks fired":                10,
	"StepFailed hooks fired":                  10,
	"files a step made, listed":               30,
	"the run's time limit, killed":            3,
	"a step's folder taken, the rest skipped": 5,
}

// #4845 (executeRun): WorkflowRunner.executeRun split into parseRunDefinition, startRun, endCancelledRun, runStep and
// failRemaining. Two runners, each with its own store and data folder, run the same random workflows: the old loop
// and the new one end with the same run row (status, error, current step, every step's result) and the same files.
// Agent steps are not run (they need a live agent): their dispatch to executeAgentStep is unchanged.
func TestExecuteRunAgreesWithBefore4845(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the steps are POSIX shell commands")
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(new(strings.Builder), nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	type side struct {
		wr      *WorkflowRunner
		wfStore *store.WorkflowStore
		dataDir string
		repo    string
	}
	// Each seed has its own two runners, stores and folders, so the seeds run in parallel (#4982: the 260 seeds ran one after
	// another, and the ones with a one-second time limit really sleep).
	newSides := func(t *testing.T) [2]side {
		var sides [2]side
		for i := range sides {
			wfStore := store.NewWorkflowStore(setupTestDB(t))
			dataDir, repo := t.TempDir(), t.TempDir()
			rt := newWfMockRuntime()
			sides[i] = side{NewWorkflowRunner(wfStore, &AgentLauncher{runtime: rt}, rt, nil, nil, dataDir, "localhost", 8420), wfStore, dataDir, repo}
		}
		return sides
	}
	commands := []string{"echo hi > out.txt", "exit 1", "echo step $CORAL_WORKFLOW_STEP of $CORAL_WORKFLOW_NAME > step.txt", "true", "printf data > a.txt; mkdir sub; printf x > sub/b.txt",
		"echo to stderr >&2; exit 3", "echo $CORAL_WORKFLOW_RUN_ID > run.txt",
		`mkdir "$CORAL_WORKFLOW_RUN_DIR/step_$((CORAL_WORKFLOW_STEP+1))"`} // takes the next step's folder: that step fails before it runs
	hook := func(event string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{event: []any{map[string]any{"hooks": []any{map[string]any{"type": "command",
			"command": "echo " + event + " $CORAL_STEP_STATUS >> \"$CORAL_WORKFLOW_RUN_DIR/hooks.log\""}}}}})
		return b
	}
	var mu sync.Mutex
	counts := map[string]int{}
	bump := func(k string) { mu.Lock(); counts[k]++; mu.Unlock() }
	t.Run("seeds", func(t *testing.T) {
		for seed := int64(1); seed <= 260; seed++ {
			t.Run(fmt.Sprint(seed), func(t *testing.T) {
				t.Parallel()
				sides := newSides(t)
				rng := rand.New(rand.NewSource(seed))
				var steps []StepDef
				for k := 1 + rng.Intn(4); k > 0; k-- {
					st := StepDef{Name: fmt.Sprintf("s%d", k), Type: "shell", Command: commands[rng.Intn(len(commands))], ContinueOnFailure: rng.Intn(2) == 0}
					switch rng.Intn(12) {
					case 0:
						st.Type = "dance"
					case 1:
						st.Connections = []string{"github"}
					case 2, 3:
						event := []string{"StepComplete", "StepFailed"}[rng.Intn(2)]
						st.Hooks = hook(event)
						if event == "StepFailed" {
							st.Command = "exit 1" // so the hook it names fires
						}
					}
					steps = append(steps, st)
				}
				stepsJSON, _ := json.Marshal(steps)
				wfDef := store.Workflow{Name: fmt.Sprintf("wf-%d", seed), StepsJSON: string(stepsJSON), MaxDurationS: 60, Enabled: 1}
				switch rng.Intn(20) {
				case 0:
					wfDef.StepsJSON = "[{not json"
				case 1:
					wfDef.DefaultAgentJSON = "{nope"
				case 2:
					wfDef.MaxDurationS, wfDef.StepsJSON = 1, `[{"name":"slow","type":"shell","command":"sleep 2","continue_on_failure":true},{"name":"after","type":"shell","command":"true"}]`
				}
				var states [2]string
				for i, s := range sides {
					wf := wfDef
					wf.RepoPath = s.repo
					created, err := s.wfStore.CreateWorkflow(context.Background(), &wf)
					require.NoError(t, err)
					run, err := s.wfStore.CreateWorkflowRun(context.Background(), created.ID, "api", nil)
					require.NoError(t, err)
					if i == 0 {
						s.wr.executeRunBefore4845(run.ID, created)
					} else {
						s.wr.executeRun(run.ID, created)
					}
					got, err := s.wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
					require.NoError(t, err)
					norm := func(x string) string {
						x = strings.ReplaceAll(x, s.dataDir, "<data>")
						x = strings.ReplaceAll(x, s.repo, "<repo>")
						x = regexp.MustCompile(`"duration_s":[0-9.e+-]+`).ReplaceAllString(x, `"duration_s":0`)
						return runStamp.ReplaceAllString(x, "<t>")
					}
					var files []string
					runDir := filepath.Join(s.dataDir, "workflows", "runs", fmt.Sprint(run.ID))
					_ = filepath.WalkDir(runDir, func(p string, d fs.DirEntry, err error) error {
						if err != nil || d.IsDir() {
							return nil
						}
						b, _ := os.ReadFile(p)
						rel, _ := filepath.Rel(runDir, p)
						files = append(files, rel+"="+norm(string(b)))
						return nil
					})
					errMsg := ""
					if got.ErrorMsg != nil {
						errMsg = *got.ErrorMsg
					}
					states[i] = fmt.Sprintf("status=%s step=%d err=%s results=%s files=%q", got.Status, got.CurrentStep, norm(errMsg), norm(got.StepResults), files)
				}
				label := fmt.Sprintf("seed %d %s", seed, wfDef.StepsJSON)
				require.Equal(t, states[0], states[1], label)

				st := states[1]
				has := func(x string) bool { return strings.Contains(st, x) }
				switch {
				case has("failed to parse steps"):
					bump("steps that don't parse")
				case has("failed to parse default_agent"):
					bump("a default agent that doesn't parse")
				case has("status=completed"):
					bump("completed")
				case has("workflow timeout exceeded"):
					bump("the run's time limit, killed")
				}
				if has("status=failed") && has(`"status":"skipped"`) {
					bump("a step failed, the rest skipped")
				}
				if has(`"status":"failed"`) && has("status=completed") {
					bump("a step failed and the run went on")
				}
				if has("already exists or can't be made") && has(`"status":"skipped"`) {
					bump("a step's folder taken, the rest skipped")
				}
				if has("unknown step type") {
					bump("an unknown step type")
				}
				if has("retired Connected Apps") {
					bump("retired connections")
				}
				if has("StepComplete completed") {
					bump("StepComplete hooks fired")
				}
				if has("StepFailed failed") {
					bump("StepFailed hooks fired")
				}
				if has("sub/b.txt") || has("out.txt") {
					bump("files a step made, listed")
				}
			})
		}
	})
	t.Logf("compared, per outcome: %v", counts)
	for name, floor := range executeRunOutcomes {
		require.GreaterOrEqual(t, counts[name], floor, "%q went (almost) unexercised", name)
	}
	_ = time.Second
}

// executeRunBefore4845 is WorkflowRunner.executeRun as it was before #4845.
func (wr *WorkflowRunner) executeRunBefore4845(runID int64, workflow *store.Workflow) {
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

	// Create the run folder, fresh, and hold it open: every step file is made and read
	// through it, never by a path an earlier step could have planted a symlink on (#3587).
	rf, err := openRunFolder(wr.dataDir, runID)
	if err != nil {
		errMsg := fmt.Sprintf("failed to create run directory: %v", err)
		wr.store.SetRunStatus(ctx, runID, "failed", &errMsg)
		return
	}
	defer rf.Close()
	runDir := rf.dir

	// Write context.json
	contextData, _ := json.MarshalIndent(map[string]interface{}{
		"workflow_name": workflow.Name,
		"workflow_id":   workflow.ID,
		"run_id":        runID,
		"steps":         steps,
	}, "", "  ")
	if err := rf.writeNew("context.json", contextData); err != nil {
		slog.Warn("[workflow] context.json not written", "run", runID, "error", err)
	}

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

		// Create step directory: a new one (one an earlier step made fails this step).
		sf, serr := rf.step(i)
		stepDir := sf.dir
		if serr != nil {
			errMsg := fmt.Sprintf("step %d (%s) failed: %v", i, step.Name, serr)
			results[i].Status = "failed"
			results[i].OutputTail = serr.Error()
			for j := i + 1; j < len(steps); j++ {
				results[j].Status = "skipped"
			}
			wr.persistResults(context.Background(), runID, i, results)
			wr.store.SetRunStatus(context.Background(), runID, "failed", &errMsg)
			return
		}

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
			stepErr = wr.executeShellStep(ctx, runID, step, sf, workflow.RepoPath, env, &results[i])
		case "agent":
			stepErr = wr.executeAgentStep(ctx, runID, step, sf, workflow.RepoPath, env, defaultAgent, &results[i])
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
				wr.fireHooks(ctx, stepHooks, "StepFailed", hookDir(workflow.RepoPath, stepDir), statusEnv)
			} else {
				wr.fireHooks(ctx, stepHooks, "StepComplete", hookDir(workflow.RepoPath, stepDir), statusEnv)
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
				killed := wr.isRunKilled(runID)
				if afterKillCheck != nil {
					afterKillCheck(runID)
				}
				if !killed {
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
