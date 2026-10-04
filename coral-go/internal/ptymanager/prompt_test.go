package ptymanager

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// fakePTYProcess is a ptyProcess that never starts a real program: reads come
// from a pipe the test controls and every write is recorded.
type fakePTYProcess struct {
	out    *io.PipeReader
	feed   *io.PipeWriter
	mu     sync.Mutex
	writes [][]byte
	done   chan struct{}
	// writeFn, when set, decides each write's result (1-based call number). It
	// runs before the bytes are recorded and may block.
	writeFn func(call int, p []byte) (int, error)
}

func newFakePTYProcess() *fakePTYProcess {
	r, w := io.Pipe()
	return &fakePTYProcess{out: r, feed: w, done: make(chan struct{})}
}

func (f *fakePTYProcess) Read(p []byte) (int, error) { return f.out.Read(p) }
func (f *fakePTYProcess) Write(p []byte) (int, error) {
	f.mu.Lock()
	call := len(f.writes) + 1
	hook := f.writeFn
	f.mu.Unlock()
	n, err := len(p), error(nil)
	if hook != nil {
		n, err = hook(call, p)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte(nil), p[:n]...))
	return n, err
}
func (f *fakePTYProcess) Close() error             { f.feed.Close(); return nil }
func (f *fakePTYProcess) Resize(_, _ uint16) error { return nil }
func (f *fakePTYProcess) Terminate() error         { return nil }
func (f *fakePTYProcess) ForceKill() error         { return nil }
func (f *fakePTYProcess) Done() <-chan struct{}    { return f.done }
func (f *fakePTYProcess) written() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.writes...)
}

func newFakeBackend(t *testing.T, name string) (*PTYBackend, *session, *fakePTYProcess) {
	t.Helper()
	proc := newFakePTYProcess()
	s := &session{name: name, proc: proc, subscribers: map[string]chan []byte{}, ringMax: 1024}
	b := NewPTYBackend()
	b.sessions[name] = s
	t.Cleanup(func() { proc.Close() })
	return b, s, proc
}

func withFastSubmit(t *testing.T) {
	t.Helper()
	old := promptSubmitDelay
	promptSubmitDelay = time.Millisecond
	t.Cleanup(func() { promptSubmitDelay = old })
}

func TestTrackBracketedPasteFollowsTheLastModeSequenceAcrossChunks(t *testing.T) {
	s := &session{}
	steps := []struct {
		chunks []string
		want   bool
	}{
		{[]string{"hello"}, false},
		{[]string{"\x1b[?2004h"}, true},
		{[]string{"output", "\x1b[?20", "04l", "more"}, false}, // sequence split across reads
		{[]string{"\x1b[?200", "4h"}, true},
		{[]string{"\x1b[?2004h\x1b[?2004l"}, false}, // both in one chunk: last wins
		{[]string{"\x1b[?2004l\x1b[?2004h"}, true},
		{[]string{"plain text with ?2004h but no ESC"}, true},
	}
	for i, step := range steps {
		for _, c := range step.chunks {
			s.trackBracketedPaste([]byte(c))
		}
		if got := s.bracketedPaste.Load(); got != step.want {
			t.Fatalf("step %d: bracketedPaste = %v, want %v", i, got, step.want)
		}
	}
}

