//go:build !windows

package background

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3587: a step can write in its run folder (CORAL_WORKFLOW_RUN_DIR), but whatever it plants
// there, Coral's own writes and reads of step files never leave the run folder.

// outsideFiles is a folder outside the run with a file whose text must never show up.
func outsideFiles(t *testing.T) (dir, secret string) {
	t.Helper()
	dir = t.TempDir()
	secret = filepath.Join(dir, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("OUTSIDE-SECRET\n"), 0o600))
	return dir, secret
}

func unchanged(t *testing.T, dir, secret string) {
	t.Helper()
	b, _ := os.ReadFile(secret)
	assert.Equal(t, "OUTSIDE-SECRET\n", string(b), "the outside file is unchanged")
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 1, "nothing new appeared outside")
}

// A step plants the NEXT step's folder, or a file in it, as a symlink: the next step fails
// instead of writing through it.
func TestPlantedNextStepIsRefused(t *testing.T) {
	for name, plant := range map[string]string{
		"step folder link": `ln -s "$OUT" "$CORAL_WORKFLOW_RUN_DIR/step_1"`,
		"stdout link":      `mkdir "$CORAL_WORKFLOW_RUN_DIR/step_1" && ln -s "$SECRET" "$CORAL_WORKFLOW_RUN_DIR/step_1/stdout.txt"`,
		"exit_code link":   `mkdir -p "$CORAL_WORKFLOW_RUN_DIR/step_1/artifacts" && ln -s "$SECRET" "$CORAL_WORKFLOW_RUN_DIR/step_1/exit_code"`,
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			out, secret := outsideFiles(t)
			t.Setenv("OUT", out)
			t.Setenv("SECRET", secret)
			final, _, _ := runWorkflowSteps(t, []StepDef{
				{Name: "plant", Type: "shell", Command: plant},
				{Name: "victim", Type: "shell", Command: "echo pwned; echo pwned >&2"},
			})
			assert.Equal(t, "failed", final.Status, "the planted step folder fails the run")
			unchanged(t, out, secret)
			assert.NotContains(t, final.StepResults, "OUTSIDE-SECRET")
		})
	}
}

// A step swaps its OWN output files for links out of the run, or for a FIFO: Coral's reads
// and writes after it ends don't follow them, don't hang, and don't show outside text.
func TestStepSwappingItsOwnFiles(t *testing.T) {
	out, secret := outsideFiles(t)
	t.Setenv("SECRET", secret)
	start := time.Now()
	final, _, runDir := runWorkflowSteps(t, []StepDef{
		{Name: "link-out", Type: "shell", ContinueOnFailure: true,
			Command:        `rm "$CORAL_WORKFLOW_STEP_DIR/stdout.txt" "$CORAL_WORKFLOW_STEP_DIR/stderr.txt"; ln -s "$SECRET" "$CORAL_WORKFLOW_STEP_DIR/stdout.txt"; ln -s "$SECRET" "$CORAL_WORKFLOW_STEP_DIR/stderr.txt"; exit 1`,
			OutputArtifact: "art.txt"},
		{Name: "fifo", Type: "shell", ContinueOnFailure: true,
			Command: `rm "$CORAL_WORKFLOW_STEP_DIR/stdout.txt"; mkfifo "$CORAL_WORKFLOW_STEP_DIR/stdout.txt"; exit 1`},
		{Name: "exit-code-link", Type: "shell",
			Command: `ln -s "$SECRET" "$CORAL_WORKFLOW_STEP_DIR/exit_code"; echo ok`},
	})
	assert.Less(t, time.Since(start), 15*time.Second, "a FIFO didn't hang the run")
	assert.NotContains(t, final.StepResults, "OUTSIDE-SECRET", "no outside text in the results")
	unchanged(t, out, secret)
	_, err := os.Stat(filepath.Join(runDir, "step_0", "art.txt"))
	assert.True(t, os.IsNotExist(err), "no artifact copied from a link out of the run")
}

