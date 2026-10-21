package background

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/internal/terminalinput"
)

// ── Hooks Tests ─────────────────────────────────────────────

func TestParseStepHooks_Valid(t *testing.T) {
	raw := json.RawMessage(`{
		"Stop": [{"hooks": [{"type": "command", "command": "echo done"}]}],
		"StepComplete": [{"hooks": [{"type": "command", "command": "curl webhook"}]}]
	}`)
	hooks := parseStepHooks(raw)
	if hooks == nil {
		t.Fatal("parseStepHooks should return non-nil for valid JSON")
	}
	if _, ok := hooks["Stop"]; !ok {
		t.Error("hooks should contain Stop event")
	}
	if _, ok := hooks["StepComplete"]; !ok {
		t.Error("hooks should contain StepComplete event")
	}
}

func TestParseStepHooks_Nil(t *testing.T) {
	hooks := parseStepHooks(nil)
	if hooks != nil {
		t.Error("parseStepHooks(nil) should return nil")
	}
}

func TestParseStepHooks_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`{invalid`)
	hooks := parseStepHooks(raw)
	if hooks != nil {
		t.Error("parseStepHooks with invalid JSON should return nil")
	}
}

func TestFilterHooksForClaude(t *testing.T) {
	hooks := map[string]interface{}{
		"PreToolUse":   []interface{}{map[string]interface{}{"hooks": []interface{}{}}},
		"PostToolUse":  []interface{}{map[string]interface{}{"hooks": []interface{}{}}},
		"Stop":         []interface{}{map[string]interface{}{"hooks": []interface{}{}}},
		"StepComplete": []interface{}{map[string]interface{}{"hooks": []interface{}{}}},
		"StepFailed":   []interface{}{map[string]interface{}{"hooks": []interface{}{}}},
	}

	filtered := filterHooksForClaude(hooks)
	if filtered == nil {
		t.Fatal("filtered hooks should not be nil")
	}

	// Claude-native events should be present
	for _, event := range []string{"PreToolUse", "PostToolUse", "Stop"} {
		if _, ok := filtered[event]; !ok {
			t.Errorf("Claude-native event %q should be in filtered hooks", event)
		}
	}

	// Coral-level events should NOT be present
	for _, event := range []string{"StepComplete", "StepFailed"} {
		if _, ok := filtered[event]; ok {
			t.Errorf("Coral event %q should NOT be in filtered hooks", event)
		}
	}
}

func TestFilterHooksForClaude_NilInput(t *testing.T) {
	if filtered := filterHooksForClaude(nil); filtered != nil {
		t.Error("filterHooksForClaude(nil) should return nil")
	}
}

func TestFilterHooksForClaude_OnlyCoralEvents(t *testing.T) {
	hooks := map[string]interface{}{
		"StepComplete": []interface{}{map[string]interface{}{"hooks": []interface{}{}}},
	}
	if filtered := filterHooksForClaude(hooks); filtered != nil {
		t.Error("filterHooksForClaude with only Coral events should return nil")
	}
}

func TestFireHooks_ExecutesCommand(t *testing.T) {
	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, "hook_fired")

	hooks := map[string]interface{}{
		"StepComplete": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": "touch '" + filepath.ToSlash(markerFile) + "'", // sh keeps a quoted C:/... path (#3638)
					},
				},
			},
		},
	}

	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	runner := NewWorkflowRunner(wfStore, nil, nil, nil, nil, tmpDir, "localhost", 8420)

	runner.fireHooks(context.Background(), hooks, "StepComplete", t.TempDir(), nil)

	// Verify the hook command was executed
	if _, err := os.Stat(markerFile); os.IsNotExist(err) {
		t.Error("hook command should have created marker file")
	}
}

func TestFireHooks_SkipsUnmatchedEvent(t *testing.T) {
	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, "should_not_exist")

	hooks := map[string]interface{}{
		"StepComplete": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": "touch '" + filepath.ToSlash(markerFile) + "'", // sh keeps a quoted C:/... path (#3638)
					},
				},
			},
		},
	}

	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	runner := NewWorkflowRunner(wfStore, nil, nil, nil, nil, tmpDir, "localhost", 8420)

	// Fire a different event — should not execute the StepComplete hook
	runner.fireHooks(context.Background(), hooks, "StepFailed", t.TempDir(), nil)

	if _, err := os.Stat(markerFile); err == nil {
		t.Error("hook should NOT have fired for unmatched event")
	}
}

