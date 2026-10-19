//go:build unix

package background

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withStepOutputCap(t *testing.T, n int64) {
	old := stepOutputCap
	stepOutputCap = n
	t.Cleanup(func() { stepOutputCap = old })
}

func runCappedShell(t *testing.T, step StepDef) (*StepResult, stepFiles, error) {
	t.Helper()
	wr := &WorkflowRunner{activeChildren: map[int64]*activeChild{}}
	sf := testStepFiles(t)
	res := &StepResult{}
	err := wr.executeShellStep(context.Background(), 1, step, sf, t.TempDir(), nil, res)
	return res, sf, err
}

func fileSize(t *testing.T, sf stepFiles, name string) int64 {
	t.Helper()
	fi, err := os.Stat(filepath.Join(sf.dir, name))
	require.NoError(t, err)
	return fi.Size()
}

// A step that prints past the cap leaves its files at the cap (and a line saying so), still succeeds, and the result says the output was cut (#5079).
func TestShellStepOutputIsCappedAndSaysSo(t *testing.T) {
	const cap = 1 << 20
	withStepOutputCap(t, cap)
	res, sf, err := runCappedShell(t, StepDef{Name: "loud", Type: "shell",
		Command: "head -c 3145728 /dev/zero | tr '\\0' a; head -c 2097152 /dev/zero | tr '\\0' b >&2"})
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	assert.Equal(t, 0, *res.ExitCode)
	for _, name := range []string{"stdout.txt", "stderr.txt"} {
		size := fileSize(t, sf, name)
		assert.GreaterOrEqual(t, size, int64(cap), name)
		assert.Less(t, size, int64(cap)+512, "%s holds the cap and one line of note, not the 3 MiB", name)
	}
	assert.Contains(t, res.Note, "output truncated: stdout.txt and stderr.txt")
}

// Output under the cap is whole and unmarked.
func TestShellStepUnderTheCapIsNotMarked(t *testing.T) {
	withStepOutputCap(t, 1<<20)
	res, sf, err := runCappedShell(t, StepDef{Name: "quiet", Type: "shell", Command: "echo hello; echo careful >&2"})
	require.NoError(t, err)
	assert.Empty(t, res.Note)
	assert.EqualValues(t, len("hello\n"), fileSize(t, sf, "stdout.txt"))
	assert.Contains(t, res.OutputTail, "hello")
}

// A runaway command is still ended at the step's timeout, with its group, and what it wrote stays at the cap.
func TestRunawayShellStepIsEndedAtItsTimeoutWithinTheCap(t *testing.T) {
	withStepOutputCap(t, 1<<20)
	start := time.Now()
	res, sf, err := runCappedShell(t, StepDef{Name: "yes", Type: "shell", TimeoutS: 1, Command: "yes"})
	require.Error(t, err, "a timed-out step fails")
	assert.Less(t, time.Since(start), 20*time.Second)
	assert.Less(t, fileSize(t, sf, "stdout.txt"), int64(1<<20)+512)
	assert.Contains(t, res.Note, "stdout.txt")
}

// A process the step leaves running in the background holding its output does not hold the step, and is not killed for it: the step ends with its shell
// (within the wait for the output to arrive), and the leftover process still writes, into the capped file, and finishes (#5079 round 2: a writer's SIGPIPE).
func TestShellStepLeavesABackgroundWriterRunning(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	start := time.Now()
	res, sf, err := runCappedShell(t, StepDef{Name: "bg", Type: "shell",
		Command: "{ sleep 5; echo late; echo done > '" + marker + "'; } & echo started"})
	require.NoError(t, err, "the step's own exit is what counts")
	assert.Less(t, time.Since(start), 4500*time.Millisecond, "not the 5 s of the background process (the wait for output is 3 s)")
	require.NotNil(t, res.ExitCode)
	assert.Equal(t, 0, *res.ExitCode)
	assert.Contains(t, res.OutputTail, "started")

	require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 15*time.Second, 50*time.Millisecond,
		"the background process was killed (a write to a closed pipe) instead of left running")
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(sf.dir, "stdout.txt"))
		return strings.Contains(string(b), "late")
	}, 5*time.Second, 50*time.Millisecond, "its late output reached the step's file")
}

// A step's output is all in its files when its result is read, however much of it there is.
func TestShellStepOutputIsCompleteWhenTheResultIsRead(t *testing.T) {
	res, sf, err := runCappedShell(t, StepDef{Name: "burst", Type: "shell", Command: "head -c 1000000 /dev/zero | tr '\\0' x; echo END"})
	require.NoError(t, err)
	assert.Empty(t, res.Note)
	assert.EqualValues(t, 1000000+len("END\n"), fileSize(t, sf, "stdout.txt"))
	assert.Contains(t, res.OutputTail, "END")
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

// A full disk drops the output from there on and says so; the writer never errors (a copier that stopped reading would block the command on a full pipe).
func TestCappedWriterSurvivesAWriteError(t *testing.T) {
	cw := &cappedWriter{w: failingWriter{}, left: 1 << 20}
	n, err := cw.Write([]byte("hello"))
	assert.Equal(t, 5, n)
	assert.NoError(t, err)
	n, err = cw.Write([]byte("more"))
	assert.Equal(t, 4, n)
	assert.NoError(t, err)
	werr, _ := cw.writeErr.Load().(error)
	require.Error(t, werr)
	assert.Contains(t, werr.Error(), "no space")
}
