//go:build !windows

package ptymanager

import (
	"errors"
	"github.com/shirou/gopsutil/v4/process"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// #6129: when Coral dies without cleaning up, the guardian it started ends every terminal's tree. The parent here is a real process (this test binary run again as one), killed with SIGKILL.

const guardParentEnv = "CORAL_GUARD_TEST_PARENT"

// TestGuardParentHelper is the "Coral" of the tests below: it runs a terminal whose agent has a child in a session of its own, says where its pids are, and waits to be killed (or, with a second
// variable, to quit cleanly).
func TestGuardParentHelper(t *testing.T) {
	dir := os.Getenv(guardParentEnv)
	if dir == "" {
		t.Skip("run by the guardian tests, as the process that is killed")
	}
	b := NewPTYBackend()
	cmd := strings.Replace(strings.Replace(attached, "%s", "", 1), "%s", filepath.Join(dir, "child"), 1)
	if err := b.Spawn("claude-00000000-0000-4000-8000-000000006129", "claude", dir, "00000000-0000-4000-8000-000000006129", cmd, 80, 24); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, "child")); err == nil {
			break
		}
	}
	gpid, regs := guardian.pid()
	os.WriteFile(filepath.Join(dir, "guardian"), []byte(strconv.Itoa(gpid)+" "+strconv.Itoa(regs)), 0o644)
	if os.Getenv("CORAL_GUARD_TEST_QUIT") == "1" {
		b.Close()
		return // exits normally: the pipe to the guardian closes with the process
	}
	for end := time.Now().Add(time.Minute); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		gp, _ := guardian.pid()
		os.WriteFile(filepath.Join(dir, "gnow"), []byte(strconv.Itoa(gp)), 0o644) // what the test reads to see the guardian replaced
	}
}

func runGuardParent(t *testing.T, quit bool) (child, guardianPID int, parent *exec.Cmd, dir string) {
	t.Helper()
	needPerl(t)
	dir = t.TempDir()
	parent = exec.Command(os.Args[0], "-test.run=^TestGuardParentHelper$")
	parent.Env = append(os.Environ(), guardParentEnv+"="+dir)
	if quit {
		parent.Env = append(parent.Env, "CORAL_GUARD_TEST_QUIT=1")
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { parent.Process.Kill(); parent.Wait() })
	child = pidFrom(t, filepath.Join(dir, "child"))
	var f []string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && len(f) < 2; time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(filepath.Join(dir, "guardian"))
		f = strings.Fields(string(b))
	}
	if len(f) < 2 {
		t.Fatal("the helper did not report its guardian")
	}
	guardianPID, _ = strconv.Atoi(f[0])
	if guardianPID <= 1 {
		t.Fatalf("no guardian was started: %v", f)
	}
	t.Cleanup(func() { syscall.Kill(guardianPID, syscall.SIGKILL) })
	return child, guardianPID, parent, dir
}