func TestFireHooks_PassesEnvVars(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "env_output")

	hooks := map[string]interface{}{
		"StepComplete": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": "echo $CORAL_STEP_NAME > '" + filepath.ToSlash(outputFile) + "'",
					},
				},
			},
		},
	}

	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	runner := NewWorkflowRunner(wfStore, nil, nil, nil, nil, tmpDir, "localhost", 8420)

	env := []string{"CORAL_STEP_NAME=test-step"}
	runner.fireHooks(context.Background(), hooks, "StepComplete", t.TempDir(), env)

	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read hook output: %v", err)
	}
	if !strings.Contains(string(data), "test-step") {
		t.Errorf("hook should have received CORAL_STEP_NAME env var, got: %q", string(data))
	}
}

func TestStepDef_HooksFieldParsed(t *testing.T) {
	stepJSON := `{
		"name": "test",
		"type": "shell",
		"command": "echo hi",
		"hooks": {
			"StepComplete": [{"hooks": [{"type": "command", "command": "echo done"}]}]
		}
	}`
	var step StepDef
	if err := json.Unmarshal([]byte(stepJSON), &step); err != nil {
		t.Fatalf("failed to unmarshal StepDef with hooks: %v", err)
	}
	if step.Hooks == nil {
		t.Error("StepDef.Hooks should not be nil")
	}
	hooks := parseStepHooks(step.Hooks)
	if hooks == nil {
		t.Error("parsed hooks should not be nil")
	}
	if _, ok := hooks["StepComplete"]; !ok {
		t.Error("parsed hooks should contain StepComplete")
	}
}

func TestStepStatusStr(t *testing.T) {
	if s := stepStatusStr(nil); s != "completed" {
		t.Errorf("expected completed, got %s", s)
	}
	if s := stepStatusStr(fmt.Errorf("fail")); s != "failed" {
		t.Errorf("expected failed, got %s", s)
	}
}

// wfMockRuntime implements AgentRuntime for workflow runner testing.
type wfMockRuntime struct {
	alive   map[string]bool
	spawned []string
	killed  []string
}

func newWfMockRuntime() *wfMockRuntime {
	return &wfMockRuntime{alive: make(map[string]bool)}
}

func (m *wfMockRuntime) SpawnAgent(_ context.Context, name, _, _, _ string) error {
	m.spawned = append(m.spawned, name)
	m.alive[name] = true
	return nil
}
func (m *wfMockRuntime) SendInput(_ context.Context, _ terminalinput.Origin, _, _ string) error {
	return nil
}
func (m *wfMockRuntime) KillAgent(_ context.Context, name string) error {
	m.killed = append(m.killed, name)
	delete(m.alive, name)
	return nil
}
func (m *wfMockRuntime) IsAlive(_ context.Context, name string) bool       { return m.alive[name] }
func (m *wfMockRuntime) ListAgents(_ context.Context) ([]AgentInfo, error) { return nil, nil }

