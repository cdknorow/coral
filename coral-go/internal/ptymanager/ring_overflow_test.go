package ptymanager

import (
	"fmt"
	"strings"
	"testing"
)

// #5726: the replay ring has a cap (ringMax, 256 KiB) and keeps the NEWEST bytes. TestSession_RingBufferOverflow feeds a cat through the pty and
// never gets past 62 KB (the line buffer of the terminal), so it cannot reach the cap; here the process itself writes more than the cap:
// 40000 numbered lines of 9 bytes each as the pty sends them (L000001 and a carriage return and a line feed), 360000 bytes.
func TestSession_RingKeepsTheNewestBytesPastItsCap(t *testing.T) {
	t.Parallel()
	skipIfWindows(t) // awk is not there
	canFork(t)
	const lines = 40000
	cmd := fmt.Sprintf("awk 'BEGIN { for (i = 1; i <= %d; i++) printf \"L%%06d\\n\", i }'; sleep 5", lines) // alive after printing (#5671)
	s, err := newSession("ring-cap", "test", t.TempDir(), "sid-ring-cap", cmd, 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}
	defer s.kill()

	last := fmt.Sprintf("L%06d", lines)
	content := pollFor(func() string { return string(s.replayBytes()) }, last)
	if !strings.Contains(content, last) {
		t.Fatalf("the last line %s never reached the replay (%d bytes)", last, len(content))
	}
	if total := lines * 9; total <= s.ringMax {
		t.Fatalf("the test writes %d bytes, which does not pass the %d-byte cap", total, s.ringMax)
	}
	if len(content) > s.ringMax {
		t.Errorf("the replay is %d bytes, over the cap of %d", len(content), s.ringMax)
	}
	// replayBytes also cuts what it returns (to ReplayBytes()), so the replay alone cannot show the ring's own bound: that one is what holds the memory.
	s.ringMu.Lock()
	held := len(s.ring)
	s.ringMu.Unlock()
	if held > s.ringMax {
		t.Errorf("the ring holds %d bytes, over its cap of %d: it grows without bound", held, s.ringMax)
	}
	if len(content) < s.ringMax-64 {
		t.Errorf("the replay is %d bytes: the ring was not kept full up to its cap of %d (older bytes may only be dropped to make room)", len(content), s.ringMax)
	}
	if strings.Contains(content, "L000001") || strings.Contains(content, "L000100") {
		t.Errorf("the oldest lines are still in the replay: the ring keeps the head, not the tail")
	}
	// What is kept is one unbroken tail: from the first whole line in the replay to the last, every number follows the one before.
	rest := content[strings.Index(content, "\n")+1:] // the first line may be cut at the front by the cap
	var prev, n int
	for i, l := range strings.Split(strings.TrimRight(rest, "\r\n"), "\n") {
		l = strings.TrimSuffix(l, "\r")
		if _, err := fmt.Sscanf(l, "L%06d", &n); err != nil {
			t.Fatalf("line %d of the replay is %q, not a numbered line", i, l)
		}
		if i > 0 && n != prev+1 {
			t.Fatalf("the replay skips from line %d to %d: the kept bytes are not one unbroken tail", prev, n)
		}
		prev = n
	}
	if prev != lines {
		t.Errorf("the replay ends at line %d, want %d", prev, lines)
	}
}