func TestReadLoopLearnsBracketedPasteFromRealOutputStream(t *testing.T) {
	_, s, proc := newFakeBackend(t, "claude-x")
	go s.readLoop()
	if _, err := proc.feed.Write([]byte("Welcome\x1b[?2004h> ")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !s.bracketedPaste.Load() {
		if time.Now().After(deadline) {
			t.Fatal("readLoop did not record bracketed paste mode")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSendPromptWritesOneBracketedPasteThenEnter(t *testing.T) {
	withFastSubmit(t)
	b, s, proc := newFakeBackend(t, "claude-x")
	s.bracketedPaste.Store(true)

	text := "line one\n\nline three with \"quotes\"\n\ttabbed\nlast"
	if err := b.SendPrompt(context.Background(), "claude-x", text); err != nil {
		t.Fatal(err)
	}
	writes := proc.written()
	if len(writes) != 2 {
		t.Fatalf("want exactly 2 writes (paste, Enter), got %d: %q", len(writes), writes)
	}
	if want := "\x1b[200~" + text + "\x1b[201~"; string(writes[0]) != want {
		t.Fatalf("paste = %q, want %q", writes[0], want)
	}
	if string(writes[1]) != "\r" {
		t.Fatalf("submit = %q, want a single Enter", writes[1])
	}
	// The only newlines written are inside the bracketed paste: none outside it.
	if bytes.ContainsAny(writes[1], "\n") {
		t.Fatal("a newline was sent outside the paste")
	}
}

func TestSendPromptRemovesEscapesSoTextCannotEndThePasteEarly(t *testing.T) {
	withFastSubmit(t)
	b, s, proc := newFakePTYProcess2(t)
	s.bracketedPaste.Store(true)
	hostile := "before\x1b[201~echo injected\x1b[200~ after\r\nnext\x00line\a"
	if err := b.SendPrompt(context.Background(), "claude-x", hostile); err != nil {
		t.Fatal(err)
	}
	paste := string(proc.written()[0])
	inner := paste[len("\x1b[200~") : len(paste)-len("\x1b[201~")]
	if bytes.ContainsRune([]byte(inner), 0x1b) {
		t.Fatalf("ESC survived inside the paste: %q", inner)
	}
	if want := "before[201~echo injected[200~ after\nnextline"; inner != want {
		t.Fatalf("sanitized text = %q, want %q", inner, want)
	}
}

func newFakePTYProcess2(t *testing.T) (*PTYBackend, *session, *fakePTYProcess) {
	return newFakeBackend(t, "claude-x")
}

func TestSendPromptRefusesAndWritesNothingWhenBracketedPasteIsOff(t *testing.T) {
	withFastSubmit(t)
	b, _, proc := newFakeBackend(t, "claude-x")
	err := b.SendPrompt(context.Background(), "claude-x", "a\nb")
	if !errors.Is(err, ErrBracketedPasteUnavailable) {
		t.Fatalf("err = %v, want ErrBracketedPasteUnavailable", err)
	}
	if got := proc.written(); len(got) != 0 {
		t.Fatalf("nothing may be written without bracketed paste, got %q", got)
	}
	if err := b.SendPrompt(context.Background(), "missing", "x"); err == nil {
		t.Fatal("unknown session accepted")
	}
}

// Cancellation is honoured only before the first byte: nothing is written and
// the error is a plain (retry-safe) one, never delivery-unknown.
func TestSendPromptCancelledBeforeStartWritesZeroBytes(t *testing.T) {
	withFastSubmit(t)
	b, s, proc := newFakeBackend(t, "claude-x")
	s.bracketedPaste.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := b.SendPrompt(ctx, "claude-x", "a\nb")
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrDeliveryUnknown) {
		t.Fatalf("err = %v, want plain context.Canceled", err)
	}
	if got := proc.written(); len(got) != 0 {
		t.Fatalf("a cancelled prompt wrote %q", got)
	}
}

// Once the paste has started, cancellation must not strand a half-sent prompt:
// Enter still goes out and the call succeeds, so the caller records delivered.
func TestSendPromptCancelledAfterPasteStillSubmits(t *testing.T) {
	withFastSubmit(t)
	b, s, proc := newFakeBackend(t, "claude-x")
	s.bracketedPaste.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	proc.writeFn = func(call int, p []byte) (int, error) {
		if call == 1 {
			cancel() // the request goes away right after the paste starts
		}
		return len(p), nil
	}
	if err := b.SendPrompt(ctx, "claude-x", "a\nb"); err != nil {
		t.Fatalf("cancel after paste must still finish: %v", err)
	}
	got := proc.written()
	if len(got) != 2 || string(got[0]) != "\x1b[200~a\nb\x1b[201~" || string(got[1]) != "\r" {
		t.Fatalf("writes = %q, want paste then Enter", got)
	}
}

func TestSendPromptShortOrFailedWritesAreUnknownOnlyWhenInputMayHaveStarted(t *testing.T) {
	withFastSubmit(t)
	type tc struct {
		name        string
		hook        func(call int, p []byte) (int, error)
		wantUnknown bool
		wantWrites  int
	}
	for _, c := range []tc{
		{"short paste write", func(call int, p []byte) (int, error) { return len(p) / 2, nil }, true, 1},
		{"paste error after partial write", func(call int, p []byte) (int, error) { return 3, errors.New("pty closed") }, true, 1},
		{"paste error with nothing written", func(call int, p []byte) (int, error) { return 0, errors.New("pty closed") }, false, 1},
		{"Enter write fails", func(call int, p []byte) (int, error) {
			if call == 2 {
				return 0, errors.New("pty closed")
			}
			return len(p), nil
		}, true, 2},
		{"Enter short write", func(call int, p []byte) (int, error) {
			if call == 2 {
				return 0, nil
			}
			return len(p), nil
		}, true, 2},
	} {
		b, s, proc := newFakeBackend(t, "claude-x")
		s.bracketedPaste.Store(true)
		proc.writeFn = c.hook
		err := b.SendPrompt(context.Background(), "claude-x", "a\nb")
		if err == nil {
			t.Fatalf("%s: expected an error", c.name)
		}
		if got := errors.Is(err, ErrDeliveryUnknown); got != c.wantUnknown {
			t.Fatalf("%s: delivery-unknown = %v, want %v (err: %v)", c.name, got, c.wantUnknown, err)
		}
		if got := len(proc.written()); got != c.wantWrites {
			t.Fatalf("%s: %d writes, want %d", c.name, got, c.wantWrites)
		}
		if c.name == "short paste write" || c.name == "paste error after partial write" {
			for _, w := range proc.written() {
				if string(w) == "\r" {
					t.Fatalf("%s: Enter was sent after a failed paste", c.name)
				}
			}
		}
	}
}

// Two prompts to one session never interleave: paste1, Enter1, paste2, Enter2.
func TestSendPromptSerializesConcurrentPromptsPerSession(t *testing.T) {
	old := promptSubmitDelay
	promptSubmitDelay = 30 * time.Millisecond
	t.Cleanup(func() { promptSubmitDelay = old })
	b, s, proc := newFakeBackend(t, "claude-x")
	s.bracketedPaste.Store(true)
	var wg sync.WaitGroup
	for _, text := range []string{"first prompt", "second prompt"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.SendPrompt(context.Background(), "claude-x", text); err != nil {
				t.Errorf("SendPrompt(%q): %v", text, err)
			}
		}()
	}
	wg.Wait()
	w := proc.written()
	if len(w) != 4 {
		t.Fatalf("writes = %q", w)
	}
	if !bytes.HasPrefix(w[0], []byte("\x1b[200~")) || string(w[1]) != "\r" ||
		!bytes.HasPrefix(w[2], []byte("\x1b[200~")) || string(w[3]) != "\r" {
		t.Fatalf("prompts interleaved: %q", w)
	}
}

// A prompt waiting for the per-session lock honours cancellation with zero
// bytes written by the waiter.
func TestSendPromptWaitingForTheLockHonoursCancellation(t *testing.T) {
	withFastSubmit(t)
	b, s, proc := newFakeBackend(t, "claude-x")
	s.bracketedPaste.Store(true)
	release := make(chan struct{})
	started := make(chan struct{})
	proc.writeFn = func(call int, p []byte) (int, error) {
		if call == 1 {
			close(started)
			<-release
		}
		return len(p), nil
	}
	done := make(chan error, 1)
	go func() { done <- b.SendPrompt(context.Background(), "claude-x", "holder") }()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := b.SendPrompt(ctx, "claude-x", "waiter")
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrDeliveryUnknown) {
		t.Fatalf("waiter err = %v, want plain deadline exceeded", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, w := range proc.written() {
		if bytes.Contains(w, []byte("waiter")) {
			t.Fatal("the cancelled waiter wrote to the PTY")
		}
	}
}

func TestPTYSessionTerminalImplementsPromptSender(t *testing.T) {
	withFastSubmit(t)
	b, s, proc := newFakeBackend(t, "claude-x")
	s.bracketedPaste.Store(true)
	var terminal SessionTerminal = NewPTYSessionTerminal(b)
	sender, ok := terminal.(PromptSender)
	if !ok {
		t.Fatal("PTYSessionTerminal must implement PromptSender")
	}
	if err := sender.SendPrompt(context.Background(), "claude-x", "p\nq", "claude", "sid"); err != nil {
		t.Fatal(err)
	}
	if got := proc.written(); len(got) != 2 || string(got[0]) != "\x1b[200~p\nq\x1b[201~" {
		t.Fatalf("writes = %q", got)
	}
}