func setupTestDB(t *testing.T) *store.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storetest.Open(t, dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func createTestWorkflow(t *testing.T, wfStore *store.WorkflowStore, repoPath string, stepsJSON string) *store.Workflow {
	t.Helper()
	wf := &store.Workflow{
		Name:         "test-workflow",
		Description:  "Test workflow",
		StepsJSON:    stepsJSON,
		RepoPath:     repoPath,
		MaxDurationS: 60,
		Enabled:      1,
	}
	created, err := wfStore.CreateWorkflow(context.Background(), wf)
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	return created
}

func TestExpandTemplates(t *testing.T) {
	env := []string{
		"CORAL_WORKFLOW_RUN_DIR=/tmp/runs/42",
		"CORAL_WORKFLOW_RUN_ID=42",
		"CORAL_WORKFLOW_STEP_DIR=/tmp/runs/42/step_1",
		"CORAL_PREV_DIR=/tmp/runs/42/step_0",
		"CORAL_PREV_STDOUT=/tmp/runs/42/step_0/stdout.txt",
		"CORAL_PREV_STDERR=/tmp/runs/42/step_0/stderr.txt",
		"CORAL_STEP_0_DIR=/tmp/runs/42/step_0",
		"CORAL_STEP_0_STDOUT=/tmp/runs/42/step_0/stdout.txt",
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"run_dir", "{{run_dir}}", "/tmp/runs/42"},
		{"run_id", "id={{run_id}}", "id=42"},
		{"step_dir", "{{step_dir}}", "/tmp/runs/42/step_1"},
		{"prev_stdout", "cat {{prev_stdout}}", "cat /tmp/runs/42/step_0/stdout.txt"},
		{"step_N_dir", "{{step_0_dir}}", "/tmp/runs/42/step_0"},
		{"step_N_stdout", "{{step_0_stdout}}", "/tmp/runs/42/step_0/stdout.txt"},
		{"unknown", "{{unknown}}", "{{unknown}}"},
		{"no templates", "echo hello", "echo hello"},
		{"multiple", "{{run_dir}}/{{run_id}}", "/tmp/runs/42/42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandTemplates(tt.input, env)
			if got != tt.expected {
				t.Errorf("expandTemplates(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestMergeAgentConfig(t *testing.T) {
	t.Run("both nil", func(t *testing.T) {
		if got := mergeAgentConfig(nil, nil); got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("default only", func(t *testing.T) {
		def := &AgentStepConfig{AgentType: "claude", Model: "sonnet"}
		got := mergeAgentConfig(def, nil)
		if got.AgentType != "claude" || got.Model != "sonnet" {
			t.Errorf("expected default values, got %+v", got)
		}
	})

	t.Run("step only", func(t *testing.T) {
		step := &AgentStepConfig{AgentType: "gemini"}
		got := mergeAgentConfig(nil, step)
		if got.AgentType != "gemini" {
			t.Errorf("expected step values, got %+v", got)
		}
	})

	t.Run("step overrides default", func(t *testing.T) {
		def := &AgentStepConfig{AgentType: "claude", Model: "sonnet"}
		step := &AgentStepConfig{Model: "opus"}
		got := mergeAgentConfig(def, step)
		if got.AgentType != "claude" || got.Model != "opus" {
			t.Errorf("expected merged values, got %+v", got)
		}
	})
}

func TestBuildStepEnv(t *testing.T) {
	wf := &store.Workflow{
		Name:     "test-wf",
		RepoPath: "/repo",
	}
	wr := &WorkflowRunner{}
	runDir := "/repo/.coral/workflows/runs/1"
	stepDir := "/repo/.coral/workflows/runs/1/step_2"

	env := wr.buildStepEnv(wf, 1, runDir, 2, stepDir, 5, nil)

	envMap := make(map[string]string)
	for _, e := range env {
		parts := splitFirst(e, '=')
		envMap[parts[0]] = parts[1]
	}

	if envMap["CORAL_WORKFLOW_NAME"] != "test-wf" {
		t.Error("missing CORAL_WORKFLOW_NAME")
	}
	if envMap["CORAL_WORKFLOW_RUN_DIR"] != runDir {
		t.Error("bad CORAL_WORKFLOW_RUN_DIR")
	}
	if envMap["CORAL_WORKFLOW_STEP"] != "2" {
		t.Error("bad CORAL_WORKFLOW_STEP")
	}
	if envMap["CORAL_PREV_DIR"] != filepath.FromSlash("/repo/.coral/workflows/runs/1/step_1") {
		t.Error("bad CORAL_PREV_DIR")
	}
	if _, ok := envMap["CORAL_STEP_0_DIR"]; !ok {
		t.Error("missing CORAL_STEP_0_DIR")
	}
	if _, ok := envMap["CORAL_STEP_1_DIR"]; !ok {
		t.Error("missing CORAL_STEP_1_DIR")
	}
}

func TestBuildStepEnvStep0NoPrev(t *testing.T) {
	wf := &store.Workflow{Name: "test", RepoPath: "/repo"}
	wr := &WorkflowRunner{}
	env := wr.buildStepEnv(wf, 1, "/rundir", 0, "/stepdir", 3, nil)

	for _, e := range env {
		if len(e) > 10 && e[:10] == "CORAL_PREV" {
			t.Errorf("step 0 should not have CORAL_PREV_* env vars, got %s", e)
		}
	}
}

func TestListStepFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "stdout.txt"), []byte("out"), 0644)
	os.WriteFile(filepath.Join(dir, "stderr.txt"), []byte("err"), 0644)
	os.MkdirAll(filepath.Join(dir, "artifacts"), 0755)
	os.WriteFile(filepath.Join(dir, "artifacts", "report.json"), []byte("{}"), 0644)

	files := listStepFiles(dir)
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(files), files)
	}

	has := map[string]bool{}
	for _, f := range files {
		has[f] = true
	}
	for _, expected := range []string{"stdout.txt", "stderr.txt", filepath.FromSlash("artifacts/report.json")} {
		if !has[expected] {
			t.Errorf("missing expected file %s in %v", expected, files)
		}
	}
}

func TestReadTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.txt")
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = "line " + strconv.Itoa(i)
	}
	os.WriteFile(path, []byte(joinLines(lines)), 0644)

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tail := tailOf(f, 5)
	if tail == "" {
		t.Fatal("expected non-empty tail")
	}
	// Should contain the last few lines
	if !strings.Contains(tail, "line 199") {
		t.Errorf("expected tail to contain 'line 199', got %q", tail)
	}
}

