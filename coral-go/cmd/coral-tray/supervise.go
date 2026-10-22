package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/safego"
)

// The tray runs Coral's server as a child process and supervises it (#4163, the operator's
// approval of #4151 proposal 1): when the server dies unexpectedly (a crash, a kill, out of
// memory) the tray starts it again after 1 s, 5 s, then 30 s, and after 3 crashes in 5 minutes
// it stops and shows why, with Open Log and Retry. A clean stop is never restarted: the tray's
// own Quit, a stop from outside, or a start that failed (the port or the data folder in use, a
// database that can't be opened). The instance lock is the child's, so it goes with the child;
// tmux agents outlive it and the next server re-adopts them (#4151).

// Exit codes of the server child (see child.go).
const (
	childExitStartFailed = 3 // startup.Start failed: retrying would fail the same way
	childExitRestart     = 4 // Settings > Restart: start again now
	// childExitQuitForUpdate: the local "Install update" started the updater (#166): the tray quits too and never starts the server again, so the updater has the
	// install folder to itself (it restarts Coral when it is done). The same number as routes.UpdateQuitExitCode (a test keeps them equal).
	childExitQuitForUpdate = 5
)

// Lines the child writes on its standard output for the tray.
const (
	childSaysReady        = "coral-child ready"
	childSaysNeedPassword = "coral-child need-password"
	// childSaysTelemetry is followed by on or off before the ready line (#5184): the server applied the saved Usage analytics setting (fail closed) and
	// the environment's opt-out, and the tray, which never reads the setting itself, sends its own events only when it hears "on".
	childSaysTelemetryOn  = "coral-child telemetry on"
	childSaysTelemetryOff = "coral-child telemetry off"
)

type exitKind int

const (
	exitClean         exitKind = iota // stopped as asked, or from outside: not restarted
	exitStartFailed                   // never restarted on its own
	exitRestart                       // restarted at once, not a crash
	exitQuitForUpdate                 // the updater has Coral: not restarted, and the tray quits
	exitCrashed                       // restarted with backoff
)

// classifyExit sorts the child's exit (cmd.Wait's error).
func classifyExit(err error) exitKind {
	if err == nil {
		return exitClean
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch ee.ExitCode() {
		case childExitStartFailed:
			return exitStartFailed
		case childExitRestart:
			return exitRestart
		case childExitQuitForUpdate:
			return exitQuitForUpdate
		}
	}
	return exitCrashed // a non-zero exit, a signal (ExitCode -1), or a wait error
}

// Crash policy: the backoff steps, and the crash-loop stop.
var (
	crashBackoff     = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
	crashLoopWindow  = 5 * time.Minute
	crashLoopLimit   = 3
	healthyRunResets = 5 * time.Minute // a run this long starts the backoff over
)

type crashPolicy struct {
	times  []time.Time // crashes within crashLoopWindow
	streak int         // crashes since the last healthy run
}

// onCrash records a crash at now after a run of ranFor, and returns how long to wait before
// starting again, or stop when it is the crashLoopLimit'th crash within crashLoopWindow.
func (p *crashPolicy) onCrash(now time.Time, ranFor time.Duration) (delay time.Duration, stop bool) {
	kept := p.times[:0]
	for _, t := range p.times {
		if now.Sub(t) < crashLoopWindow {
			kept = append(kept, t)
		}
	}
	p.times = append(kept, now)
	if len(p.times) >= crashLoopLimit {
		return 0, true
	}
	if ranFor >= healthyRunResets {
		p.streak = 0
	}
	step := p.streak
	if step >= len(crashBackoff) {
		step = len(crashBackoff) - 1
	}
	p.streak++
	return crashBackoff[step], false
}

func (p *crashPolicy) reset() { p.times, p.streak = nil, 0 }

// serverState is what the tray's menu shows.
type serverState int

const (
	stateStarting serverState = iota
	stateRunning
	stateRestarting // after a crash, waiting out the backoff
	stateFailed     // crashed too often, or could not start: Retry starts it again
	stateStopped    // stopped from outside: Retry starts it again
)

