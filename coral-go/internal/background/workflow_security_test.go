package background

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
	"github.com/subgentic/coral/internal/store"
)

// runWorkflowSteps runs steps through the real runner and returns the finished run, the
// repo folder (each step's working directory) and the run folder.
func runWorkflowSteps(t *testing.T, steps []StepDef) (*store.WorkflowRun, string, string) {
	t.Helper()
	return runWorkflowStepsIn(t, t.TempDir(), steps)
}

func runWorkflowStepsIn(t *testing.T, repo string, steps []StepDef) (*store.WorkflowRun, string, string) {
	t.Helper()
	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	dataDir := t.TempDir()
	runner := NewWorkflowRunner(wfStore, &AgentLauncher{runtime: rt}, rt, nil, nil, dataDir, "localhost", 8420)
	stepsJSON, _ := json.Marshal(steps)
	wf := createTestWorkflow(t, wfStore, repo, string(stepsJSON))
	run, err := runner.TriggerWorkflow(context.Background(), wf, "api", nil)
	require.NoError(t, err)
	var final *store.WorkflowRun
	require.Eventually(t, func() bool {
		final, _ = wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
		return final != nil && (final.Status == "completed" || final.Status == "failed")
	}, 20*time.Second, 50*time.Millisecond)
	return final, repo, filepath.Join(dataDir, "workflows", "runs", strconv.FormatInt(run.ID, 10))
}

// CRIT-01: a step's output inserted with a _content template is one quoted shell word; it
// can never run a command, whatever it contains.
func TestStepOutputTemplatesCannotInjectShell(t *testing.T) {
	for i, payload := range []string{
		"x'; touch PWNED; '",
		"$(touch PWNED)",
		"`touch PWNED`",
		"a\"; touch PWNED; \"b",
		"line1\ntouch PWNED\n",
		"it's \\ a $HOME test",
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) { // (the name is in the temp path: keep it plain)
			src := filepath.Join(t.TempDir(), "payload")
			require.NoError(t, os.WriteFile(src, []byte(payload), 0o644))
			final, repo, _ := runWorkflowSteps(t, []StepDef{
				{Name: "produce", Type: "shell", Command: "cat '" + filepath.ToSlash(src) + "'"},
				{Name: "use", Type: "shell", Command: "printf %s {{prev_stdout_content}} > got.txt && printf %s {{step_0_stdout_content}} > got0.txt"},
			})
			assert.Equal(t, "completed", final.Status, "%v %s", final.ErrorMsg, final.StepResults)
			assert.NoFileExists(t, filepath.Join(repo, "PWNED"), "the payload ran a command")
			for _, f := range []string{"got.txt", "got0.txt"} {
				got, err := os.ReadFile(filepath.Join(repo, f))
				require.NoError(t, err)
				// Exactly, as one word (trailing newlines dropped, as command substitution does).
				assert.Equal(t, strings.TrimRight(payload, "\n"), string(got), "the content arrives exactly, as one word")
			}
		})
	}
}

// lnS is ln -s that makes a real symlink: Git's sh on Windows copies the target unless MSYS asks
// for a native link, and a copy would leave nothing for these refusal tests to refuse (#3638).
var lnS = func() string {
	if runtime.GOOS == "windows" {
		return "MSYS=winsymlinks:nativestrict ln -s"
	}
	return "ln -s"
}()