func TestShellStepExecution(t *testing.T) {
	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	launcher := &AgentLauncher{runtime: rt}
	dataDir := t.TempDir()
	runner := NewWorkflowRunner(wfStore, launcher, rt, nil, nil, dataDir, "localhost", 8420)

	repoPath := t.TempDir()
	steps := []StepDef{
		{Name: "echo", Type: "shell", Command: "echo hello world"},
	}
	stepsJSON, _ := json.Marshal(steps)

	wf := createTestWorkflow(t, wfStore, repoPath, string(stepsJSON))

	trigger := "api"
	run, err := runner.TriggerWorkflow(context.Background(), wf, trigger, nil)
	if err != nil {
		t.Fatalf("trigger workflow: %v", err)
	}

	// Wait for execution to complete
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
		if r != nil && (r.Status == "completed" || r.Status == "failed") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	finalRun, err := wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finalRun.Status != "completed" {
		t.Errorf("expected completed, got %s (error: %v)", finalRun.Status, finalRun.ErrorMsg)
	}

	// Verify stdout was captured
	stdoutPath := filepath.Join(dataDir, "workflows", "runs", strconv.FormatInt(run.ID, 10), "step_0", "stdout.txt")
	content, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if got := string(content); got != "hello world\n" {
		t.Errorf("stdout = %q, want %q", got, "hello world\n")
	}

	// Verify step results
	var results []StepResult
	json.Unmarshal([]byte(finalRun.StepResults), &results)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != "completed" {
		t.Errorf("step status = %s, want completed", results[0].Status)
	}
	if results[0].ExitCode == nil || *results[0].ExitCode != 0 {
		t.Errorf("step exit_code = %v, want 0", results[0].ExitCode)
	}
}

