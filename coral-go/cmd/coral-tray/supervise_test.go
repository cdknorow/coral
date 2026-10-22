package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/server/routes"
)

// TestHelperChild is the fake server child for the supervisor tests: this test binary,
// started with TRAY_CHILD_MODE set, behaves as the mode says and exits.
func TestHelperChild(t *testing.T) {
	mode := os.Getenv("TRAY_CHILD_MODE")
	if mode == "" {
		t.Skip("only a helper process")
	}
	n := 0
	if f := os.Getenv("TRAY_CHILD_COUNT"); f != "" { // which start this is (1, 2, ...)
		b, _ := os.ReadFile(f)
		n = strings.Count(string(b), "x") + 1
		os.WriteFile(f, []byte(strings.Repeat("x", n)), 0o600)
	}
	serve := func() { // ready, then until standard input closes
		fmt.Println(childSaysReady)
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	switch mode {
	case "crash":
		fmt.Fprintln(os.Stderr, "panic: runtime error: invalid memory address")
		os.Exit(2)
	case "startfail":
		fmt.Fprintln(os.Stderr, "2026/10/17 01:00:00 Failed to start: can't listen on 127.0.0.1 port 8420 (in use?)")
		os.Exit(childExitStartFailed)
	case "quit-for-update": // the local "Install update" started the updater (#166)
		fmt.Println(childSaysReady)
		os.Exit(childExitQuitForUpdate)
	case "restart-once":
		if n == 1 {
			fmt.Println(childSaysReady)
			os.Exit(childExitRestart)
		}
		serve()
	case "crash-then-serve":
		if n == 1 {
			os.Exit(2)
		}
		serve()
	case "serve":
		serve()
	case "telemetry-switched-off": // on at startup, off a moment after ready: a switch in Settings (#5702)
		fmt.Println(childSaysTelemetryOn)
		fmt.Println(childSaysReady)
		time.Sleep(50 * time.Millisecond)
		fmt.Println(childSaysTelemetryOff)
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "telemetry-on", "telemetry-off", "telemetry-on-then-off": // the line startup's applied setting becomes (#5184)
		if mode == "telemetry-off" || (mode == "telemetry-on-then-off" && n == 2) {
			fmt.Println(childSaysTelemetryOff)
		} else {
			fmt.Println(childSaysTelemetryOn)
		}
		if mode == "telemetry-on-then-off" && n == 1 {
			fmt.Println(childSaysReady)
			os.Exit(childExitRestart)
		}
		serve()
	case "stops-itself":
		fmt.Println(childSaysReady)
		os.Exit(0)
	case "password-check": // accepts only "good", as a server opening an encrypted database
		fmt.Println(childSaysNeedPassword)
		pw, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(pw) != "good" {
			fmt.Fprintln(os.Stderr, "Failed to start: failed to open database: wrong password")
			os.Exit(childExitStartFailed)
		}
		serve()
	case "password":
		fmt.Println(childSaysNeedPassword)
		pw, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Fprintf(os.Stderr, "got password %q\n", strings.TrimSpace(pw))
		if n == 1 {
			fmt.Println(childSaysReady)
			os.Exit(2) // then crash: the restart must get the same password without a prompt
		}
		serve()
	}
	os.Exit(9)
}

type stateLog struct {
	mu     sync.Mutex
	states []serverState
	last   string
}

func (l *stateLog) set(s serverState, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.states = append(l.states, s)
	l.last = reason
}

func (l *stateLog) has(s serverState) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, x := range l.states {
		if x == s {
			return true
		}
	}
	return false
}

func (l *stateLog) reason() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

// newTestSupervisor runs this test binary as the child in mode, with short backoff steps.
func newTestSupervisor(t *testing.T, mode string) (*supervisor, *stateLog, func() int, func() string) {
	t.Helper()
	prev := crashBackoff
	crashBackoff = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}
	t.Cleanup(func() { crashBackoff = prev })
	count := filepath.Join(t.TempDir(), "starts")
	t.Setenv("TRAY_CHILD_MODE", mode)
	t.Setenv("TRAY_CHILD_COUNT", count)
	var logBuf strings.Builder
	var logMu sync.Mutex
	w := writerFunc(func(p []byte) (int, error) { logMu.Lock(); defer logMu.Unlock(); return logBuf.Write(p) })
	s := newSupervisor(os.Args[0], []string{"-test.run=^TestHelperChild$"}, w)
	states := &stateLog{}
	s.onState = states.set
	starts := func() int {
		b, _ := os.ReadFile(count)
		return len(b)
	}
	t.Cleanup(func() { s.stop(5 * time.Second) })
	logs := func() string { logMu.Lock(); defer logMu.Unlock(); return logBuf.String() }
	return s, states, starts, logs
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func startAndRun(t *testing.T, s *supervisor) <-chan struct{} {
	t.Helper()
	ready, err := s.start()
	require.NoError(t, err)
	go s.run()
	return ready
}