// CRIT-02: output_artifact never escapes the step's folder: not with "..", not an absolute
// path, not through a symlink at the file or at a folder on the way.
func TestOutputArtifactStaysInTheStepFolder(t *testing.T) {
	linktest.RequireSymlinks(t) // the step makes its link with ln -s; without the privilege it makes a plain file and the test checks nothing
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	require.NoError(t, os.WriteFile(victim, []byte("orig"), 0o644))

	_, _, runDir := runWorkflowSteps(t, []StepDef{
		{Name: "dotdot", Type: "shell", Command: "echo pwned", OutputArtifact: "../../../../../../../../.." + victim},
		{Name: "abs", Type: "shell", Command: "echo pwned", OutputArtifact: victim},
		{Name: "linkfile", Type: "shell", Command: lnS + ` "` + victim + `" "$CORAL_WORKFLOW_STEP_DIR/art.txt"; echo pwned`, OutputArtifact: "art.txt"},
		{Name: "linkdir", Type: "shell", Command: lnS + ` "` + outside + `" "$CORAL_WORKFLOW_STEP_DIR/sub"; echo pwned`, OutputArtifact: "sub/victim.txt"},
		{Name: "ok", Type: "shell", Command: "echo fine", OutputArtifact: "reports/out.txt"},
	})
	got, _ := os.ReadFile(victim)
	assert.Equal(t, "orig", string(got), "nothing was written outside the step folder")
	entries, _ := os.ReadDir(outside)
	assert.Len(t, entries, 1, "and nothing new appeared there")
	ok, err := os.ReadFile(filepath.Join(runDir, "step_4", "reports", "out.txt"))
	require.NoError(t, err)
	assert.Equal(t, "fine\n", string(ok), "a normal relative artifact still works")

	// The reading side refuses a link out of the run folder too, and a path out of the step.
	root, err := os.OpenRoot(runDir)
	require.NoError(t, err)
	defer root.Close()
	sf := stepFiles{run: &runFiles{root: root, dir: runDir}, rel: "step_2", dir: filepath.Join(runDir, "step_2")}
	_, err = sf.readArtifact("art.txt", maxArtifactRead)
	assert.Error(t, err)
	_, err = sf.readArtifact("../x", maxArtifactRead)
	assert.Error(t, err)
}

// MED-03: a big artifact is left out of the environment (no E2BIG): the next step starts,
// sees CORAL_PREV_ARTIFACT_TRUNCATED=1 and reads the file itself; a small one is passed,
// without NUL bytes.
func TestBigArtifactIsNotPutInTheEnvironment(t *testing.T) {
	final, repo, _ := runWorkflowSteps(t, []StepDef{
		{Name: "big", Type: "shell", Command: "head -c 2000000 /dev/zero | tr '\\000' a", OutputArtifact: "big.txt"},
		{Name: "use", Type: "shell", Command: `printf %s "$CORAL_PREV_ARTIFACT_TRUNCATED" > t.txt; printf %s "${CORAL_PREV_ARTIFACT_CONTENT-unset}" > c.txt; wc -c < "$CORAL_PREV_ARTIFACT" | tr -d ' ' > n.txt`},
		{Name: "small", Type: "shell", Command: `printf 'a\000b'`, OutputArtifact: "small.txt"},
		{Name: "use2", Type: "shell", Command: `printf %s "$CORAL_PREV_ARTIFACT_CONTENT" > s.txt`},
	})
	assert.Equal(t, "completed", final.Status, final.ErrorMsg)
	read := func(f string) string {
		b, _ := os.ReadFile(filepath.Join(repo, f))
		return strings.TrimSpace(string(b))
	}
	assert.Equal(t, "1", read("t.txt"))
	assert.Equal(t, "unset", read("c.txt"))
	assert.Equal(t, "2000000", read("n.txt"))
	assert.Equal(t, "ab", read("s.txt"), "NUL stripped")
}

// Prompts keep plain text (not shell-quoted); shell commands get one quoted word.
func TestContentTemplateModes(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.txt")
	require.NoError(t, os.WriteFile(out, []byte("it's"), 0o644))
	env := []string{"CORAL_PREV_STDOUT=" + out, "CORAL_WORKFLOW_RUN_DIR=" + dir} // a step always has its run folder
	assert.Equal(t, "say it's", expandTemplates("say {{prev_stdout_content}}", env))
	got, err := expandShellTemplates("echo {{prev_stdout_content}}", env)
	require.NoError(t, err)
	assert.Equal(t, `echo 'it'\''s'`, got)
	got, err = expandShellTemplates("cat {{prev_stdout}}", env)
	require.NoError(t, err)
	assert.Equal(t, "cat '"+filepath.ToSlash(out)+"'", got, "a path, with forward slashes in a shell command (#3638; unchanged on Unix), as one quoted word (#3930)")
}

