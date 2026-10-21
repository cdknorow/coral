package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/logexit"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a bytes.Buffer safe for the terminal goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// stalledTerminal blocks every Write until released, like a terminal window that
// stopped reading (Ctrl-S, or a full pty buffer nobody drains).
type stalledTerminal struct {
	release chan struct{}
	out     syncBuffer
}

func (s *stalledTerminal) Write(p []byte) (int, error) {
	<-s.release
	return s.out.Write(p)
}

// #3226: production stopped at 19:03:55 because a log line could not be written
// to the terminal, which froze every goroutine that logs and kept the line out of
// coral.log. Logging must keep going, to the file, whatever the terminal does.
func TestLogOutputNeverBlocksOnAStalledTerminal(t *testing.T) {
	var file syncBuffer
	term := &stalledTerminal{release: make(chan struct{})}
	t.Cleanup(func() { close(term.release) })
	out, _ := logOutput(&file, term)
	logger := log.New(out, "", 0)

	const lines = 10000
	done := make(chan struct{})
	go func() {
		for i := 0; i < lines; i++ {
			logger.Printf("line %d", i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logging blocked on a terminal that is not reading")
	}
	assert.Equal(t, lines, strings.Count(file.String(), "\n"), "every line reached the file")
	assert.Contains(t, file.String(), fmt.Sprintf("line %d\n", lines-1))
}

// When the terminal reads again, it gets the queued lines in order and a note of
// how many it missed.
func TestTerminalWriterResumesAndReportsDrops(t *testing.T) {
	term := &stalledTerminal{release: make(chan struct{})}
	w := newTerminalWriter(term, 4)
	for i := 0; i < 20; i++ {
		fmt.Fprintf(w, "line %d\n", i)
	}
	close(term.release)
	// The queue may still be full for a moment after the terminal reads again, and a line
	// written into a full queue is dropped by design, so keep writing until one gets through.
	require.Eventually(t, func() bool {
		fmt.Fprintf(w, "after\n")
		return strings.Contains(term.out.String(), "after\n")
	}, 5*time.Second, 10*time.Millisecond)
	out := term.out.String()
	assert.Contains(t, out, "line(s) were not shown here while the terminal was not reading; they are in coral.log")
	assert.Less(t, strings.Index(out, "line 0\n"), strings.Index(out, "after\n"), "order is kept")
}

// The log package reuses its buffer between calls: a queued line must be a copy.
func TestTerminalWriterCopiesTheLine(t *testing.T) {
	term := &stalledTerminal{release: make(chan struct{})}
	w := newTerminalWriter(term, 4)
	buf := []byte("first\n")
	w.Write(buf)
	copy(buf, "XXXXX\n")
	close(term.release)
	require.Eventually(t, func() bool { return term.out.String() != "" }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "first\n", term.out.String())
}

// slowTerminal reads, but slowly, like a busy terminal window.
type slowTerminal struct {
	out syncBuffer
}

func (s *slowTerminal) Write(p []byte) (int, error) {
	time.Sleep(time.Millisecond)
	return s.out.Write(p)
}

// Flush waits for every queued line, so lines logged just before an exit reach
// the terminal (#3255 review).
func TestTerminalWriterFlushWritesQueuedLines(t *testing.T) {
	term := &slowTerminal{}
	w := newTerminalWriter(term, 64)
	for i := 0; i < 50; i++ {
		fmt.Fprintf(w, "line %d\n", i)
	}
	w.Flush(5 * time.Second)
	assert.Equal(t, 50, strings.Count(term.out.String(), "\n"))
	assert.True(t, strings.HasSuffix(term.out.String(), "line 49\n"))
}

// A terminal that is not reading must not hold up an exit: Flush gives up at
// its deadline, whether the queue is full or a write is stuck.
func TestTerminalWriterFlushReturnsOnAStalledTerminal(t *testing.T) {
	for _, depth := range []int{1, 64} {
		term := &stalledTerminal{release: make(chan struct{})}
		w := newTerminalWriter(term, depth)
		for i := 0; i < 200; i++ {
			fmt.Fprintf(w, "line %d\n", i)
		}
		start := time.Now()
		w.Flush(200 * time.Millisecond)
		elapsed := time.Since(start)
		close(term.release)
		assert.GreaterOrEqual(t, elapsed, 150*time.Millisecond, "depth %d: waited for the terminal", depth)
		assert.Less(t, elapsed, 2*time.Second, "depth %d: gave up at the deadline", depth)
	}
}

type failingFile struct{}

func (failingFile) Write(p []byte) (int, error) { return 0, errors.New("disk full") }

// A failing coral.log (a full disk) must not also hide the line from the
// terminal; the writer still reports the file's error.
func TestLogOutputKeepsTheTerminalCopyWhenTheFileFails(t *testing.T) {
	term := &slowTerminal{}
	out, terminal := logOutput(failingFile{}, term)
	n, err := out.Write([]byte("still shown\n"))
	assert.EqualError(t, err, "disk full")
	assert.Equal(t, len("still shown\n"), n)
	terminal.Flush(5 * time.Second)
	assert.Equal(t, "still shown\n", term.out.String())
}

// The real exit path: the server logs, then a fatal error ends it. The fatal
// line ("Failed to start: ... another Coral server is already running" at
// 19:30) must reach the terminal, not only coral.log.
func TestFatalLineReachesTheTerminal(t *testing.T) {
	if dir := os.Getenv("CORAL_TEST_FATAL_DIR"); dir != "" {
		setupCrashLogging(dir)
		for i := 0; i < 2000; i++ {
			log.Printf("busy %d", i)
		}
		logexit.Fatalf("Failed to start: the last line")
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFatalLineReachesTheTerminal$")
	cmd.Env = append(os.Environ(), "CORAL_TEST_FATAL_DIR="+dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 1, exit.ExitCode())
	assert.Contains(t, stderr.String(), "Failed to start: the last line")
	assert.Contains(t, stderr.String(), "busy 1999")
	file, rerr := os.ReadFile(filepath.Join(dir, "coral.log"))
	require.NoError(t, rerr)
	assert.Contains(t, string(file), "Failed to start: the last line")
}
