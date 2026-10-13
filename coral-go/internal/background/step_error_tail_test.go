package background

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// #3170: a failed step keeps the end of its stderr in the result, so the run view can show why it
// failed; a passing step does not carry its stderr (warnings on success are not an error).
func TestShellStepKeepsTheErrorTailOnlyWhenItFails(t *testing.T) {
	wr := &WorkflowRunner{activeChildren: map[int64]*activeChild{}}
	run := func(cmd string) *StepResult {
		dir := t.TempDir()
		res := &StepResult{}
		_ = wr.executeShellStep(context.Background(), 1, StepDef{Name: "s", Type: "shell", Command: cmd}, dir, dir, nil, res)
		return res
	}

	failed := run(`echo out; echo warming up >&2; echo "boom: config missing" >&2; exit 3`)
	require.NotNil(t, failed.ExitCode)
	require.Equal(t, 3, *failed.ExitCode)
	require.Contains(t, failed.OutputTail, "out")
	require.Contains(t, failed.ErrorTail, "warming up\nboom: config missing")

	ok := run(`echo fine; echo just a warning >&2`)
	require.Equal(t, 0, *ok.ExitCode)
	require.Empty(t, ok.ErrorTail, "a passing step's stderr is not an error")
}
