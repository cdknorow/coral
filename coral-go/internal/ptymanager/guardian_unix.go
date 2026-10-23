//go:build !windows

package ptymanager

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/subgentic/coral/internal/safego"
)

// The guardian (#6129): when Coral dies without cleaning up (SIGKILL, a crash, the machine killing it), the system hangs up each terminal, which ends an agent that exits on hangup and leaves the
// helpers that have a session or group of their own. So Coral keeps one small process beside it, the same program started again in a hidden mode, that does nothing while Coral lives and, when
// Coral is gone, ends every registered terminal's tree with the code a Kill uses.
//
// Coral writes to the guardian's stdin, a pipe only Coral holds the other end of, one line per change: "add <shell pid> <shell start time> <tag>" when a terminal starts, "del <shell pid>" when it
// has ended. EOF on that pipe means Coral is gone, however it went. The guardian samples each registered terminal's tree every guardianSample (and once when it is added) so that a helper that
// detaches from its parent later is still known by then; it reads no file, no credential and no network, and its only input is that pipe from its own parent.

// guardianArg and guardianEnv start the hidden mode: both, and nothing else on the command line.
const (
	guardianArg = "__coral-pty-guardian"
	guardianEnv = "CORAL_PTY_GUARDIAN"
)

// guardianSample is how often the guardian looks at the trees it guards, while there are any.
const guardianSample = 3 * time.Second

// init runs the guardian when this program was started as one. It is here, in a package every program that runs terminals imports, so it runs before main: before any data directory, setting or
// network connection is opened, and in every program (coral, the tray, the app) and test binary that can start a terminal without each knowing about it.
func init() {
	if isGuardianStart(os.Args, os.Getenv(guardianEnv)) {
		// Its only input is the pipe from its own parent: a terminal or a file as stdin is somebody driving it by hand, and it ends at once.
		if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
			os.Exit(2)
		}
		runGuardian(os.Stdin)
		os.Exit(0)
	}
}

// isGuardianStart: the guardian mode is exactly the one argument and the marker, nothing else.
func isGuardianStart(args []string, marker string) bool {
	return len(args) == 2 && args[1] == guardianArg && marker == "1"
}

// runSampler calls sample(0) every `every` while count() says there is something guarded. A panic in it restarts the loop (safego.Restart) rather than ending the sampling for the life of the
// guardian: a guardian that sampled no more would still end the trees it knew, but not the helpers that detach from now on.
// It ends when done is closed (the guardian passes nil: it runs until the process ends).
func runSampler(every time.Duration, done <-chan struct{}, count func() int, sample func(int)) {
	safego.Restart("ptymanager/guardian-sample", func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if count() > 0 {
					sample(0)
				}
			}
		}
	})
}

// parseGuardianAdd reads an "add <pid> <start time> <tag>" line (already split into fields): the terminal's shell, and the tag its tree carries. A pid that is not a number, 0, 1 or negative is refused (the guardian
// ends registered trees when Coral is gone: pid 1 is init, 0 and a negative pid are process groups), and so are a start time that is not a number and an empty tag; nothing is registered for them.
func parseGuardianAdd(f []string) (root member, tag string, ok bool) {
	if len(f) != 4 || f[0] != "add" {
		return member{}, "", false
	}
	pid, e1 := strconv.Atoi(f[1])
	created, e2 := strconv.ParseInt(f[2], 10, 64)
	if e1 != nil || e2 != nil || pid <= 1 || f[3] == "" {
		return member{}, "", false
	}
	return member{pid, created}, f[3], true
}

type guarded struct {
	root member
	tag  string
	seen []member
}