// supervisor runs one server child at a time.
type supervisor struct {
	exe  string
	args []string
	log  io.Writer // the child's standard error goes here (tray.log)

	// password answers the child's request for the database password (the tray's native
	// prompt). It is kept for restarts after a crash, as it was kept in the server's memory
	// when the server ran inside the tray.
	password func() (string, error)
	onState  func(serverState, string) // called on every change, with the reason
	// onQuitForUpdate is called, once, when the server exited for an update (#166): the tray ends itself so nothing of Coral runs from the install folder.
	onQuitForUpdate func()
	// onTelemetry hears whether the server's Usage analytics are on, once per start, before ready (#5184). Never called when the child says nothing.
	onTelemetry func(on bool)

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	tail     *lineTail
	started  time.Time
	stopping bool
	policy   crashPolicy
	pw       string // the password a server accepted (it said ready after using it)
	havePW   bool
	pending  string // the password given to the server starting now, not yet accepted
	havePend bool
	ready    chan struct{} // closed when the current child says ready
	exited   chan struct{} // closed when the current child has exited (run's Wait returned)
	wake     chan struct{} // Retry, or stop, during a backoff or a failed state
	pwAsk    chan io.WriteCloser
	done     chan struct{} // closed by stop
}

func newSupervisor(exe string, args []string, logW io.Writer) *supervisor {
	return &supervisor{exe: exe, args: args, log: logW, wake: make(chan struct{}, 1), pwAsk: make(chan io.WriteCloser, 1), done: make(chan struct{}), onState: func(serverState, string) {}}
}

// start launches a child; ready is closed when it says it is serving.
func (s *supervisor) start() (ready <-chan struct{}, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd := exec.Command(s.exe, s.args...)
	cmd.Env = os.Environ()
	executil.HideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	s.tail = newLineTail(40)
	cmd.Stderr = io.MultiWriter(s.log, s.tail)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s.cmd, s.stdin, s.started = cmd, stdin, time.Now()
	s.ready = make(chan struct{})
	s.exited = make(chan struct{})
	select { // a Retry pressed while the server ran is not a later wake-up
	case <-s.wake:
	default:
	}
	readyCh, in := s.ready, stdin
	go func() {
		defer safego.Recover("coral-tray/supervise:stdout")
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			switch strings.TrimSpace(sc.Text()) {
			case childSaysReady:
				close(readyCh)
				s.mu.Lock()
				if s.havePend { // the server accepted it: kept for a restart after a crash
					s.pw, s.havePW, s.pending, s.havePend = s.pending, true, "", false
				}
				s.mu.Unlock()
				s.onState(stateRunning, "")
			case childSaysTelemetryOn, childSaysTelemetryOff:
				if s.onTelemetry != nil {
					s.onTelemetry(strings.TrimSpace(sc.Text()) == childSaysTelemetryOn)
				}
			case childSaysNeedPassword:
				if pw, ok := s.cachedPassword(); ok {
					io.WriteString(in, pw+"\n")
					continue
				}
				select { // awaitFirst or servePasswords prompts
				case s.pwAsk <- in:
				default:
					in.Close() // a prompt is already open: the child gives up and exits
				}
			default:
				fmt.Fprintln(s.log, sc.Text())
			}
		}
	}()
	s.onState(stateStarting, "")
	log.Printf("[supervise] server started (pid %d)", cmd.Process.Pid)
	return readyCh, nil
}

func (s *supervisor) cachedPassword() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pw, s.havePW
}

// awaitFirst waits, on the caller's goroutine, until the first server serves (true) or exits
// (false). It runs the password prompt there when the server asks for one: the macOS prompt
// must run on the tray's main thread. A server started later (after a crash) gets the same
// password. run must already be running.
func (s *supervisor) awaitFirst(ready <-chan struct{}) bool {
	s.mu.Lock()
	exited := s.exited
	s.mu.Unlock()
	for {
		select {
		case <-ready:
			return true
		case <-exited:
			return false
		case w := <-s.pwAsk:
			s.answerPassword(w)
		}
	}
}

// servePasswords answers the password requests of every later server (a Retry after a wrong
// password, a start after a cached password stopped working) until stop. The tray starts it
// once awaitFirst has returned: on macOS the prompt then runs on the main thread through the
// run loop.
func (s *supervisor) servePasswords() {
	for {
		select {
		case <-s.done:
			return
		case w := <-s.pwAsk:
			s.answerPassword(w)
		}
	}
}

// answerPassword prompts and hands the password to the server asking for it. It is kept only
// once that server says ready: a wrong password is never reused.
func (s *supervisor) answerPassword(w io.WriteCloser) {
	if s.password == nil {
		w.Close()
		return
	}
	pw, err := s.password()
	if err != nil {
		w.Close() // cancelled: the server can't open its databases and says why
		return
	}
	s.mu.Lock()
	s.pending, s.havePend = pw, true
	s.mu.Unlock()
	io.WriteString(w, pw+"\n")
}