func TestAgentsEndWhenCoralIsKilled(t *testing.T) {
	child, guardianPID, parent, _ := runGuardParent(t, false)
	time.Sleep(guardianSample + 500*time.Millisecond) // the guardian has seen the child while it was still attached (what it can know on a system that hides environments)
	if err := syscall.Kill(parent.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	parent.Wait()
	waitGone(t, child, 10*time.Second)
	waitGone(t, guardianPID, 10*time.Second) // and the guardian itself ends once it has done that
}

func TestACleanQuitLeavesNoGuardian(t *testing.T) {
	_, guardianPID, parent, _ := runGuardParent(t, true)
	parent.Wait()
	waitGone(t, guardianPID, 10*time.Second)
}

// A guardian that dies is replaced, and the replacement is told about the terminals already registered.
func TestAGuardianThatDiesIsReplacedAndToldAgain(t *testing.T) {
	needPerl(t)
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	spawn := func(n string) {
		id := "00000000-0000-4000-8000-00000000613" + n
		if err := b.Spawn("claude-"+id, "claude", t.TempDir(), id, "sleep 600", 80, 24); err != nil {
			t.Fatal(err)
		}
	}
	spawn("0")
	first, regs := guardian.pid()
	if first <= 1 || regs < 1 {
		t.Fatalf("a guardian should be running with the terminal registered (%d, %d)", first, regs)
	}
	syscall.Kill(first, syscall.SIGKILL)
	second := waitNewGuardian(t, first) // with no terminal started or ended meanwhile: Coral waits on the guardian and replaces it as soon as it exits
	if _, regs2 := guardian.pid(); regs2 < regs {
		t.Fatalf("the replacement is told every terminal (%d registered)", regs2)
	}
	spawn("1")
	if p, regs3 := guardian.pid(); p != second || regs3 < regs+1 {
		t.Fatalf("a new terminal is registered with the replacement (%d, %d registered)", p, regs3)
	}
}

// The hidden mode runs before main and opens nothing: started with the marker, an empty data directory and an empty home, it ends at once when its pipe ends and leaves both as they were.
func TestTheGuardianModeOpensNothing(t *testing.T) {
	data, home := t.TempDir(), t.TempDir()
	c := exec.Command(os.Args[0], guardianArg)
	c.Env = []string{guardianEnv + "=1", "CORAL_DATA_DIR=" + data, "HOME=" + home, "XDG_CONFIG_HOME=" + home}
	c.Stdin = strings.NewReader("") // EOF at once
	done := make(chan error, 1)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the guardian mode should end cleanly: %v", err)
		}
	case <-time.After(5 * time.Second):
		c.Process.Kill()
		t.Fatal("the guardian mode did not end on EOF")
	}
	for _, d := range []string{data, home} {
		if entries, _ := os.ReadDir(d); len(entries) != 0 {
			t.Fatalf("the guardian mode created %d entries in %s", len(entries), d)
		}
	}
}

// Anything but exactly the argument and the marker is an ordinary start (a program run with that argument by accident, or this test binary with more arguments, must not turn into a guardian).
func TestOnlyTheArgumentAndTheMarkerStartTheGuardian(t *testing.T) {
	for _, c := range []struct {
		args   []string
		marker string
		want   bool
	}{
		{[]string{"coral", guardianArg}, "1", true},
		{[]string{"coral", guardianArg}, "", false},
		{[]string{"coral", guardianArg}, "0", false},
		{[]string{"coral"}, "1", false},
		{[]string{"coral", guardianArg, "--port", "1"}, "1", false},
		{[]string{"coral", "--port", "1", guardianArg}, "1", false},
	} {
		if got := isGuardianStart(c.args, c.marker); got != c.want {
			t.Errorf("isGuardianStart(%v, %q) = %v, want %v", c.args, c.marker, got, c.want)
		}
	}
}

// Its only input is the pipe from its parent: with a file, or no input at all (a terminal looks the same), it ends at once without reading.
func TestTheGuardianRefusesAFileOrADeviceAsInput(t *testing.T) {
	file := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(file, []byte("add 12345 1 tag\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, open := range map[string]func() (*os.File, error){
		"a file":      func() (*os.File, error) { return os.Open(file) },
		"/dev/null":   func() (*os.File, error) { return os.Open(os.DevNull) },
		"a directory": func() (*os.File, error) { return os.Open(filepath.Dir(file)) },
	} {
		in, err := open()
		if err != nil {
			t.Fatal(err)
		}
		c := exec.Command(os.Args[0], guardianArg)
		c.Env = []string{guardianEnv + "=1"}
		c.Stdin = in
		done := make(chan error, 1)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { done <- c.Wait() }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: the guardian mode should refuse it (a non-zero exit)", name)
			}
		case <-time.After(5 * time.Second):
			c.Process.Kill()
			t.Errorf("%s: the guardian mode read it instead of refusing", name)
		}
		in.Close()
	}
}