func TestShellStepFailure(t *testing.T) {
	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	launcher := &AgentLauncher{runtime: rt}
	runner := NewWorkflowRunner(wfStore, launcher, rt, nil, nil, t.TempDir(), "localhost", 8420)

	repoPath := t.TempDir()
	steps := []StepDef{
		{Name: "fail", Type: "shell", Command: "exit 1"},
		{Name: "skip", Type: "shell", Command: "echo should not run"},
	}
	stepsJSON, _ := json.Marshal(steps)

	wf := createTestWorkflow(t, wfStore, repoPath, string(stepsJSON))
	run, err := runner.TriggerWorkflow(context.Background(), wf, "api", nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	waitForRun(t, wfStore, run.ID, 10*time.Second)

	finalRun, _ := wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
	if finalRun.Status != "failed" {
		t.Errorf("expected failed, got %s", finalRun.Status)
	}

	var results []StepResult
	json.Unmarshal([]byte(finalRun.StepResults), &results)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Status != "failed" {
		t.Errorf("step 0 status = %s, want failed", results[0].Status)
	}
	if results[1].Status != "skipped" {
		t.Errorf("step 1 status = %s, want skipped", results[1].Status)
	}
}

func TestShellStepContinueOnFailure(t *testing.T) {
	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	launcher := &AgentLauncher{runtime: rt}
	runner := NewWorkflowRunner(wfStore, launcher, rt, nil, nil, t.TempDir(), "localhost", 8420)

	repoPath := t.TempDir()
	steps := []StepDef{
		{Name: "fail", Type: "shell", Command: "exit 1", ContinueOnFailure: true},
		{Name: "continue", Type: "shell", Command: "echo continued"},
	}
	stepsJSON, _ := json.Marshal(steps)

	wf := createTestWorkflow(t, wfStore, repoPath, string(stepsJSON))
	run, err := runner.TriggerWorkflow(context.Background(), wf, "api", nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	waitForRun(t, wfStore, run.ID, 10*time.Second)

	finalRun, _ := wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
	if finalRun.Status != "completed" {
		t.Errorf("expected completed, got %s", finalRun.Status)
	}

	var results []StepResult
	json.Unmarshal([]byte(finalRun.StepResults), &results)
	if results[0].Status != "failed" {
		t.Errorf("step 0 status = %s, want failed", results[0].Status)
	}
	if results[1].Status != "completed" {
		t.Errorf("step 1 status = %s, want completed", results[1].Status)
	}
}

func TestMultiStepWithTemplates(t *testing.T) {
	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	launcher := &AgentLauncher{runtime: rt}
	dataDir := t.TempDir()
	runner := NewWorkflowRunner(wfStore, launcher, rt, nil, nil, dataDir, "localhost", 8420)

	repoPath := t.TempDir()
	steps := []StepDef{
		{Name: "produce", Type: "shell", Command: "echo test-output"},
		{Name: "consume", Type: "shell", Command: "cat $CORAL_PREV_STDOUT"},
	}
	stepsJSON, _ := json.Marshal(steps)

	wf := createTestWorkflow(t, wfStore, repoPath, string(stepsJSON))
	run, err := runner.TriggerWorkflow(context.Background(), wf, "api", nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	waitForRun(t, wfStore, run.ID, 10*time.Second)

	finalRun, _ := wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
	if finalRun.Status != "completed" {
		t.Errorf("expected completed, got %s (error: %v)", finalRun.Status, finalRun.ErrorMsg)
	}

	// Step 1 should have consumed step 0's output
	step1Stdout := filepath.Join(dataDir, "workflows", "runs", strconv.FormatInt(run.ID, 10), "step_1", "stdout.txt")
	content, err := os.ReadFile(step1Stdout)
	if err != nil {
		t.Fatalf("read step 1 stdout: %v", err)
	}
	if got := string(content); got != "test-output\n" {
		t.Errorf("step 1 stdout = %q, want %q", got, "test-output\n")
	}
}

func TestKillRun(t *testing.T) {
	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	launcher := &AgentLauncher{runtime: rt}
	runner := NewWorkflowRunner(wfStore, launcher, rt, nil, nil, t.TempDir(), "localhost", 8420)

	repoPath := t.TempDir()
	steps := []StepDef{
		{Name: "slow", Type: "shell", Command: "sleep 60"},
	}
	stepsJSON, _ := json.Marshal(steps)

	wf := createTestWorkflow(t, wfStore, repoPath, string(stepsJSON))
	run, err := runner.TriggerWorkflow(context.Background(), wf, "api", nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	// Wait for run to start
	time.Sleep(500 * time.Millisecond)

	killed := runner.KillRun(context.Background(), run.ID)
	if !killed {
		t.Error("expected kill to return true")
	}

	// Wait for the run to finish (its last status write), not a fixed time: under
	// -race the step's failure used to land after KillRun's "killed" (#3474).
	deadline := time.Now().Add(10 * time.Second)
	for runner.IsRunActive(run.ID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runner.IsRunActive(run.ID) {
		t.Fatal("the run never finished after KillRun")
	}

	finalRun, _ := wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
	if finalRun.Status != "killed" {
		t.Errorf("expected killed, got %s", finalRun.Status)
	}
}

// #3474: KillRun kills the step's process before it marks the run killed, so the
// step's failure reaches the run loop first (always, under -race on epoch). It must
// not be recorded over the kill: the run ends "killed", not "failed".
func TestKillRunIsNotOverwrittenByTheStepsFailure(t *testing.T) {
	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	runner := NewWorkflowRunner(wfStore, &AgentLauncher{runtime: rt}, rt, nil, nil, t.TempDir(), "localhost", 8420)
	stepsJSON, _ := json.Marshal([]StepDef{{Name: "slow", Type: "shell", Command: "sleep 60"}})
	wf := createTestWorkflow(t, wfStore, t.TempDir(), string(stepsJSON))
	run, err := runner.TriggerWorkflow(context.Background(), wf, "api", nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		started := runner.activeChildren[run.ID] != nil
		runner.mu.Unlock()
		if started {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// KillRun waits until the loop has checked; the loop writes only after KillRun has
	// marked the run killed.
	checked := make(chan struct{})
	killRunPause = func() {
		select {
		case <-checked:
		case <-time.After(10 * time.Second):
		}
	}
	afterKillCheck = func(id int64) {
		if id != run.ID {
			return
		}
		close(checked)
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			if r, _ := wfStore.GetWorkflowRunDirect(context.Background(), id); r != nil && r.Status == "killed" {
				return
			}
		}
	}
	t.Cleanup(func() { killRunPause, afterKillCheck = nil, nil })
	if !runner.KillRun(context.Background(), run.ID) {
		t.Fatal("expected kill to return true")
	}
	for end := time.Now().Add(10 * time.Second); runner.IsRunActive(run.ID) && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	if runner.IsRunActive(run.ID) {
		t.Fatal("the run never finished after its step was killed")
	}
	finalRun, _ := wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
	if finalRun.Status != "killed" {
		t.Errorf("expected killed, got %s", finalRun.Status)
	}
}

func TestArtifactDirectoryCreation(t *testing.T) {
	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	launcher := &AgentLauncher{runtime: rt}
	dataDir := t.TempDir()
	runner := NewWorkflowRunner(wfStore, launcher, rt, nil, nil, dataDir, "localhost", 8420)

	repoPath := t.TempDir()
	steps := []StepDef{
		{Name: "write-artifact", Type: "shell", Command: "echo report > $CORAL_WORKFLOW_STEP_DIR/artifacts/report.txt"},
	}
	stepsJSON, _ := json.Marshal(steps)

	wf := createTestWorkflow(t, wfStore, repoPath, string(stepsJSON))
	run, err := runner.TriggerWorkflow(context.Background(), wf, "api", nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	waitForRun(t, wfStore, run.ID, 10*time.Second)

	// Check artifact file exists
	artifactPath := filepath.Join(dataDir, "workflows", "runs", strconv.FormatInt(run.ID, 10), "step_0", "artifacts", "report.txt")
	if _, err := os.Stat(artifactPath); os.IsNotExist(err) {
		t.Error("expected artifact file to exist")
	}

	// Check step results include the artifact file
	finalRun, _ := wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
	var results []StepResult
	json.Unmarshal([]byte(finalRun.StepResults), &results)

	has := false
	for _, f := range results[0].Files {
		if f == filepath.FromSlash("artifacts/report.txt") {
			has = true
		}
	}
	if !has {
		t.Errorf("expected artifacts/report.txt in files list, got %v", results[0].Files)
	}
}

func TestContextJSON(t *testing.T) {
	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	launcher := &AgentLauncher{runtime: rt}
	dataDir := t.TempDir()
	runner := NewWorkflowRunner(wfStore, launcher, rt, nil, nil, dataDir, "localhost", 8420)

	repoPath := t.TempDir()
	steps := []StepDef{
		{Name: "noop", Type: "shell", Command: "true"},
	}
	stepsJSON, _ := json.Marshal(steps)

	wf := createTestWorkflow(t, wfStore, repoPath, string(stepsJSON))
	run, err := runner.TriggerWorkflow(context.Background(), wf, "api", nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	waitForRun(t, wfStore, run.ID, 10*time.Second)

	contextPath := filepath.Join(dataDir, "workflows", "runs", strconv.FormatInt(run.ID, 10), "context.json")
	data, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("read context.json: %v", err)
	}

	var ctx map[string]interface{}
	if err := json.Unmarshal(data, &ctx); err != nil {
		t.Fatalf("parse context.json: %v", err)
	}
	if ctx["workflow_name"] != "test-workflow" {
		t.Errorf("context workflow_name = %v, want test-workflow", ctx["workflow_name"])
	}
}

// --- helpers ---

func waitForRun(t *testing.T, wfStore *store.WorkflowStore, runID int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r, _ := wfStore.GetWorkflowRunDirect(context.Background(), runID)
		if r != nil && r.Status != "pending" && r.Status != "running" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("run %d did not finish within %v", runID, timeout)
}

func splitFirst(s string, sep byte) [2]string {
	idx := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			idx = i
			break
		}
	}
	return [2]string{s[:idx], s[idx+1:]}
}

func joinLines(lines []string) string {
	result := ""
	for _, l := range lines {
		result += l + "\n"
	}
	return result
}