// run waits on the child and starts it again as the policy says, until stop. Call it once,
// after the first start.
func (s *supervisor) run() {
	for {
		s.mu.Lock()
		cmd, tail, started, exited := s.cmd, s.tail, s.started, s.exited
		s.mu.Unlock()
		err := cmd.Wait()
		close(exited)
		s.mu.Lock()
		stopping := s.stopping
		s.mu.Unlock()
		if stopping {
			return
		}
		kind := classifyExit(err)
		reason := exitReason(err, tail)
		switch kind {
		case exitQuitForUpdate:
			log.Printf("[supervise] server quit for an update; not restarting, and the tray quits")
			s.mu.Lock()
			s.stopping = true
			s.mu.Unlock()
			s.onState(stateStopped, "Coral is being updated")
			if s.onQuitForUpdate != nil {
				s.onQuitForUpdate()
			}
			return
		case exitRestart:
			log.Printf("[supervise] server asked to restart")
		case exitClean:
			log.Printf("[supervise] server stopped from outside; not restarting")
			s.onState(stateStopped, "Coral's server was stopped")
			if !s.waitForRetry() {
				return
			}
		case exitStartFailed:
			log.Printf("[supervise] server could not start: %s", reason)
			// A wrong or outdated password must not be reused: the next start asks again.
			s.mu.Lock()
			s.pw, s.havePW, s.pending, s.havePend = "", false, "", false
			s.mu.Unlock()
			s.onState(stateFailed, reason)
			if !s.waitForRetry() {
				return
			}
		case exitCrashed:
			s.mu.Lock()
			delay, stop := s.policy.onCrash(time.Now(), time.Since(started))
			s.mu.Unlock()
			if stop {
				log.Printf("[supervise] server crashed %d times in %s; not restarting: %s", crashLoopLimit, crashLoopWindow, reason)
				s.onState(stateFailed, fmt.Sprintf("crashed %d times in %d minutes: %s", crashLoopLimit, int(crashLoopWindow.Minutes()), reason))
				if !s.waitForRetry() {
					return
				}
				break
			}
			log.Printf("[supervise] server stopped unexpectedly (%s); starting it again in %s", reason, delay)
			s.onState(stateRestarting, reason)
			select {
			case <-time.After(delay):
			case <-s.wake:
			}
			if s.isStopping() {
				return
			}
		}
		if _, err := s.start(); err != nil {
			log.Printf("[supervise] could not start the server: %v", err)
			s.onState(stateFailed, err.Error())
			if !s.waitForRetry() {
				return
			}
			if _, err := s.start(); err != nil {
				return
			}
		}
	}
}

// waitForRetry blocks until Retry (true) or stop (false).
func (s *supervisor) waitForRetry() bool {
	<-s.wake
	if s.isStopping() {
		return false
	}
	s.mu.Lock()
	s.policy.reset()
	s.mu.Unlock()
	return true
}

// retry starts the server again from a failed or stopped state.
func (s *supervisor) retry() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *supervisor) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

// stop asks the child to shut down (its standard input closes) and waits up to limit, then
// kills it. Nothing is restarted after stop.
func (s *supervisor) stop(limit time.Duration) {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	close(s.done)
	cmd, stdin, exited := s.cmd, s.stdin, s.exited
	s.mu.Unlock()
	s.retry() // wake run from a backoff or a failed state
	if cmd == nil || cmd.Process == nil {
		return
	}
	stdin.Close()
	select {
	case <-exited:
	case <-time.After(limit):
		log.Printf("[supervise] server did not stop within %s; killing it", limit)
		cmd.Process.Kill()
	}
}

// exitReason is the line to show for an exit: the error the child logged ("Failed to start:
// ...", a panic, a fatal error), else the exit status.
func exitReason(err error, tail *lineTail) string {
	if tail != nil {
		lines := tail.lines()
		for i := len(lines) - 1; i >= 0; i-- {
			l := lines[i]
			for _, marker := range []string{"Failed to start: ", "panic: ", "fatal error: "} {
				if j := strings.Index(l, marker); j >= 0 {
					return strings.TrimSpace(l[j:])
				}
			}
		}
	}
	if err == nil {
		return "exited"
	}
	return err.Error()
}

// lineTail keeps the last n lines written to it.
type lineTail struct {
	mu   sync.Mutex
	n    int
	buf  []string
	part string
}

func newLineTail(n int) *lineTail { return &lineTail{n: n} }

func (t *lineTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.part + string(p)
	parts := strings.Split(s, "\n")
	t.part = parts[len(parts)-1]
	if len(t.part) > 4096 {
		t.part = t.part[len(t.part)-4096:]
	}
	for _, l := range parts[:len(parts)-1] {
		t.buf = append(t.buf, l)
		if len(t.buf) > t.n {
			t.buf = t.buf[len(t.buf)-t.n:]
		}
	}
	return len(p), nil
}

func (t *lineTail) lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := append([]string(nil), t.buf...)
	if t.part != "" {
		out = append(out, t.part)
	}
	return out
}
