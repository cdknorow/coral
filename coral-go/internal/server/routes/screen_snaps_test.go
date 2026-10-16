package routes

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/ptymanager"
)

// #3887: one capture answers the read-only pollers for screenSnapTTL; concurrent pollers share
// one capture; a failure isn't kept; a poller that gives up doesn't spoil it for the others.
func TestScreenSnapsShareOneCapture(t *testing.T) {
	var c screenSnaps
	var fetches atomic.Int32
	fetch := func(context.Context) (string, error) { fetches.Add(1); return "screen", nil }
	ctx := context.Background()
	t0 := time.Now()

	out, _, err := c.capture(ctx, "claude-a", 40, t0, fetch)
	require.NoError(t, err)
	assert.Equal(t, "screen", out)
	_, _, _ = c.capture(ctx, "claude-a", 40, t0.Add(screenSnapTTL/2), fetch)
	assert.EqualValues(t, 1, fetches.Load(), "within the TTL: the same capture")
	_, _, _ = c.capture(ctx, "claude-b", 40, t0, fetch)
	_, _, _ = c.capture(ctx, "claude-a", 20, t0, fetch)
	assert.EqualValues(t, 3, fetches.Load(), "another terminal or line count is its own capture")
	_, _, _ = c.capture(ctx, "claude-a", 40, t0.Add(screenSnapTTL), fetch)
	assert.EqualValues(t, 4, fetches.Load(), "after the TTL: a new capture")

	// Many pollers at once: one capture.
	var slow screenSnaps
	release := make(chan struct{})
	var slowFetches atomic.Int32
	slowFetch := func(context.Context) (string, error) { slowFetches.Add(1); <-release; return "busy", nil }
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _, err := slow.capture(ctx, "claude-c", 40, t0, slowFetch)
			assert.NoError(t, err)
			assert.Equal(t, "busy", out)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.EqualValues(t, 1, slowFetches.Load())

	// A failure isn't kept.
	var failing screenSnaps
	var tries atomic.Int32
	_, _, err = failing.capture(ctx, "claude-d", 40, t0, func(context.Context) (string, error) { tries.Add(1); return "", errors.New("gone") })
	require.Error(t, err)
	out, _, err = failing.capture(ctx, "claude-d", 40, t0, func(context.Context) (string, error) { tries.Add(1); return "back", nil })
	require.NoError(t, err)
	assert.Equal(t, "back", out)
	assert.EqualValues(t, 2, tries.Load())

	// A poller that gives up gets its own error; the capture still completes for the next one.
	var patient screenSnaps
	hold := make(chan struct{})
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, _, err = patient.capture(short, "claude-e", 40, t0, func(context.Context) (string, error) { <-hold; return "late", nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(hold)
	out, _, err = patient.capture(ctx, "claude-e", 40, t0, func(context.Context) (string, error) { return "never called", nil })
	require.NoError(t, err)
	assert.Equal(t, "late", out)
}

// A capture older than screenSnapKeep is dropped when another is stored: a stopped session's
// screen doesn't stay in memory (Security's note on #3887).
func TestScreenSnapsDropOldCaptures(t *testing.T) {
	var c screenSnaps
	fetch := func(context.Context) (string, error) { return "screen", nil }
	t0 := time.Now()
	_, _, _ = c.capture(context.Background(), "claude-gone", 40, t0, fetch)
	_, _, _ = c.capture(context.Background(), "claude-live", 40, t0.Add(screenSnapKeep), fetch)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, kept := c.m["claude-gone\x0040"]
	assert.False(t, kept, "the stopped session's capture is gone")
	assert.Len(t, c.m, 1)
}

// stampingBackend reports an output stamp the test sets (ptymanager.OutputStamper).
type stampingBackend struct {
	ptymanager.TerminalBackend
	mu    sync.Mutex
	stamp string
}

func (b *stampingBackend) OutputStamp(string, string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stamp, true
}

func (b *stampingBackend) set(stamp string) {
	b.mu.Lock()
	b.stamp = stamp
	b.mu.Unlock()
}

// #3887: the agent poll is the read-only path QA's census found (122 of 175 capture-panes): two
// tabs polling one agent share a capture while its output is unchanged. #3954: the output stamp is
// part of the sharing key, so once the pane prints, the next poll captures fresh even within the
// sharing TTL (it showed the old screen for up to 2 s); without a stamp nothing is shared.
func TestPollSharesCapturesOnlyForTheSameOutput(t *testing.T) {
	defer func(old time.Duration) { screenSnapTTL = old }(screenSnapTTL)
	screenSnapTTL = time.Hour // every poll below is within the TTL
	_, h, terminal, _ := setupSessionsTestServer(t)
	ctx := context.Background()
	const name, sid = "claude-11111111-2222-4333-8444-555555555555", "11111111-2222-4333-8444-555555555555"
	terminal.addSession(name, "/tmp")
	terminal.setOutput(name, "first screen")
	captures := func() int {
		terminal.mu.Lock()
		defer terminal.mu.Unlock()
		return len(terminal.captures)
	}
	poll := func() string {
		t.Helper()
		text, err := h.captureForPoll(ctx, name, "claude", sid)
		require.NoError(t, err)
		return text
	}

	// A backend without output stamps: nothing tells two captures apart, so each poll captures.
	h.backend = nil
	poll()
	terminal.setOutput(name, "printed")
	assert.Equal(t, "printed", poll(), "no stamp: never an older screen")
	assert.Equal(t, 2, captures(), "no stamp: no sharing")

	// With output stamps: the same stamp shares one capture; a new stamp captures again at once.
	b := &stampingBackend{stamp: "a"}
	h.backend = b
	terminal.setOutput(name, "first screen")
	n := captures()
	assert.Equal(t, "first screen", poll())
	assert.Equal(t, "first screen", poll())
	assert.Equal(t, n+1, captures(), "same output: one capture")
	terminal.setOutput(name, "second screen")
	b.set("b") // the pane printed
	assert.Equal(t, "second screen", poll(), "the next poll shows the new output, within the TTL")
	assert.Equal(t, n+2, captures(), "new output: a fresh capture")
	assert.Equal(t, "second screen", poll())
	assert.Equal(t, n+2, captures(), "unchanged again: shared")
}
