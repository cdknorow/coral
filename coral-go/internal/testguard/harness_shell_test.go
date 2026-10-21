package testguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5762: the two shell helpers every stress runner uses. They run in bash here (the Windows run uses Git Bash through the runners themselves; the cmd rmdir branch of
// coral_test_rm_tree is for a Windows-only system folder and is not run here).

func bashLib(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("run by the stress runners under Git Bash; paths and signals differ here")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "lib", name))
	require.NoError(t, err)
	require.FileExists(t, p)
	return p
}

// The standing rule: only a folder named coral-test* is ever removed. Anything else, a real folder or an unset variable's stand-in, is refused and said.
func TestRmTreeRemovesOnlyCoralTestFolders(t *testing.T) {
	lib := bashLib(t, "mock_sandbox.sh")
	root := t.TempDir()
	mine, other := filepath.Join(root, "coral-test-stress.AbC123"), filepath.Join(root, "project")
	for _, d := range []string{mine, other} {
		require.NoError(t, os.MkdirAll(filepath.Join(d, "sub"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "sub", "f"), []byte("x"), 0o644))
	}
	cmd := exec.Command("bash", "-c", `source "$1"; coral_test_rm_tree "$2"; coral_test_rm_tree "$3"; coral_test_rm_tree ""; coral_test_rm_tree "$4/no-such"; echo done`, "bash", lib, mine, other, root)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.NoDirExists(t, mine, "a coral-test folder is removed")
	assert.DirExists(t, other, "a folder with any other name is kept")
	assert.FileExists(t, filepath.Join(other, "sub", "f"))
	assert.Contains(t, string(out), "refusing to remove "+other)
	assert.NotContains(t, string(out), "refusing to remove "+mine)
	assert.Contains(t, string(out), "done", "it never fails the run")
}

// The watchdog checks that the runner is still there before it signals it (a number reused by another process must not get the signal), and still stops a runner
// that hangs.
func TestWatchdogSignalsOnlyARunnerThatIsStillThere(t *testing.T) {
	lib := bashLib(t, "backend.sh")
	root := t.TempDir()
	logf := filepath.Join(root, "kill.log")
	// every kill the watchdog's subshell makes is logged, then done for real
	wrap := `kill() { echo "kill $*" >> "$LOG"; builtin kill "$@"; }`

	// a runner that has gone by the time the watchdog fires: no signal is sent
	gone := `source "$1"; ` + wrap + `; coral_test_watchdog 1; exit 0`
	cmd0 := exec.Command("bash", "-c", gone, "bash", lib)
	cmd0.Env = append(os.Environ(), "LOG="+logf)
	out, err := cmd0.CombinedOutput()
	require.NoError(t, err, string(out))
	time.Sleep(2500 * time.Millisecond) // the orphaned watchdog fires and finds nobody
	b, _ := os.ReadFile(logf)
	assert.NotContains(t, string(b), "-USR1", "no signal for a runner that is gone: %q", b)

	// a runner that hangs: it is checked (kill -0) and then stopped with exit 124
	_ = os.Remove(logf)
	hang := `source "$1"; ` + wrap + `; coral_test_watchdog 1; for i in $(seq 1 100); do sleep 0.1; done; echo not-stopped`
	cmd := exec.Command("bash", "-c", hang, "bash", lib)
	cmd.Env = append(os.Environ(), "LOG="+logf)
	out, err = cmd.CombinedOutput()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, string(out))
	assert.Equal(t, 124, ee.ExitCode(), string(out))
	assert.Contains(t, string(out), "still running after 1s")
	b, _ = os.ReadFile(logf)
	s := string(b)
	i0, iU := strings.Index(s, "kill -0 "), strings.Index(s, "kill -USR1 ")
	assert.True(t, i0 >= 0 && iU > i0, "kill -0 comes before kill -USR1: %q", s)
}
