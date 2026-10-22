//go:build !windows

package ptymanager

import (
	"github.com/shirou/gopsutil/v4/process"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	time.Sleep(time.Minute)
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
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if p, _ := guardian.pid(); p == 0 { // noticed (and reaped) by the client
			break
		}
	}
	if p, _ := guardian.pid(); p != 0 {
		t.Fatalf("the client did not notice that its guardian died (%d)", p)
	}
	spawn("1")
	second, regs2 := guardian.pid()
	if second <= 1 || second == first {
		t.Fatalf("a new guardian should be running (%d after %d)", second, first)
	}
	if regs2 < regs+1 {
		t.Fatalf("the replacement is told every terminal (%d registered)", regs2)
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
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if p, _ := guardian.pid(); p == 0 {
			break
		}
	}
	if p, _ := guardian.pid(); p != 0 {
		t.Fatalf("SIGTERM should end the guardian (still %d)", p)
	}
	time.Sleep(300 * time.Millisecond) // time enough for it to have killed something, if it did
	if !b.IsRunning(first) {
		t.Fatal("SIGTERM to the guardian ended a terminal's tree: only EOF may")
	}
	second := spawn("1")
	g2, regs := guardian.pid()
	if g2 <= 1 || g2 == g1 || regs < 2 {
		t.Fatalf("Coral should have started a new guardian, told both terminals (pid %d after %d, %d registered)", g2, g1, regs)
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
