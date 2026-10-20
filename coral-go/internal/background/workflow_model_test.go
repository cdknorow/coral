package background

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/store"
)

// #5480: a workflow step's model becomes the CLI's --model argument only through agentmodels.ModelArgs: a value that is not a model id (flag-shaped, spaced) fails the step before
// anything is started, and a valid one (a custom model with [ ] in it) is one literal argument.
func TestWorkflowStepModelIsCheckedBeforeItReachesTheCLI(t *testing.T) {
	db, path := fakeClaude(t)
	dir := t.TempDir()
	argsFile := dir + "/args"
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 9.9.9; exit 0; fi\nif [ \"$1\" = auth ]; then exit 0; fi\nprintf '%s\\n' \"$@\" > '"+argsFile+"'\ncat > /dev/null\n"), 0o755))
	runner := NewWorkflowRunner(store.NewWorkflowStore(db), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)

	for _, bad := range []string{"--dangerously-skip-permissions", "a b", "-x", "a\nb", "m;touch x"} {
		os.Remove(argsFile)
		err := runner.executeAgentPrint(context.Background(), 1, StepDef{Name: "s"}, testStepFiles(t), t.TempDir(), os.Environ(), &AgentStepConfig{AgentType: at.Claude, Model: bad}, "p", nil, &StepResult{})
		require.Error(t, err, "%q", bad)
		assert.Contains(t, err.Error(), "not a valid model id")
		_, statErr := os.Stat(argsFile)
		assert.True(t, os.IsNotExist(statErr), "%q: the CLI was never started", bad)
	}
	require.NoError(t, runner.executeAgentPrint(context.Background(), 1, StepDef{Name: "s"}, testStepFiles(t), t.TempDir(), os.Environ(), &AgentStepConfig{AgentType: at.Claude, Model: "claude-sonnet-4-5[1m]"}, "p", nil, &StepResult{}))
	got, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Equal(t, []string{"--print", "--no-session-persistence", "--model", "claude-sonnet-4-5[1m]"}, strings.Split(strings.TrimSpace(string(got)), "\n"))
}
