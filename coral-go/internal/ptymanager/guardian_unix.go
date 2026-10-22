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
				g.seen = unionOf(g.seen, snap.tree(g.root, g.tag))
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
	go func() {
		for range time.Tick(guardianSample) {
			mu.Lock()
			n := len(guard)
			mu.Unlock()
			if n > 0 {
				sample(0)
			}
		}
	}()
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		switch {
		case len(f) == 4 && f[0] == "add":
			pid, e1 := strconv.Atoi(f[1])
			created, e2 := strconv.ParseInt(f[2], 10, 64)
			if e1 != nil || e2 != nil || pid <= 1 || f[3] == "" {
				continue
			}
			mu.Lock()
			guard[pid] = &guarded{root: member{pid, created}, tag: f[3]}
			mu.Unlock()
			sample(pid)
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

// guardianClient is Coral's end: the guardian it started, and what it has told it, so a guardian that died is replaced and told again. It holds no goroutine: the guardian is found dead by a write to
// it failing or by a non-blocking wait (pid), so a test's goroutine leak check sees nothing of it.
type guardianClient struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	in   io.WriteCloser
	regs map[int]guarded
	off  bool
}

var guardian guardianClient

// disable is for a test that must not start one.
func (g *guardianClient) disable() { g.mu.Lock(); g.off = true; g.mu.Unlock() }

// start runs the guardian (g.mu held). The environment it gets is only the marker: it needs none of Coral's.
func (g *guardianClient) start() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c, in, err := startGuardianProcess(exe)
	if err != nil {
		return err
	}
	g.cmd, g.in = c, in
	for _, r := range g.regs { // a replacement is told what the first knew
		fmt.Fprintf(in, "add %d %d %s\n", r.root.pid, r.root.created, r.tag)
	}
	return nil
}

// drop forgets a guardian that has died, and reaps it (g.mu held).
func (g *guardianClient) drop() {
	if g.cmd != nil {
		g.in.Close()
		var ws syscall.WaitStatus
		syscall.Wait4(g.cmd.Process.Pid, &ws, syscall.WNOHANG, nil)
	}
	g.cmd, g.in = nil, nil
}

// alive reports whether the guardian is running (g.mu held): a non-blocking wait says so, and reaps one that has died.
func (g *guardianClient) alive() bool {
	if g.cmd == nil {
		return false
	}
	var ws syscall.WaitStatus
	if wpid, err := syscall.Wait4(g.cmd.Process.Pid, &ws, syscall.WNOHANG, nil); wpid == g.cmd.Process.Pid || err == syscall.ECHILD {
		g.cmd, g.in = nil, nil
		return false
	}
	return true
}

// send writes a line, starting a new guardian first if there is none (which is told every registered terminal, this line's included).
func (g *guardianClient) send(line string) {
	for try := 0; try < 2; try++ {
		if !g.alive() {
			if g.start() != nil {
				return
			}
			return // it was told everything registered
		}
		if _, err := io.WriteString(g.in, line); err == nil {
			return
		}
		g.drop()
	}
}

// add registers a terminal's shell with the guardian.
func (g *guardianClient) add(root member, tag string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.off || root.pid <= 1 {
		return
	}
	if g.regs == nil {
		g.regs = map[int]guarded{}
	}
	g.regs[root.pid] = guarded{root: root, tag: tag}
	g.send(fmt.Sprintf("add %d %d %s\n", root.pid, root.created, tag))
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
		g.send(fmt.Sprintf("del %d\n", pid))
	}
}

// pid is the guardian's pid (0 when none is running), and how many terminals it was told about.
func (g *guardianClient) pid() (pid, registered int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.alive() {
		pid = g.cmd.Process.Pid
	}
	return pid, len(g.regs)
}

// stop ends the guardian by closing its pipe, as Coral's exit would, for a test that checks no process is left.
func (g *guardianClient) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cmd != nil {
		g.in.Close()
		g.cmd.Wait()
		g.cmd, g.in = nil, nil
	}
}
