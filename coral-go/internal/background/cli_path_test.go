package background

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agent"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/store"
)

// fakeClaude writes a claude stand-in outside PATH that prints its arguments,
// points the cli_path_claude setting at it, and leaves PATH without claude:
// the macOS app started from Finder, where agents launch but a bare "claude"
// is not found (#3169).
func fakeClaude(t *testing.T) (db *store.DB, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin") // sh, no claude
	path = filepath.Join(t.TempDir(), "claude-custom")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 9.9.9; exit 0; fi\necho \"FAKE-CLAUDE $*\"\n"), 0o755))
	db = setupTestDB(t)
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), path))
	return db, path
}

func settingsOf(t *testing.T, db *store.DB) map[string]string {
	t.Helper()
	s, err := store.NewSessionStore(db).GetSettings(context.Background())
	require.NoError(t, err)
	return s
}

// Team Knowledge Distill's CLI check uses the configured path.
func TestDistillUsesConfiguredClaudePath(t *testing.T) {
	db, path := fakeClaude(t)
	got, err := checkClaudeCLI(context.Background(), settingsOf(t, db))
	require.NoError(t, err)
	assert.Equal(t, path, got)
}

// The session summarizer runs the configured claude, not a fallback summary.
func TestSummarizerUsesConfiguredClaudePath(t *testing.T) {
	db, _ := fakeClaude(t)
	out, err := callClaude(context.Background(), settingsOf(t, db), "user: fix the bug")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "FAKE-CLAUDE --print"), "ran the configured binary: %q", out)
}

// An agent workflow step runs the configured claude (it failed with "agent
// binary \"claude\" not found" when PATH had none).
func TestWorkflowAgentStepUsesConfiguredClaudePath(t *testing.T) {
	db, _ := fakeClaude(t)
	runner := NewWorkflowRunner(store.NewWorkflowStore(db), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)
	sf := testStepFiles(t)
	result := &StepResult{}
	err := runner.executeAgentPrint(context.Background(), 1, StepDef{Name: "agent-step"}, sf, t.TempDir(), os.Environ(),
		&AgentStepConfig{AgentType: at.Claude}, "say hi", nil, result)
	require.NoError(t, err)
	out, err := os.ReadFile(filepath.Join(sf.dir, "stdout.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(out), "FAKE-CLAUDE", "the step ran the configured binary")
	require.NotNil(t, result.ExitCode)
	assert.Equal(t, 0, *result.ExitCode)
}

// Goal generation keeps resolving the same way (it already honoured the setting).
func TestGoalsResolveConfiguredClaudePath(t *testing.T) {
	db, path := fakeClaude(t)
	assert.Equal(t, path, resolveClaudeCLI(settingsOf(t, db)))
}

// With no CLI anywhere, an agent step fails with FindCLI's message once,
// prefixed by the step name (not "not found: … not found") (#3169).
func TestWorkflowAgentStepWithoutCLIReportsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH layout")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	runner := NewWorkflowRunner(store.NewWorkflowStore(setupTestDB(t)), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)
	err := runner.executeAgentPrint(context.Background(), 1, StepDef{Name: "agent-step"}, testStepFiles(t), t.TempDir(), nil,
		&AgentStepConfig{AgentType: "zz-no-such-agent"}, "hi", nil, &StepResult{})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), `step "agent-step": `), err.Error())
	assert.Equal(t, 1, strings.Count(err.Error(), "not found"), err.Error())
	assert.Contains(t, err.Error(), "Settings > CLI paths")
}

// testStepFiles is a fresh run folder's step_0, for calling a step executor directly.
func testStepFiles(t *testing.T) stepFiles {
	t.Helper()
	rf, err := openRunFolder(t.TempDir(), 1)
	require.NoError(t, err)
	t.Cleanup(func() { rf.Close() })
	sf, err := rf.step(0)
	require.NoError(t, err)
	return sf
}