// runGuardian serves the pipe until it ends, then ends what it guards.
func runGuardian(in io.Reader) {
	signal.Ignore(syscall.SIGHUP, syscall.SIGINT, syscall.SIGPIPE)
	var mu sync.Mutex
	guard := map[int]*guarded{}
	sample := func(pid int) {
		snap := takeSnap()
		mu.Lock()
		defer mu.Unlock()
		for p, g := range guard {
			if pid == 0 || p == pid {
				g.seen, _ = mergeCapped(stillThereBy(g.seen, snap.has), snap.tree(g.root, g.tag), maxRemembered) // what has ended is forgotten, and the set is capped like Coral's own
			}
		}
	}
	finish := func() {
		mu.Lock()
		all := make([]*guarded, 0, len(guard))
		for _, g := range guard {
			all = append(all, g)
		}
		mu.Unlock()
		var wg sync.WaitGroup
		for _, g := range all {
			wg.Add(1)
			go func(g *guarded) {
				defer safego.Recover("ptymanager/guardian-finish")
				defer wg.Done()
				left := killTree(g.seen, g.root, g.tag)
				for end := time.Now().Add(time.Second); anyAlive(left) && time.Now().Before(end); {
					time.Sleep(10 * time.Millisecond)
				}
			}(g)
		}
		wg.Wait()
		os.Exit(0)
	}
	// SIGTERM is not handled: it ends the guardian like any process, and ends no tree. The pipe reaching EOF is the one thing that makes it end terminals, so a stray "kill" or "pkill" that matches its
	// name cannot end every agent; Coral finds a dead guardian on its next write and starts another, told every terminal.
	runSampler(guardianSample, nil, func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(guard)
	}, sample)
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		switch {
		case len(f) == 4 && f[0] == "add":
			root, tag, ok := parseGuardianAdd(f)
			if !ok {
				continue
			}
			mu.Lock()
			guard[root.pid] = &guarded{root: root, tag: tag}
			mu.Unlock()
			sample(root.pid)
		case len(f) == 2 && f[0] == "del":
			if pid, err := strconv.Atoi(f[1]); err == nil {
				mu.Lock()
				delete(guard, pid)
				mu.Unlock()
			}
		}
	}
	finish()
}

// guardianClient is Coral's end: the guardian it started, and what it has told it. A goroutine waits on the guardian process (watch) and, as soon as it exits while terminals are registered, starts another
// and tells it every terminal, after a delay that grows if it keeps dying at once, so a guardian that cannot run cannot make Coral spin. Terminals are therefore guarded again within a moment of a guardian
// being killed, not at the next terminal start or end. The goroutine lives for the process by design (testleak ignores it).
type guardianClient struct {
	mu        sync.Mutex
	h         *guardianHandle
	regs      map[int]guarded
	off       bool
	stopping  bool
	delay     time.Duration // the wait before the next restart: it doubles while guardians die within guardianLives of starting
	onReplace func()        // a test's hook, called as a replacement is about to be decided
}

type guardianHandle struct {
	cmd      *exec.Cmd
	in       io.WriteCloser
	done     chan struct{} // closed when the process has exited and been waited for
	started  time.Time
	deciding bool // its watcher has begun deciding on a replacement (g.mu held)
}

var guardian guardianClient

const (
	guardianRestartMin = 100 * time.Millisecond
	guardianRestartMax = 5 * time.Second
	guardianLives      = 10 * time.Second // a guardian that ran at least this long is not a crash loop: the delay starts again
)

// disable is for a test that must not start one.
func (g *guardianClient) disable() { g.mu.Lock(); g.off = true; g.mu.Unlock() }

// start runs the guardian (g.mu held) and tells it every registered terminal. The environment it gets is only the marker: it needs none of Coral's.
func (g *guardianClient) start() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c, in, err := startGuardianProcess(exe)
	if err != nil {
		return err
	}
	h := &guardianHandle{cmd: c, in: in, done: make(chan struct{}), started: time.Now()}
	g.h = h
	for _, r := range g.regs { // a replacement is told what the first knew
		fmt.Fprintf(in, "add %d %d %s\n", r.root.pid, r.root.created, r.tag)
	}
	safego.Go("ptymanager/guardian-watch", func() { g.watch(h) })
	return nil
}

// watch waits for the guardian to exit; if Coral did not stop it and terminals are registered, it starts the next one. The wait happens once; deciding on the replacement is
// what a panic restarts (replace is safe to run again), so a panic cannot leave a dead guardian unreplaced.
func (g *guardianClient) watch(h *guardianHandle) {
	h.cmd.Wait()
	close(h.done)
	safego.Restart("ptymanager/guardian-replace", func() { g.replace(h) })
}

// replace starts the guardian that follows h, if h was the current one and there is still something to guard. It leaves g.mu free if it panics, and running it again after that decides afresh.
func (g *guardianClient) replace(h *guardianHandle) {
	if g.onReplace != nil {
		g.onReplace()
	}
	wait, ok := func() (time.Duration, bool) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.h != h && !h.deciding { // not the current guardian any more (a failed write retired it): its replacement is not ours to start
			return 0, false
		}
		h.deciding = true // a run again after a panic goes on from here
		if g.h == h {
			g.h = nil
		}
		if g.stopping || g.off || len(g.regs) == 0 {
			return 0, false
		}
		g.delay = nextRestartDelay(g.delay, time.Since(h.started))
		return g.delay, true
	}()
	if !ok {
		return
	}
	time.Sleep(wait)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.h == nil && !g.stopping && !g.off && len(g.regs) > 0 { // nothing else has started one meanwhile, and there is still something to guard
		g.start()
	}
}

