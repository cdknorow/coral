package jsonl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/stretchr/testify/require"
)

// TestConcurrentSessionReads verifies that a slow read or turn-event inspection
// on one session does not serialize or block chat transcript reads on another session.
func TestConcurrentSessionReads(t *testing.T) {
	reader := NewSessionReader()
	tempDir := t.TempDir()

	// Create transcript for Session B (quick/active session)
	sessionBPath := filepath.Join(tempDir, "sessionB.jsonl")
	sessionBFile, err := os.Create(sessionBPath)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		msg := map[string]any{
			"type": "USER_INPUT",
			"content": fmt.Sprintf("Message %d for Session B", i),
		}
		b, _ := json.Marshal(msg)
		sessionBFile.Write(append(b, '\n'))
	}
	sessionBFile.Close()

	// Register session caches
	cA := reader.getOrCreateSessionCache("sessionA")
	cB := reader.getOrCreateSessionCache("sessionB")
	cB.path = sessionBPath
	cB.pathVerified = true

	// Artificially hold Session A's lock to simulate a slow 50MB file read or directory walk
	cA.mu.Lock()
	sessionABlocked := make(chan struct{})
	sessionADone := make(chan struct{})

	go func() {
		close(sessionABlocked)
		time.Sleep(200 * time.Millisecond)
		cA.mu.Unlock()
		close(sessionADone)
	}()

	<-sessionABlocked

	// Now Session B's ReadAllMessagesForLive should NOT block on Session A
	start := time.Now()
	messages, total := reader.ReadAllMessagesForLive("sessionB", "", at.Agy)
	elapsed := time.Since(start)

	require.Equal(t, 5, total)
	require.Len(t, messages, 5)
	// Must complete well before Session A finishes its 200ms lock
	require.Less(t, elapsed, 100*time.Millisecond, "Session B was blocked by Session A's locked state!")

	<-sessionADone
}

// TestNegativePathBackoff verifies that an unresolvable transcript path
// does not trigger repeated directory scans on every consecutive tick.
func TestNegativePathBackoff(t *testing.T) {
	reader := NewSessionReader()
	c := reader.getOrCreateSessionCache("unresolvable-id")

	// First attempt resolves and records attempt
	reader.ReadCodexTurnEvent("unresolvable-id", "/nonexistent")
	firstAttempt := c.lastResolved

	require.False(t, firstAttempt.IsZero(), "lastResolved should be set after resolution attempt")

	// Immediate subsequent attempt within backoff window should not re-run resolution
	reader.ReadCodexTurnEvent("unresolvable-id", "/nonexistent")
	secondAttempt := c.lastResolved

	require.Equal(t, firstAttempt, secondAttempt, "lastResolved should not update within 2s backoff window")
}

// TestConcurrentMultiSessionStress verifies multiple goroutines rapidly switching
// and reading transcripts concurrently without deadlock or data races.
func TestConcurrentMultiSessionStress(t *testing.T) {
	reader := NewSessionReader()
	tempDir := t.TempDir()

	numSessions := 8
	for i := 0; i < numSessions; i++ {
		sid := fmt.Sprintf("session-%d", i)
		p := filepath.Join(tempDir, sid+".jsonl")
		f, err := os.Create(p)
		require.NoError(t, err)
		for j := 0; j < 10; j++ {
			msg := map[string]any{
				"type": "USER_INPUT",
				"content": fmt.Sprintf("Session %d Message %d", i, j),
			}
			b, _ := json.Marshal(msg)
			f.Write(append(b, '\n'))
		}
		f.Close()
		c := reader.getOrCreateSessionCache(sid)
		c.path = p
		c.pathVerified = true
	}

	var wg sync.WaitGroup
	for worker := 0; worker < 20; worker++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for iter := 0; iter < 20; iter++ {
				sid := fmt.Sprintf("session-%d", (w+iter)%numSessions)
				msgs, total := reader.ReadAllMessagesForLive(sid, "", at.Agy)
				if total != 10 || len(msgs) != 10 {
					t.Errorf("worker %d iter %d got total %d", w, iter, total)
				}
			}
		}(worker)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("Deadlock or extreme contention during concurrent multi-session reads")
	}
}
