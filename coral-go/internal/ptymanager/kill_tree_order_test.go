//go:build !windows

package ptymanager

import (
	"sync"
	"syscall"
	"testing"
)

// The order of killTree's signals (#6125): everything found is stopped (so nothing in it can fork), swept again, stopped again until a sweep finds nothing new, and only then killed; and a stop is never
// left without its kill. The fork race itself is not deterministic, so the order is pinned through the seams: the signal sent, the sweep, and whether a member is still the process found.

type sent struct {
	pid int
	sig syscall.Signal
}

func fakeTree(t *testing.T, sweeps [][]member) (log *[]sent) {
	t.Helper()
	var mu sync.Mutex
	log = &[]sent{}
	oldSend, oldSweep, oldLive, oldRounds := sendSignal, sweepTree, memberLive, stopRounds
	t.Cleanup(func() { sendSignal, sweepTree, memberLive, stopRounds = oldSend, oldSweep, oldLive, oldRounds })
	n := 0
	sendSignal = func(pid int, sig syscall.Signal) {
		mu.Lock()
		defer mu.Unlock()
		*log = append(*log, sent{pid, sig})
	}
	sweepTree = func(member, string) []member {
		mu.Lock()
		defer mu.Unlock()
		i := min(n, len(sweeps)-1)
		n++
		if sweeps[i] == nil {
			panic("sweep failed")
		}
		return sweeps[i]
	}
	memberLive = func(member) bool { return true }
	return log
}

func pidsWith(log []sent, sig syscall.Signal) []int {
	var out []int
	for _, s := range log {
		if s.sig == sig && s.pid > 0 {
			out = append(out, s.pid)
		}
	}
	return out
}

func lastIndex(log []sent, sig syscall.Signal) int {
	at := -1
	for i, s := range log {
		if s.sig == sig {
			at = i
		}
	}
	return at
}

func firstIndex(log []sent, sig syscall.Signal) int {
	for i, s := range log {
		if s.sig == sig {
			return i
		}
	}
	return -1
}

func TestKillTreeStopsEverythingBeforeAnythingIsKilledAndSweepsAgain(t *testing.T) {
	a, b, c := member{101, 1}, member{102, 2}, member{103, 3}
	// known {a}; the first sweep finds a and b; the second (after the stop) finds a new child c; the third finds nothing new
	log := fakeTree(t, [][]member{{a, b}, {a, b, c}, {a, b, c}})
	got := killTree([]member{a}, member{}, "tag")
	if len(got) != 3 {
		t.Fatalf("the set that was killed has %d members, want 3: %v", len(got), got)
	}
	if first, last := firstIndex(*log, syscall.SIGKILL), lastIndex(*log, syscall.SIGSTOP); first < 0 || last < 0 || last > first {
		t.Fatalf("every SIGSTOP comes before the first SIGKILL (last stop at %d, first kill at %d): %v", last, first, *log)
	}
	stopped := map[int]bool{}
	for _, pid := range pidsWith(*log, syscall.SIGSTOP) {
		stopped[pid] = true
	}
	if !stopped[101] || !stopped[102] || !stopped[103] {
		t.Fatalf("the whole set is stopped, the member found by the second sweep included: %v", *log)
	}
	killed := map[int]bool{}
	for _, pid := range pidsWith(*log, syscall.SIGKILL) {
		killed[pid] = true
	}
	if !killed[101] || !killed[102] || !killed[103] {
		t.Fatalf("the whole set is killed: %v", *log)
	}
}

// A sweep that fails (a panic) cannot leave a stop without a kill.
func TestKillTreeEndsWhatItStoppedEvenIfASweepFails(t *testing.T) {
	a, b := member{201, 1}, member{202, 2}
	log := fakeTree(t, [][]member{{a, b}, nil}) // the second sweep panics, after the first stop
	func() {
		defer func() { recover() }()
		killTree([]member{a}, member{}, "tag")
	}()
	if k := pidsWith(*log, syscall.SIGKILL); len(k) == 0 || len(pidsWith(*log, syscall.SIGSTOP)) == 0 {
		t.Fatalf("what was stopped is killed though the sweep failed: %v", *log)
	}
	if last, first := lastIndex(*log, syscall.SIGSTOP), firstIndex(*log, syscall.SIGKILL); last > first {
		t.Fatalf("no stop after the kill began: %v", *log)
	}
}