// F1: as command substitution, trailing newlines are dropped and embedded ones kept, so the
// usual "use the previous step's output as an argument" works (git rev-parse --verify).
func TestContentTemplateDropsTrailingNewlines(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.txt")
	env := []string{"CORAL_PREV_STDOUT=" + out, "CORAL_WORKFLOW_RUN_DIR=" + dir} // a step always has its run folder
	for in, want := range map[string]string{"main\n": "'main'", "a\nb\n": "'a\nb'", "x\n\n\n": "'x'", "": "''"} {
		require.NoError(t, os.WriteFile(out, []byte(in), 0o644))
		got, err := expandShellTemplates("{{prev_stdout_content}}", env)
		require.NoError(t, err)
		assert.Equal(t, want, got, "%q", in)
	}

	repo := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		b, err := cmd.CombinedOutput()
		require.NoError(t, err, string(b))
		return string(b)
	}
	run("init", "-q", "-b", "main")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x")
	final, _, _ := runWorkflowStepsIn(t, repo, []StepDef{
		{Name: "branch", Type: "shell", Command: "git rev-parse --abbrev-ref HEAD"},
		{Name: "verify", Type: "shell", Command: "git rev-parse --verify {{prev_stdout_content}} > sha.txt"},
	})
	assert.Equal(t, "completed", final.Status, "%v %s", final.ErrorMsg, final.StepResults)
	sha, _ := os.ReadFile(filepath.Join(repo, "sha.txt"))
	assert.Len(t, strings.TrimSpace(string(sha)), 40)
}

// F2: a refused artifact is visible in the step (a note and a stderr line), and the next
// step's {{prev_artifact_content}} is an empty word, never the template text.
func TestRefusedArtifactIsVisibleAndEmpty(t *testing.T) {
	linktest.RequireSymlinks(t) // the step makes its link with ln -s; without the privilege it makes a plain file and the test checks nothing
	outside := t.TempDir()
	final, repo, _ := runWorkflowSteps(t, []StepDef{
		{Name: "link", Type: "shell", Command: lnS + ` "` + outside + `" "$CORAL_WORKFLOW_STEP_DIR/sub"; echo data`, OutputArtifact: "sub/a.txt"},
		{Name: "use", Type: "shell", Command: `printf '[%s]' {{prev_artifact_content}} > got.txt`},
	})
	assert.Equal(t, "completed", final.Status, "%v", final.ErrorMsg)
	var results []StepResult
	require.NoError(t, json.Unmarshal([]byte(final.StepResults), &results))
	assert.Contains(t, results[0].Note, `output_artifact "sub/a.txt" was not written`)
	got, _ := os.ReadFile(filepath.Join(repo, "got.txt"))
	assert.Equal(t, "[]", string(got), "an empty word, not {{prev_artifact_content}}")
}

// F3: content over 64 KiB fails the step with a message that names the file form, never an
// exec failure with no message.
func TestTooBigContentTemplateFailsClearly(t *testing.T) {
	final, _, _ := runWorkflowSteps(t, []StepDef{
		{Name: "big", Type: "shell", Command: "head -c 200000 /dev/zero | tr '\\000' a"},
		{Name: "use", Type: "shell", Command: "printf %s {{prev_stdout_content}} | wc -c"},
	})
	assert.Equal(t, "failed", final.Status)
	var results []StepResult
	require.NoError(t, json.Unmarshal([]byte(final.StepResults), &results))
	require.Len(t, results, 2)
	require.NotNil(t, results[1].ExitCode)
	assert.Equal(t, 1, *results[1].ExitCode)
	assert.Contains(t, results[1].ErrorTail, "{{prev_stdout_content}} is 200000 bytes")
	assert.Contains(t, results[1].ErrorTail, "$CORAL_PREV_STDOUT")
}
