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

// bashLib is the path of one of tests/lib's scripts for bash, or skips: no bash, or (Windows) a bash that is not Git Bash (WSL's, say). On Windows the path is given
// with forward slashes, which Git Bash reads. On the windows-tests runner (GITHUB_ACTIONS), which has Git Bash, none of these is a skip but a failure: a green
// run there means the shell tests ran under Git Bash, not that they were skipped (#5789).
func bashLib(t *testing.T, name string) string {
	t.Helper()
	unavailable := t.Skipf
	if runtime.GOOS == "windows" && os.Getenv("GITHUB_ACTIONS") == "true" {
		unavailable = t.Fatalf
	}
	if _, err := exec.LookPath("bash"); err != nil {
		unavailable("no bash")
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("bash", "-c", "uname -s").Output()
		kind := strings.ToUpper(strings.TrimSpace(string(out)))
		if err != nil || !strings.HasPrefix(kind, "MINGW") && !strings.HasPrefix(kind, "MSYS") {
			unavailable("the bash on PATH is not Git Bash (%q)", kind)
		}
	}
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "lib", name))
	require.NoError(t, err)
	require.FileExists(t, p)
	if runtime.GOOS == "windows" {
		return filepath.ToSlash(p)
	}
	return p
}

// bashRun is bash running script with the lib's path as $1 and the rest of args after it. The environment names the pty backend, which the library requires
// where there is no tmux (Windows), as the runners set it.
func bashRun(script string, args ...string) *exec.Cmd {
	cmd := exec.Command("bash", append([]string{"-c", script, "bash"}, args...)...)
	cmd.Env = append(os.Environ(), "CORAL_TEST_BACKEND=pty")
	return cmd
}

// skipOnWindows is for the tests of what the runners do with signals and folder names, which the Windows run does not exercise here.
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("run by the stress runners under Git Bash; signals and folder names differ here")
	}
}

// The standing rule: only a folder named coral-test* is ever removed. Anything else, a real folder or an unset variable's stand-in, is refused and said.
func TestRmTreeRemovesOnlyCoralTestFolders(t *testing.T) {
	skipOnWindows(t)
	lib := bashLib(t, "mock_sandbox.sh")
	root := t.TempDir()
	mine, other := filepath.Join(root, "coral-test-stress.AbC123"), filepath.Join(root, "project")
	for _, d := range []string{mine, other} {
		require.NoError(t, os.MkdirAll(filepath.Join(d, "sub"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "sub", "f"), []byte("x"), 0o644))
	}
	cmd := bashRun(`source "$1"; coral_test_rm_tree "$2"; coral_test_rm_tree "$3"; coral_test_rm_tree ""; coral_test_rm_tree "$4/no-such"; echo done`, lib, mine, other, root)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.NoDirExists(t, mine, "a coral-test folder is removed")
	assert.DirExists(t, other, "a folder with any other name is kept")
	assert.FileExists(t, filepath.Join(other, "sub", "f"))
	assert.Contains(t, string(out), "refusing to remove "+other)
	assert.NotContains(t, string(out), "refusing to remove "+mine)
	assert.Contains(t, string(out), "done", "it never fails the run")
}

