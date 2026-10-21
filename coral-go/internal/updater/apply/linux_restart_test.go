//go:build linux

package apply

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/runrecord"
)

// installProcesses are the pids whose executable is a file of dir.
func installProcesses(dir string) []int {
	var pids []int
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe")); err == nil && under(exe, dir) {
			pids = append(pids, pid)
		}
	}
	return pids
}

func waitHealthy(t *testing.T, port string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:" + port + "/api/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the server did not answer /api/health")
}

// #5410 (QA's #5401 finding 2): a real Coral writes its run record at startup, and the Linux apply starts Coral again from that record, nothing else: the second
// Coral serves on the same port from the same data folder, and Healthy sees it. Without the server's write the apply's Start failed with "no record of how Coral was
// started" and every update outside a service manager was rolled back.
func TestARealCoralIsStartedAgainFromItsOwnRunRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real server")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool")
	}
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	install := filepath.Join(root, "install")
	data := filepath.Join(root, "data")
	work := filepath.Join(root, "work")
	home := filepath.Join(root, "home")
	for _, d := range []string{install, data, work, home} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	build := exec.Command(goBin, "build", "-tags", "dev", "-o", filepath.Join(install, "coral"), "./cmd/coral")
	build.Dir = filepath.Join("..", "..", "..")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()

	// The first Coral: started by hand, with a clean environment plus one variable the record must carry and one it must not.
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CORAL_DATA_DIR=" + data, "CORAL_CLOUD_URL=off", "CORAL_TELEMETRY_DISABLED=1",
		"CORAL_SESSION_TOKEN=must-not-be-recorded", "ANTHROPIC_API_KEY=must-not-be-recorded"}
	first := exec.Command(filepath.Join(install, "coral"), "--home", data, "--host", "127.0.0.1", "--port", port, "--no-browser", "--cloud-url", "off", "--backend", "pty")
	first.Dir, first.Env = work, env
	require.NoError(t, first.Start())
	exited := make(chan struct{})
	go func() { first.Wait(); close(exited) }()
	t.Cleanup(func() {
		for _, pid := range installProcesses(install) {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	waitHealthy(t, port)

	l := &Linux{DataDir: data, Self: os.Getpid(), Manager: func() bool { return false }, HealthFor: 5 * time.Second}
	// The record is written right after the server's listeners are up (cmd/coral recordRun), so /api/health can answer a moment before it exists: poll for it.
	var rr runrecord.Record
	require.Eventually(t, func() bool { var e error; rr, e = l.runRecord(); return e == nil }, 10*time.Second, 50*time.Millisecond, "the server wrote its run record at startup")
	assert.Equal(t, []string{filepath.Join(install, "coral"), "--home", data, "--host", "127.0.0.1", "--port", port, "--no-browser", "--cloud-url", "off", "--backend", "pty"}, rr.Argv)
	assert.Equal(t, work, rr.Cwd)
	assert.Contains(t, rr.Env, "CORAL_DATA_DIR="+data)
	assert.Contains(t, rr.Env, "HOME="+home)
	b, err := os.ReadFile(filepath.Join(data, "updates", "run.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(b), "must-not-be-recorded", "no token or key reaches the record")
	fi, err := os.Stat(filepath.Join(data, "updates", "run.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	// Coral ends (as the apply waits for it to), then the apply starts it again from the record alone.
	require.NoError(t, first.Process.Signal(syscall.SIGTERM))
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatal("the first Coral did not stop")
	}
	require.Empty(t, installProcesses(install))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	require.NoError(t, l.Start(ctx, install))
	waitHealthy(t, port)
	require.NoError(t, l.Healthy(ctx, install), "a process of the install is running and stays up")
	pids := installProcesses(install)
	require.NotEmpty(t, pids)
	for _, pid := range pids {
		cmdline, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
		assert.False(t, strings.Contains(string(cmdline), "must-not-be-recorded"), "the restarted Coral has only the recorded environment")
		assert.Contains(t, string(cmdline), "CORAL_DATA_DIR="+data)
	}
}