// A server that keeps crashing is started again after each backoff step, and after the third
// crash in five minutes it is left stopped, with the crash's own line as the reason. Retry
// starts it again.
func TestSupervisorStopsACrashLoop(t *testing.T) {
	s, states, starts, _ := newTestSupervisor(t, "crash")
	startAndRun(t, s)
	require.Eventually(t, func() bool { return states.has(stateFailed) }, 15*time.Second, 10*time.Millisecond)
	assert.Equal(t, crashLoopLimit, starts(), "started, then twice more after a backoff")
	assert.True(t, states.has(stateRestarting))
	assert.Contains(t, states.reason(), "crashed 3 times in 5 minutes")
	assert.Contains(t, states.reason(), "panic: runtime error")
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, crashLoopLimit, starts(), "not started again on its own")

	s.retry()
	require.Eventually(t, func() bool { return starts() > crashLoopLimit }, 10*time.Second, 10*time.Millisecond, "Retry starts it again")
}

// A server that could not start (the port in use) is never restarted on its own; the reason
// is the server's own line.
func TestSupervisorNeverRestartsAStartFailure(t *testing.T) {
	s, states, starts, _ := newTestSupervisor(t, "startfail")
	startAndRun(t, s)
	require.Eventually(t, func() bool { return states.has(stateFailed) }, 10*time.Second, 10*time.Millisecond)
	assert.Contains(t, states.reason(), "can't listen on 127.0.0.1 port 8420 (in use?)")
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, starts())
	assert.False(t, states.has(stateRestarting))
}

// The tray's stop is clean: the server stops when its standard input closes and is not
// started again; one stopped from outside is not restarted either.
func TestSupervisorCleanStopsAreNotRestarted(t *testing.T) {
	s, states, starts, _ := newTestSupervisor(t, "serve")
	ready := startAndRun(t, s)
	require.True(t, s.awaitFirst(ready))
	require.Eventually(t, func() bool { return states.has(stateRunning) }, 5*time.Second, 10*time.Millisecond)
	s.stop(5 * time.Second)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, starts())
	assert.False(t, states.has(stateRestarting))

	s2, states2, starts2, _ := newTestSupervisor(t, "stops-itself")
	startAndRun(t, s2)
	require.Eventually(t, func() bool { return states2.has(stateStopped) }, 10*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, starts2(), "stopped from outside: not restarted")
}

// Settings > Restart: the server exits with childExitRestart and is started again at once,
// not counted as a crash.
func TestSupervisorRestartsOnRequest(t *testing.T) {
	s, states, starts, _ := newTestSupervisor(t, "restart-once")
	startAndRun(t, s)
	require.Eventually(t, func() bool { return starts() == 2 }, 10*time.Second, 10*time.Millisecond)
	assert.False(t, states.has(stateRestarting), "not a crash")
	s.mu.Lock()
	assert.Empty(t, s.policy.times)
	s.mu.Unlock()
}

// The local "Install update" (#166): the server exits with childExitQuitForUpdate; the supervisor does not start it again, calls onQuitForUpdate once (the tray ends itself so
// nothing runs from the install folder), and counts nothing as a crash.
func TestSupervisorQuitsForAnUpdateAndNeverRestarts(t *testing.T) {
	s, states, starts, _ := newTestSupervisor(t, "quit-for-update")
	quit := make(chan struct{}, 4)
	s.onQuitForUpdate = func() { quit <- struct{}{} }
	startAndRun(t, s)
	select {
	case <-quit:
	case <-time.After(10 * time.Second):
		t.Fatal("the tray was not asked to quit")
	}
	time.Sleep(500 * time.Millisecond) // a restart, if there were one, would have happened by now (the crash backoff starts at 1 s, a restart is at once)
	assert.Equal(t, 1, starts(), "not started again")
	assert.False(t, states.has(stateRestarting), "not a crash")
	assert.Len(t, quit, 0, "asked to quit once")
	s.mu.Lock()
	assert.Empty(t, s.policy.times, "not counted as a crash")
	s.mu.Unlock()
}

