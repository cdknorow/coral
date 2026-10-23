//go:build !windows

package ptymanager

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// Tests for what the mutation audit of 159 (#6196) found nothing pinning: the guardian client's pid guards, remove() and stop(), and the guardian's own "del". Each uses its own guardianClient (not
// the process-wide one) and children that are not Coral's terminals: a "sleep" started by a shell that exits, so it belongs to init and is not a zombie when the guardian ends it.

// detachedSleeper starts a sleep that no test process is the parent of, and returns its pid and its (pid, start time) member.
func detachedSleeper(t *testing.T) (int, member) {
	t.Helper()
	out, err := exec.Command("sh", "-c", "sleep 600 >/dev/null 2>&1 & echo $!").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("no pid from %q: %v", out, err)
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		t.Fatal(err)
	}
	created, err := p.CreateTime()
	if err != nil {
		t.Fatal(err)
	}
	return pid, member{pid, created}
}

// waitGuardian waits until g has a running guardian and returns its pid.
func waitGuardian(t *testing.T, g *guardianClient) int {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if p, _ := g.pid(); p > 1 {
			return p
		}
	}
	t.Fatal("no guardian was started")
	return 0
}

// A terminal whose pid is 0, 1 or negative is never registered and starts no guardian: kill(0) is the caller's whole process group and kill(1) is init. The start time given is not a real one, so
// even a guardian that took the line would find no such process to end.
func TestTheGuardianClientNeverRegistersPid0Or1(t *testing.T) {
	g := &guardianClient{}
	t.Cleanup(g.stop)
	for _, pid := range []int{-3, 0, 1} {
		g.add(member{pid, 1}, "qa-tag")
	}
	if p, regs := g.pid(); p != 0 || regs != 0 {
		t.Fatalf("pid 0, 1 and -3 registered nothing and started no guardian, got guardian %d and %d registered", p, regs)
	}
	// the control: a real terminal is registered by the same call
	_, m := detachedSleeper(t)
	g.add(m, "qa-tag")
	if _, regs := g.pid(); regs != 1 {
		t.Fatalf("a real terminal is registered, got %d", regs)
	}
}

// remove() forgets a terminal that ended: with nothing left registered, a guardian that dies is not replaced.
func TestARemovedTerminalIsNotToldToAReplacementGuardian(t *testing.T) {
	g := &guardianClient{}
	t.Cleanup(g.stop)
	_, m := detachedSleeper(t)
	g.add(m, "qa-tag")
	gpid := waitGuardian(t, g)
	g.remove(m.pid)
	if _, regs := g.pid(); regs != 0 {
		t.Fatalf("the terminal is forgotten once removed, got %d registered", regs)
	}
	syscall.Kill(gpid, syscall.SIGKILL)
	time.Sleep(guardianRestartMin + 600*time.Millisecond) // longer than the wait before a replacement would start
	if p, _ := g.pid(); p != 0 {
		t.Fatalf("with nothing registered the dead guardian is not replaced, got guardian %d", p)
	}
}

// stop() is Coral exiting: the guardian ends, and no replacement is started for it however long the watcher takes.
func TestAGuardianThatCoralStoppedIsNotReplaced(t *testing.T) {
	g := &guardianClient{}
	_, m := detachedSleeper(t)
	g.add(m, "qa-tag")
	waitGuardian(t, g)
	g.stop()
	time.Sleep(guardianRestartMin + 600*time.Millisecond)
	if p, _ := g.pid(); p != 0 {
		t.Fatalf("a stopped guardian is not replaced, got guardian %d", p)
	}
}

// A "del" tells the guardian a terminal ended: when Coral is gone the guardian ends the trees it still holds and leaves that one alone.
func TestTheGuardianLeavesATerminalItWasToldEnded(t *testing.T) {
	g := &guardianClient{}
	a, ma := detachedSleeper(t)
	b, mb := detachedSleeper(t)
	g.add(ma, "qa-tag")
	g.add(mb, "qa-tag")
	waitGuardian(t, g)
	time.Sleep(guardianSample + 300*time.Millisecond) // the guardian has seen both
	g.remove(a)                                       // "del a": a's terminal ended (and its process was not Coral's to end)
	time.Sleep(300 * time.Millisecond)                // the line is read before the pipe closes
	g.stop()                                          // as Coral's exit would: the guardian ends what it still holds
	waitGone(t, b, 10*time.Second)
	if err := syscall.Kill(a, 0); err != nil {
		t.Fatalf("the terminal the guardian was told had ended was ended with the others: %v", err)
	}
	_ = mb
}