// A later step replaces an earlier step's folder with a link out of the run: a template
// reading that step's output reads nothing.
func TestTemplateReadsStayInTheRun(t *testing.T) {
	out, secret := outsideFiles(t)
	require.NoError(t, os.WriteFile(filepath.Join(out, "stdout.txt"), []byte("OUTSIDE-SECRET\n"), 0o600))
	t.Setenv("OUT", out)
	final, repo, _ := runWorkflowSteps(t, []StepDef{
		{Name: "first", Type: "shell", Command: "echo first"},
		{Name: "swap", Type: "shell", Command: `rm -rf "$CORAL_WORKFLOW_RUN_DIR/step_0" && ln -s "$OUT" "$CORAL_WORKFLOW_RUN_DIR/step_0"`},
		{Name: "read", Type: "shell", Command: "printf %s {{step_0_stdout_content}} > got.txt"},
	})
	assert.Equal(t, "completed", final.Status, "%v %s", final.ErrorMsg, final.StepResults)
	got, err := os.ReadFile(filepath.Join(repo, "got.txt"))
	require.NoError(t, err)
	assert.Empty(t, string(got), "the template read nothing through the link")
	_ = secret
}

func TestRunFolderIsFreshAndPrivate(t *testing.T) {
	data := t.TempDir()
	runs := filepath.Join(data, "workflows", "runs")
	require.NoError(t, os.MkdirAll(filepath.Join(runs, "7"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runs, "7", "old.txt"), []byte("old"), 0o644))

	// A folder left by an earlier database is moved aside, not reused.
	rf, err := openRunFolder(data, 7)
	require.NoError(t, err)
	entries, _ := os.ReadDir(rf.dir)
	assert.Empty(t, entries, "a fresh folder")
	matches, _ := filepath.Glob(filepath.Join(runs, "7.stale-*", "old.txt"))
	assert.Len(t, matches, 1, "the old one kept aside")

	sf, err := rf.step(0)
	require.NoError(t, err)
	f, err := sf.create("stdout.txt")
	require.NoError(t, err)
	f.Close()
	for p, want := range map[string]os.FileMode{rf.dir: 0o700, sf.dir: 0o700, filepath.Join(sf.dir, "artifacts"): 0o700, filepath.Join(sf.dir, "stdout.txt"): 0o600} {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, want, fi.Mode().Perm(), p)
	}
	_, err = sf.create("stdout.txt")
	assert.Error(t, err, "a step file is never made twice")
	_, err = rf.step(0)
	assert.Error(t, err, "nor a step folder")
	// A real, empty folder planted where a later step goes is refused too.
	require.NoError(t, os.Mkdir(filepath.Join(rf.dir, "step_1"), 0o700))
	_, err = rf.step(1)
	assert.Error(t, err, "a step folder that already exists")
	rf.Close()

	// A symlink or a file where the run folder goes: refused.
	out, secret := outsideFiles(t)
	require.NoError(t, os.Symlink(out, filepath.Join(runs, "8")))
	_, err = openRunFolder(data, 8)
	assert.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runs, "9"), nil, 0o600))
	_, err = openRunFolder(data, 9)
	assert.Error(t, err)
	unchanged(t, out, secret)
}

// The tail of a huge output is read with a cap, and a FIFO is refused, not waited on.
func TestTailIsBounded(t *testing.T) {
	rf, err := openRunFolder(t.TempDir(), 1)
	require.NoError(t, err)
	defer rf.Close()
	sf, err := rf.step(0)
	require.NoError(t, err)
	f, err := sf.create("stdout.txt")
	require.NoError(t, err)
	big := strings.Repeat("x", 4<<20) + "\nlast line\n"
	_, err = f.WriteString(big)
	require.NoError(t, err)
	f.Close()
	tail := sf.readTail("stdout.txt", 3)
	assert.True(t, strings.HasSuffix(tail, "last line\n"), "the end is there")
	assert.LessOrEqual(t, len(tail), tailBytes, "at most tailBytes were read")

	require.NoError(t, syscall.Mkfifo(filepath.Join(sf.dir, "fifo"), 0o600))
	done := make(chan string, 1)
	go func() { done <- sf.readTail("fifo", 3) }()
	select {
	case got := <-done:
		assert.Empty(t, got)
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}