// A member that was stopped when Coral died is ended too: SIGKILL ends a stopped process.
func TestAStoppedMemberIsEndedWhenCoralIsKilled(t *testing.T) {
	child, guardianPID, parent, _ := runGuardParent(t, false)
	time.Sleep(guardianSample + 500*time.Millisecond)
	if err := syscall.Kill(child, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(parent.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	parent.Wait()
	waitGone(t, child, 10*time.Second)
	waitGone(t, guardianPID, 10*time.Second)
}

// SIGTERM ends the guardian and nothing else: only its pipe reaching EOF ends terminals, so a stray kill or pkill that matches its name cannot end every agent. Coral replaces it.
func TestSIGTERMEndsTheGuardianButLeavesTheTerminalsRunning(t *testing.T) {
	canFork(t)
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	spawn := func(n string) string {
		id := "00000000-0000-4000-8000-00000000614" + n
		name := "claude-" + id
		if err := b.Spawn(name, "claude", t.TempDir(), id, "sleep 600", 80, 24); err != nil {
			t.Fatal(err)
		}
		return name
	}
	first := spawn("0")
	g1, _ := guardian.pid()
	if g1 <= 1 {
		t.Fatal("a guardian should be running")
	}
	time.Sleep(700 * time.Millisecond) // the guardian is up and serving its pipe (a SIGTERM during its start would end it by the default action, whatever it does with the signal later)
	if err := syscall.Kill(g1, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	g2 := waitNewGuardian(t, g1)       // SIGTERM ended it, and Coral replaced it
	time.Sleep(300 * time.Millisecond) // time enough for the old one to have killed something, if it did
	if !b.IsRunning(first) {
		t.Fatal("SIGTERM to the guardian ended a terminal's tree: only EOF may")
	}
	second := spawn("1")
	if p, regs := guardian.pid(); p != g2 || regs < 2 {
		t.Fatalf("the replacement guardian is told both terminals (pid %d, %d registered)", p, regs)
	}
	if !b.IsRunning(first) || !b.IsRunning(second) {
		t.Fatal("both terminals run")
	}
}

// The guardian is named in a process list.
func TestTheGuardianIsNamedInTheProcessList(t *testing.T) {
	canFork(t)
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	const id = "00000000-0000-4000-8000-000000006150"
	if err := b.Spawn("claude-"+id, "claude", t.TempDir(), id, "sleep 600", 80, 24); err != nil {
		t.Fatal(err)
	}
	pid, _ := guardian.pid()
	if pid <= 1 {
		t.Fatal("a guardian should be running")
	}
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		t.Fatal(err)
	}
	var cmdline string
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end) && !strings.Contains(cmdline, guardianName); time.Sleep(20 * time.Millisecond) {
		cmdline, _ = p.Cmdline()
	}
	if !strings.Contains(cmdline, guardianName) {
		t.Fatalf("the process list should say what it is (%q): %q", guardianName, cmdline)
	}
}

// waitNewGuardian waits for the client to have a running guardian other than old (it replaces a dead one by itself) and returns its pid.
func waitNewGuardian(t *testing.T, old int) int {
	t.Helper()
	for end := time.Now().Add(6 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if p, _ := guardian.pid(); p > 1 && p != old {
			return p
		}
	}
	t.Fatalf("no replacement for guardian %d was started", old)
	return 0
}

// The guardian is killed, Coral replaces it with no terminal started or ended, and then Coral is killed: the replacement, which was told the terminal, still ends its tree.
func TestAGuardianKilledAndThenCoralKilledStillEndsTheTree(t *testing.T) {
	child, g1, parent, dir := runGuardParent(t, false)
	if err := syscall.Kill(g1, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	var g2 int
	for end := time.Now().Add(8 * time.Second); time.Now().Before(end) && g2 == 0; time.Sleep(50 * time.Millisecond) {
		b, _ := os.ReadFile(filepath.Join(dir, "gnow"))
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 1 && n != g1 {
			g2 = n
		}
	}
	if g2 == 0 {
		t.Fatal("Coral did not start a replacement guardian by itself")
	}
	t.Cleanup(func() { syscall.Kill(g2, syscall.SIGKILL) })
	time.Sleep(guardianSample + 500*time.Millisecond)
	if err := syscall.Kill(parent.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	parent.Wait()
	waitGone(t, child, 10*time.Second)
	waitGone(t, g2, 10*time.Second)
}

// A guardian that dies at once cannot make Coral spin: the wait before each replacement doubles up to a maximum, and starts again after a guardian that lived.
func TestTheRestartDelayBacksOffAndResets(t *testing.T) {
	d := time.Duration(0)
	var got []time.Duration
	for i := 0; i < 9; i++ {
		d = nextRestartDelay(d, 10*time.Millisecond) // each one died almost at once
		got = append(got, d)
	}
	want := []time.Duration{100, 200, 400, 800, 1600, 3200, 5000, 5000, 5000}
	for i := range want {
		if got[i] != want[i]*time.Millisecond {
			t.Fatalf("delay %d = %v, want %v (all: %v)", i, got[i], want[i]*time.Millisecond, got)
		}
	}
	if d := nextRestartDelay(5*time.Second, guardianLives); d != guardianRestartMin {
		t.Fatalf("a guardian that lived long enough starts the delay again, got %v", d)
	}
}

// failingPipe is a guardian's pipe whose writes fail while the guardian is alive: what is behind it is untouched, and a Close while the guardian still runs is recorded (it would be EOF: the guardian would
// end every registered tree while Coral lives).
type failingPipe struct {
	io.WriteCloser
	pid           int
	closedAlive   *atomic.Bool
	failedWrites  *atomic.Int32
	failRemaining atomic.Int32
}

func (f *failingPipe) Write(b []byte) (int, error) {
	if f.failRemaining.Add(-1) >= 0 {
		f.failedWrites.Add(1)
		return 0, errors.New("forced write error")
	}
	return f.WriteCloser.Write(b)
}

func (f *failingPipe) Close() error {
	if syscall.Kill(f.pid, 0) == nil && !isZombie(f.pid) {
		f.closedAlive.Store(true)
	}
	return f.WriteCloser.Close()
}

func isZombie(pid int) bool {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return true
	}
	st, err := p.Status()
	return err != nil || (len(st) > 0 && st[0] == "Z")
}

// A write to the guardian fails while it is alive: it is told to exit and waited for, its pipe is never closed while it runs, no terminal's tree is ended, and a new guardian is told every terminal.
func TestAFailedWriteNeverClosesALiveGuardiansPipe(t *testing.T) {
	canFork(t)
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	spawn := func(n string) string {
		id := "00000000-0000-4000-8000-00000000615" + n
		name := "claude-" + id
		if err := b.Spawn(name, "claude", t.TempDir(), id, "sleep 600", 80, 24); err != nil {
			t.Fatal(err)
		}
		return name
	}
	first := spawn("0")
	g1, _ := guardian.pid()
	if g1 <= 1 {
		t.Fatal("a guardian should be running")
	}
	time.Sleep(700 * time.Millisecond) // it is up and serving
	fp := &failingPipe{pid: g1, closedAlive: new(atomic.Bool), failedWrites: new(atomic.Int32)}
	fp.failRemaining.Store(1)
	guardian.mu.Lock()
	fp.WriteCloser = guardian.h.in
	guardian.h.in = fp
	guardian.mu.Unlock()

	second := spawn("1") // its registration is the write that fails
	if fp.failedWrites.Load() != 1 {
		t.Fatalf("the write was meant to fail once (%d)", fp.failedWrites.Load())
	}
	if fp.closedAlive.Load() {
		t.Fatal("the pipe of a guardian that was still running was closed: it would end every registered tree while Coral lives")
	}
	if p, regs := guardian.pid(); p <= 1 || p == g1 || regs < 2 {
		t.Fatalf("a new guardian should be running, told both terminals (pid %d after %d, %d registered)", p, g1, regs)
	}
	time.Sleep(500 * time.Millisecond) // time for a wrongly closed pipe's guardian to have ended something
	if !b.IsRunning(first) || !b.IsRunning(second) {
		t.Fatal("a terminal's tree was ended though Coral is running")
	}
}