// The watchdog signals only the process that started it (its start time is compared when the time is up: a number reused by another process must not get the
// signal), and still stops a runner that hangs.
func TestWatchdogSignalsOnlyARunnerThatIsStillThere(t *testing.T) {
	skipOnWindows(t)
	lib := bashLib(t, "backend.sh")
	root := t.TempDir()
	logf := filepath.Join(root, "kill.log")
	// every kill the watchdog's subshell makes is logged, then done for real
	wrap := `kill() { echo "kill $*" >> "$LOG"; builtin kill "$@"; }`

	// a runner that has gone by the time the watchdog fires: no signal is sent
	gone := `source "$1"; ` + wrap + `; coral_test_watchdog 1; exit 0`
	cmd0 := bashRun(gone, lib)
	cmd0.Env = append(cmd0.Env, "LOG="+logf)
	out, err := cmd0.CombinedOutput()
	require.NoError(t, err, string(out))
	time.Sleep(2500 * time.Millisecond) // the orphaned watchdog fires and finds nobody
	b, _ := os.ReadFile(logf)
	assert.NotContains(t, string(b), "-USR1", "no signal for a runner that is gone: %q", b)

	// a runner that hangs: the watchdog stops it with exit 124
	_ = os.Remove(logf)
	hang := `source "$1"; ` + wrap + `; [ -n "$(coral_test_proc_start $$)" ] || { echo cannot-tell; exit 0; }; coral_test_watchdog 1; for i in $(seq 1 100); do sleep 0.1; done; echo not-stopped`
	cmd := bashRun(hang, lib)
	cmd.Env = append(cmd.Env, "LOG="+logf)
	out, err = cmd.CombinedOutput()
	if strings.Contains(string(out), "cannot-tell") {
		t.Skip("this system gives no start time for a process: the watchdog is not started here (and says so)")
	}
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, string(out))
	assert.Equal(t, 124, ee.ExitCode(), string(out))
	assert.Contains(t, string(out), "still running after 1s")
	b, _ = os.ReadFile(logf)
	assert.Contains(t, string(b), "kill -USR1 ", "the runner that is still there is signalled")

	// a process that took the runner's number over (here: the start time read when the time is up is another one): no signal, the runner is left alone
	_ = os.Remove(logf)
	cnt := filepath.Join(root, "count")
	reused := `source "$1"; ` + wrap + `; coral_test_proc_start() { n=$(cat "$CNT" 2>/dev/null || echo 0); echo $((n+1)) > "$CNT"; echo "start-$n"; }; ` +
		`coral_test_watchdog 1; for i in $(seq 1 30); do sleep 0.1; done; echo not-stopped`
	cmd2 := bashRun(reused, lib)
	cmd2.Env = append(cmd2.Env, "LOG="+logf, "CNT="+cnt)
	out, err = cmd2.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "not-stopped", "a different start time at fire time means a different process: it is not signalled")
	b, _ = os.ReadFile(logf)
	assert.NotContains(t, string(b), "-USR1", "%q", b)
}

// The identity the watchdog compares (#5776): a process's start time. The same process matches itself, another process, a dead one and an empty start do not.
func TestProcStartTellsOneProcessFromAnother(t *testing.T) {
	lib := bashLib(t, "backend.sh")
	script := `source "$1"
a=$(coral_test_proc_start $$)
[ -n "$a" ] || { echo cannot-tell; exit 0; }
coral_test_same_process $$ "$a" && echo self-same
sleep 1.3   # ps shows start times to the second: the other process starts a clear second after this one
sleep 5 & other=$!
coral_test_same_process $other "$a" && echo other-same || echo other-differs
kill $other; wait $other 2>/dev/null
coral_test_same_process $other "$a" && echo dead-same || echo dead-differs
coral_test_same_process $$ "" && echo empty-same || echo empty-differs`
	out, err := bashRun(script, lib).CombinedOutput()
	require.NoError(t, err, string(out))
	if strings.Contains(string(out), "cannot-tell") {
		if runtime.GOOS == "windows" {
			t.Fatalf("Git Bash gave no start time for a process: the watchdog would not run on Windows (#5789): %s", out)
		}
		t.Skip("this system gives no start time for a process")
	}
	for _, want := range []string{"self-same", "other-differs", "dead-differs", "empty-differs"} {
		assert.Contains(t, string(out), want, string(out))
	}
}

// Git Bash's fallback (#5789): when /proc gives nothing, Windows is asked for the start time through PowerShell. Run directly here, so a runner where /proc already
// answers still proves the fallback works: it gives a number for the shell itself, and nothing for a process that is gone.
func TestProcStartAsksWindowsThroughPowerShell(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Git Bash on Windows only")
	}
	lib := bashLib(t, "backend.sh")
	script := `source "$1"
a=$(coral_test_proc_start_windows $$)
echo "start=[$a]"
# Windows allocates PIDs in multiples of four. This positive Int32 value is
# never assigned, unlike an exited child's PID which Windows can reuse (#5957).
gone=2147483647
# Do not let an MSYS /proc entry translate it to some other Windows PID.
[[ ! -e /proc/$gone/winpid ]] || exit 1
echo "gone=[$(coral_test_proc_start_windows $gone)]"`
	out, err := bashRun(script, lib).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Regexp(t, `start=\[[0-9]{10,}\]`, string(out), "UTC ticks of the shell's start")
	assert.Contains(t, string(out), "gone=[]", "a process that is gone has no start time")
}
