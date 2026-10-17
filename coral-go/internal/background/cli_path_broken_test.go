//go:build !windows

package background

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agent"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/store"
)

// brokenClaudePath sets cli_path_claude to a path that can't be run (a wrapper that was moved), with a claude stub
// first on PATH that records every call (the #4381 env rules). It returns the store and the stub's call log.
func brokenClaudePath(t *testing.T) (*store.DB, string) {
	t.Helper()
	iso := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, iso)
	}
	t.Setenv("HOME", filepath.Join(iso, "home"))
	t.Setenv("USERPROFILE", filepath.Join(iso, "home"))
	t.Setenv("CORAL_DATA_DIR", filepath.Join(iso, "data"))
	bin := filepath.Join(iso, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o700))
	calls := filepath.Join(iso, "calls.log")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho \"claude $*\" >> '"+calls+"'\necho 9.9.9\n"), 0o700))
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	db := setupTestDB(t)
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), filepath.Join(iso, "moved", "claude-wrapper")))
	return db, calls
}

func assertNoCLIRan(t *testing.T, calls string) {
	t.Helper()
	data, err := os.ReadFile(calls)
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(data)), "the claude on PATH ran in place of the configured one")
}

// #4432 (Security's #4431 SHOULD 4): with a configured claude path that can't be run, no background job runs the claude
// on PATH instead. Each caller of FindCLI fails, or does without the CLI, as it does when there is no CLI.
func TestABrokenConfiguredPathRunsNoOtherCLI(t *testing.T) {
	db, calls := brokenClaudePath(t)
	ctx := context.Background()
	settings := settingsOf(t, db)

	_, err := checkClaudeCLI(ctx, settings) // the knowledge distiller
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude-wrapper")

	out, err := callClaude(ctx, settings, "user: fix the bug") // the session summarizer: the fallback summary
	require.NoError(t, err)
	assert.NotContains(t, out, "9.9.9")

	assert.Empty(t, resolveClaudeCLI(settings), "goal generation finds no CLI")
	assert.Empty(t, agent.CLIVersion(settings, at.Claude), "the version probe asks nothing")

	runner := NewWorkflowRunner(store.NewWorkflowStore(db), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)
	err = runner.executeAgentPrint(ctx, 1, StepDef{Name: "agent-step"}, testStepFiles(t), t.TempDir(), os.Environ(),
		&AgentStepConfig{AgentType: at.Claude}, "say hi", nil, &StepResult{})
	require.Error(t, err, "a workflow agent step fails")
	assert.Contains(t, err.Error(), "claude-wrapper")

	assertNoCLIRan(t, calls)
}