// The exit code is the one the server uses (routes.UpdateQuitExitCode), and it is not one of the codes that restart or fail.
func TestQuitForUpdateExitCode(t *testing.T) {
	assert.Equal(t, routes.UpdateQuitExitCode, childExitQuitForUpdate)
	assert.NotEqual(t, childExitRestart, childExitQuitForUpdate)
	assert.NotEqual(t, childExitStartFailed, childExitQuitForUpdate)
}

// One crash, then a server that runs: it is restarted after the first backoff step.
func TestSupervisorRestartsACrash(t *testing.T) {
	s, states, starts, _ := newTestSupervisor(t, "crash-then-serve")
	startAndRun(t, s)
	require.Eventually(t, func() bool { return starts() == 2 && states.has(stateRunning) }, 10*time.Second, 10*time.Millisecond)
	assert.True(t, states.has(stateRestarting))
	assert.False(t, states.has(stateFailed))
}

// The database password is asked for once, on the caller's goroutine (the tray's main thread);
// the server started after a crash gets the same password without another prompt.
func TestSupervisorPasswordIsAskedOnce(t *testing.T) {
	s, states, starts, logs := newTestSupervisor(t, "password")
	asked := 0
	s.password = func() (string, error) { asked++; return "s3cret", nil }
	ready := startAndRun(t, s)
	require.True(t, s.awaitFirst(ready))
	require.Eventually(t, func() bool {
		return starts() == 2 && states.has(stateRunning) && strings.Count(logs(), `got password "s3cret"`) == 2
	}, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, asked)
}

func TestCrashPolicy(t *testing.T) {
	var p crashPolicy
	t0 := time.Now()
	d, stop := p.onCrash(t0, time.Second)
	assert.Equal(t, time.Second, d)
	assert.False(t, stop)
	d, stop = p.onCrash(t0.Add(10*time.Second), time.Second)
	assert.Equal(t, 5*time.Second, d)
	assert.False(t, stop)
	_, stop = p.onCrash(t0.Add(20*time.Second), time.Second)
	assert.True(t, stop, "the third crash within five minutes")

	// Crashes spread out: the window never holds three, and the backoff goes on to 30 s.
	p.reset()
	for i, want := range []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 30 * time.Second} {
		d, stop := p.onCrash(t0.Add(time.Duration(i)*3*time.Minute), 2*time.Minute)
		assert.False(t, stop, "crash %d", i)
		assert.Equal(t, want, d, "crash %d", i)
	}
	// A run of five minutes or more starts the backoff over.
	d, _ = p.onCrash(t0.Add(time.Hour), 10*time.Minute)
	assert.Equal(t, time.Second, d)
}

func TestExitReasonPrefersTheServersLine(t *testing.T) {
	tail := newLineTail(5)
	fmt.Fprint(tail, "2026/10/17 [STARTUP] starting\n2026/10/17 Failed to start: can't listen on 127.0.0.1 port 8420 (in use?)\n")
	assert.Equal(t, "Failed to start: can't listen on 127.0.0.1 port 8420 (in use?)", exitReason(nil, tail))
	assert.Equal(t, "Not running: can't listen on 127.0.0.1 port 8420 (in use?)", statusTitle("Not running: ", exitReason(nil, tail)))
}

// #4163 review (Security): a wrong password is never kept. The server refuses it and stops
// (a start failure, not restarted); Retry starts a server that asks again, and the right
// password gets it running.
func TestSupervisorAsksAgainAfterAWrongPassword(t *testing.T) {
	s, states, starts, _ := newTestSupervisor(t, "password-check")
	var mu sync.Mutex
	answers, asked := []string{"bad", "good"}, 0
	s.password = func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		asked++
		return answers[asked-1], nil
	}
	ready := startAndRun(t, s)
	require.False(t, s.awaitFirst(ready), "the wrong password: the server stops")
	go s.servePasswords()
	require.Eventually(t, func() bool { return states.has(stateFailed) }, 10*time.Second, 10*time.Millisecond)
	assert.Contains(t, states.reason(), "wrong password")

	s.retry()
	require.Eventually(t, func() bool { return starts() == 2 && states.has(stateRunning) }, 10*time.Second, 10*time.Millisecond)
	mu.Lock()
	assert.Equal(t, 2, asked, "asked again after the wrong one")
	mu.Unlock()
}