// nextRestartDelay is how long to wait before replacing a guardian that has just exited, given the wait before the last one and how long this one lived: the minimum for the first death and after a
// guardian that lived long enough to be no crash loop, otherwise double the last, up to the maximum.
func nextRestartDelay(prev, lived time.Duration) time.Duration {
	if lived >= guardianLives || prev == 0 {
		return guardianRestartMin
	}
	return min(prev*2, guardianRestartMax)
}

// alive reports whether the guardian is running (g.mu held).
func (g *guardianClient) alive() bool {
	if g.h == nil {
		return false
	}
	select {
	case <-g.h.done:
		return false
	default:
		return true
	}
}

// send writes a line, starting a guardian first if there is none (which is told every registered terminal, this line's included).
//
// It returns a guardian whose pipe failed while it may still be running, for the caller to retire AFTER releasing g.mu (retire can wait seconds, and every terminal start and end would wait behind it); then
// the caller starts the next (afterRetire).
func (g *guardianClient) send(line string) (failed *guardianHandle) {
	if !g.alive() {
		if g.h != nil { // exited, its watcher not yet through
			g.h = nil
		}
		g.start()
		return nil
	}
	if _, err := io.WriteString(g.h.in, line); err != nil {
		failed, g.h = g.h, nil
	}
	return failed
}

// afterRetire retires a guardian whose pipe failed, without holding g.mu, and then starts the next if nothing else has (g.mu not held).
func (g *guardianClient) afterRetire(failed *guardianHandle) {
	if failed == nil {
		return
	}
	g.retire(failed)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.h == nil && !g.stopping && !g.off && len(g.regs) > 0 {
		g.start()
	}
}

// retire gets rid of a guardian whose pipe failed (g.mu NOT held: it waits), and never closes the pipe of one that is still running: EOF on it is what makes a guardian end every registered tree, and Coral is alive.
// A running guardian is first told to exit (SIGTERM ends it and ends no tree; SIGKILL after two seconds, which ends nothing either) and waited for; its pipe is closed only once it has gone.
func (g *guardianClient) retire(h *guardianHandle) {
	select {
	case <-h.done:
	default:
		// Through the os.Process, which knows when it has already been waited for: a pid that was reused in the gap since the check is never signalled.
		h.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-h.done:
		case <-time.After(2 * time.Second):
			h.cmd.Process.Kill()
			select {
			case <-h.done:
			case <-time.After(2 * time.Second):
				return // it will not go: leave its pipe open rather than end the trees of a Coral that is running
			}
		}
	}
	h.in.Close()
}

// add registers a terminal's shell with the guardian.
func (g *guardianClient) add(root member, tag string) {
	g.mu.Lock()
	if g.off || root.pid <= 1 {
		g.mu.Unlock()
		return
	}
	if g.regs == nil {
		g.regs = map[int]guarded{}
	}
	g.regs[root.pid] = guarded{root: root, tag: tag}
	failed := g.send(fmt.Sprintf("add %d %d %s\n", root.pid, root.created, tag))
	g.mu.Unlock()
	g.afterRetire(failed)
}

// remove unregisters a terminal that has ended.
func (g *guardianClient) remove(pid int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.regs[pid]; !ok {
		return
	}
	delete(g.regs, pid)
	if g.alive() {
		io.WriteString(g.h.in, fmt.Sprintf("del %d\n", pid))
	}
}

// pid is the guardian's pid (0 when none is running), and how many terminals it was told about.
func (g *guardianClient) pid() (pid, registered int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.alive() {
		pid = g.h.cmd.Process.Pid
	}
	return pid, len(g.regs)
}

// stop ends the guardian by closing its pipe, as Coral's exit would, and does not replace it (a test that checks no process is left).
func (g *guardianClient) stop() {
	g.mu.Lock()
	h := g.h
	g.stopping = true
	g.mu.Unlock()
	if h != nil {
		h.in.Close()
		<-h.done
	}
}
